package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/base"
	"github.com/2guntan/diguee/internal/base/basetest"
	"github.com/2guntan/diguee/internal/empreinte"
	"github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/questionnaire"
	"github.com/2guntan/diguee/internal/sauvegarde"
	"github.com/2guntan/diguee/internal/sauvegarde/sauvegardetest"
)

// construireExecutable compile diguee pour les scripts de deploiement/.
func construireExecutable(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "diguee")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build : %v\n%s", err, out)
	}
	return bin
}

// Parcours complet du ticket #15 : « diguee sauvegarde faire » sur une vraie
// base, puis restauration de la plus récente par le script documenté
// (deploiement/restaurer-sauvegarde.sh), avec contrôle des lignes et de la
// chaîne du journal public, comparée au journal « publié ».
func TestCommandeSauvegardeEtScriptDeRestauration(t *testing.T) {
	sauvegardetest.Programmes(t, "age", "age-keygen", "pg_dump", "pg_restore", "psql", "bash")
	ctx := context.Background()
	source := basetest.NouvelleBase(t)
	b, err := base.Ouvrir(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Fermer()
	if _, err := b.Migrer(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.EcrireLot(ctx, time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC), []empreinte.Empreinte{{7}, {8}},
		[]questionnaire.Reponse{{Version: 1, Q1: 1}, {Version: 1, Q1: 2}}); err != nil {
		t.Fatal(err)
	}
	j := journal.Nouveau(b, func() time.Time { return time.Now().UTC() })
	for i := range 3 {
		if _, err := j.Inscrire(ctx, journal.Methodologie, map[string]any{"rang": i}); err != nil {
			t.Fatal(err)
		}
	}

	identite, dest := sauvegardetest.Cles(t)
	dir := t.TempDir()
	pub := filepath.Join(dir, "sauvegarde.age.pub")
	os.WriteFile(pub, []byte(dest+"\n"), 0o644)
	compartiment := filepath.Join(dir, "compartiment")
	etat := filepath.Join(dir, "etat.json")
	env := map[string]string{
		"DIGUEE_SAUVEGARDE_STOCKAGE":     "fake",
		"DIGUEE_SAUVEGARDE_DOSSIER":      compartiment,
		"DIGUEE_SAUVEGARDE_POSTGRES_URL": source,
		"DIGUEE_SAUVEGARDE_CLE_PUBLIQUE": pub,
		"DIGUEE_SAUVEGARDE_ETAT":         etat,
		"DIGUEE_SAUVEGARDE_TEMPORAIRE":   t.TempDir(),
	}
	getenv := func(k string) string { return env[k] }
	var sortie bytes.Buffer
	if err := sauvegarder(ctx, []string{"faire"}, getenv, &sortie, silencieux()); err != nil {
		t.Fatalf("faire : %v\n%s", err, sortie.String())
	}
	if !strings.Contains(sortie.String(), "sauvegarde envoyée : sauvegardes/diguee-") {
		t.Errorf("sortie %s", sortie.String())
	}
	if e, err := sauvegarde.LireEtat(etat); err != nil || e == nil || !e.Reussie || e.EnAlerte(time.Now()) {
		t.Errorf("état %+v %v", e, err)
	}
	sortie.Reset()
	if err := sauvegarder(ctx, []string{"lister"}, getenv, &sortie, silencieux()); err != nil || !strings.Contains(sortie.String(), "1 sauvegarde(s)") {
		t.Errorf("lister : %v\n%s", err, sortie.String())
	}

	// Le journal « publié » continue après la sauvegarde.
	if _, err := j.Inscrire(ctx, journal.Methodologie, map[string]any{"rang": "après"}); err != nil {
		t.Fatal(err)
	}
	es, err := j.Toutes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	publie := filepath.Join(dir, "journal-publie.json")
	f, _ := os.Create(publie)
	journal.Exporter(f, es)
	f.Close()

	bin := construireExecutable(t)
	script, _ := filepath.Abs("../../deploiement/restaurer-sauvegarde.sh")
	lancerScript := func(args ...string) (string, error) {
		cmd := exec.Command("bash", append([]string{script}, args...)...)
		cmd.Env = append(os.Environ(), "DIGUEE_SAUVEGARDE_STOCKAGE=fake", "DIGUEE_SAUVEGARDE_DOSSIER="+compartiment)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	cible := basetest.NouvelleBase(t)
	out, err := lancerScript("--identite", identite, "--base", cible, "--diguee", bin, "--comparer-avec", publie)
	if err != nil || !strings.Contains(out, "RESTAURATION RÉUSSIE") {
		t.Fatalf("restauration : %v\n%s", err, out)
	}
	for _, attendu := range []string{"participation                2", "journal_public               3",
		"journal public valide : 3 entrée(s)", "les 3 entrées de l'export précédent sont inchangées"} {
		if !strings.Contains(out, attendu) {
			t.Errorf("sortie sans %q :\n%s", attendu, out)
		}
	}
	// Base cible non vide : refus.
	if out, err := lancerScript("--identite", identite, "--base", cible, "--diguee", bin); err == nil || !strings.Contains(out, "n'est pas vide") {
		t.Errorf("base non vide : %v\n%s", err, out)
	}
	// Mauvaise clé privée : échec, rien n'est restauré.
	autre, _ := sauvegardetest.Cles(t)
	vide := basetest.NouvelleBase(t)
	if out, err := lancerScript("--identite", autre, "--base", vide, "--diguee", bin); err == nil || !strings.Contains(out, "RESTAURATION EN ÉCHEC") {
		t.Errorf("mauvaise clé : %v\n%s", err, out)
	}

	// Clôture : suppression de toutes les sauvegardes, confirmée.
	if err := sauvegarder(ctx, []string{"supprimer-toutes"}, getenv, &sortie, silencieux()); err == nil {
		t.Error("supprimer-toutes sans -confirmer")
	}
	sortie.Reset()
	if err := sauvegarder(ctx, []string{"supprimer-toutes", "-confirmer"}, getenv, &sortie, silencieux()); err != nil ||
		!strings.Contains(sortie.String(), "1 objet(s) supprimé(s)") {
		t.Errorf("supprimer-toutes : %v %s", err, sortie.String())
	}
}

// En production, un dossier local n'est pas une sauvegarde : refusé, et
// l'échec est inscrit dans l'état.
func TestCommandeSauvegardeProductionRefuseLeDossier(t *testing.T) {
	etat := filepath.Join(t.TempDir(), "etat.json")
	env := map[string]string{"DIGUEE_ENV": "production", "DIGUEE_SAUVEGARDE_ETAT": etat}
	err := sauvegarder(context.Background(), []string{"faire"}, func(k string) string { return env[k] }, &bytes.Buffer{}, silencieux())
	if err == nil || !strings.Contains(err.Error(), "DIGUEE_SAUVEGARDE_STOCKAGE vaut fake") ||
		!strings.Contains(err.Error(), "DIGUEE_SAUVEGARDE_POSTGRES_URL est obligatoire") {
		t.Fatalf("%v", err)
	}
	if e, _ := sauvegarde.LireEtat(etat); e == nil || e.Reussie || !e.EnAlerte(time.Now()) {
		t.Errorf("état %+v", e)
	}
}
