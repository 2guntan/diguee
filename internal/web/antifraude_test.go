package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/altcha"
	"github.com/2guntan/diguee/internal/antifraude"
)

// Sans solution ALTCHA valide côté serveur, aucun jeton n'est créé (ni
// session) ; une solution déjà utilisée est refusée.
func TestSansCaptchaValideAucunJeton(t *testing.T) {
	b := nouveauBanc(t, map[string]string{})
	n := &navigateur{t: t, h: b.srv}
	page := n.get("/verifier")
	solution := resoudreAltcha(t, page.corps)

	// Sans JavaScript : pas de solution, page d'explication.
	if p := n.post("/verifier", url.Values{}); p.statut != http.StatusSeeOther || p.location != "/verifier/sans-javascript" {
		t.Errorf("sans solution : %d vers %q", p.statut, p.location)
	}
	if p := n.get("/verifier/sans-javascript"); p.statut != http.StatusOK || !strings.Contains(p.corps, `href="/verifier/sms"`) {
		t.Errorf("page sans JavaScript : %d", p.statut)
	}
	// Solutions fausses.
	autre, _ := altcha.Nouveau(altcha.Options{Cle: []byte(strings.Repeat("z", 32)), Difficulte: 10})
	d, _ := autre.NouveauDefi()
	signeeAilleurs, _ := altcha.Resoudre(d)
	for nom, v := range map[string]string{"illisible": "AAAA", "signée par une autre clé": signeeAilleurs} {
		p := n.post("/verifier", url.Values{altcha.Champ: {v}})
		if p.statut != http.StatusBadRequest || !strings.Contains(texte(p.corps), "La vérification anti-robot n'a pas abouti.") ||
			!strings.Contains(p.corps, "data-altcha-defi=") {
			t.Errorf("%s : %d", nom, p.statut)
		}
	}
	if n.cookie != nil || b.verificateur.Metriques().JetonsCrees.Load() != 0 {
		t.Fatal("aucune session ni aucun jeton ne doit être créé sans captcha valide")
	}

	// La bonne solution crée un jeton ; rejouée, elle est refusée.
	if p := n.post("/verifier", url.Values{altcha.Champ: {solution}}); p.statut != http.StatusSeeOther || p.location != "/verifier/whatsapp" {
		t.Fatalf("solution valide : %d vers %q", p.statut, p.location)
	}
	n2 := &navigateur{t: t, h: b.srv}
	if p := n2.post("/verifier", url.Values{altcha.Champ: {solution}}); p.statut != http.StatusBadRequest {
		t.Errorf("solution rejouée : %d", p.statut)
	}
	if n2.cookie != nil || b.verificateur.Metriques().JetonsCrees.Load() != 1 {
		t.Errorf("solution rejouée : %d jetons", b.verificateur.Metriques().JetonsCrees.Load())
	}

	// Défi expiré : nouveau défi et message clair.
	vieux := resoudreAltcha(t, n2.get("/verifier").corps)
	b.horloge.avancer(altcha.DureeDefaut)
	if p := n2.post("/verifier", url.Values{altcha.Champ: {vieux}}); p.statut != http.StatusBadRequest ||
		!strings.Contains(texte(p.corps), "La vérification anti-robot a expiré.") {
		t.Errorf("défi expiré : %d", p.statut)
	}
	if b.verificateur.Metriques().JetonsCrees.Load() != 1 {
		t.Error("un défi expiré ne crée pas de jeton")
	}
}

// La 4ᵉ vérification d'une même empreinte en 24 h est refusée.
func TestQuatriemeVerificationRefusee(t *testing.T) {
	b := nouveauBanc(t, map[string]string{})
	expediteur, _ := numeroDeTest(21)
	for i := range 3 {
		n := &navigateur{t: t, h: b.srv}
		if p := verifier(t, b, n, expediteur); p.statut != http.StatusSeeOther || p.location != "/questionnaire" {
			t.Fatalf("vérification %d : %d vers %q", i+1, p.statut, p.location)
		}
		b.horloge.avancer(time.Hour)
	}
	n := &navigateur{t: t, h: b.srv}
	p := verifier(t, b, n, expediteur)
	if p.statut != http.StatusOK || !strings.Contains(texte(p.corps), "Ce numéro a été vérifié trop de fois aujourd'hui.") {
		t.Fatalf("4ᵉ vérification : %d", p.statut)
	}
	if estVerifiee(n) || b.verificateur.Metriques().Limitees.Load() != 1 {
		t.Error("la 4ᵉ vérification ne doit pas vérifier la session")
	}
	// Un autre numéro n'est pas concerné ; le même redevient possible 24 h
	// après la première vérification.
	autre, _ := numeroDeTest(22)
	if p := verifier(t, b, &navigateur{t: t, h: b.srv}, autre); p.location != "/questionnaire" {
		t.Errorf("autre numéro : %d vers %q", p.statut, p.location)
	}
	b.horloge.avancer(21 * time.Hour)
	if p := verifier(t, b, &navigateur{t: t, h: b.srv}, expediteur); p.location != "/questionnaire" {
		t.Errorf("le lendemain : %d vers %q", p.statut, p.location)
	}
}

// Au-delà de 30 demandes par heure depuis une même adresse, les réponses
// ralentissent et une alerte part, mais aucune demande n'est bloquée.
// L'adresse est celle de la connexion : X-Forwarded-For n'y change rien.
func TestLimiteParAdresseRalentitSansBloquer(t *testing.T) {
	b := nouveauBanc(t, map[string]string{})
	n := &navigateur{t: t, h: b.srv}
	solution := func() string { return resoudreAltcha(t, n.get("/verifier").corps) }
	demander := func(adresse, transmis string) page {
		form := url.Values{altcha.Champ: {solution()}}
		req := httptest.NewRequest(http.MethodPost, "/verifier", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = adresse
		req.Header.Set("X-Forwarded-For", transmis)
		return n.faire(req)
	}
	for i := range 45 {
		p := demander("203.0.113.77:40000", "198.51.100."+string(rune('0'+i%10)))
		if p.statut != http.StatusSeeOther || p.location != "/verifier/whatsapp" {
			t.Fatalf("demande %d bloquée : %d vers %q", i+1, p.statut, p.location)
		}
	}
	ds := b.attentes.liste()
	if len(ds) != 15 {
		t.Fatalf("%d demandes ralenties, 15 attendues", len(ds))
	}
	for i := 1; i < len(ds); i++ {
		if ds[i] <= ds[i-1] || ds[i] > antifraude.RalentissementMax {
			t.Errorf("délais non croissants ou non bornés : %v", ds)
		}
	}
	if b.ralentisseur.Alertes() != 1 || strings.Count(b.journal.String(), "dépasse la limite de demandes par heure") != 1 {
		t.Errorf("une alerte attendue : %d", b.ralentisseur.Alertes())
	}
	// Une autre adresse n'est pas ralentie.
	demander("203.0.113.78:40000", "203.0.113.77")
	if len(b.attentes.liste()) != 15 {
		t.Error("une autre adresse ne doit pas être ralentie")
	}
	for _, adresse := range []string{"203.0.113.77", "203.0.113.78", "198.51.100."} {
		if strings.Contains(b.journal.String(), adresse) {
			t.Errorf("adresse %s dans le journal", adresse)
		}
	}
}

// Les métriques agrégées comptent les préfixes à la vérification et les
// régions à l'écriture des lots, sans aucune donnée individuelle ; ni IP ni
// numéro n'apparaissent dans les journaux ni dans les métriques.
func TestMetriquesAgregeesSansDonneeIndividuelle(t *testing.T) {
	b := nouveauBanc(t, map[string]string{})
	var numeros []string
	for i := range 5 {
		expediteur, normalise := numeroDeTest(30 + i)
		numeros = append(numeros, expediteur, normalise, normalise[4:])
		form := formulaireVoulu(i)
		form.Set("region", "3")
		if p := parcourir(t, b, expediteur, form); p.statut != http.StatusOK {
			t.Fatalf("envoi %d : %d", i, p.statut)
		}
	}
	avant := b.surveillance.Instantane()
	for _, s := range avant {
		if s.Dimension == antifraude.DimensionRegion {
			t.Fatal("aucune région ne doit être comptée avant l'écriture du lot")
		}
	}
	if err := b.tampon.Vider(context.Background()); err != nil {
		t.Fatal(err)
	}
	var prefixe, region int64
	for _, s := range b.surveillance.Instantane() {
		switch {
		case s.Dimension == antifraude.DimensionPrefixe && s.Valeur == "77":
			prefixe = s.Total
		case s.Dimension == antifraude.DimensionRegion && s.Valeur == "3":
			region = s.Total
		default:
			t.Errorf("série inattendue : %+v", s)
		}
		for _, num := range numeros {
			if strings.Contains(s.Valeur, num) {
				t.Errorf("numéro dans les métriques : %+v", s)
			}
		}
	}
	if prefixe != 5 || region != 5 {
		t.Errorf("préfixe 77 : %d, région 3 : %d ; 5 attendus", prefixe, region)
	}
	journal := b.journal.String()
	for _, num := range numeros {
		if strings.Contains(journal, num) {
			t.Errorf("numéro %s dans le journal", num)
		}
	}
	if strings.Contains(journal, "192.0.2.1") {
		t.Error("adresse IP dans le journal")
	}
}
