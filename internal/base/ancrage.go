package base

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/2guntan/diguee/internal/ancrage"
)

// EnregistrerPreuve implémente ancrage.Magasin.
func (b *Base) EnregistrerPreuve(ctx context.Context, p ancrage.Preuve) (bool, error) {
	t, err := b.Pool.Exec(ctx, `INSERT INTO ancrage_preuve (jour, revision, numero, empreinte, ots, calendriers, bitcoin)
		VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT DO NOTHING`,
		dateDe(p.Jour), p.Revision, p.Numero, p.Empreinte[:], p.OTS, p.Calendriers, p.Bitcoin)
	if err != nil {
		return false, err
	}
	return t.RowsAffected() == 1, nil
}

// EnregistrerArchive implémente ancrage.Magasin.
func (b *Base) EnregistrerArchive(ctx context.Context, a ancrage.Archive) (bool, error) {
	t, err := b.Pool.Exec(ctx, `INSERT INTO ancrage_archive (jour, cible, lien) VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING`, dateDe(a.Jour), string(a.Cible), a.Lien)
	if err != nil {
		return false, err
	}
	return t.RowsAffected() == 1, nil
}

const colonnesPreuve = "jour, revision, numero, empreinte, ots, calendriers, bitcoin"

func lirePreuve(r pgx.Row) (*ancrage.Preuve, error) {
	var (
		p   ancrage.Preuve
		j   pgtype.Date
		emp []byte
	)
	if err := r.Scan(&j, &p.Revision, &p.Numero, &emp, &p.OTS, &p.Calendriers, &p.Bitcoin); err != nil {
		return nil, err
	}
	if len(emp) != len(p.Empreinte) {
		return nil, fmt.Errorf("preuve du %s mal formée", j.Time.Format(time.DateOnly))
	}
	copy(p.Empreinte[:], emp)
	p.Jour = j.Time.UTC()
	return &p, nil
}

// LirePreuve implémente ancrage.Magasin.
func (b *Base) LirePreuve(ctx context.Context, jour time.Time) (*ancrage.Preuve, error) {
	p, err := lirePreuve(b.Pool.QueryRow(ctx, "SELECT "+colonnesPreuve+
		" FROM ancrage_preuve WHERE jour = $1 ORDER BY revision DESC LIMIT 1", dateDe(jour)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

// Ancrages implémente ancrage.Magasin.
func (b *Base) Ancrages(ctx context.Context, limite int) ([]ancrage.Etat, error) {
	lignes, err := b.Pool.Query(ctx, `SELECT jour FROM (
		SELECT jour FROM ancrage_preuve UNION SELECT jour FROM ancrage_archive) j
		ORDER BY jour DESC LIMIT $1`, limite)
	if err != nil {
		return nil, err
	}
	var etats []ancrage.Etat
	index := map[time.Time]int{}
	for lignes.Next() {
		var j pgtype.Date
		if err := lignes.Scan(&j); err != nil {
			lignes.Close()
			return nil, err
		}
		index[j.Time.UTC()] = len(etats)
		etats = append(etats, ancrage.Etat{Jour: j.Time.UTC()})
	}
	lignes.Close()
	if err := lignes.Err(); err != nil || len(etats) == 0 {
		return etats, err
	}
	plus := dateDe(etats[len(etats)-1].Jour)

	lignes, err = b.Pool.Query(ctx, "SELECT DISTINCT ON (jour) "+colonnesPreuve+
		" FROM ancrage_preuve WHERE jour >= $1 ORDER BY jour, revision DESC", plus)
	if err != nil {
		return nil, err
	}
	for lignes.Next() {
		p, err := lirePreuve(lignes)
		if err != nil {
			lignes.Close()
			return nil, err
		}
		if i, ok := index[p.Jour]; ok {
			etats[i].Preuve = p
		}
	}
	lignes.Close()
	if err := lignes.Err(); err != nil {
		return nil, err
	}

	lignes, err = b.Pool.Query(ctx, "SELECT jour, cible, lien FROM ancrage_archive WHERE jour >= $1 ORDER BY jour, cible", plus)
	if err != nil {
		return nil, err
	}
	defer lignes.Close()
	for lignes.Next() {
		var (
			a ancrage.Archive
			j pgtype.Date
			c string
		)
		if err := lignes.Scan(&j, &c, &a.Lien); err != nil {
			return nil, err
		}
		a.Jour, a.Cible = j.Time.UTC(), ancrage.Cible(c)
		if i, ok := index[a.Jour]; ok {
			etats[i].Archives = append(etats[i].Archives, a)
		}
	}
	return etats, lignes.Err()
}
