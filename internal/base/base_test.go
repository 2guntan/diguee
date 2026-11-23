package base_test

import (
	"context"
	"testing"

	"github.com/2guntan/diguee/internal/base"
	"github.com/2guntan/diguee/internal/base/basetest"
)

func TestMigrationsNumerotees(t *testing.T) {
	ms, err := base.Migrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) == 0 || ms[0].Version != 1 {
		t.Fatalf("migrations : %+v", ms)
	}
}

func TestMigrerPuisCompter(t *testing.T) {
	ctx := context.Background()
	b := basetest.OuvrirVide(t)

	appliquees, err := b.Migrer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(appliquees) == 0 {
		t.Fatal("aucune migration appliquée")
	}
	// Une seconde exécution n'applique rien.
	if again, err := b.Migrer(ctx); err != nil || len(again) != 0 {
		t.Fatalf("seconde migration : %v, %v", again, err)
	}

	n, err := b.CompterReponses(ctx)
	if err != nil || n != 0 {
		t.Fatalf("compteur initial : %d, %v", n, err)
	}
	if _, err := b.Pool.Exec(ctx, `INSERT INTO reponse (id, jour, version_questionnaire, q3)
		VALUES (gen_random_uuid(), current_date, 1, 1), (gen_random_uuid(), current_date, 1, 5)`); err != nil {
		t.Fatal(err)
	}
	if n, err := b.CompterReponses(ctx); err != nil || n != 2 {
		t.Fatalf("compteur : %d, %v", n, err)
	}
}

func TestSchemaSansHorodatage(t *testing.T) {
	ctx := context.Background()
	b := basetest.Ouvrir(t)
	rows, err := b.Pool.Query(ctx, `SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND (data_type LIKE 'timestamp%' OR data_type LIKE 'time%' OR column_default LIKE 'nextval%')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var table, col string
		_ = rows.Scan(&table, &col)
		t.Errorf("colonne horodatée ou séquentielle interdite : %s.%s", table, col)
	}
}

func TestEmpreinteDe32Octets(t *testing.T) {
	ctx := context.Background()
	b := basetest.Ouvrir(t)
	if _, err := b.Pool.Exec(ctx, "INSERT INTO participation VALUES ($1)", []byte("court")); err == nil {
		t.Error("une empreinte qui ne fait pas 32 octets devrait être refusée")
	}
	if _, err := b.Pool.Exec(ctx, "INSERT INTO participation VALUES ($1)", make([]byte, 32)); err != nil {
		t.Error(err)
	}
}
