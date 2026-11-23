package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/altcha"
	"github.com/2guntan/diguee/internal/antifraude"
	"github.com/2guntan/diguee/internal/codes"
	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/i18n"
	"github.com/2guntan/diguee/internal/integration"
	"github.com/2guntan/diguee/internal/sms"
)

// demanderSMS ouvre la saisie du numéro du SMS et l'envoie, avec la solution
// du captcha si avecJS, sans elle sinon (navigateur sans JavaScript).
func demanderSMS(t *testing.T, n *navigateur, numero string, avecJS bool) page {
	t.Helper()
	p := n.get("/verifier/sms")
	if m := reFormAction.FindStringSubmatch(p.corps); p.statut != http.StatusOK || m == nil || m[1] != "/verifier/sms" {
		t.Fatalf("formulaire du SMS absent : %d %s", p.statut, texte(p.corps))
	}
	f := url.Values{"numero": {numero}}
	if avecJS {
		f.Set(altcha.Champ, resoudreAltcha(t, p.corps))
	}
	return n.post("/verifier/sms", f)
}

// smsEnvoye demande un code par SMS et renvoie le code écrit par la console.
func smsEnvoye(t *testing.T, b *banc, n *navigateur, numero string, avecJS bool) string {
	t.Helper()
	p := demanderSMS(t, n, numero, avecJS)
	if p.statut != http.StatusSeeOther || p.location != "/verifier/sms/saisie" || n.cookie == nil {
		t.Fatalf("demande de SMS : %d vers %q : %s", p.statut, p.location, texte(p.corps))
	}
	c := b.consoleSMS.Dernier()
	if len(c) != 6 || strings.Trim(c, "0123456789") != "" {
		t.Fatalf("code inattendu %q", c)
	}
	return c
}

func saisirSMS(n *navigateur, code string) page {
	return n.post("/verifier/sms/saisie", url.Values{"code": {code}})
}

// finirParcours remplit le questionnaire d'une session vérifiée et l'envoie
// jusqu'à la page Merci.
func finirParcours(t *testing.T, n *navigateur, i int) {
	t.Helper()
	p := n.post("/questionnaire/recapitulatif", formulaireVoulu(i))
	_, caches := champs(p.corps)
	if p = n.post("/envoyer", caches); p.statut != http.StatusSeeOther || p.location != "/merci" {
		t.Fatalf("envoi : %d %q", p.statut, p.location)
	}
	if p = n.get("/merci"); p.statut != http.StatusOK {
		t.Fatalf("merci : %d", p.statut)
	}
}

// Le lien « Je n'ai pas WhatsApp » mène au code par SMS ; avec la fausse
// implémentation, le parcours va jusqu'à la page Merci. Ni le numéro ni le
// code ne sont dans le journal ; la console montre le code, jamais le numéro.
func TestParcoursSMSComplet(t *testing.T) {
	b := nouveauBanc(t, map[string]string{})
	n := &navigateur{t: t, h: b.srv}
	for _, chemin := range []string{"/verifier", "/verifier/code"} {
		if p := n.get(chemin); !strings.Contains(p.corps, `href="/verifier/sms"`) || !strings.Contains(texte(p.corps), "Je n'ai pas WhatsApp") {
			t.Fatalf("%s : lien « Je n'ai pas WhatsApp » absent", chemin)
		}
	}
	_, normalise := numeroDeTest(101)
	code := smsEnvoye(t, b, n, "77 "+normalise[6:9]+" "+normalise[9:11]+" "+normalise[11:], true)
	if b.console.Dernier() != "" {
		t.Fatal("le code SMS ne doit pas passer par WhatsApp")
	}
	p := n.get("/verifier/sms/saisie")
	if c := texte(p.corps); p.statut != http.StatusOK || !strings.Contains(c, "Il vous reste 3 essais.") ||
		!strings.Contains(c, "Code reçu par SMS") || !strings.Contains(p.corps, `action="/verifier/sms/saisie"`) ||
		!strings.Contains(p.corps, `href="/dev/sms"`) {
		t.Fatalf("page de saisie : %d %s", p.statut, c)
	}
	if p := n.get("/dev/sms"); p.statut != http.StatusOK || !strings.Contains(texte(p.corps), code) {
		t.Fatalf("page de simulation des SMS : %d", p.statut)
	}
	if p := saisirSMS(n, faux(code)); p.statut != http.StatusBadRequest || !strings.Contains(texte(p.corps), "Il vous reste 2 essais.") {
		t.Fatalf("code faux : %d %s", p.statut, texte(p.corps))
	}
	ancien := n.cookie.Value
	if p := saisirSMS(n, code); p.statut != http.StatusSeeOther || p.location != "/questionnaire" {
		t.Fatalf("code juste : %d vers %q", p.statut, p.location)
	}
	if n.cookie == nil || n.cookie.Value == ancien || !estVerifiee(n) {
		t.Fatal("la session doit être vérifiée, avec un nouvel identifiant")
	}
	finirParcours(t, n, 5)
	if err := b.tampon.Vider(t.Context()); err != nil {
		t.Fatal(err)
	}
	if parts, reps := b.compter(t); parts != 1 || reps != 1 {
		t.Fatalf("base : %d participations, %d réponses", parts, reps)
	}
	// Le même numéro, par SMS : « déjà participé », après un code juste.
	n2 := &navigateur{t: t, h: b.srv}
	code2 := smsEnvoye(t, b, n2, normalise, true)
	if p := saisirSMS(n2, code2); p.statut != http.StatusConflict || !strings.Contains(texte(p.corps), "Ce numéro a déjà participé.") {
		t.Fatalf("doublon : %d", p.statut)
	}
	j := b.journal.String()
	for _, interdit := range []string{normalise, normalise[1:], normalise[4:], normalise[6:], code, code2} {
		if strings.Contains(j, interdit) {
			t.Errorf("le journal contient %q", interdit)
		}
	}
	if c := b.sortieSMS.String(); !strings.Contains(c, code) || !strings.Contains(c, "SMS") || strings.Contains(c, normalise[4:]) {
		t.Errorf("console des SMS : %q", c)
	}
}

// Sans JavaScript, le SMS reste possible sans captcha, jusqu'à la page
// Merci, mais sous un quota séparé, par adresse et pour tout le site. Une
// solution du captcha présente mais fausse reste refusée.
func TestParcoursSansJavaScriptParSMS(t *testing.T) {
	b := nouveauBanc(t, map[string]string{"DIGUEE_SMS_SANS_JS_PAR_HEURE": "3"})
	n := &navigateur{t: t, h: b.srv, adresse: "198.51.100.10:1000"}
	// Sans solution du captcha, /verifier mène à l'explication, qui mène au SMS.
	if p := n.post("/verifier", url.Values{}); p.location != "/verifier/sans-javascript" {
		t.Fatalf("sans JavaScript : %d %q", p.statut, p.location)
	}
	p := n.get("/verifier/sans-javascript")
	if c := texte(p.corps); !strings.Contains(p.corps, `href="/verifier/sms"`) || !strings.Contains(c, "limités à quelques-uns par heure") {
		t.Fatalf("page sans JavaScript : %s", c)
	}
	if p := n.get("/verifier/sms"); !strings.Contains(p.corps, "<noscript>") || !strings.Contains(texte(p.corps), "les demandes sans JavaScript sont limitées") {
		t.Fatal("la page du SMS doit expliquer le quota sans JavaScript")
	}
	_, normalise := numeroDeTest(111)
	code := smsEnvoye(t, b, n, normalise, false)
	if p := saisirSMS(n, code); p.location != "/questionnaire" {
		t.Fatalf("code juste : %d %q", p.statut, p.location)
	}
	finirParcours(t, n, 7)

	// Solution fausse : refusée, sans entamer le quota.
	if p := n.post("/verifier/sms", url.Values{"numero": {normalise}, altcha.Champ: {"AAAA"}}); p.statut != http.StatusBadRequest ||
		!strings.Contains(texte(p.corps), "La vérification anti-robot n'a pas abouti.") {
		t.Fatalf("captcha faux : %d", p.statut)
	}
	// 2 demandes sans captcha par adresse et par heure (la première est faite).
	_, autre := numeroDeTest(112)
	smsEnvoye(t, b, &navigateur{t: t, h: b.srv, adresse: n.adresse}, autre, false)
	envoyes := b.codesSMS.Metriques().Envoyes.Load()
	p = demanderSMS(t, &navigateur{t: t, h: b.srv, adresse: n.adresse}, autre, false)
	if p.statut != http.StatusTooManyRequests || !strings.Contains(texte(p.corps), "Trop de codes ont été demandés sans JavaScript") {
		t.Fatalf("3e demande sans captcha de la même adresse : %d %s", p.statut, texte(p.corps))
	}
	// Avec le captcha, la même adresse passe encore.
	_, troisieme := numeroDeTest(113)
	smsEnvoye(t, b, &navigateur{t: t, h: b.srv, adresse: n.adresse}, troisieme, true)
	// Quota global (3 par heure) : la 3e adresse passe, la 4e est refusée,
	// et l'alerte ne cite rien d'individuel.
	_, quatrieme := numeroDeTest(114)
	smsEnvoye(t, b, &navigateur{t: t, h: b.srv, adresse: "198.51.100.11:1000"}, quatrieme, false)
	_, cinquieme := numeroDeTest(115)
	if p := demanderSMS(t, &navigateur{t: t, h: b.srv, adresse: "198.51.100.12:1000"}, cinquieme, false); p.statut != http.StatusTooManyRequests {
		t.Fatalf("quota global sans captcha : %d", p.statut)
	}
	if got := b.codesSMS.Metriques().Envoyes.Load(); got != envoyes+2 {
		t.Fatalf("SMS envoyés : %d, attendu %d", got, envoyes+2)
	}
	if j := b.journal.String(); !strings.Contains(j, "quota horaire des codes SMS sans captcha atteint") ||
		strings.Contains(j, "198.51.100") || strings.Contains(j, cinquieme[4:]) {
		t.Errorf("alerte du quota sans captcha : %s", j)
	}
	// Une heure plus tard, le quota est rétabli.
	b.horloge.avancer(time.Hour)
	smsEnvoye(t, b, &navigateur{t: t, h: b.srv, adresse: "198.51.100.12:1000"}, cinquieme, false)
}

// Les limites du SMS sont plus strictes que celles du WhatsApp : 2 codes par
// empreinte et par 24 h (au lieu de 3), 3 essais (au lieu de 5), 10
// demandes par adresse et par heure avant ralentissement (au lieu de 30).
func TestLimitesSMSPlusStrictes(t *testing.T) {
	b := nouveauBanc(t, map[string]string{})
	if b.cfg.SMSParEmpreinte24h >= b.cfg.VerifParEmpreinte24h || b.cfg.SMSCodeEssais >= b.cfg.CodeEssais ||
		b.cfg.SMSIPParHeure >= b.cfg.IPParHeure {
		t.Fatalf("limites SMS par défaut : %+v", b.cfg)
	}
	_, normalise := numeroDeTest(121)
	for range 2 {
		smsEnvoye(t, b, &navigateur{t: t, h: b.srv}, normalise, true)
	}
	envoyes := b.codesSMS.Metriques().Envoyes.Load()
	if p := demanderSMS(t, &navigateur{t: t, h: b.srv}, normalise, true); p.statut != http.StatusBadRequest ||
		!strings.Contains(texte(p.corps), "vérifié trop de fois aujourd'hui") {
		t.Fatalf("3e SMS pour le même numéro : %d", p.statut)
	}
	if b.codesSMS.Metriques().Envoyes.Load() != envoyes {
		t.Fatal("aucun SMS ne doit partir au-delà de la limite")
	}
	// La limite partagée n'a compté que les deux SMS : le WhatsApp passe
	// encore une fois.
	if p := demanderCode(t, &navigateur{t: t, h: b.srv}, normalise); p.location != "/verifier/code/saisie" {
		t.Fatalf("code WhatsApp après 2 SMS : %d %q", p.statut, p.location)
	}
	// 3 essais.
	_, autre := numeroDeTest(122)
	n := &navigateur{t: t, h: b.srv}
	code := smsEnvoye(t, b, n, autre, true)
	saisirSMS(n, faux(code))
	saisirSMS(n, faux(code))
	if p := saisirSMS(n, faux(code)); !strings.Contains(texte(p.corps), "trop de fois") {
		t.Fatalf("3e essai faux : %s", texte(p.corps))
	}
	if p := saisirSMS(n, code); p.statut == http.StatusSeeOther || estVerifiee(n) {
		t.Fatal("le bon code après 3 essais ne doit rien vérifier")
	}
	// Ralentissement par adresse dès la 11e demande de SMS de l'heure.
	avant := len(b.attentes.liste())
	for range 10 {
		demanderSMS(t, &navigateur{t: t, h: b.srv}, "12", true)
	}
	if att := b.attentes.liste()[avant:]; len(att) == 0 || att[0] != antifraude.PasRalentissement {
		t.Fatalf("ralentissement du SMS attendu : %v", att)
	}
}

// Les limites du SMS se règlent, et ne peuvent pas être plus lâches que
// celles du WhatsApp.
func TestLimitesSMSConfigurables(t *testing.T) {
	b := nouveauBanc(t, map[string]string{"DIGUEE_SMS_PAR_EMPREINTE_24H": "1", "DIGUEE_SMS_CODE_ESSAIS": "1"})
	_, normalise := numeroDeTest(131)
	n := &navigateur{t: t, h: b.srv}
	code := smsEnvoye(t, b, n, normalise, true)
	if p := saisirSMS(n, faux(code)); !strings.Contains(texte(p.corps), "trop de fois") {
		t.Fatalf("1 essai : %s", texte(p.corps))
	}
	if p := demanderSMS(t, n, normalise, true); !strings.Contains(texte(p.corps), "vérifié trop de fois aujourd'hui") {
		t.Fatalf("1 SMS par empreinte : %d", p.statut)
	}
	for nom, valeur := range map[string]string{
		"DIGUEE_SMS_PAR_EMPREINTE_24H":    "4",
		"DIGUEE_SMS_CODE_ESSAIS":          "6",
		"DIGUEE_SMS_IP_PAR_HEURE":         "31",
		"DIGUEE_SMS_SANS_JS_PAR_HEURE":    "101",
		"DIGUEE_SMS_SANS_JS_IP_PAR_HEURE": "11",
	} {
		_, err := config.Charger(func(k string) string {
			return map[string]string{"DIGUEE_POSTGRES_URL": "postgres://localhost/diguee", nom: valeur}[k]
		})
		if err == nil || !strings.Contains(err.Error(), nom) {
			t.Errorf("%s=%s devrait être refusé : %v", nom, valeur, err)
		}
	}
}

// La limite par empreinte est partagée entre la vérification inversée, le
// code par WhatsApp et le code par SMS.
func TestLimiteParEmpreintePartageeEntreLesCanaux(t *testing.T) {
	b := nouveauBanc(t, map[string]string{})
	expediteur, normalise := numeroDeTest(141)
	if p := verifier(t, b, &navigateur{t: t, h: b.srv}, expediteur); p.location != "/questionnaire" {
		t.Fatalf("vérification inversée : %d %q", p.statut, p.location)
	}
	codeEnvoye(t, b, &navigateur{t: t, h: b.srv}, normalise)
	smsEnvoye(t, b, &navigateur{t: t, h: b.srv}, normalise, true)
	// 4e tentative, sur chaque canal : refusée.
	if p := demanderSMS(t, &navigateur{t: t, h: b.srv}, normalise, true); !strings.Contains(texte(p.corps), "vérifié trop de fois") {
		t.Errorf("SMS : %d", p.statut)
	}
	if p := demanderCode(t, &navigateur{t: t, h: b.srv}, normalise); !strings.Contains(texte(p.corps), "vérifié trop de fois") {
		t.Errorf("code WhatsApp : %d", p.statut)
	}
	n := &navigateur{t: t, h: b.srv}
	verifier(t, b, n, expediteur)
	if !strings.Contains(texte(n.get("/verifier/whatsapp").corps), "vérifié trop de fois") {
		t.Error("vérification inversée : refus attendu")
	}
	// Un SMS demandé révoque le code WhatsApp en cours de la session, et
	// inversement : un seul parcours à la fois.
	_, autre := numeroDeTest(142)
	m := &navigateur{t: t, h: b.srv}
	cw := codeEnvoye(t, b, m, autre)
	smsEnvoye(t, b, m, autre, true)
	if p := saisir(m, cw); p.statut == http.StatusSeeOther {
		t.Fatal("le code WhatsApp doit être révoqué par la demande de SMS")
	}
}

// T14 : un pic simulé déclenche le coupe-circuit ; le parcours SMS est
// suspendu (page explicative, aucun envoi, ni avec ni sans JavaScript),
// une alerte sans donnée personnelle part, et le parcours reprend après le
// délai de réactivation.
func TestCoupeCircuitSMSSurPic(t *testing.T) {
	b := nouveauBanc(t, map[string]string{"DIGUEE_SMS_REACTIVATION": "2h"})
	// Historique : 2 SMS par heure pendant 7 jours (moyenne 2) ; la règle
	// coupe au-delà de 3 × 2 = 6 SMS, mais pas avant 20 SMS dans l'heure
	// (plancher).
	for range 7 * 24 {
		for range 2 {
			if !b.coupeSMS.Autoriser() {
				t.Fatal("l'historique ne doit pas déclencher le coupe-circuit")
			}
		}
		b.horloge.avancer(time.Hour)
	}
	envoyesAvant := b.codesSMS.Metriques().Envoyes.Load()
	var numeros []string
	for i := range 19 {
		_, nu := numeroDeTest(200 + i)
		numeros = append(numeros, nu)
		smsEnvoye(t, b, &navigateur{t: t, h: b.srv}, nu, true)
	}
	if strings.Contains(b.journal.String(), "coupe-circuit") {
		t.Fatal("pas d'alerte sous le plancher")
	}
	dernier := b.consoleSMS.Dernier()
	_, vingtieme := numeroDeTest(219)
	numeros = append(numeros, vingtieme)
	p := demanderSMS(t, &navigateur{t: t, h: b.srv}, vingtieme, true)
	if p.statut != http.StatusServiceUnavailable || !strings.Contains(texte(p.corps), "momentanément indisponible") {
		t.Fatalf("20e SMS de l'heure : %d %s", p.statut, texte(p.corps))
	}
	if got := b.codesSMS.Metriques().Envoyes.Load(); got != envoyesAvant+19 || b.consoleSMS.Dernier() != dernier {
		t.Fatalf("aucun SMS ne doit partir au déclenchement : %d envoyés", got-envoyesAvant)
	}
	// L'alerte : un avertissement, des nombres, aucune donnée personnelle.
	j := b.journal.String()
	if !strings.Contains(j, `"level":"WARN","msg":"alerte : coupe-circuit SMS déclenché, parcours SMS suspendu"`) ||
		!strings.Contains(j, `"raison":"pic"`) {
		t.Fatalf("alerte absente : %s", j)
	}
	for _, nu := range numeros {
		if strings.Contains(j, nu[4:]) {
			t.Fatalf("le journal contient un numéro")
		}
	}
	// Suspendu : page explicative, aucun envoi, avec ou sans JavaScript.
	n := &navigateur{t: t, h: b.srv, adresse: "203.0.113.5:4000"}
	if p := n.get("/verifier/sms"); p.statut != http.StatusServiceUnavailable || !strings.Contains(p.corps, `href="/verifier"`) {
		t.Fatalf("page du SMS suspendu : %d", p.statut)
	}
	_, autre := numeroDeTest(250)
	if p := n.post("/verifier/sms", url.Values{"numero": {autre}}); p.statut != http.StatusServiceUnavailable {
		t.Fatalf("SMS sans JavaScript pendant la suspension : %d", p.statut)
	}
	b.horloge.avancer(time.Hour + 59*time.Minute)
	if p := n.get("/verifier/sms"); p.statut != http.StatusServiceUnavailable {
		t.Fatalf("encore suspendu avant le délai : %d", p.statut)
	}
	if got := b.codesSMS.Metriques().Envoyes.Load(); got != envoyesAvant+19 {
		t.Fatalf("aucun SMS pendant la suspension : %d", got-envoyesAvant)
	}
	// Les autres canaux ne sont pas touchés.
	if p := demanderCode(t, &navigateur{t: t, h: b.srv}, autre); p.location != "/verifier/code/saisie" {
		t.Fatalf("code WhatsApp pendant la suspension : %d", p.statut)
	}
	// Après le délai, réactivation automatique.
	b.horloge.avancer(time.Minute)
	_, encore := numeroDeTest(251)
	smsEnvoye(t, b, &navigateur{t: t, h: b.srv}, encore, true)
	if !strings.Contains(b.journal.String(), "parcours SMS réactivé automatiquement") {
		t.Error("réactivation automatique non journalisée")
	}
}

// Le plafond horaire absolu coupe toujours, même sans historique (au
// démarrage) ; la réactivation manuelle rouvre le parcours.
func TestCoupeCircuitSMSPlafondEtReactivationManuelle(t *testing.T) {
	b := nouveauBanc(t, map[string]string{"DIGUEE_SMS_PAR_HEURE": "3", "DIGUEE_SMS_SANS_JS_PAR_HEURE": "3",
		"DIGUEE_SMS_REACTIVATION": "6h"})
	for i := range 3 {
		_, nu := numeroDeTest(300 + i)
		smsEnvoye(t, b, &navigateur{t: t, h: b.srv}, nu, true)
	}
	_, quatrieme := numeroDeTest(303)
	if p := demanderSMS(t, &navigateur{t: t, h: b.srv}, quatrieme, true); p.statut != http.StatusServiceUnavailable {
		t.Fatalf("au-delà du plafond horaire : %d", p.statut)
	}
	if e := b.coupeSMS.Etat(); !e.Suspendu || e.Raison != antifraude.RaisonPlafond {
		t.Fatalf("état : %+v", e)
	}
	if !strings.Contains(b.journal.String(), `"raison":"plafond"`) {
		t.Fatal("alerte du plafond absente")
	}
	b.horloge.avancer(30 * time.Minute)
	if !b.codesSMS.Suspendu() {
		t.Fatal("encore suspendu")
	}
	// Réactivé à la main dans la même heure : le plafond est toujours
	// atteint, le circuit coupe de nouveau au premier envoi.
	if !b.coupeSMS.Reactiver() || b.codesSMS.Suspendu() {
		t.Fatal("réactivation manuelle")
	}
	if p := demanderSMS(t, &navigateur{t: t, h: b.srv}, quatrieme, true); p.statut != http.StatusServiceUnavailable {
		t.Fatalf("plafond toujours atteint : %d", p.statut)
	}
	// L'heure suivante, après une nouvelle réactivation manuelle (le délai
	// automatique de 6 heures n'est pas écoulé), l'envoi repart.
	b.horloge.avancer(31 * time.Minute)
	if !b.codesSMS.Suspendu() || !b.coupeSMS.Reactiver() {
		t.Fatal("réactivation manuelle")
	}
	// (Chaque demande refusée au déclenchement a compté pour la limite par
	// empreinte de quatrieme : un autre numéro.)
	_, cinquieme := numeroDeTest(304)
	smsEnvoye(t, b, &navigateur{t: t, h: b.srv}, cinquieme, true)
	if e := b.coupeSMS.Etat(); e.Declenchements != 2 {
		t.Fatalf("déclenchements : %d", e.Declenchements)
	}
}

// La console des SMS (mode « fake ») est interdite en production, et
// l'intégration « sms » est déclarée dans le registre.
func TestSMSFauxInterditEnProduction(t *testing.T) {
	env := map[string]string{"DIGUEE_POSTGRES_URL": "postgres://localhost/diguee", "DIGUEE_ENV": "production"}
	cfg, err := config.Charger(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	r := &integration.Registre{}
	i, err := sms.Construire(cfg, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if i.Envoyeur != nil {
		t.Fatal("aucune console en production")
	}
	if fs := r.Fausses(); len(fs) != 1 || fs[0].Nom != "sms" || fs[0].Variable != "DIGUEE_SMS" {
		t.Fatalf("registre : %+v", fs)
	}
	cs, err := i18n.Charger()
	if err != nil {
		t.Fatal(err)
	}
	console, _ := codes.NouvelleConsole("SMS", nil, false)
	if _, err := Nouveau(Options{Config: cfg, Catalogues: cs, SMS: &sms.Integration{Mode: "fake", Envoyeur: console}}); err == nil ||
		!strings.Contains(err.Error(), "DIGUEE_SMS=fake") {
		t.Fatalf("serveur en production avec la console SMS : %v", err)
	}
}
