package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/i18n"
	"github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/journal/journaltest"
)

func serveurJournal(t *testing.T, n int) (*Serveur, *journal.Journal) {
	t.Helper()
	cfg, err := config.Charger(func(k string) string {
		return map[string]string{"DIGUEE_POSTGRES_URL": "postgres://localhost/diguee"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	cs, _ := i18n.Charger()
	h := time.Date(2026, 11, 23, 6, 0, 0, 0, time.UTC)
	j := journal.Nouveau(&journaltest.Memoire{}, func() time.Time { h = h.Add(time.Hour); return h })
	ctx := context.Background()
	for i := range n {
		var err error
		switch i % 3 {
		case 0:
			_, err = j.Inscrire(ctx, journal.PublicationQuotidienne, map[string]any{
				"jour": "2027-01-16", "total": 12345, "instantane_sha256": strings.Repeat("7", 64),
				"par_region": map[string]int{"dakar": 4000, "thies": 2000}})
		case 1:
			_, err = j.Inscrire(ctx, journal.MiseEnProduction, journal.ContenuMiseEnProduction{
				Version: "v1.0." + strings.Repeat("9", i%5), SHA256Executable: strings.Repeat("ab", 32)})
		default:
			_, err = j.Inscrire(ctx, journal.Methodologie, map[string]any{"parametre": "SEUIL_CASE", "valeur": 30})
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	s, err := Nouveau(Options{
		Config: cfg, Catalogues: cs, Compteur: compteurFixe{},
		Version: "v0.0.0-test", SHA256: strings.Repeat("ab", 32),
		Journal:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		JournalPublic: j,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, j
}

func TestPageJournal(t *testing.T) {
	s, j := serveurJournal(t, 7)
	es, _ := j.Toutes(context.Background())
	res, corps := get(t, s, "/journal")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("statut %d", res.StatusCode)
	}
	c := texte(corps)
	for _, attendu := range []string{
		"Journal public", "Empreinte de la dernière entrée (nº 7)", es[6].Empreinte.String(),
		`href="/journal.json"`, journal.CommandeVerification,
		"Entrée nº 7", "Entrée nº 1", "Publication quotidienne", "Mise en production", "Méthodologie",
		"Version du code", "SHA-256 de l'exécutable", "12 345", `{"dakar":4000,"thies":2000}`,
		"23 novembre 2026, 07:00 GMT", `datetime="2026-11-23T07:00:00Z"`,
		es[0].EmpreintePrecedente.String(),
	} {
		if !strings.Contains(c, attendu) {
			t.Errorf("/journal ne contient pas %q", attendu)
		}
	}
	// Les plus récentes d'abord.
	if strings.Index(c, "Entrée nº 7") > strings.Index(c, "Entrée nº 1") {
		t.Error("les entrées les plus récentes doivent venir en premier")
	}
	if strings.Contains(c, "journal.page") || strings.Contains(c, "Page 1 sur") {
		t.Error("pas de pagination pour une seule page")
	}

	vide, _ := serveurJournal(t, 0)
	if _, corps := get(t, vide, "/journal"); !strings.Contains(texte(corps), "aucune entrée") {
		t.Error("journal vide mal affiché")
	}
}

// La page reste sous 100 Ko, sans script, quelle que soit la taille du journal.
func TestPageJournalLegereEtPaginee(t *testing.T) {
	s, j := serveurJournal(t, 2*EntreesParPage+7)
	_, css := get(t, s, "/statique/diguee.css")
	_, logo := get(t, s, "/statique/logo.svg")
	tete, _ := j.Lire(context.Background(), 2*EntreesParPage+7, 2*EntreesParPage+7)
	for page, attendu := range map[string][]string{
		"/journal":        {"Page 1 sur 3", `href="/journal?page=2"`, "Entrée nº 107", "Entrée nº 58"},
		"/journal?page=2": {"Page 2 sur 3", `href="/journal?page=1"`, `href="/journal?page=3"`, "Entrée nº 57", "Entrée nº 8"},
		"/journal?page=3": {"Page 3 sur 3", `href="/journal?page=2"`, "Entrée nº 7", "Entrée nº 1"},
	} {
		res, corps := get(t, s, page)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s : statut %d", page, res.StatusCode)
		}
		c := texte(corps)
		for _, a := range append(attendu, tete[0].Empreinte.String()) {
			if !strings.Contains(c, a) {
				t.Errorf("%s ne contient pas %q", page, a)
			}
		}
		if n := strings.Count(corps, "<article"); n > EntreesParPage {
			t.Errorf("%s : %d entrées", page, n)
		}
		if poids := len(corps) + len(css) + len(logo); poids >= 100*1024 {
			t.Errorf("%s pèse %d octets", page, poids)
		}
		bas := strings.ToLower(corps)
		for _, interdit := range []string{"<script", "javascript:", " onload=", " onclick=", "https://", "http://"} {
			if strings.Contains(bas, interdit) {
				t.Errorf("%s contient %q", page, interdit)
			}
		}
		if strings.Contains(corps, "⟦") {
			t.Errorf("%s utilise une clé absente du catalogue", page)
		}
	}
	for _, p := range []string{"/journal?page=0", "/journal?page=4", "/journal?page=x"} {
		if res, _ := get(t, s, p); res.StatusCode != http.StatusNotFound {
			t.Errorf("%s : statut %d", p, res.StatusCode)
		}
	}
}

func TestExportJournal(t *testing.T) {
	s, j := serveurJournal(t, 60)
	res, corps := get(t, s, "/journal.json")
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("statut %d, %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	es, rap, err := journal.LireExport(strings.NewReader(corps))
	if err != nil {
		t.Fatal(err)
	}
	toutes, _ := j.Toutes(context.Background())
	if rap.Entrees != 60 || len(es) != 60 || rap.Tete != toutes[59].Empreinte {
		t.Errorf("export incomplet : %+v", rap)
	}
}

func TestTypesDuJournalTraduits(t *testing.T) {
	cs, _ := i18n.Charger()
	fr := cs.Choisir("fr")
	for _, ty := range journal.Types() {
		if !fr.Existe("journal.type." + string(ty)) {
			t.Errorf("type %s sans libellé dans le catalogue fr", ty)
		}
	}
}
