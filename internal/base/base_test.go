package base_test

import (
	"context"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/base"
	"github.com/2guntan/diguee/internal/base/basetest"
	"github.com/2guntan/diguee/internal/empreinte"
	"github.com/2guntan/diguee/internal/questionnaire"
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

// Seule exception : l'horodatage à la minute du journal public (spec §10), dont
// les entrées sont publiques et jamais écrites à cause d'un envoi.
func TestSchemaSansHorodatage(t *testing.T) {
	ctx := context.Background()
	b := basetest.Ouvrir(t)
	rows, err := b.Pool.Query(ctx, `SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND (data_type LIKE 'timestamp%' OR data_type LIKE 'time%' OR column_default LIKE 'nextval%')
		  AND NOT (table_name = 'journal_public' AND column_name = 'horodatage')`)
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

func TestEcrireLot(t *testing.T) {
	ctx := context.Background()
	b := basetest.Ouvrir(t)
	es := []empreinte.Empreinte{{1}, {2}}
	rs := []questionnaire.Reponse{
		{Version: 1, Q1: 1, Q3: 2, Q7: []int16{1, 3}, Themes: []int16{1, 2, 3}, Region: 15},
		{Version: 1, Q3: 0}, // tableaux nil : « Je ne sais pas »
	}
	jour := time.Date(2027, 2, 3, 23, 59, 59, 0, time.FixedZone("UTC+1", 3600))
	if err := b.EcrireLot(ctx, jour, es, rs); err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		if ok, err := b.ParticipationExiste(ctx, e); err != nil || !ok {
			t.Errorf("participation %x absente : %v", e[0], err)
		}
	}
	if ok, _ := b.ParticipationExiste(ctx, empreinte.Empreinte{3}); ok {
		t.Error("participation inattendue")
	}
	var n, vides, v4 int
	var jours []string
	rows, _ := b.Pool.Query(ctx, `SELECT jour::text, cardinality(q7) = 0 AND cardinality(themes) = 0,
		substr(id::text, 15, 1) = '4' FROM reponse`)
	for rows.Next() {
		var j string
		var vide, quatre bool
		_ = rows.Scan(&j, &vide, &quatre)
		n++
		jours = append(jours, j)
		if vide {
			vides++
		}
		if quatre {
			v4++
		}
	}
	if n != 2 || vides != 1 || v4 != 2 {
		t.Errorf("%d réponses, %d vides, %d UUID v4", n, vides, v4)
	}
	for _, j := range jours {
		if j != "2027-02-03" {
			t.Errorf("jour %s", j)
		}
	}
	// Un doublon fait échouer tout le lot : rien n'est écrit.
	if err := b.EcrireLot(ctx, jour, []empreinte.Empreinte{{4}, {1}}, rs); err == nil {
		t.Fatal("doublon accepté")
	}
	if ok, _ := b.ParticipationExiste(ctx, empreinte.Empreinte{4}); ok {
		t.Error("le lot en échec a été écrit en partie")
	}
	if c, _ := b.CompterReponses(ctx); c != 2 {
		t.Errorf("%d réponses après l'échec", c)
	}
}
