package base

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/2guntan/diguee/internal/journal"
)

// verrouJournal est la clé du verrou consultatif qui sérialise les écritures
// du journal public. Un verrou consultatif ne demande aucun droit sur la table
// (LOCK TABLE en exclusif demanderait UPDATE ou DELETE, que le rôle de
// l'application ne doit pas avoir). Même sans lui, la clé primaire et le
// déclencheur de chaînage refuseraient une seconde entrée au même numéro.
const verrouJournal = 0x6a6f75726e616c // « journal »

const colonnesJournal = "numero, horodatage, type, contenu, empreinte_precedente, empreinte"

type vueJournal struct{ tx pgx.Tx }

func (v vueJournal) Derniere(ctx context.Context) (*journal.Entree, error) {
	return uneEntree(v.tx.QueryRow(ctx, "SELECT "+colonnesJournal+" FROM journal_public ORDER BY numero DESC LIMIT 1"))
}

func (v vueJournal) DerniereDuType(ctx context.Context, t journal.Type) (*journal.Entree, error) {
	return uneEntree(v.tx.QueryRow(ctx, "SELECT "+colonnesJournal+
		" FROM journal_public WHERE type = $1 ORDER BY numero DESC LIMIT 1", string(t)))
}

// AjouterAuJournal implémente journal.Magasin : verrou, lecture, écriture,
// dans une seule transaction.
func (b *Base) AjouterAuJournal(ctx context.Context, preparer func(context.Context, journal.Vue) (*journal.Entree, error)) (*journal.Entree, error) {
	var e *journal.Entree
	err := pgx.BeginFunc(ctx, b.Pool, func(tx pgx.Tx) error {
		var err error
		e, err = ajouterAuJournal(ctx, tx, preparer)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("journal public : %w", err)
	}
	return e, nil
}

// ajouterAuJournal prend le verrou d'écriture du journal dans la
// transaction, puis inscrit l'entrée préparée.
func ajouterAuJournal(ctx context.Context, tx pgx.Tx, preparer func(context.Context, journal.Vue) (*journal.Entree, error)) (*journal.Entree, error) {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", verrouJournal); err != nil {
		return nil, err
	}
	e, err := preparer(ctx, vueJournal{tx})
	if err != nil || e == nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO journal_public ("+colonnesJournal+") VALUES ($1, $2, $3, $4, $5, $6)",
		e.Numero, e.Horodatage, string(e.Type), string(e.Contenu), e.EmpreintePrecedente[:], e.Empreinte[:])
	if err != nil {
		return nil, err
	}
	return e, nil
}

// journalTx est le journal public vu depuis une transaction en cours : une
// entrée inscrite par lui n'existe que si la transaction est validée.
type journalTx struct{ tx pgx.Tx }

func (j journalTx) AjouterAuJournal(ctx context.Context, preparer func(context.Context, journal.Vue) (*journal.Entree, error)) (*journal.Entree, error) {
	e, err := ajouterAuJournal(ctx, j.tx, preparer)
	if err != nil {
		return nil, fmt.Errorf("journal public : %w", err)
	}
	return e, nil
}

func (j journalTx) CompterJournal(ctx context.Context) (int64, error) {
	var n int64
	err := j.tx.QueryRow(ctx, "SELECT coalesce(max(numero), 0) FROM journal_public").Scan(&n)
	return n, err
}

func (j journalTx) LireJournal(ctx context.Context, premier, dernier int64) ([]journal.Entree, error) {
	return lireJournal(ctx, j.tx, premier, dernier)
}

// CompterJournal implémente journal.Magasin.
func (b *Base) CompterJournal(ctx context.Context) (int64, error) {
	var n int64
	err := b.Pool.QueryRow(ctx, "SELECT coalesce(max(numero), 0) FROM journal_public").Scan(&n)
	return n, err
}

// LireJournal implémente journal.Magasin.
func (b *Base) LireJournal(ctx context.Context, premier, dernier int64) ([]journal.Entree, error) {
	return lireJournal(ctx, b.Pool, premier, dernier)
}

type requeteur interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func lireJournal(ctx context.Context, q requeteur, premier, dernier int64) ([]journal.Entree, error) {
	lignes, err := q.Query(ctx, "SELECT "+colonnesJournal+
		" FROM journal_public WHERE numero BETWEEN $1 AND $2 ORDER BY numero", premier, dernier)
	if err != nil {
		return nil, err
	}
	defer lignes.Close()
	var es []journal.Entree
	for lignes.Next() {
		e, err := lireEntree(lignes)
		if err != nil {
			return nil, err
		}
		es = append(es, *e)
	}
	return es, lignes.Err()
}

func uneEntree(r pgx.Row) (*journal.Entree, error) {
	e, err := lireEntree(r)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return e, err
}

func lireEntree(r pgx.Row) (*journal.Entree, error) {
	var (
		e         journal.Entree
		h         time.Time
		t, c      string
		prec, emp []byte
	)
	if err := r.Scan(&e.Numero, &h, &t, &c, &prec, &emp); err != nil {
		return nil, err
	}
	if len(prec) != len(e.EmpreintePrecedente) || len(emp) != len(e.Empreinte) {
		return nil, fmt.Errorf("journal public : entrée %d mal formée", e.Numero)
	}
	e.Horodatage, e.Type, e.Contenu = h.UTC(), journal.Type(t), []byte(c)
	copy(e.EmpreintePrecedente[:], prec)
	copy(e.Empreinte[:], emp)
	return &e, nil
}
