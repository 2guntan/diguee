package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/ancrage"
	"github.com/2guntan/diguee/internal/base/basetest"
	"github.com/2guntan/diguee/internal/journal"
)

// En production, les faux services d'ancrage sont refusés ; sans réglage,
// le mode réel (sans compte) est choisi.
func TestProductionRefuseLAncrageFactice(t *testing.T) {
	valeursCompletes(t)
	t.Setenv("DIGUEE_ENV", "production")
	t.Setenv("DIGUEE_POSTGRES_URL", "postgres://personne@127.0.0.1:1/aucune")
	t.Setenv("DIGUEE_ANCRAGE", "fake")
	err := lancer(nil, silencieux())
	if err == nil || !strings.Contains(err.Error(), "l'intégration ancrage est en mode fake (réglage : DIGUEE_ANCRAGE)") {
		t.Fatalf("refus attendu : %v", err)
	}
	t.Setenv("DIGUEE_ANCRAGE", "")
	err = lancer(nil, silencieux())
	if err == nil || strings.Contains(err.Error(), "ancrage") {
		t.Fatalf("le mode réel doit être admis en production : %v", err)
	}
}

// Au démarrage, l'ancrage du jour qui manque est rattrapé en tâche de fond
// (faux services en développement) et publié à côté du journal ; « diguee
// ancrer » n'y ajoute rien.
func TestAncrageAuDemarrage(t *testing.T) {
	b, sc := basetest.OuvrirMigree(t)
	valeursCompletes(t)
	t.Setenv("DIGUEE_POSTGRES_URL", sc.URL)
	racine, arreter := demarrer(t)
	var liste []struct {
		Jour      string            `json:"jour"`
		Empreinte string            `json:"empreinte"`
		Preuve    string            `json:"preuve_ots"`
		Archives  map[string]string `json:"archives"`
	}
	for fin := time.Now().Add(10 * time.Second); len(liste) == 0 || len(liste[0].Archives) < 2; {
		if time.Now().After(fin) {
			t.Fatalf("aucun ancrage : %+v", liste)
		}
		time.Sleep(50 * time.Millisecond)
		res, err := http.Get(racine + "/journal/ancrages.json")
		if err != nil {
			t.Fatal(err)
		}
		_ = json.NewDecoder(res.Body).Decode(&liste)
		res.Body.Close()
	}
	res, err := http.Get(racine + liste[0].Preuve)
	if err != nil {
		t.Fatal(err)
	}
	ots, _ := io.ReadAll(res.Body)
	res.Body.Close()
	f, err := ancrage.LireFichier(ots)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("preuve servie : %d, %v", res.StatusCode, err)
	}
	es, _ := journal.Nouveau(b, nil).Toutes(context.Background())
	if tete := es[len(es)-1].Empreinte; liste[0].Empreinte != tete.String() || string(f.Condensat) != string(tete[:]) {
		t.Errorf("tête ancrée %s, tête du journal %s", liste[0].Empreinte, tete)
	}
	res, err = http.Get(racine + "/journal")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(page), `href="`+liste[0].Preuve+`"`) || !strings.Contains(string(page), "archive.diguee.invalid") {
		t.Error("/journal n'affiche pas l'ancrage du jour")
	}
	if err := arreter(); err != nil {
		t.Fatal(err)
	}
	if err := lancer([]string{"ancrer"}, silencieux()); err != nil {
		t.Fatal(err)
	}
	etats, err := b.Ancrages(context.Background(), 10)
	if err != nil || len(etats) != 1 || etats[0].Preuve == nil || etats[0].Preuve.Revision != 0 || len(etats[0].Archives) != 2 {
		t.Fatalf("ancrages : %+v, %v", etats, err)
	}
}
