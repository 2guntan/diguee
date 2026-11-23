// Package base gère la connexion à PostgreSQL, les migrations de schéma et
// les requêtes de la consultation.
package base

import (
	"context"
	"crypto/rand"
	"embed"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/2guntan/diguee/internal/empreinte"
	"github.com/2guntan/diguee/internal/questionnaire"
)

//go:embed migrations/*.sql
var fichiersMigrations embed.FS

// verrouMigrations est la clé du verrou consultatif pris pendant les migrations.
const verrouMigrations = 0x646967756565 // « diguee »

// Base enveloppe le pool de connexions.
type Base struct {
	Pool *pgxpool.Pool
}

// Ouvrir se connecte à PostgreSQL et vérifie la connexion.
func Ouvrir(ctx context.Context, url string) (*Base, error) {
	return OuvrirVariable(ctx, url, "DIGUEE_POSTGRES_URL")
}

// OuvrirVariable est Ouvrir pour une adresse lue dans la variable nom
// (DIGUEE_POSTGRES_URL_MIGRATIONS, par exemple), citée en cas d'erreur.
func OuvrirVariable(ctx context.Context, url, nom string) (*Base, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		// Le message d'erreur de pgx peut contenir l'URL, donc le mot de passe.
		return nil, fmt.Errorf("%s invalide", nom)
	}
	return OuvrirAvec(ctx, cfg)
}

// OuvrirAvec se connecte avec une configuration déjà analysée.
func OuvrirAvec(ctx context.Context, cfg *pgxpool.Config) (*Base, error) {
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connexion à PostgreSQL : %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connexion à PostgreSQL : %w", err)
	}
	return &Base{Pool: pool}, nil
}

// Fermer ferme le pool.
func (b *Base) Fermer() { b.Pool.Close() }

// Migration est un fichier de migrations/NNNN_nom.sql.
type Migration struct {
	Version int
	Nom     string
	SQL     string
}

// Migrations renvoie les migrations embarquées, dans l'ordre.
func Migrations() ([]Migration, error) {
	entrees, err := fs.ReadDir(fichiersMigrations, "migrations")
	if err != nil {
		return nil, err
	}
	var ms []Migration
	for _, e := range entrees {
		nom := e.Name()
		num, _, ok := strings.Cut(nom, "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || v <= 0 {
			return nil, fmt.Errorf("migration mal nommée : %s (attendu NNNN_nom.sql)", nom)
		}
		sql, err := fichiersMigrations.ReadFile("migrations/" + nom)
		if err != nil {
			return nil, err
		}
		ms = append(ms, Migration{Version: v, Nom: nom, SQL: string(sql)})
	}
	slices.SortFunc(ms, func(a, b Migration) int { return a.Version - b.Version })
	for i := range ms {
		if ms[i].Version != i+1 {
			return nil, fmt.Errorf("migrations non consécutives : %s", ms[i].Nom)
		}
	}
	return ms, nil
}

// Migrer applique, dans une seule transaction et sous verrou, les migrations
// qui ne l'ont pas encore été. Elle renvoie les noms des migrations appliquées.
func (b *Base) Migrer(ctx context.Context) ([]string, error) {
	ms, err := Migrations()
	if err != nil {
		return nil, err
	}
	var appliquees []string
	err = pgx.BeginFunc(ctx, b.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", verrouMigrations); err != nil {
			return err
		}
		// Pas d'horodatage ici non plus : rien ne doit dater l'activité de la base.
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
			version integer PRIMARY KEY,
			nom     text NOT NULL
		)`); err != nil {
			return err
		}
		var derniere int
		if err := tx.QueryRow(ctx, "SELECT coalesce(max(version), 0) FROM schema_migrations").Scan(&derniere); err != nil {
			return err
		}
		for _, m := range ms {
			if m.Version <= derniere {
				continue
			}
			if _, err := tx.Exec(ctx, m.SQL); err != nil {
				return fmt.Errorf("migration %s : %w", m.Nom, err)
			}
			if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version, nom) VALUES ($1, $2)", m.Version, m.Nom); err != nil {
				return err
			}
			appliquees = append(appliquees, m.Nom)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return appliquees, nil
}

// CompterReponses renvoie le nombre total de réponses enregistrées.
func (b *Base) CompterReponses(ctx context.Context) (int64, error) {
	var n int64
	err := b.Pool.QueryRow(ctx, "SELECT count(*) FROM reponse").Scan(&n)
	return n, err
}

// ParticipationExiste indique si l'empreinte est déjà dans la table des
// participations.
func (b *Base) ParticipationExiste(ctx context.Context, e empreinte.Empreinte) (bool, error) {
	var existe bool
	err := b.Pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM participation WHERE empreinte = $1)", e[:]).Scan(&existe)
	return existe, err
}

// EcrireLot écrit un lot du tampon en une seule transaction (spec §6.2) : les
// participations puis les réponses, chacune dans l'ordre reçu (le tampon les a
// déjà mélangées). Chaque réponse reçoit un identifiant aléatoire (UUID v4) et
// seulement le jour d'écriture, jamais l'heure.
func (b *Base) EcrireLot(ctx context.Context, jour time.Time, empreintes []empreinte.Empreinte, reponses []questionnaire.Reponse) error {
	j := pgtype.Date{Time: time.Date(jour.Year(), jour.Month(), jour.Day(), 0, 0, 0, 0, time.UTC), Valid: true}
	return pgx.BeginFunc(ctx, b.Pool, func(tx pgx.Tx) error {
		lot := &pgx.Batch{}
		for _, e := range empreintes {
			lot.Queue("INSERT INTO participation (empreinte) VALUES ($1)", e[:])
		}
		for _, r := range reponses {
			id, err := uuidAleatoire()
			if err != nil {
				return err
			}
			lot.Queue(`INSERT INTO reponse (id, jour, version_questionnaire,
				q1, q2, q3, q4, q5, q6, q7, themes, region, tranche_age, sexe, inscrit)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
				id, j, r.Version, r.Q1, r.Q2, r.Q3, r.Q4, r.Q5, r.Q6,
				nonNul(r.Q7), nonNul(r.Themes), r.Region, r.TrancheAge, r.Sexe, r.Inscrit)
		}
		return tx.SendBatch(ctx, lot).Close()
	})
}

// nonNul remplace un tableau nil par un tableau vide (« Je ne sais pas »).
func nonNul(xs []int16) []int16 {
	if xs == nil {
		return []int16{}
	}
	return xs
}

// uuidAleatoire tire un UUID version 4 avec le générateur cryptographique.
func uuidAleatoire() (pgtype.UUID, error) {
	var u pgtype.UUID
	if _, err := rand.Read(u.Bytes[:]); err != nil {
		return u, err
	}
	u.Bytes[6] = u.Bytes[6]&0x0f | 0x40
	u.Bytes[8] = u.Bytes[8]&0x3f | 0x80
	u.Valid = true
	return u, nil
}

// Ping vérifie que la base répond.
func (b *Base) Ping(ctx context.Context) error { return b.Pool.Ping(ctx) }
