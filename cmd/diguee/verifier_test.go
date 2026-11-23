package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/journal/journaltest"
)

func exportDeTest(t *testing.T, n int) (string, []journal.Entree) {
	t.Helper()
	h := time.Date(2027, 1, 15, 6, 0, 0, 0, time.UTC)
	j := journal.Nouveau(&journaltest.Memoire{}, func() time.Time { h = h.Add(24 * time.Hour); return h })
	for i := range n {
		if _, err := j.Inscrire(context.Background(), journal.PublicationQuotidienne, map[string]int{"jour": i, "total": 1000 + i}); err != nil {
			t.Fatal(err)
		}
	}
	es, _ := j.Toutes(context.Background())
	var buf bytes.Buffer
	if err := journal.Exporter(&buf, es); err != nil {
		t.Fatal(err)
	}
	return buf.String(), es
}

func ecrire(t *testing.T, nom, contenu string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), nom)
	if err := os.WriteFile(p, []byte(contenu), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func verifier(args ...string) (string, error) {
	var sortie bytes.Buffer
	err := verifierJournal(context.Background(), args, &sortie)
	return sortie.String(), err
}

// T11 avec la commande publiée : chaîne intacte validée, modification d'une
// entrée ancienne détectée, depuis un fichier ou une adresse.
func TestCommandeVerifierJournal(t *testing.T) {
	export, es := exportDeTest(t, 8)
	fichier := ecrire(t, "journal.json", export)
	sortie, err := verifier(fichier)
	if err != nil || !strings.Contains(sortie, "journal valide : 8 entrées") || !strings.Contains(sortie, es[7].Empreinte.String()) {
		t.Fatalf("%v : %s", err, sortie)
	}

	// Modification d'une entrée ancienne : le total de l'entrée 2.
	ancien := `"total": 1001`
	if !strings.Contains(export, ancien) {
		t.Fatalf("export inattendu :\n%s", export)
	}
	modifie := ecrire(t, "modifie.json", strings.Replace(export, ancien, `"total": 1501`, 1))
	if _, err := verifier(modifie); err == nil || !strings.Contains(err.Error(), "entrée 2") {
		t.Errorf("modification non détectée : %v", err)
	}

	// Ancrage : empreinte publiée et export précédent.
	if _, err := verifier("-attendu", es[3].Empreinte.String(), "-precedent", fichier, fichier); err != nil {
		t.Errorf("ancrage valide refusé : %v", err)
	}
	if _, err := verifier("-attendu", strings.Repeat("5", 64), fichier); err == nil {
		t.Error("empreinte absente non signalée")
	}
	court, _ := exportDeTest(t, 5)
	if _, err := verifier("-precedent", fichier, ecrire(t, "court.json", court)); err == nil {
		t.Error("journal raccourci non signalé")
	}

	// Par une adresse.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/journal.json" {
			_, _ = w.Write([]byte(export))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	if sortie, err := verifier(srv.URL + "/journal.json"); err != nil || !strings.Contains(sortie, "journal valide") {
		t.Errorf("par adresse : %v %s", err, sortie)
	}
	if _, err := verifier(srv.URL + "/absent"); err == nil {
		t.Error("statut 404 non signalé")
	}
	if _, err := verifier(); err == nil {
		t.Error("argument manquant non signalé")
	}
}
