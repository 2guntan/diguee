package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/base/basetest"
	"github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/publication"
	"github.com/2guntan/diguee/internal/questionnaire"
)

// « diguee publier » produit la publication due une seule fois ; un second
// appel ne change rien. Le fichier de population est lu et contrôlé.
func TestCommandePublier(t *testing.T) {
	b, sc := basetest.OuvrirMigree(t)
	valeursCompletes(t)
	t.Setenv("DIGUEE_POSTGRES_URL", sc.URL)
	t.Setenv("DIGUEE_SEUIL_PUBLICATION", "10")
	ctx := context.Background()
	// 40 réponses écrites il y a deux jours : toujours antérieures au jour dû.
	rs := make([]questionnaire.Reponse, 40)
	for i := range rs {
		rs[i] = questionnaire.Reponse{Version: 1, Q3: int16(i % 5), Q7: []int16{}, Themes: []int16{}, Region: 1}
	}
	if err := b.EcrireLot(ctx, time.Now().UTC().AddDate(0, 0, -2), nil, rs); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	mauvais := filepath.Join(dir, "mauvais.json")
	_ = os.WriteFile(mauvais, []byte(`{"source": "ANSD"}`), 0o600)
	t.Setenv("DIGUEE_POPULATION_ANSD", mauvais)
	if err := lancer([]string{"publier"}, silencieux()); err == nil || !strings.Contains(err.Error(), "DIGUEE_POPULATION_ANSD") {
		t.Fatalf("fichier de population incomplet accepté : %v", err)
	}
	bon := filepath.Join(dir, "population.json")
	_ = os.WriteFile(bon, []byte(`{"source": "ANSD, essai", "region": {"1": 1, "2": 1, "3": 1, "4": 1, "5": 1, "6": 1, "7": 1,
		"8": 1, "9": 1, "10": 1, "11": 1, "12": 1, "13": 1, "14": 1},
		"tranche_age": {"1": 1, "2": 1, "3": 1, "4": 1, "5": 1}, "sexe": {"1": 1, "2": 1}}`), 0o600)
	t.Setenv("DIGUEE_POPULATION_ANSD", bon)
	for range 2 {
		if err := lancer([]string{"publier"}, silencieux()); err != nil {
			t.Fatal(err)
		}
	}
	rsm, err := b.Resumes(ctx)
	if err != nil || len(rsm) != 1 || !rsm[0].ResultatsPublies || rsm[0].Reponses != 40 ||
		!rsm[0].Jour.Equal(publication.JourDu(time.Now(), 6*time.Hour)) {
		t.Fatalf("publications : %+v, %v", rsm, err)
	}
	p, _ := b.LirePublication(ctx, rsm[0].Jour)
	if inst, err := p.LireInstantane(); err != nil || inst.SourcePopulation != "ANSD, essai" {
		t.Errorf("source de la population : %+v, %v", inst, err)
	}
	es, _ := journal.Nouveau(b, nil).Toutes(ctx)
	if len(duType(es, journal.PublicationQuotidienne)) != 1 {
		t.Errorf("%d entrées au journal", len(es))
	}
}
