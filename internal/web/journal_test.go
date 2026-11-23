package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/ancrage"
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
		Config: cfg, Catalogues: cs, Publications: compteurFixe{},
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

// ancragesFixes sert des ancrages préparés pour les tests.
type ancragesFixes struct{ etats []ancrage.Etat }

func (a ancragesFixes) Ancrages(_ context.Context, limite int) ([]ancrage.Etat, error) {
	return a.etats[:min(limite, len(a.etats))], nil
}

func (a ancragesFixes) LirePreuve(_ context.Context, jour time.Time) (*ancrage.Preuve, error) {
	for _, e := range a.etats {
		if e.Jour.Equal(jour) {
			return e.Preuve, nil
		}
	}
	return nil, nil
}

// preuveDeTest construit une preuve .ots valide (faux calendriers), complétée
// d'une attestation Bitcoin si bloc > 0.
func preuveDeTest(t *testing.T, jour time.Time, e journal.Entree, bloc uint64) *ancrage.Preuve {
	t.Helper()
	f := &ancrage.Fichier{OpFichier: ancrage.OpSHA256, Condensat: e.Empreinte[:], Horodatage: &ancrage.Horodatage{Message: e.Empreinte[:]}}
	for _, nom := range []string{"a", "b"} {
		h, err := ancrage.FauxCalendrier{Nom: nom}.Soumettre(context.Background(), e.Empreinte[:])
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Horodatage.Fusionner(h); err != nil {
			t.Fatal(err)
		}
	}
	if bloc > 0 {
		f.Horodatage.Feuilles()[0].Noeud.Attester(ancrage.Bitcoin(bloc))
	}
	ots, err := f.Encoder()
	if err != nil {
		t.Fatal(err)
	}
	return &ancrage.Preuve{Jour: jour, Numero: e.Numero, Empreinte: e.Empreinte, OTS: ots, Calendriers: 2, Bitcoin: bloc > 0}
}

// /journal montre les derniers ancrages (preuve .ots, liens archive.org) ;
// les preuves sont servies telles quelles, et la liste complète en JSON.
func TestAncragesSurLeJournal(t *testing.T) {
	_, j := serveurJournal(t, EntreesParPage+3)
	es, _ := j.Toutes(context.Background())
	var etats []ancrage.Etat
	for i := range 9 {
		jour := time.Date(2026, 11, 23-i, 0, 0, 0, 0, time.UTC)
		e := ancrage.Etat{Jour: jour, Preuve: preuveDeTest(t, jour, es[len(es)-1-i], uint64(870000*min(1, i)+i)), Archives: []ancrage.Archive{
			{Jour: jour, Cible: ancrage.CibleJournal, Lien: "https://web.archive.org/web/202611" + strings.Repeat("0", 8) + "/https://consultation.diguee.sn/journal"},
			{Jour: jour, Cible: ancrage.CibleExport, Lien: "https://web.archive.org/web/202611" + strings.Repeat("1", 8) + "/https://consultation.diguee.sn/journal.json"},
		}}
		etats = append(etats, e)
	}
	etats = append(etats, ancrage.Etat{Jour: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}) // archives seules, sans preuve
	cfg, _ := config.Charger(func(k string) string {
		return map[string]string{"DIGUEE_POSTGRES_URL": "postgres://localhost/diguee"}[k]
	})
	cs, _ := i18n.Charger()
	s, err := Nouveau(Options{Config: cfg, Catalogues: cs, Publications: compteurFixe{}, Version: "v0.0.0-test",
		SHA256: strings.Repeat("ab", 32), Journal: slog.New(slog.NewTextHandler(io.Discard, nil)),
		JournalPublic: j, Ancrages: ancragesFixes{etats}})
	if err != nil {
		t.Fatal(err)
	}
	res, corps := get(t, s, "/journal")
	c := texte(corps)
	for _, attendu := range []string{
		"Ancrage hors de nos serveurs", "23 novembre 2026", `href="/journal/ancrages/2026-11-23.ots"`,
		"entrée nº\u00a053", es[len(es)-1].Empreinte.String(), "en attente de son bloc Bitcoin",
		"inscrite dans le bloc Bitcoin nº\u00a0" + cs.Choisir("fr").Nombre(870001), "page archivée sur archive.org", "export archivé sur archive.org",
		`href="https://web.archive.org/web/20261100000000/https://consultation.diguee.sn/journal"`,
		ancrage.CommandeVerification[:20], "ots verify -d", `href="/journal/ancrages.json"`,
	} {
		if !strings.Contains(c, attendu) && !strings.Contains(corps, attendu) {
			t.Errorf("/journal ne contient pas %q", attendu)
		}
	}
	if strings.Contains(c, "16 novembre 2026") || !strings.Contains(c, "17 novembre 2026") {
		t.Errorf("plus de %d jours affichés", AncragesAffiches)
	}
	_, css := get(t, s, "/statique/diguee.css")
	if res.StatusCode != http.StatusOK || len(corps)+len(css) >= 100*1024 || strings.Contains(corps, "⟦") || strings.Contains(corps, "%!") {
		t.Fatalf("/journal : statut %d, %d octets", res.StatusCode, len(corps)+len(css))
	}

	res, ots := get(t, s, "/journal/ancrages/2026-11-22.ots")
	if res.StatusCode != http.StatusOK || ots != string(etats[1].Preuve.OTS) ||
		!strings.Contains(res.Header.Get("Content-Disposition"), "diguee-journal-2026-11-22.ots") {
		t.Fatalf(".ots : statut %d, %s", res.StatusCode, res.Header)
	}
	f, err := ancrage.LireFichier([]byte(ots))
	if err != nil || !bytes.Equal(f.Condensat, etats[1].Preuve.Empreinte[:]) {
		t.Fatalf(".ots servi illisible : %v", err)
	}
	for _, p := range []string{"/journal/ancrages/2026-11-01.ots", "/journal/ancrages/2025-01-01.ots",
		"/journal/ancrages/2026-11-23.txt", "/journal/ancrages/hier.ots"} {
		if res, _ := get(t, s, p); res.StatusCode != http.StatusNotFound {
			t.Errorf("%s : statut %d", p, res.StatusCode)
		}
	}

	res, js := get(t, s, "/journal/ancrages.json")
	var liste []ExportAncrage
	if err := json.Unmarshal([]byte(js), &liste); err != nil || res.StatusCode != http.StatusOK || len(liste) != 10 {
		t.Fatalf("ancrages.json : %v, %d", err, len(liste))
	}
	if l := liste[1]; l.Jour != "2026-11-22" || l.Preuve != "/journal/ancrages/2026-11-22.ots" || l.Entree != es[len(es)-2].Numero ||
		len(l.Bitcoin) != 1 || l.Archives["/journal.json"] == "" || liste[9].Preuve != "" {
		t.Errorf("ancrages.json : %+v", liste[:2])
	}
}
