// Package basetest fournit aux tests une base PostgreSQL isolée.
//
// La base est donnée par DIGUEE_TEST_POSTGRES_URL. Si la variable est absente,
// le test est sauté, sauf en intégration continue (CI non vide) où il échoue.
// Chaque appel crée un schéma temporaire, migré, supprimé à la fin du test.
package basetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/2guntan/diguee/internal/base"
)

// Variable est le nom de la variable qui donne la base de test.
const Variable = "DIGUEE_TEST_POSTGRES_URL"

// Ouvrir renvoie une base migrée dans un schéma propre au test.
func Ouvrir(t testing.TB) *base.Base {
	t.Helper()
	b, _ := OuvrirMigree(t)
	return b
}

// OuvrirMigree renvoie une base migrée dans un schéma propre au test, et la
// description de ce schéma (pour pg_dump, par exemple).
func OuvrirMigree(t testing.TB) (*base.Base, Schema) {
	t.Helper()
	b, sc := OuvrirSchema(t)
	if _, err := b.Migrer(context.Background()); err != nil {
		t.Fatalf("migrations : %v", err)
	}
	return b, sc
}

// OuvrirVide renvoie une base dans un schéma propre au test, sans migration.
func OuvrirVide(t testing.TB) *base.Base {
	t.Helper()
	b, _ := OuvrirSchema(t)
	return b
}

// Schema décrit le schéma temporaire d'un test.
type Schema struct {
	Nom string // nom du schéma
	URL string // URL de la base de test, dont les connexions utilisent ce schéma
}

// NouveauSchema crée un schéma temporaire vide, supprimé à la fin du test.
func NouveauSchema(t testing.TB) Schema {
	t.Helper()
	url := os.Getenv(Variable)
	if url == "" {
		if os.Getenv("CI") != "" {
			t.Fatalf("%s est obligatoire en intégration continue", Variable)
		}
		t.Skipf("%s absente : test sauté", Variable)
	}
	ctx := context.Background()

	aleas := make([]byte, 8)
	_, _ = rand.Read(aleas)
	schema := "test_" + hex.EncodeToString(aleas)

	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connexion à %s : %v", Variable, err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), url)
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		_, _ = c.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	})

	// pgx transmet les paramètres inconnus de l'URL comme paramètres de session.
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	return Schema{Nom: schema, URL: url + sep + "search_path=" + schema}
}

// OuvrirSchema renvoie une base dans un schéma propre au test, sans
// migration, et la description de ce schéma.
func OuvrirSchema(t testing.TB) (*base.Base, Schema) {
	t.Helper()
	sc := NouveauSchema(t)
	cfg, err := pgxpool.ParseConfig(sc.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, err := base.OuvrirAvec(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Fermer)
	return b, sc
}

// RoleTest est le rôle de connexion des tests qui jouent le rôle de
// l'application : membre de base.RoleApplication, sans autre droit. Il est
// créé au besoin (le rôle de DIGUEE_TEST_POSTGRES_URL doit pouvoir créer des
// rôles : c'est le cas du superutilisateur local et de celui de la CI). Son
// mot de passe ne protège que la base de test.
const RoleTest = "diguee_test_app"

// verrouRoles sérialise la création des rôles entre les paquets de tests,
// lancés en parallèle sur la même base.
const verrouRoles = 0x6469677565655f72

// Application décrit une base de test vue par le rôle de l'application.
type Application struct {
	Proprietaire *base.Base // connexion du test, propriétaire du schéma
	App          *base.Base // connexion avec RoleTest, droits de l'application seulement
	Schema       Schema     // schéma et URL du propriétaire
	URLApp       string     // URL de la base de test avec RoleTest, sur ce schéma
}

// OuvrirApplication renvoie une base migrée dans un schéma propre au test,
// avec les droits de l'application accordés à base.RoleApplication comme
// le fait « diguee migrer », et une seconde connexion avec RoleTest, qui
// n'a que ces droits (ticket #7).
func OuvrirApplication(t testing.TB) Application {
	t.Helper()
	proprio, sc := OuvrirMigree(t)
	ctx := context.Background()
	creerRoles(t)
	if err := proprio.AccorderDroits(ctx, base.RoleApplication); err != nil {
		t.Fatalf("droits de l'application : %v", err)
	}
	u, err := url.Parse(sc.URL)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(RoleTest, RoleTest)
	cfg, err := pgxpool.ParseConfig(u.String())
	if err != nil {
		t.Fatal(err)
	}
	app, err := base.OuvrirAvec(ctx, cfg)
	if err != nil {
		t.Fatalf("connexion avec le rôle %s : %v", RoleTest, err)
	}
	t.Cleanup(app.Fermer)
	return Application{Proprietaire: proprio, App: app, Schema: sc, URLApp: u.String()}
}

// creerRoles crée base.RoleApplication (NOLOGIN) et RoleTest s'ils manquent.
func creerRoles(t testing.TB) {
	t.Helper()
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, os.Getenv(Variable))
	if err != nil {
		t.Fatalf("connexion à %s : %v", Variable, err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "SELECT pg_advisory_lock($1)", int64(verrouRoles)); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "SELECT pg_advisory_unlock($1)", int64(verrouRoles))
	for _, ordre := range []string{
		"CREATE ROLE " + base.RoleApplication + " NOLOGIN",
		"CREATE ROLE " + RoleTest + " LOGIN PASSWORD '" + RoleTest + "' IN ROLE " + base.RoleApplication,
	} {
		if _, err := admin.Exec(ctx, ordre); err != nil {
			var pgErr *pgconn.PgError
			// 42710 : le rôle existe déjà (créé par un autre test).
			if errors.As(err, &pgErr) && pgErr.Code == "42710" {
				continue
			}
			t.Fatalf("%s : %v", ordre, err)
		}
	}
}
