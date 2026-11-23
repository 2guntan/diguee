package base

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/publication"
	"github.com/2guntan/diguee/internal/questionnaire"
)

// verrouPublication est la clé du verrou consultatif qui sérialise les
// publications, entre processus aussi (serveur et « diguee publier »). Il est
// toujours pris avant celui du journal, jamais après : pas d'interblocage.
const verrouPublication = 0x7075626c6963 // « public »

func dateDe(jour time.Time) pgtype.Date {
	return pgtype.Date{Time: publication.Jour(jour), Valid: true}
}

// reponsesTx lit les réponses dans la transaction de la publication.
type reponsesTx struct{ tx pgx.Tx }

// Parcourir implémente publication.Reponses : les réponses de la version
// donnée dont le jour est antérieur à avant, lues ligne à ligne, sans
// identifiant ni jour.
func (r reponsesTx) Parcourir(ctx context.Context, avant time.Time, version int16, f func(questionnaire.Reponse) error) error {
	lignes, err := r.tx.Query(ctx, `SELECT q1, q2, q3, q4, q5, q6, q7, themes, region, tranche_age, sexe, inscrit
		FROM reponse WHERE jour < $1 AND version_questionnaire = $2`, dateDe(avant), version)
	if err != nil {
		return err
	}
	defer lignes.Close()
	for lignes.Next() {
		rep := questionnaire.Reponse{Version: version}
		if err := lignes.Scan(&rep.Q1, &rep.Q2, &rep.Q3, &rep.Q4, &rep.Q5, &rep.Q6, &rep.Q7, &rep.Themes,
			&rep.Region, &rep.TrancheAge, &rep.Sexe, &rep.Inscrit); err != nil {
			return err
		}
		if err := f(rep); err != nil {
			return err
		}
	}
	return lignes.Err()
}

// EnregistrerPublication implémente publication.Magasin.
func (b *Base) EnregistrerPublication(ctx context.Context, jour time.Time,
	produire func(context.Context, publication.Reponses) (*publication.Publication, error),
	journaliser func(context.Context, journal.Magasin, *publication.Publication) error) (*publication.Publication, bool, error) {
	var (
		pub      *publication.Publication
		nouvelle bool
	)
	err := pgx.BeginFunc(ctx, b.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", verrouPublication); err != nil {
			return err
		}
		var err error
		if pub, err = lirePublication(ctx, tx, jour); err != nil || pub != nil {
			return err
		}
		if pub, err = produire(ctx, reponsesTx{tx}); err != nil {
			return err
		}
		if !pub.Jour.Equal(publication.Jour(jour)) {
			return fmt.Errorf("instantané du %s pour le %s", pub.Jour.Format(time.DateOnly), jour.Format(time.DateOnly))
		}
		if _, err := tx.Exec(ctx, `INSERT INTO publication (jour, reponses, resultats_publies, instantane, html, empreinte)
			VALUES ($1, $2, $3, $4, $5, $6)`, dateDe(jour), pub.Reponses, pub.ResultatsPublies,
			string(pub.JSON), string(pub.HTML), pub.Empreinte[:]); err != nil {
			return err
		}
		nouvelle = true
		return journaliser(ctx, journalTx{tx}, pub)
	})
	if err != nil {
		return nil, false, err
	}
	return pub, nouvelle, nil
}

type lecteurLigne interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func lirePublication(ctx context.Context, q lecteurLigne, jour time.Time) (*publication.Publication, error) {
	var (
		p         publication.Publication
		j         pgtype.Date
		js, html  string
		empreinte []byte
	)
	err := q.QueryRow(ctx, `SELECT jour, reponses, resultats_publies, instantane, html, empreinte
		FROM publication WHERE jour = $1`, dateDe(jour)).Scan(&j, &p.Reponses, &p.ResultatsPublies, &js, &html, &empreinte)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(empreinte) != len(p.Empreinte) {
		return nil, fmt.Errorf("publication du %s mal formée", jour.Format(time.DateOnly))
	}
	p.Jour, p.JSON, p.HTML = j.Time.UTC(), []byte(js), []byte(html)
	copy(p.Empreinte[:], empreinte)
	return &p, nil
}

// LirePublication implémente publication.Magasin.
func (b *Base) LirePublication(ctx context.Context, jour time.Time) (*publication.Publication, error) {
	return lirePublication(ctx, b.Pool, jour)
}

// DernierResume implémente publication.Magasin.
func (b *Base) DernierResume(ctx context.Context) (*publication.Resume, error) {
	var (
		r publication.Resume
		j pgtype.Date
	)
	err := b.Pool.QueryRow(ctx, "SELECT jour, reponses, resultats_publies FROM publication ORDER BY jour DESC LIMIT 1").
		Scan(&j, &r.Reponses, &r.ResultatsPublies)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.Jour = j.Time.UTC()
	return &r, nil
}

// Resumes implémente publication.Magasin.
func (b *Base) Resumes(ctx context.Context) ([]publication.Resume, error) {
	lignes, err := b.Pool.Query(ctx, "SELECT jour, reponses, resultats_publies FROM publication ORDER BY jour DESC")
	if err != nil {
		return nil, err
	}
	defer lignes.Close()
	var rs []publication.Resume
	for lignes.Next() {
		var (
			r publication.Resume
			j pgtype.Date
		)
		if err := lignes.Scan(&j, &r.Reponses, &r.ResultatsPublies); err != nil {
			return nil, err
		}
		r.Jour = j.Time.UTC()
		rs = append(rs, r)
	}
	return rs, lignes.Err()
}
