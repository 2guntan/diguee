package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/altcha"
)

// demanderCode ouvre la saisie du numéro, résout le captcha et envoie le
// numéro. Il renvoie la réponse au formulaire.
func demanderCode(t *testing.T, n *navigateur, numero string) page {
	t.Helper()
	p := n.get("/verifier/code")
	if m := reFormAction.FindStringSubmatch(p.corps); p.statut != http.StatusOK || m == nil || m[1] != "/verifier/code" {
		t.Fatalf("formulaire du code absent : %d", p.statut)
	}
	return n.post("/verifier/code", url.Values{altcha.Champ: {resoudreAltcha(t, p.corps)}, "numero": {numero}})
}

// codeEnvoye demande un code et renvoie le code écrit par la console.
func codeEnvoye(t *testing.T, b *banc, n *navigateur, numero string) string {
	t.Helper()
	p := demanderCode(t, n, numero)
	if p.statut != http.StatusSeeOther || p.location != "/verifier/code/saisie" || n.cookie == nil {
		t.Fatalf("demande de code : %d vers %q : %s", p.statut, p.location, texte(p.corps))
	}
	c := b.console.Dernier()
	if len(c) != 6 || strings.Trim(c, "0123456789") != "" {
		t.Fatalf("code inattendu %q", c)
	}
	return c
}

func saisir(n *navigateur, code string) page {
	return n.post("/verifier/code/saisie", url.Values{"code": {code}})
}

// faux renvoie un code différent de c.
func faux(c string) string {
	if c == "000000" {
		return "000001"
	}
	return "000000"
}

// Un code juste, saisi dans les 10 minutes, vérifie la session, qui change
// d'identifiant ; le parcours va ensuite jusqu'à l'envoi. Ni le numéro ni le
// code ne sont dans le journal.
func TestCodeJusteVerifieLaSession(t *testing.T) {
	b := nouveauBanc(t, map[string]string{})
	n := &navigateur{t: t, h: b.srv}
	if p := n.get("/verifier"); !strings.Contains(p.corps, `href="/verifier/code"`) ||
		!strings.Contains(texte(p.corps), "Mon WhatsApp est sur un autre téléphone") {
		t.Fatal("lien vers le code de secours absent de la page de vérification")
	}
	_, normalise := numeroDeTest(31)
	saisie := "77 " + normalise[6:9] + " " + normalise[9:11] + " " + normalise[11:]
	code := codeEnvoye(t, b, n, saisie)
	p := n.get("/verifier/code/saisie")
	if p.statut != http.StatusOK || !strings.Contains(texte(p.corps), "Il vous reste 5 essais.") ||
		!strings.Contains(p.corps, `autocomplete="one-time-code"`) {
		t.Fatalf("page de saisie : %d %s", p.statut, texte(p.corps))
	}
	ancien := n.cookie.Value
	b.horloge.avancer(9 * time.Minute)
	p = saisir(n, code[:3]+" "+code[3:])
	if p.statut != http.StatusSeeOther || p.location != "/questionnaire" {
		t.Fatalf("code juste : %d vers %q : %s", p.statut, p.location, texte(p.corps))
	}
	if n.cookie == nil || n.cookie.Value == ancien {
		t.Fatal("l'identifiant de session doit changer à la vérification")
	}
	if !estVerifiee(n) {
		t.Fatal("session non vérifiée")
	}
	// Le code est consommé.
	if p := saisir(&navigateur{t: t, h: b.srv, cookie: n.cookie}, code); p.statut != http.StatusSeeOther {
		t.Errorf("code rejoué : %d", p.statut)
	}
	p = n.post("/questionnaire/recapitulatif", formulaireVoulu(3))
	_, caches := champs(p.corps)
	if p = n.post("/envoyer", caches); p.statut != http.StatusSeeOther || p.location != "/merci" {
		t.Fatalf("envoi : %d %q", p.statut, p.location)
	}
	if err := b.tampon.Vider(t.Context()); err != nil {
		t.Fatal(err)
	}
	if parts, reps := b.compter(t); parts != 1 || reps != 1 {
		t.Fatalf("base : %d participations, %d réponses", parts, reps)
	}
	// Le même numéro, de nouveau par code : « déjà participé », et rien
	// d'autre, seulement après un code juste.
	n2 := &navigateur{t: t, h: b.srv}
	code = codeEnvoye(t, b, n2, normalise)
	if p := saisir(n2, code); p.statut != http.StatusConflict || !strings.Contains(texte(p.corps), "Ce numéro a déjà participé.") || n2.cookie != nil {
		t.Fatalf("doublon : %d", p.statut)
	}
	j := b.journal.String()
	for _, interdit := range []string{normalise, normalise[1:], normalise[4:], normalise[6:], code} {
		if strings.Contains(j, interdit) {
			t.Errorf("le journal contient %q", interdit)
		}
	}
	// La console de développement montre le code, jamais le numéro.
	if c := b.sortieCodes.String(); !strings.Contains(c, code) || strings.Contains(c, normalise[4:]) {
		t.Errorf("console des codes : %q", c)
	}
}

// Un code expiré, ou faux après 5 essais, ne vérifie rien et doit être
// redemandé.
func TestCodeExpireOuEpuise(t *testing.T) {
	b := nouveauBanc(t, map[string]string{})
	_, normalise := numeroDeTest(32)

	n := &navigateur{t: t, h: b.srv}
	code := codeEnvoye(t, b, n, normalise)
	b.horloge.avancer(10 * time.Minute)
	p := saisir(n, code)
	if p.statut != http.StatusBadRequest || !strings.Contains(texte(p.corps), "Ce code n'est plus valable.") ||
		strings.Contains(p.corps, `name="code"`) {
		t.Fatalf("code expiré : %d %s", p.statut, texte(p.corps))
	}
	if estVerifiee(n) {
		t.Fatal("un code expiré ne doit rien vérifier")
	}

	n = &navigateur{t: t, h: b.srv}
	code = codeEnvoye(t, b, n, normalise)
	for i := 4; i >= 1; i-- {
		p := saisir(n, faux(code))
		if p.statut != http.StatusBadRequest || !strings.Contains(texte(p.corps), "Ce code n'est pas le bon.") {
			t.Fatalf("essai faux : %d", p.statut)
		}
		attendu := "Il vous reste 1 essai."
		if i > 1 {
			attendu = "Il vous reste " + string(rune('0'+i)) + " essais."
		}
		if !strings.Contains(texte(p.corps), attendu) {
			t.Fatalf("%q attendu : %s", attendu, texte(p.corps))
		}
	}
	if p := saisir(n, "abc"); p.statut != http.StatusBadRequest || !strings.Contains(texte(p.corps), "trop de fois") {
		t.Fatalf("5e essai : %d %s", p.statut, texte(p.corps))
	}
	if p := saisir(n, code); p.statut != http.StatusBadRequest || estVerifiee(n) {
		t.Fatalf("le bon code après 5 essais ne doit rien vérifier : %d", p.statut)
	}
	if p := n.get("/verifier/code/saisie"); p.statut != http.StatusSeeOther || p.location != "/verifier/code" {
		t.Fatalf("après invalidation : %d %q", p.statut, p.location)
	}
	// Redemandé, un nouveau code fonctionne (3e code : la limite est de 3).
	code = codeEnvoye(t, b, n, normalise)
	if p := saisir(n, code); p.location != "/questionnaire" {
		t.Fatalf("nouveau code : %d %q", p.statut, p.location)
	}
}

// Les limites par empreinte (ticket #8) s'appliquent au code et sont
// partagées avec la vérification inversée ; le format et le préfixe sont
// contrôlés ; le captcha est obligatoire.
func TestCodeLimitesEtControles(t *testing.T) {
	b := nouveauBanc(t, map[string]string{})
	expediteur, normalise := numeroDeTest(33)
	// Deux vérifications inversées du même numéro…
	for range 2 {
		n := &navigateur{t: t, h: b.srv}
		if p := verifier(t, b, n, expediteur); p.location != "/questionnaire" {
			t.Fatalf("vérification inversée : %d %q", p.statut, p.location)
		}
	}
	// … puis un code : la 3e tentative passe, la 4e est refusée, quel que
	// soit le parcours.
	n := &navigateur{t: t, h: b.srv}
	codeEnvoye(t, b, n, normalise)
	envoyes := b.codes.Metriques().Envoyes.Load()
	p := demanderCode(t, n, "+221"+normalise[4:])
	if p.statut != http.StatusBadRequest || !strings.Contains(texte(p.corps), "vérifié trop de fois aujourd'hui") {
		t.Fatalf("4e tentative : %d %s", p.statut, texte(p.corps))
	}
	if b.codes.Metriques().Envoyes.Load() != envoyes {
		t.Fatal("aucun code ne doit partir au-delà de la limite")
	}
	if p := saisir(n, b.console.Dernier()); p.statut == http.StatusSeeOther {
		t.Fatal("le code précédent doit être révoqué par la nouvelle demande")
	}
	n2 := &navigateur{t: t, h: b.srv}
	if p := verifier(t, b, n2, expediteur); strings.Contains(p.location, "questionnaire") ||
		!strings.Contains(texte(n2.get("/verifier/whatsapp").corps), "vérifié trop de fois") {
		t.Fatalf("vérification inversée après les codes : %d %q", p.statut, p.location)
	}

	for numero, attendu := range map[string]string{
		"+33 6 12 34 56 78": "n'est pas un numéro mobile sénégalais",
		"77 123 45":         "n'est pas un numéro mobile sénégalais",
		"72 123 45 67":      "préfixe mobile sénégalais accepté",
	} {
		p := demanderCode(t, &navigateur{t: t, h: b.srv}, numero)
		if p.statut != http.StatusBadRequest || !strings.Contains(texte(p.corps), attendu) {
			t.Errorf("%s : %d %s", numero, p.statut, texte(p.corps))
		}
	}
	// Captcha absent : page sans JavaScript ; captcha faux : refus. Aucun envoi.
	envoyes = b.codes.Metriques().Envoyes.Load()
	n3 := &navigateur{t: t, h: b.srv}
	if p := n3.post("/verifier/code", url.Values{"numero": {"77 000 00 01"}}); p.location != "/verifier/sans-javascript" {
		t.Errorf("sans captcha : %d %q", p.statut, p.location)
	}
	if p := n3.post("/verifier/code", url.Values{"numero": {"77 000 00 01"}, altcha.Champ: {"x"}}); p.statut != http.StatusBadRequest {
		t.Errorf("captcha faux : %d", p.statut)
	}
	if b.codes.Metriques().Envoyes.Load() != envoyes {
		t.Error("un code est parti sans captcha valable")
	}
	// Chaque demande passe par la limite par adresse.
	avant := len(b.attentes.liste())
	for range 31 {
		demanderCode(t, &navigateur{t: t, h: b.srv}, "+33 6 00 00 00 00")
	}
	if len(b.attentes.liste()) == avant {
		t.Error("le ralentisseur par adresse devrait s'appliquer aux demandes de code")
	}
	j := b.journal.String()
	for _, interdit := range []string{normalise[4:], "612345678", "721234567"} {
		if strings.Contains(j, interdit) {
			t.Errorf("le journal contient %q", interdit)
		}
	}
}

// Le parcours n'est servi que s'il est configuré, et la page de saisie
// demande une session.
func TestCodeSansSession(t *testing.T) {
	b := nouveauBanc(t, map[string]string{})
	n := &navigateur{t: t, h: b.srv}
	if p := n.get("/verifier/code/saisie"); p.statut != http.StatusSeeOther || p.location != "/verifier/code" {
		t.Fatalf("sans session : %d %q", p.statut, p.location)
	}
	if p := saisir(n, "123456"); p.statut != http.StatusUnauthorized {
		t.Fatalf("POST sans session : %d", p.statut)
	}
	// Une session avec un jeton mais sans code.
	demanderJeton(t, n)
	if p := saisir(n, "123456"); p.statut != http.StatusBadRequest || estVerifiee(n) {
		t.Fatalf("session sans code : %d", p.statut)
	}
}
