package base_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/2guntan/diguee/internal/base"
	"github.com/2guntan/diguee/internal/base/basetest"
	"github.com/2guntan/diguee/internal/journal"
)

func journalDeTest(t *testing.T) (*base.Base, basetest.Schema, *journal.Journal) {
	t.Helper()
	b, sc := basetest.OuvrirMigree(t)
	return b, sc, journal.Nouveau(b, nil)
}

func TestJournalEnBase(t *testing.T) {
	ctx := context.Background()
	b, _, j := journalDeTest(t)
	if n, err := j.Compter(ctx); err != nil || n != 0 {
		t.Fatalf("%d, %v", n, err)
	}
	contenu := map[string]any{"texte": "<é>& \"", "total": 1234, "liste": []int{3, 1, 2}, "vide": map[string]int{}}
	for range 5 {
		if _, err := j.Inscrire(ctx, journal.Methodologie, contenu); err != nil {
			t.Fatal(err)
		}
	}
	es, err := j.Toutes(ctx)
	if err != nil || len(es) != 5 {
		t.Fatalf("%d entrées, %v", len(es), err)
	}
	if _, err := journal.VerifierChaine(es); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := journal.Exporter(&buf, es); err != nil {
		t.Fatal(err)
	}
	if _, rap, err := journal.LireExport(&buf); err != nil || rap.Entrees != 5 {
		t.Fatalf("%v, %+v", err, rap)
	}
	// Le contenu est conservé octet pour octet (pas de normalisation jsonb).
	var brut string
	if err := b.Pool.QueryRow(ctx, "SELECT contenu FROM journal_public WHERE numero = 1").Scan(&brut); err != nil {
		t.Fatal(err)
	}
	if brut != string(es[0].Contenu) || !strings.HasPrefix(brut, `{"liste":[3,1,2],"texte":"<é>&`) {
		t.Errorf("contenu stocké %q", brut)
	}
	page, err := j.Lire(ctx, 2, 3)
	if err != nil || len(page) != 2 || page[0].Numero != 2 || page[1].Numero != 3 {
		t.Errorf("lecture partielle : %v, %v", page, err)
	}
}

// Deux écritures simultanées, même depuis des connexions différentes, ne
// cassent jamais la chaîne.
func TestJournalEcrituresSimultanees(t *testing.T) {
	ctx := context.Background()
	_, _, j := journalDeTest(t)
	const n = 40
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := j.Inscrire(ctx, journal.StatutPublie, map[string]int{"i": i}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	es, err := j.Toutes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rap, err := journal.VerifierChaine(es); err != nil || rap.Entrees != n {
		t.Fatalf("%v, %+v", err, rap)
	}
}

// Une seule entrée « mise en production » par nouvelle version, même pour des
// démarrages simultanés.
func TestJournalMiseEnProductionEnBase(t *testing.T) {
	ctx := context.Background()
	_, _, j := journalDeTest(t)
	sha := strings.Repeat("d", 64)
	var wg sync.WaitGroup
	var mu sync.Mutex
	inscrites := 0
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ins, err := j.InscrireMiseEnProduction(ctx, "v1.2.3", sha)
			if err != nil {
				t.Error(err)
			}
			if ins {
				mu.Lock()
				inscrites++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if inscrites != 1 {
		t.Fatalf("%d entrées, 1 attendue", inscrites)
	}
	if _, ins, err := j.InscrireMiseEnProduction(ctx, "v1.2.3", sha); err != nil || ins {
		t.Fatalf("redémarrage sur la même version : %v, %v", ins, err)
	}
	if _, ins, err := j.InscrireMiseEnProduction(ctx, "v1.2.4", strings.Repeat("e", 64)); err != nil || !ins {
		t.Fatalf("nouvelle version : %v, %v", ins, err)
	}
	if n, _ := j.Compter(ctx); n != 2 {
		t.Errorf("%d entrées, 2 attendues", n)
	}
}

// roleApplication crée un rôle sans privilège de superutilisateur, à qui l'on
// donne tous les droits sur le journal (le pire cas), et renvoie une
// connexion qui l'a endossé (SET ROLE).
func roleApplication(t *testing.T, b *base.Base, sc basetest.Schema) *pgx.Conn {
	t.Helper()
	ctx := context.Background()
	aleas := make([]byte, 6)
	_, _ = rand.Read(aleas)
	role := "app_" + hex.EncodeToString(aleas)
	for _, q := range []string{
		"CREATE ROLE " + role + " NOLOGIN NOSUPERUSER NOBYPASSRLS",
		"GRANT USAGE ON SCHEMA " + sc.Nom + " TO " + role,
		"GRANT ALL PRIVILEGES ON journal_public TO " + role,
	} {
		if _, err := b.Pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = b.Pool.Exec(context.Background(), "DROP OWNED BY "+role)
		_, _ = b.Pool.Exec(context.Background(), "DROP ROLE "+role)
	})
	c, err := pgx.Connect(ctx, sc.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	if _, err := c.Exec(ctx, "SET ROLE "+role); err != nil {
		t.Fatal(err)
	}
	return c
}

// Le journal est en ajout seul jusque dans la base : UPDATE, DELETE et
// TRUNCATE échouent, avec le rôle de l'application comme avec le
// propriétaire de la table.
func TestJournalAjoutSeulEnSQL(t *testing.T) {
	ctx := context.Background()
	b, sc, j := journalDeTest(t)
	for range 3 {
		if _, err := j.Inscrire(ctx, journal.Ouverture, map[string]string{"jour": "2027-01-15"}); err != nil {
			t.Fatal(err)
		}
	}
	roles := map[string]func(string) error{
		"propriétaire": func(q string) error { _, err := b.Pool.Exec(ctx, q); return err },
	}
	app := roleApplication(t, b, sc)
	roles["application"] = func(q string) error { _, err := app.Exec(ctx, q); return err }
	for nom, exec := range roles {
		for _, q := range []string{
			`UPDATE journal_public SET contenu = '{"jour":"2027-02-01"}' WHERE numero = 2`,
			`UPDATE journal_public SET horodatage = horodatage`,
			`DELETE FROM journal_public WHERE numero = 3`,
			`DELETE FROM journal_public`,
			`TRUNCATE journal_public`,
		} {
			if err := exec(q); err == nil || !strings.Contains(err.Error(), "ajout seul") {
				t.Errorf("%s : %q devrait être refusé par le déclencheur : %v", nom, q, err)
			}
		}
	}
	es, _ := j.Toutes(ctx)
	if _, err := journal.VerifierChaine(es); err != nil || len(es) != 3 {
		t.Fatalf("journal altéré : %d entrées, %v", len(es), err)
	}

	// Une insertion directe qui ne suit pas la dernière entrée est refusée.
	d := es[2]
	h := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	for nom, args := range map[string][]any{
		"numéro sauté":            {d.Numero + 2, h, d.Empreinte[:]},
		"numéro répété":           {d.Numero, h, d.Empreinte[:]},
		"mauvaise précédente":     {d.Numero + 1, h, es[1].Empreinte[:]},
		"horodatage qui recule":   {d.Numero + 1, d.Horodatage.Add(-time.Minute), d.Empreinte[:]},
		"horodatage à la seconde": {d.Numero + 1, h.Add(time.Second), d.Empreinte[:]},
	} {
		_, err := app.Exec(ctx, `INSERT INTO journal_public (numero, horodatage, type, contenu, empreinte_precedente, empreinte)
			VALUES ($1, $2, 'ouverture', '{}', $3, $4)`, append(args, bytes.Repeat([]byte{9}, 32))...)
		if err == nil {
			t.Errorf("%s : insertion acceptée", nom)
		}
	}
	// Une insertion bien chaînée passe : le déclencheur ne recalcule pas
	// l'empreinte, c'est le rôle du script de vérification.
	if _, err := app.Exec(ctx, `INSERT INTO journal_public (numero, horodatage, type, contenu, empreinte_precedente, empreinte)
		VALUES ($1, $2, 'ouverture', '{}', $3, $4)`, d.Numero+1, h, d.Empreinte[:], bytes.Repeat([]byte{9}, 32)); err != nil {
		t.Errorf("insertion bien chaînée refusée : %v", err)
	}
}
