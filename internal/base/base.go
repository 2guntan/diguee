// Package base gère la connexion à PostgreSQL, les migrations de schéma et
// les requêtes de la consultation.
package base

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		// Le message d'erreur de pgx peut contenir l'URL, donc le mot de passe.
		return nil, fmt.Errorf("DIGUEE_POSTGRES_URL invalide")
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

// Ping vérifie que la base répond.
func (b *Base) Ping(ctx context.Context) error { return b.Pool.Ping(ctx) }
