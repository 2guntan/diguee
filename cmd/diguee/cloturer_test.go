package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/base"
	"github.com/2guntan/diguee/internal/base/basetest"
	"github.com/2guntan/diguee/internal/cloture"
	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/empreinte"
	"github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/publication"
	"github.com/2guntan/diguee/internal/questionnaire"
	"github.com/2guntan/diguee/internal/sauvegarde/sauvegardetest"
)

type tamponVide struct{}

func (tamponVide) Fermer(context.Context) error { return nil }

// « diguee cloturer » en développement (empreinteur fake, sans volume
// chiffré, sauvegardes dans un dossier) : procédure complète, puis relance
// sans effet.
func TestCommandeCloturer(t *testing.T) {
	sauvegardetest.Programmes(t)
	ctx := context.Background()
	url := basetest.NouvelleBase(t)
	b, err := base.Ouvrir(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Fermer()
	if _, err := b.Migrer(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.EcrireLot(ctx, time.Date(2026, 6, 30, 9, 0, 0, 0, time.UTC), []empreinte.Empreinte{{7}, {8}},
		[]questionnaire.Reponse{{Version: 1, Q3: 1}, {Version: 1, Q3: 2}}); err != nil {
		t.Fatal(err)
	}
	_, dest := sauvegardetest.Cles(t)
	dir := t.TempDir()
	pub := filepath.Join(dir, "sauvegarde.age.pub")
	if err := os.WriteFile(pub, []byte(dest+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"DIGUEE_POSTGRES_URL":            url,
		"DIGUEE_OUVERTURE":               "2026-01-01",
		"DIGUEE_CLOTURE":                 "2026-07-01",
		"DIGUEE_SAUVEGARDE_STOCKAGE":     "fake",
		"DIGUEE_SAUVEGARDE_DOSSIER":      filepath.Join(dir, "compartiment"),
		"DIGUEE_SAUVEGARDE_POSTGRES_URL": url,
		"DIGUEE_SAUVEGARDE_CLE_PUBLIQUE": pub,
		"DIGUEE_SAUVEGARDE_TEMPORAIRE":   t.TempDir(),
	}
	getenv := func(k string) string { return env[k] }
	var sortie bytes.Buffer
	// Sans la fermeture du serveur : refus, rien n'est supprimé.
	if err := cloturer(ctx, []string{"-sans-volume"}, getenv, &sortie, silencieux()); err == nil {
		t.Fatal("clôture acceptée sans la fermeture")
	}
	if existe, _ := b.TableExiste(ctx, "participation"); !existe {
		t.Fatal("participation supprimée")
	}
	// La fermeture, comme le serveur la fait.
	publieur := &publication.Publieur{Magasin: b, Questionnaire: questionnaire.Courant(), SeuilPublication: 1, SeuilCase: 1,
		Rendu: func(*publication.Instantane) ([]byte, error) { return []byte("<p></p>"), nil }}
	fe := &cloture.Fermeture{Cloture: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), Heure: 6 * time.Hour,
		Tampon: tamponVide{}, Publieur: publieur, Reponses: b, Journal: journal.Nouveau(b, nil), Log: silencieux()}
	if bf, err := fe.Executer(ctx); err != nil || !bf.Faite {
		t.Fatalf("fermeture : %+v %v", bf, err)
	}
	sortie.Reset()
	if err := cloturer(ctx, []string{"-sans-volume"}, getenv, &sortie, silencieux()); err != nil {
		t.Fatalf("clôture : %v\n%s", err, sortie.String())
	}
	if !strings.Contains(sortie.String(), "CLÔTURE TERMINÉE") || !strings.Contains(sortie.String(), "procès-verbal inscrit") {
		t.Errorf("sortie :\n%s", sortie.String())
	}
	if existe, _ := b.TableExiste(ctx, "participation"); existe {
		t.Error("participation existe encore")
	}
	sortie.Reset()
	if err := cloturer(ctx, nil, getenv, &sortie, silencieux()); err != nil || !strings.Contains(sortie.String(), "déjà terminée") {
		t.Errorf("relance : %v\n%s", err, sortie.String())
	}
}

// En production : ni -sans-volume, ni empreinteur fake, ni stockage local,
// et l'identité de clôture du KMS est obligatoire.
func TestCommandeCloturerProduction(t *testing.T) {
	commun := map[string]string{
		"DIGUEE_ENV": "production", "DIGUEE_POSTGRES_URL": "postgres://localhost/diguee",
		"DIGUEE_OUVERTURE": "2026-01-01", "DIGUEE_CLOTURE": "2026-07-01",
	}
	cas := []struct {
		args    []string
		extra   map[string]string
		attendu string
	}{
		{[]string{"-sans-volume"}, nil, ""},
		{nil, nil, "DIGUEE_POSTGRES_URL_MIGRATIONS"},
		{nil, map[string]string{"DIGUEE_POSTGRES_URL_MIGRATIONS": "postgres://localhost/diguee", "DIGUEE_SAUVEGARDE_POSTGRES_URL": "x",
			"DIGUEE_SAUVEGARDE_CLE_PUBLIQUE": "x"}, "DIGUEE_SAUVEGARDE_STOCKAGE"},
	}
	for _, c := range cas {
		env := map[string]string{}
		for k, v := range commun {
			env[k] = v
		}
		for k, v := range c.extra {
			env[k] = v
		}
		var sortie bytes.Buffer
		err := cloturer(context.Background(), c.args, func(k string) string { return env[k] }, &sortie, silencieux())
		if err == nil || !strings.Contains(err.Error(), c.attendu) {
			t.Errorf("%v %v : %v", c.args, c.extra, err)
		}
	}
	// L'identité de clôture du KMS est exigée avec DIGUEE_EMPREINTEUR=ovh.
	if _, err := config.ChargerCloture(func(string) string { return "" }, true); err == nil || !strings.Contains(err.Error(), "DIGUEE_CLOTURE_KMS_CERTIFICAT") {
		t.Errorf("identité de clôture : %v", err)
	}
}
