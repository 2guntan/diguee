package equipe_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"html"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/2guntan/diguee/internal/equipe"
	"github.com/2guntan/diguee/internal/journal"
)

// televerser envoie le formulaire d'une source, avec son fichier.
func (c *client) televerser(champs map[string]string, fichier []byte) reponse {
	c.t.Helper()
	var corps bytes.Buffer
	mw := multipart.NewWriter(&corps)
	for k, v := range champs {
		_ = mw.WriteField(k, v)
	}
	if fichier != nil {
		f, _ := mw.CreateFormFile("fichier", "copie.bin")
		_, _ = f.Write(fichier)
	}
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/equipe/observatoire/sources", &corps)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return c.faire(req)
}

var reLienArchive = regexp.MustCompile(`href="(/observatoire/archives/([0-9a-f]{64})\.pdf)"`)

func texteDe(corps string) string { return html.UnescapeString(corps) }

func attendre(t *testing.T, quoi string, p reponse, statut int, contient ...string) reponse {
	t.Helper()
	if p.statut != statut {
		t.Fatalf("%s : statut %d (attendu %d) %s\n%s", quoi, p.statut, statut, p.location, p.corps)
	}
	for _, c := range contient {
		if !strings.Contains(texteDe(p.corps), c) {
			t.Fatalf("%s : %q absent de la page", quoi, c)
		}
	}
	if strings.Contains(p.corps, "⟦") || strings.Contains(p.corps, "%!") {
		t.Fatalf("%s : clé absente ou mise en forme cassée", quoi)
	}
	return p
}

// Le circuit complet par les formulaires de l'espace équipe, puis les pages
// publiques : saisie, validation à deux (T7 par l'API web), publication
// inscrite au journal, « non tenu » non publiable, historique dans l'ordre,
// lien d'origine et copie archivée sous son SHA-256.
func TestObservatoireDeBoutEnBout(t *testing.T) {
	b := nouveauBanc(t)
	ctx := context.Background()
	awa := b.creer(t, "awa", equipe.Redacteur, equipe.Relecteur)
	moussa := b.creer(t, "moussa", equipe.Relecteur, equipe.Redacteur)
	admin := b.creer(t, "admin", equipe.Administrateur)
	ca, cm, cadm := b.client(t), b.client(t), b.client(t)
	ca.connecterCompte(awa)
	b.horloge.Avancer(30e9) // un code TOTP par connexion et par pas
	cm.connecterCompte(moussa)
	cadm.connecterCompte(admin)
	jeton := ca.csrf("/equipe/observatoire")
	jetonM := cm.csrf("/equipe/observatoire")

	// Rôles : l'administrateur seul n'a pas accès à l'observatoire.
	attendre(t, "administrateur", cadm.get("/equipe/observatoire"), http.StatusForbidden)
	// CSRF : refus sans jeton.
	attendre(t, "sans CSRF", ca.post("/equipe/observatoire/elus", url.Values{"nom": {"X"}}), http.StatusForbidden)

	// Élu.
	attendre(t, "élu invalide", ca.post("/equipe/observatoire/elus", url.Values{"csrf": {jeton}, "nom": {"Aminata Ndiaye"},
		"fonction": {"Maire"}, "mandat_debut": {"2027-01-01"}, "mandat_fin": {"2022-01-01"}}), http.StatusBadRequest, "date de fin postérieure")
	attendre(t, "élu", ca.post("/equipe/observatoire/elus", url.Values{"csrf": {jeton}, "nom": {"Aminata Ndiaye"},
		"fonction": {"Maire de Thiès"}, "mandat_debut": {"2022-01-23"}, "mandat_fin": {"2027-01-23"},
		"contact": {"Cabinet du maire, contact officiel"}}), http.StatusSeeOther)
	attendre(t, "liste des élus", ca.get("/equipe/observatoire/elus"), http.StatusOK, "Aminata Ndiaye", "Cabinet du maire")

	// Sources : type et taille contrôlés, puis une source PDF.
	pdf := []byte("%PDF-1.4\n% discours du 2 avril 2024\n%%EOF\n")
	sha := sha256.Sum256(pdf)
	champsSource := map[string]string{"csrf": jeton, "titre": "Discours d'installation", "url": "https://exemple.sn/discours?id=1&lang=fr", "date": "2024-04-02"}
	attendre(t, "fichier HTML", ca.televerser(champsSource, []byte("<html><script>alert(1)</script>")), http.StatusBadRequest, "PDF, PNG et JPEG")
	attendre(t, "fichier trop lourd", ca.televerser(champsSource, append([]byte("%PDF-"), make([]byte, 64<<10)...)), http.StatusBadRequest, "trop lourd")
	attendre(t, "fichier démesuré", ca.televerser(champsSource, make([]byte, 200<<10)), http.StatusRequestEntityTooLarge)
	attendre(t, "lien javascript", ca.televerser(map[string]string{"csrf": jeton, "titre": "x", "url": "javascript:alert(1)", "date": "2024-04-02"}, pdf),
		http.StatusBadRequest, "http://")
	attendre(t, "source", ca.televerser(champsSource, pdf), http.StatusSeeOther)
	attendre(t, "preuve PNG", ca.televerser(map[string]string{"csrf": jeton, "titre": "Photo de l'école", "url": "https://exemple.sn/photo", "date": "2026-11-30"},
		[]byte("\x89PNG\r\n\x1a\nimage")), http.StatusSeeOther)
	attendre(t, "liste des sources", ca.get("/equipe/observatoire/sources"), http.StatusOK, hex.EncodeToString(sha[:]))
	if lu, err := b.archives.Lire(ctx, sha); err != nil || !bytes.Equal(lu, pdf) {
		t.Fatalf("archive dans le stockage : %v", err)
	}

	// Engagement : saisi par awa, validé par moussa seulement.
	citation := "Nous construirons dix écoles avant la fin de 2026."
	p := attendre(t, "engagement", ca.post("/equipe/observatoire/engagements", url.Values{"csrf": {jeton}, "elu": {"1"},
		"citation": {citation}, "contexte": {"Discours d'installation du conseil."}, "date_declaration": {"2024-04-02"},
		"echeance": {"2026-12-31"}, "sources": {"1"}}), http.StatusSeeOther)
	if p.location != "/equipe/observatoire/engagements/1?fait=engagement" {
		t.Fatalf("redirection %s", p.location)
	}
	attendre(t, "validation par le rédacteur", ca.post("/equipe/observatoire/engagements/1/valider", url.Values{"csrf": {jeton}}),
		http.StatusBadRequest, "une autre personne que le rédacteur")
	attendre(t, "statut avant validation", ca.post("/equipe/observatoire/engagements/1/statuts", url.Values{"csrf": {jeton},
		"valeur": {"en_cours"}, "explication": {"x"}, "preuves": {"1"}}), http.StatusBadRequest, "d'abord être validé")
	attendre(t, "fiche publique avant publication", ca.get("/observatoire/engagements/1"), http.StatusNotFound)
	attendre(t, "archive publique avant publication", ca.get("/observatoire/archives/"+hex.EncodeToString(sha[:])+".pdf"), http.StatusNotFound)
	attendre(t, "archive dans l'espace équipe", ca.get("/equipe/observatoire/archives/"+hex.EncodeToString(sha[:])+".pdf"), http.StatusOK)
	attendre(t, "validation", cm.post("/equipe/observatoire/engagements/1/valider", url.Values{"csrf": {jetonM}}), http.StatusSeeOther)
	attendre(t, "source après validation", ca.post("/equipe/observatoire/engagements/1/sources", url.Values{"csrf": {jeton}, "source": {"2"}}),
		http.StatusBadRequest, "ses sources ne changent plus")

	// Trois statuts : « en cours », puis « tenu », publiés ; « non tenu »
	// validé mais non publiable.
	publier := func(valeur, explication string, id int) {
		t.Helper()
		attendre(t, "brouillon "+valeur, ca.post("/equipe/observatoire/engagements/1/statuts", url.Values{"csrf": {jeton},
			"valeur": {valeur}, "explication": {explication}}), http.StatusSeeOther)
		chemin := "/equipe/observatoire/statuts/" + string(rune('0'+id))
		attendre(t, "proposition sans preuve", ca.post(chemin+"/proposer", url.Values{"csrf": {jeton}}), http.StatusBadRequest, "au moins une preuve")
		attendre(t, "preuve par un autre", cm.post(chemin+"/preuves", url.Values{"csrf": {jetonM}, "source": {"2"}}), http.StatusBadRequest, "Seul le rédacteur")
		attendre(t, "preuve", ca.post(chemin+"/preuves", url.Values{"csrf": {jeton}, "source": {"2"}}), http.StatusSeeOther)
		attendre(t, "validation d'un brouillon", cm.post(chemin+"/valider", url.Values{"csrf": {jetonM}}), http.StatusBadRequest, "à cette étape")
		attendre(t, "proposition", ca.post(chemin+"/proposer", url.Values{"csrf": {jeton}}), http.StatusSeeOther)
		// T7 par l'API web : le rédacteur ne valide pas sa propre saisie.
		attendre(t, "validation par le rédacteur", ca.post(chemin+"/valider", url.Values{"csrf": {jeton}}), http.StatusBadRequest,
			"une autre personne que le rédacteur")
		attendre(t, "page du statut proposé", ca.get(chemin), http.StatusOK, "un autre membre doit la relire")
		attendre(t, "publication avant validation", cm.post(chemin+"/publier", url.Values{"csrf": {jetonM}}), http.StatusBadRequest, "à cette étape")
		attendre(t, "validation", cm.post(chemin+"/valider", url.Values{"csrf": {jetonM}}), http.StatusSeeOther)
	}
	publier("en_cours", "Quatre écoles sur dix sont ouvertes.", 1)
	attendre(t, "publication", cm.post("/equipe/observatoire/statuts/1/publier", url.Values{"csrf": {jetonM}}), http.StatusSeeOther)
	attendre(t, "double publication", cm.post("/equipe/observatoire/statuts/1/publier", url.Values{"csrf": {jetonM}}), http.StatusBadRequest)
	publier("tenu", "Les dix écoles sont ouvertes.", 2)
	attendre(t, "publication par le rédacteur", ca.post("/equipe/observatoire/statuts/2/publier", url.Values{"csrf": {jeton}}), http.StatusSeeOther)
	publier("non_tenu", "Erreur de test.", 3)
	attendre(t, "page du non tenu validé", ca.get("/equipe/observatoire/statuts/3"), http.StatusOK, "préavis")
	attendre(t, "publication d'un non tenu", cm.post("/equipe/observatoire/statuts/3/publier", url.Values{"csrf": {jetonM}}),
		http.StatusBadRequest, "demande d'abord un préavis")
	attendre(t, "tableau de bord", ca.get("/equipe/observatoire"), http.StatusOK, "Statuts validés, à publier", "Statut nº 3")

	// Journal public : une entrée par publication, rien pour le non tenu.
	es, err := journal.Nouveau(b.base, nil).Toutes(ctx)
	if err != nil || len(es) != 2 {
		t.Fatalf("journal : %d entrée(s), %v", len(es), err)
	}
	for i, e := range es {
		c := string(e.Contenu)
		if e.Type != journal.StatutPublie || !strings.Contains(c, `"engagement":1`) || !strings.Contains(c, hex.EncodeToString(sha[:])) ||
			!strings.Contains(c, `"valide_par":"Personne moussa"`) || !strings.Contains(c, `"statut":`+string(rune('1'+i))) || strings.Contains(c, "écoles") {
			t.Errorf("entrée %d : %s %s", e.Numero, e.Type, c)
		}
	}

	// Pages publiques.
	public := b.client(t)
	liste := attendre(t, "liste publique", public.get("/observatoire"), http.StatusOK, "Aminata Ndiaye", citation, "Tenu", "1 engagement.")
	for _, filtre := range []string{"?statut=tenu", "?elu=1", "?mandat=2022-01-23_2027-01-23", "?elu=1&statut=tenu&page=1"} {
		attendre(t, "filtre "+filtre, public.get("/observatoire"+filtre), http.StatusOK, citation)
	}
	for _, filtre := range []string{"?statut=en_cours", "?statut=non_tenu", "?elu=2", "?mandat=2020-01-01_2025-01-01"} {
		attendre(t, "filtre "+filtre, public.get("/observatoire"+filtre), http.StatusOK, "Aucun engagement publié")
	}
	fiche := attendre(t, "fiche publique", public.get("/observatoire/engagements/1"), http.StatusOK, citation,
		"https://exemple.sn/discours?id=1&lang=fr", "Quatre écoles sur dix", "Les dix écoles", "31 décembre 2026", "Personne moussa", "entrée nº 2")
	if strings.Contains(fiche.corps, "Erreur de test") || strings.Contains(fiche.corps, "Cabinet du maire") {
		t.Error("la fiche publique montre un statut non publié ou le contact de l'élu")
	}
	if i, j := strings.Index(fiche.corps, "Quatre écoles"), strings.Index(fiche.corps, "Les dix écoles"); i < 0 || j < i {
		t.Error("l'historique n'est pas dans l'ordre de publication")
	}
	for nom, p := range map[string]reponse{"liste": liste, "fiche": fiche} {
		if len(p.corps) >= 100*1024 || strings.Contains(strings.ToLower(p.corps), "<script") {
			t.Errorf("%s : %d octets ou script", nom, len(p.corps))
		}
	}
	m := reLienArchive.FindStringSubmatch(fiche.corps)
	if m == nil || m[2] != hex.EncodeToString(sha[:]) {
		t.Fatalf("lien de la copie archivée absent : %v", m)
	}
	rec := httptest.NewRecorder()
	b.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, m[1], nil))
	h := rec.Header()
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), pdf) || sha256.Sum256(rec.Body.Bytes()) != sha {
		t.Fatalf("copie archivée : %d", rec.Code)
	}
	if h.Get("Content-Type") != "application/pdf" || h.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.HasPrefix(h.Get("Content-Disposition"), "attachment;") || !strings.Contains(h.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("en-têtes de la copie : %v", h)
	}
	attendre(t, "archive inconnue", public.get("/observatoire/archives/"+strings.Repeat("0", 64)+".pdf"), http.StatusNotFound)
	attendre(t, "archive mal nommée", public.get("/observatoire/archives/..%2f..%2fetc.pdf"), http.StatusNotFound)
	if p := public.get("/observatoire/archives/" + hex.EncodeToString(sha[:]) + ".png"); p.statut != http.StatusMovedPermanently {
		t.Errorf("mauvaise extension : %d", p.statut)
	}
	// Une copie altérée dans le stockage n'est jamais servie.
	if err := b.archivesAlterer(sha); err != nil {
		t.Fatal(err)
	}
	if p := public.get(m[1]); p.statut != http.StatusServiceUnavailable {
		t.Errorf("copie altérée servie : %d", p.statut)
	}
	attendre(t, "journal public", public.get("/journal"), http.StatusOK, "Statut publié", "Validé par")

	// Toutes les pages de l'espace se rendent sans clé absente ni script.
	for _, chemin := range []string{"/equipe/observatoire", "/equipe/observatoire/elus", "/equipe/observatoire/sources",
		"/equipe/observatoire/engagements", "/equipe/observatoire/engagements/1", "/equipe/observatoire/statuts/1",
		"/equipe/observatoire/statuts/3", "/equipe/"} {
		p := attendre(t, chemin, ca.get(chemin), http.StatusOK)
		if strings.Contains(strings.ToLower(p.corps), "<script") || len(p.corps) > 100_000 {
			t.Errorf("%s : script ou page trop lourde", chemin)
		}
	}
	attendre(t, "statut inconnu", ca.get("/equipe/observatoire/statuts/99"), http.StatusNotFound)
	if j := b.journal.String(); strings.Contains(j, "192.0.2.44") || strings.Contains(j, citation) {
		t.Error("le journal applicatif contient une adresse ou une saisie")
	}
}
