package base_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/2guntan/diguee/internal/base"
	"github.com/2guntan/diguee/internal/base/basetest"
	"github.com/2guntan/diguee/internal/empreinte"
	"github.com/2guntan/diguee/internal/journal"
)

// refusPrivilege vérifie que err est un refus de droits de PostgreSQL
// (42501, insufficient_privilege), et non l'erreur d'un déclencheur : le
// rôle de l'application ne doit même pas pouvoir tenter l'opération.
func refusPrivilege(t *testing.T, ordre string, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" || !strings.Contains(pgErr.Message, "permission denied") &&
		!strings.Contains(pgErr.Message, "must be owner") && !strings.Contains(pgErr.Message, "droit refusé") &&
		!strings.Contains(pgErr.Message, "doit être le propriétaire") {
		t.Errorf("%s : refus de droits (42501) attendu, obtenu %v", ordre, err)
	}
}

// Avec le rôle de l'application, UPDATE, DELETE, TRUNCATE et la
// désactivation des déclencheurs échouent sur toutes les tables, faute de
// droits ; seules quatre colonnes de membre sont modifiables (ticket #7).
func TestRoleApplicationNeModifieRien(t *testing.T) {
	ctx := context.Background()
	a := basetest.OuvrirApplication(t)

	// Les insertions de l'application passent : participation, réponse
	// (par EcrireLot) et journal public.
	e, _ := empreinte.NouveauHMACLocal(nil).Calculer(ctx, "+221770000001")
	if err := a.App.EcrireLot(ctx, time.Now(), []empreinte.Empreinte{e}, nil); err != nil {
		t.Fatalf("écriture d'un lot avec le rôle de l'application : %v", err)
	}
	if _, err := journal.Nouveau(a.App, nil).Inscrire(ctx, journal.Methodologie, map[string]any{"version": 1}); err != nil {
		t.Fatalf("journal public avec le rôle de l'application : %v", err)
	}

	tables := base.TablesApplication()
	if len(tables) < 25 {
		t.Fatalf("%d tables dans la politique", len(tables))
	}
	for _, table := range tables {
		var colonne string
		if err := a.Proprietaire.Pool.QueryRow(ctx, `SELECT column_name FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = $1 ORDER BY ordinal_position LIMIT 1`, table).Scan(&colonne); err != nil {
			t.Fatalf("%s : %v", table, err)
		}
		for _, ordre := range []string{
			"UPDATE " + table + " SET " + colonne + " = " + colonne,
			"DELETE FROM " + table,
			"TRUNCATE " + table,
			"ALTER TABLE " + table + " DISABLE TRIGGER ALL",
			"ALTER TABLE " + table + " DISABLE TRIGGER USER",
			"ALTER TABLE " + table + " OWNER TO " + basetest.RoleTest,
			"DROP TABLE " + table,
		} {
			_, err := a.App.Pool.Exec(ctx, ordre)
			refusPrivilege(t, ordre, err)
		}
	}
	// membre : seules les colonnes prévues sont modifiables, et jamais de
	// suppression (le déclencheur la refuse aussi pour le propriétaire).
	for _, ordre := range []string{
		"UPDATE membre SET nom = nom", "UPDATE membre SET identifiant = identifiant", "DELETE FROM membre",
	} {
		_, err := a.App.Pool.Exec(ctx, ordre)
		refusPrivilege(t, ordre, err)
	}
	if _, err := a.App.Pool.Exec(ctx, "UPDATE membre SET actif = actif, roles = roles WHERE false"); err != nil {
		t.Errorf("modification des colonnes prévues de membre : %v", err)
	}
	// Ni création d'objet dans le schéma, ni contournement des déclencheurs
	// par session_replication_role (superutilisateur seulement).
	for _, ordre := range []string{
		"CREATE TABLE intrus (x int)",
		"CREATE FUNCTION intrus() RETURNS int LANGUAGE sql AS 'SELECT 1'",
		"SET session_replication_role = replica",
	} {
		_, err := a.App.Pool.Exec(ctx, ordre)
		refusPrivilege(t, ordre, err)
	}

	// Rien n'a bougé.
	var np, nj int
	if err := a.Proprietaire.Pool.QueryRow(ctx,
		"SELECT (SELECT count(*) FROM participation), (SELECT count(*) FROM journal_public)").Scan(&np, &nj); err != nil || np != 1 || nj != 1 {
		t.Fatalf("%d participation(s), %d entrée(s), %v", np, nj, err)
	}
	// La Clôture (#16) supprimera participation avec le rôle propriétaire.
	if _, err := a.Proprietaire.Pool.Exec(ctx, "DELETE FROM participation"); err != nil {
		t.Errorf("le propriétaire doit pouvoir vider participation : %v", err)
	}
}

// VerifierRoleApplication accepte le rôle de l'application et refuse le
// propriétaire (ici le superutilisateur des tests).
func TestVerifierRoleApplication(t *testing.T) {
	ctx := context.Background()
	a := basetest.OuvrirApplication(t)
	if err := a.App.VerifierRoleApplication(ctx); err != nil {
		t.Fatalf("rôle de l'application refusé : %v", err)
	}
	err := a.Proprietaire.VerifierRoleApplication(ctx)
	if err == nil {
		t.Fatal("le propriétaire devrait être refusé")
	}
	for _, motif := range []string{"propriétaire du schéma", "propriétaire de la table participation", "peut créer"} {
		if !strings.Contains(err.Error(), motif) {
			t.Errorf("%q absent de : %v", motif, err)
		}
	}
	// Un droit de trop accordé à la main est détecté.
	if _, err := a.Proprietaire.Pool.Exec(ctx, "GRANT DELETE ON reponse TO "+base.RoleApplication); err != nil {
		t.Fatal(err)
	}
	if err := a.App.VerifierRoleApplication(ctx); err == nil || !strings.Contains(err.Error(), "droits de trop sur reponse : DELETE") {
		t.Errorf("DELETE sur reponse non détecté : %v", err)
	}
	// AccorderDroits le retire.
	if err := a.Proprietaire.AccorderDroits(ctx, base.RoleApplication); err != nil {
		t.Fatal(err)
	}
	if err := a.App.VerifierRoleApplication(ctx); err != nil {
		t.Errorf("après AccorderDroits : %v", err)
	}
}

// Une table sans droits déclarés bloque AccorderDroits (fermé par défaut),
// et un rôle absent est signalé.
func TestAccorderDroitsExigeUnePolitiqueComplete(t *testing.T) {
	ctx := context.Background()
	a := basetest.OuvrirApplication(t)
	if _, err := a.Proprietaire.Pool.Exec(ctx, "CREATE TABLE nouvelle (x int)"); err != nil {
		t.Fatal(err)
	}
	if err := a.Proprietaire.AccorderDroits(ctx, base.RoleApplication); err == nil || !strings.Contains(err.Error(), "table nouvelle absente de la politique") {
		t.Errorf("table inconnue acceptée : %v", err)
	}
	if err := a.Proprietaire.AccorderDroits(ctx, "diguee_role_inexistant"); !errors.Is(err, base.ErrRoleAbsent) {
		t.Errorf("rôle absent : %v", err)
	}
}

// Le serveur en production n'applique pas les migrations : il vérifie
// seulement que le schéma est à jour.
func TestVerifierSchemaAJour(t *testing.T) {
	ctx := context.Background()
	a := basetest.OuvrirApplication(t)
	if err := a.App.VerifierSchemaAJour(ctx); err != nil {
		t.Fatal(err)
	}
	vide := basetest.OuvrirVide(t)
	if err := vide.VerifierSchemaAJour(ctx); err == nil || !strings.Contains(err.Error(), "diguee migrer") {
		t.Errorf("schéma vide accepté : %v", err)
	}
	if _, err := a.Proprietaire.Pool.Exec(ctx, "INSERT INTO schema_migrations VALUES (999, '0999_futur.sql')"); err != nil {
		t.Fatal(err)
	}
	if err := a.App.VerifierSchemaAJour(ctx); err == nil || !strings.Contains(err.Error(), "plus récent") {
		t.Errorf("schéma plus récent accepté : %v", err)
	}
}
