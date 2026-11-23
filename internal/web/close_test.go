package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/codes"
	"github.com/2guntan/diguee/internal/whatsapp"
)

// Clôture (ticket #16) : à DIGUEE_CLOTURE, toutes les pages du parcours
// affichent « Consultation close », aucun nouveau jeton ni code n'est
// délivré, un jeton ou un code délivré avant ne vérifie plus rien, et le
// webhook répond 200 sans rien vérifier.
func TestApresLaClotureAucunJetonNiCode(t *testing.T) {
	// Horloge du banc : 2026-12-01 12:00 ; clôture le lendemain.
	b := nouveauBanc(t, map[string]string{"DIGUEE_CLOTURE": "2026-12-02"})
	expediteur, _ := numeroDeTest(31)

	// Avant : un jeton (non encore utilisé), un code WhatsApp et un code SMS
	// en attente de saisie, et une session vérifiée qui n'a pas encore
	// envoyé ses réponses.
	nJeton := &navigateur{t: t, h: b.srv}
	jeton := demanderJeton(t, nJeton)
	nCode := &navigateur{t: t, h: b.srv}
	if p := demanderCode(t, nCode, "77 000 00 32"); p.statut != http.StatusSeeOther {
		t.Fatalf("code : %d", p.statut)
	}
	code := b.console.Dernier()
	nSMS := &navigateur{t: t, h: b.srv}
	codeSMS := smsEnvoye(t, b, nSMS, "77 000 00 33", true)
	nVerifie := &navigateur{t: t, h: b.srv}
	if p := verifier(t, b, nVerifie, expediteur); p.statut != http.StatusSeeOther || p.location != "/questionnaire" {
		t.Fatalf("vérification : %d %q", p.statut, p.location)
	}
	recap := nVerifie.post("/questionnaire/recapitulatif", formulaireVoulu(1))
	_, caches := champs(recap.corps)
	jetonsAvant := b.verificateur.Metriques().JetonsCrees.Load()
	envoyesAvant := b.codes.Metriques().Envoyes.Load() + b.codesSMS.Metriques().Envoyes.Load()

	b.horloge.avancer(12 * time.Hour) // 2026-12-02 00:00 : close

	nouveau := &navigateur{t: t, h: b.srv}
	for _, chemin := range []string{"/verifier", "/verifier/whatsapp", "/verifier/code", "/verifier/code/saisie",
		"/verifier/sms", "/verifier/sms/saisie", "/verifier/sans-javascript", "/questionnaire"} {
		for _, n := range []*navigateur{nouveau, nJeton, nCode, nSMS, nVerifie} {
			if p := n.get(chemin); p.statut != http.StatusOK || !strings.Contains(p.corps, "Consultation close") {
				t.Errorf("GET %s : %d, « Consultation close » absent", chemin, p.statut)
			}
		}
	}
	for chemin, f := range map[string]url.Values{
		"/verifier":                    {},
		"/verifier/code":               {"numero": {"77 000 00 34"}},
		"/verifier/sms":                {"numero": {"77 000 00 35"}},
		"/verifier/code/saisie":        {"code": {code}},
		"/verifier/sms/saisie":         {"code": {codeSMS}},
		"/questionnaire/recapitulatif": formulaireVoulu(1),
		"/envoyer":                     caches,
		"/verifier/whatsapp":           {},
	} {
		if p := nouveau.post(chemin, f); p.statut != http.StatusGone || !strings.Contains(p.corps, "Consultation close") {
			t.Errorf("POST %s : %d", chemin, p.statut)
		}
	}
	if p := nVerifie.post("/envoyer", caches); p.statut != http.StatusGone {
		t.Errorf("envoi d'une session vérifiée avant la clôture : %d", p.statut)
	}
	if p := nJeton.get("/verifier/etat"); !strings.Contains(p.corps, `"etat":"fin"`) {
		t.Errorf("/verifier/etat : %s", p.corps)
	}
	// Le jeton délivré avant la clôture, envoyé maintenant : 200 pour Meta,
	// mais rien n'est vérifié.
	b.envoyerWhatsApp(t, expediteur, jeton)
	if got := b.verificateur.Metriques().HorsPeriode.Load(); got != 1 {
		t.Errorf("messages hors période : %d", got)
	}
	if got := b.verificateur.Metriques().Verifications.Load(); got != 1 {
		t.Errorf("vérifications : %d (seule celle d'avant la clôture)", got)
	}
	if b.verificateur.Metriques().JetonsCrees.Load() != jetonsAvant {
		t.Error("un jeton a été créé après la clôture")
	}
	if b.codes.Metriques().Envoyes.Load()+b.codesSMS.Metriques().Envoyes.Load() != envoyesAvant {
		t.Error("un code a été envoyé après la clôture")
	}
	if p, r := b.compter(t); p != 0 || r != 0 {
		t.Errorf("participations %d, réponses %d : rien ne doit être enregistré", p, r)
	}

	// Défense en profondeur : les vérificateurs refusent d'eux-mêmes.
	if _, err := b.verificateur.Creer("session"); err != whatsapp.ErrClose {
		t.Errorf("Creer après la clôture : %v", err)
	}
	if s, err := b.codes.Demander(context.Background(), "session", "77 000 00 36"); err != nil || s != codes.Close {
		t.Errorf("Demander après la clôture : %v %v", s, err)
	}
	if s, _, err := b.codesSMS.Valider(context.Background(), "session", "000000"); err != nil || s != codes.Close {
		t.Errorf("Valider après la clôture : %v %v", s, err)
	}
}
