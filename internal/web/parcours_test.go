package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/altcha"
	"github.com/2guntan/diguee/internal/antifraude"
	"github.com/2guntan/diguee/internal/base"
	"github.com/2guntan/diguee/internal/base/basetest"
	"github.com/2guntan/diguee/internal/codes"
	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/empreinte"
	"github.com/2guntan/diguee/internal/i18n"
	"github.com/2guntan/diguee/internal/integration"
	"github.com/2guntan/diguee/internal/publication"
	"github.com/2guntan/diguee/internal/questionnaire"
	"github.com/2guntan/diguee/internal/session"
	"github.com/2guntan/diguee/internal/sms"
	"github.com/2guntan/diguee/internal/tampon"
	"github.com/2guntan/diguee/internal/whatsapp"
)

// journalSur est un journal en mémoire, sûr pour un usage concurrent.
type journalSur struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (j *journalSur) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.buf.Write(p)
}

func (j *journalSur) String() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.buf.String()
}

// banc réunit un serveur avec le parcours complet, sa base, son tampon et la
// vérification inversée en mode « fake ».
type banc struct {
	srv          *Serveur
	base         *base.Base
	schema       basetest.Schema
	tampon       *tampon.Tampon
	journal      *journalSur
	whatsapp     *whatsapp.Integration
	verificateur *whatsapp.Verificateur
	sessions     *session.Magasin
	horloge      *horloge
	altcha       *altcha.Altcha
	limite       *antifraude.LimiteEmpreinte
	surveillance *antifraude.Surveillance
	ralentisseur *antifraude.Ralentisseur
	attentes     *attentes
	codes        *codes.Verificateur
	console      *codes.Console
	sortieCodes  *journalSur // console des codes (mode « fake »), hors du journal
	// Code par SMS (ticket #10) : console, vérificateur et coupe-circuit.
	sms        *sms.Integration
	codesSMS   *codes.Verificateur
	consoleSMS *codes.Console
	sortieSMS  *journalSur
	coupeSMS   *antifraude.CoupeCircuit
	limiteSMS  *antifraude.LimiteEmpreinte
	cfg        *config.Config
}

// attentes note les délais du ralentisseur sans dormir.
type attentes struct {
	mu sync.Mutex
	ds []time.Duration
}

func (a *attentes) attendre(_ context.Context, d time.Duration) {
	a.mu.Lock()
	a.ds = append(a.ds, d)
	a.mu.Unlock()
}

func (a *attentes) liste() []time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]time.Duration(nil), a.ds...)
}

// horloge est une heure réglable par les tests (jetons et sessions).
type horloge struct {
	mu sync.Mutex
	t  time.Time
}

func (h *horloge) maintenant() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.t
}

func (h *horloge) avancer(d time.Duration) {
	h.mu.Lock()
	h.t = h.t.Add(d)
	h.mu.Unlock()
}

func nouveauBanc(t *testing.T, env map[string]string) *banc {
	t.Helper()
	b, sc := basetest.OuvrirMigree(t)
	env["DIGUEE_POSTGRES_URL"] = "postgres://localhost/diguee"
	if _, ok := env["DIGUEE_OUVERTURE"]; !ok {
		env["DIGUEE_OUVERTURE"] = "2026-11-01"
	}
	cfg, err := config.Charger(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	cs, err := i18n.Charger()
	if err != nil {
		t.Fatal(err)
	}
	j := &journalSur{}
	journal := slog.New(slog.NewJSONHandler(j, nil))
	h := &horloge{t: time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC)}
	surveillance := antifraude.NouvelleSurveillance(h.maintenant, journal)
	limite := antifraude.NouvelleLimiteEmpreinte(cfg.VerifParEmpreinte24h, h.maintenant)
	tp := tampon.Nouveau(tampon.Options{Ecrivain: b, Taille: cfg.TamponTaille, Delai: cfg.TamponDelai, Journal: journal,
		Surveillance: surveillance})
	// Difficulté faible : les tests résolvent des centaines de défis.
	captcha, err := altcha.Nouveau(altcha.Options{Cle: []byte(strings.Repeat("k", 32)), Difficulte: 2000, Maintenant: h.maintenant})
	if err != nil {
		t.Fatal(err)
	}
	att := &attentes{}
	ralentisseur := antifraude.NouveauRalentisseur(antifraude.OptionsRalentisseur{
		ParHeure: cfg.IPParHeure, Maintenant: h.maintenant, Attendre: att.attendre, Journal: journal,
	})
	sessions := session.NouveauMagasin()
	sessions.Maintenant = h.maintenant
	empreinteur := empreinte.NouveauHMACLocal(nil)
	wa, err := whatsapp.Construire(cfg, &integration.Registre{})
	if err != nil {
		t.Fatal(err)
	}
	v, err := whatsapp.NouveauVerificateur(whatsapp.OptionsVerificateur{
		Empreinteur: empreinteur, Doublons: tp, Sessions: sessions, Prefixes: cfg.PrefixesAutorises,
		Limiteur: limite, Surveillance: surveillance,
		Duree: cfg.JetonDuree, Maintenant: h.maintenant, Journal: journal,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Code de secours : la console écrit dans un tampon du test, et la
	// limite par empreinte est la même instance que celle du vérificateur.
	sortieCodes := &journalSur{}
	console, err := codes.NouvelleConsole("WhatsApp", sortieCodes, false)
	if err != nil {
		t.Fatal(err)
	}
	wa.Envoyeur = console
	cv, err := codes.Nouveau(codes.Options{
		Canal: "WhatsApp", Envoyeur: console, Empreinteur: empreinteur, Doublons: tp, Sessions: sessions,
		Limiteur: limite, Surveillance: surveillance, Prefixes: cfg.PrefixesAutorises,
		Duree: cfg.CodeDuree, Essais: cfg.CodeEssais, Maintenant: h.maintenant, Journal: journal,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Code par SMS : sa propre console, la même limite par empreinte
	// partagée, une limite propre au SMS et le coupe-circuit.
	sortieSMS := &journalSur{}
	integrationSMS, err := sms.Construire(cfg, &integration.Registre{}, sortieSMS)
	if err != nil {
		t.Fatal(err)
	}
	consoleSMS := integrationSMS.Envoyeur.(*codes.Console)
	coupe := antifraude.NouveauCoupeCircuit(antifraude.OptionsCoupeCircuit{
		Facteur: cfg.SMSCoupeCircuit, Plancher: cfg.SMSCoupeCircuitPlancher, PlafondHoraire: cfg.SMSParHeure,
		Reactivation: cfg.SMSReactivation, Maintenant: h.maintenant, Journal: journal,
	})
	limiteSMS := antifraude.NouvelleLimiteEmpreinte(cfg.SMSParEmpreinte24h, h.maintenant)
	cs2, err := codes.Nouveau(codes.Options{
		Canal: "SMS", Envoyeur: consoleSMS, Empreinteur: empreinteur, Doublons: tp, Sessions: sessions,
		Limiteur: limite, LimiteurCanal: limiteSMS, Disjoncteur: coupe, Surveillance: surveillance,
		Prefixes: cfg.PrefixesAutorises, Duree: cfg.CodeDuree, Essais: cfg.SMSCodeEssais, Maintenant: h.maintenant, Journal: journal,
	})
	if err != nil {
		t.Fatal(err)
	}
	ralentisseurSMS := antifraude.NouveauRalentisseur(antifraude.OptionsRalentisseur{
		ParHeure: cfg.SMSIPParHeure, Maintenant: h.maintenant, Attendre: att.attendre, Journal: journal,
	})
	s, err := Nouveau(Options{
		Config: cfg, Catalogues: cs, Publications: b,
		Version: "v0.0.0-test", SHA256: strings.Repeat("ab", 32), Journal: journal,
		Maintenant:  h.maintenant,
		Empreinteur: empreinteur, Tampon: tp, Sessions: sessions,
		WhatsApp: wa, Verificateur: v, Altcha: captcha, Ralentisseur: ralentisseur, Codes: cv,
		SMS: integrationSMS, CodesSMS: cs2, RalentisseurSMS: ralentisseurSMS,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &banc{srv: s, base: b, schema: sc, tampon: tp, journal: j, whatsapp: wa, verificateur: v, sessions: sessions, horloge: h,
		altcha: captcha, limite: limite, surveillance: surveillance, ralentisseur: ralentisseur, attentes: att,
		codes: cv, console: console, sortieCodes: sortieCodes,
		sms: integrationSMS, codesSMS: cs2, consoleSMS: consoleSMS, sortieSMS: sortieSMS, coupeSMS: coupe, limiteSMS: limiteSMS, cfg: cfg}
}

func (b *banc) compter(t *testing.T) (participations, reponses int) {
	t.Helper()
	ctx := context.Background()
	if err := b.base.Pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM participation), (SELECT count(*) FROM reponse)").
		Scan(&participations, &reponses); err != nil {
		t.Fatal(err)
	}
	return
}

// navigateur imite un navigateur sans JavaScript : il suit les liens et
// envoie les formulaires, et garde le cookie de session (le cookiejar de Go
// n'envoie pas les cookies Secure en HTTP).
type navigateur struct {
	t       *testing.T
	h       http.Handler
	cookie  *http.Cookie
	adresse string // adresse de la connexion ; celle de httptest si vide
}

type page struct {
	statut   int
	corps    string
	location string
	entetes  http.Header
}

func (n *navigateur) faire(req *http.Request) page {
	n.t.Helper()
	if n.adresse != "" {
		req.RemoteAddr = n.adresse
	}
	if n.cookie != nil {
		req.AddCookie(n.cookie)
	}
	rec := httptest.NewRecorder()
	n.h.ServeHTTP(rec, req)
	res := rec.Result()
	for _, c := range res.Cookies() {
		if c.Name != session.NomCookie {
			continue
		}
		if c.MaxAge < 0 {
			n.cookie = nil
		} else {
			n.cookie = c
		}
	}
	corps, _ := io.ReadAll(res.Body)
	return page{statut: res.StatusCode, corps: string(corps), location: res.Header.Get("Location"), entetes: res.Header}
}

func (n *navigateur) get(chemin string) page {
	return n.faire(httptest.NewRequest(http.MethodGet, chemin, nil))
}

func (n *navigateur) post(chemin string, form url.Values) page {
	req := httptest.NewRequest(http.MethodPost, chemin, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return n.faire(req)
}

var (
	reInput      = regexp.MustCompile(`<input[^>]*>`)
	reAttr       = regexp.MustCompile(`(\w+)="([^"]*)"`)
	reFormAction = regexp.MustCompile(`<form[^>]*action="([^"]*)"`)
)

// champs liste les couples (nom, valeur) des champs d'une page.
func champs(corps string) (types map[string]string, valeurs map[string][]string) {
	types, valeurs = map[string]string{}, map[string][]string{}
	for _, in := range reInput.FindAllString(corps, -1) {
		a := map[string]string{}
		for _, m := range reAttr.FindAllStringSubmatch(in, -1) {
			a[m[1]] = m[2]
		}
		types[a["name"]] = a["type"]
		valeurs[a["name"]] = append(valeurs[a["name"]], a["value"])
	}
	return
}

// formulaireVoulu donne des réponses qui encodent i (i < 320) dans Q1, Q2,
// Q4 et Q6, pour reconnaître chaque réponse dans le dump.
func formulaireVoulu(i int) url.Values {
	return url.Values{
		"q1": {strconv.Itoa(i % 5)}, "q2": {strconv.Itoa(i / 5 % 4)}, "q3": {strconv.Itoa(i % 5)},
		"q4": {strconv.Itoa(i / 20 % 4)}, "q5": {strconv.Itoa(i % 5)}, "q6": {strconv.Itoa(i / 80 % 4)},
		"q7": {"1", "4"}, "themes": {"2", "7", "11"},
		"region": {strconv.Itoa(i % 16)}, "tranche_age": {strconv.Itoa(i % 6)},
		"sexe": {strconv.Itoa(i % 3)}, "inscrit": {strconv.Itoa(i % 4)},
		questionnaire.ChampMajeur: {"1"},
	}
}

func indiceReponse(q1, q2, q4, q6 int) int { return q1 + 5*q2 + 20*q4 + 80*q6 }

// numeroDeTest renvoie le i-ième numéro, tel que Meta transmet l'expéditeur
// (indicatif compris, sans « + »), et sa forme normalisée.
func numeroDeTest(i int) (expediteur, normalise string) {
	n := fmt.Sprintf("77%07d", (1234567+i*48271)%10000000)
	return "221" + n, "+221" + n
}

var (
	reJeton      = regexp.MustCompile(`<p class="jeton">(DIGUEE [2-9]{4}-[A-Z]{2})</p>`)
	reDefiAltcha = regexp.MustCompile(`data-altcha-algorithme="([^"]*)" data-altcha-defi="([^"]*)" data-altcha-max="([^"]*)" data-altcha-sel="([^"]*)" data-altcha-signature="([^"]*)"`)
)

// resoudreAltcha lit le défi ALTCHA écrit dans la page de vérification et le
// résout, comme le fait le navigateur avec /statique/altcha.js.
func resoudreAltcha(t *testing.T, corps string) string {
	t.Helper()
	m := reDefiAltcha.FindStringSubmatch(corps)
	if m == nil {
		t.Fatal("défi ALTCHA absent de la page de vérification")
	}
	max, _ := strconv.ParseInt(m[3], 10, 64)
	s, err := altcha.Resoudre(altcha.Defi{Algorithm: m[1], Challenge: m[2], MaxNumber: max, Salt: html.UnescapeString(m[4]), Signature: m[5]})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// demanderJeton ouvre la vérification, résout le captcha et renvoie le jeton
// affiché.
func demanderJeton(t *testing.T, n *navigateur) string {
	t.Helper()
	p := n.get("/verifier")
	if m := reFormAction.FindStringSubmatch(p.corps); m == nil || m[1] != "/verifier" {
		t.Fatalf("formulaire de vérification absent : %d", p.statut)
	}
	p = n.post("/verifier", url.Values{altcha.Champ: {resoudreAltcha(t, p.corps)}})
	if p.statut != http.StatusSeeOther || p.location != "/verifier/whatsapp" || n.cookie == nil {
		t.Fatalf("création du jeton : %d vers %q", p.statut, p.location)
	}
	p = n.get(p.location)
	m := reJeton.FindStringSubmatch(p.corps)
	if p.statut != http.StatusOK || m == nil {
		t.Fatalf("page du jeton : %d", p.statut)
	}
	return m[1]
}

// envoyerWhatsApp joue le rôle du citoyen et de Meta : un message signé
// arrive au webhook.
func (b *banc) envoyerWhatsApp(t *testing.T, expediteur, texte string) {
	t.Helper()
	if statut, err := b.whatsapp.Simuler(b.srv, expediteur, texte); err != nil || statut != http.StatusOK {
		t.Fatalf("webhook : %d %v", statut, err)
	}
}

// verifier fait la vérification inversée complète, sans JavaScript (bouton
// « J'ai envoyé le message »), et renvoie la page obtenue.
func verifier(t *testing.T, b *banc, n *navigateur, expediteur string) page {
	t.Helper()
	jeton := demanderJeton(t, n)
	b.envoyerWhatsApp(t, expediteur, jeton)
	return n.post("/verifier/whatsapp", url.Values{})
}

// parcourir fait tout le parcours, de l'accueil à la page Merci, comme une
// personne sans JavaScript. Il renvoie la dernière page.
func parcourir(t *testing.T, b *banc, expediteur string, form url.Values) page {
	t.Helper()
	n := &navigateur{t: t, h: b.srv}
	if p := n.get("/"); !strings.Contains(p.corps, `href="/verifier"`) {
		t.Fatal("l'accueil devrait mener au parcours")
	}
	p := verifier(t, b, n, expediteur)
	if p.statut != http.StatusSeeOther {
		return p
	}
	if p.location != "/questionnaire" || n.cookie == nil {
		t.Fatalf("vérification : %d vers %q", p.statut, p.location)
	}
	p = n.get(p.location)
	types, offerts := champs(p.corps)
	for nom, vs := range form {
		for _, v := range vs {
			if !contient(offerts[nom], v) {
				t.Fatalf("le questionnaire n'offre pas %s=%s", nom, v)
			}
		}
		if nom != questionnaire.ChampMajeur && types[nom] != "radio" && types[nom] != "checkbox" {
			t.Fatalf("%s : type %q", nom, types[nom])
		}
	}
	m := reFormAction.FindStringSubmatch(p.corps)
	if m == nil {
		t.Fatal("formulaire du questionnaire absent")
	}
	p = n.post(m[1], form)
	if p.statut != http.StatusOK || !strings.Contains(texte(p.corps), "Votre réponse est définitive et ne pourra pas être modifiée, car nous ne pouvons pas la retrouver.") {
		t.Fatalf("récapitulatif : %d", p.statut)
	}
	// Le récapitulatif renvoie les réponses par ses champs cachés.
	_, caches := champs(p.corps)
	m = reFormAction.FindStringSubmatch(p.corps)
	p = n.post(m[1], caches)
	if p.statut != http.StatusSeeOther {
		return p
	}
	if p.location != "/merci" || n.cookie != nil {
		t.Fatalf("envoi : %d vers %q, cookie %v", p.statut, p.location, n.cookie)
	}
	return n.get(p.location)
}

func contient(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func TestParcoursSansJavaScriptEtRienAvantConfirmation(t *testing.T) {
	b := nouveauBanc(t, map[string]string{})
	n := &navigateur{t: t, h: b.srv}
	expediteur, _ := numeroDeTest(1)

	p := verifier(t, b, n, expediteur)
	if p.statut != http.StatusSeeOther || p.location != "/questionnaire" {
		t.Fatalf("vérification : %d", p.statut)
	}
	sc := p.entetes.Get("Set-Cookie")
	for _, attr := range []string{"HttpOnly", "Secure", "SameSite=Strict", "Path=/"} {
		if !strings.Contains(sc, attr) {
			t.Errorf("cookie sans %s : %q", attr, sc)
		}
	}
	p = n.get("/questionnaire")
	if !strings.Contains(texte(p.corps), "Je déclare avoir 18 ans ou plus.") || !strings.Contains(texte(p.corps), "Emploi des jeunes") {
		t.Fatal("questionnaire incomplet")
	}
	// Questionnaire incomplet : erreurs, valeurs conservées, rien d'enregistré.
	f := formulaireVoulu(3)
	delete(f, questionnaire.ChampMajeur)
	f["themes"] = []string{"1", "2"}
	p = n.post("/questionnaire/recapitulatif", f)
	c := texte(p.corps)
	if p.statut != http.StatusUnprocessableEntity || !strings.Contains(c, "Vous devez avoir 18 ans ou plus pour répondre.") ||
		!strings.Contains(c, "Choisissez exactement 3 thèmes, ou seulement") || !strings.Contains(p.corps, `value="2" checked`) {
		t.Fatalf("erreurs attendues : %d", p.statut)
	}
	// Récapitulatif, puis « Modifier » : le questionnaire revient prérempli.
	p = n.post("/questionnaire/recapitulatif", formulaireVoulu(3))
	// Seuls les thèmes choisis (2, 7, 11) y figurent.
	if c := texte(p.corps); p.statut != http.StatusOK || strings.Contains(c, "Emploi des jeunes") || !strings.Contains(c, "Coût de la vie") {
		t.Fatalf("récapitulatif : %d", p.statut)
	}
	if !strings.Contains(p.corps, `formaction="/questionnaire"`) {
		t.Error("bouton Modifier absent")
	}
	_, caches := champs(p.corps)
	p = n.post("/questionnaire", caches)
	if p.statut != http.StatusOK || !strings.Contains(p.corps, `name="themes" value="7" checked`) {
		t.Fatalf("modification : %d", p.statut)
	}
	if np, nr := b.compter(t); np != 0 || nr != 0 || b.tampon.Attente() != 0 {
		t.Fatalf("rien ne doit être enregistré avant la confirmation : %d, %d, %d", np, nr, b.tampon.Attente())
	}
	// Confirmation : l'envoi part dans le tampon, pas encore en base.
	p = n.post("/envoyer", caches)
	if p.statut != http.StatusSeeOther || p.location != "/merci" || n.cookie != nil {
		t.Fatalf("envoi : %d %q", p.statut, p.location)
	}
	if np, nr := b.compter(t); np != 0 || nr != 0 || b.tampon.Attente() != 1 {
		t.Fatalf("l'envoi doit attendre dans le tampon : %d, %d, %d", np, nr, b.tampon.Attente())
	}
	if err := b.tampon.Vider(context.Background()); err != nil {
		t.Fatal(err)
	}
	if np, nr := b.compter(t); np != 1 || nr != 1 {
		t.Fatalf("après écriture : %d, %d", np, nr)
	}
	// La session est détruite : un second envoi est refusé.
	if p := n.post("/envoyer", caches); p.statut != http.StatusUnauthorized || !strings.Contains(p.corps, "Votre session a expiré") {
		t.Errorf("envoi sans session : %d", p.statut)
	}
}

func TestDoublonEnBaseEtDansLeTampon(t *testing.T) {
	b := nouveauBanc(t, map[string]string{})
	expediteur, norm := numeroDeTest(7)
	if p := parcourir(t, b, expediteur, formulaireVoulu(1)); p.statut != http.StatusOK || !strings.Contains(texte(p.corps), "Merci") {
		t.Fatalf("premier envoi : %d", p.statut)
	}
	deja := "Ce numéro a déjà participé. Merci !"
	// Encore dans le tampon, même écrit autrement.
	for _, s := range []string{expediteur, norm} {
		p := parcourir(t, b, s, formulaireVoulu(2))
		if p.statut != http.StatusConflict || !strings.Contains(texte(p.corps), deja) {
			t.Fatalf("doublon dans le tampon (%d) : %d", len(s), p.statut)
		}
	}
	if err := b.tampon.Vider(context.Background()); err != nil {
		t.Fatal(err)
	}
	// En base. Le même numéro a déjà été vérifié 3 fois aujourd'hui : on se
	// place le lendemain, sinon la limite par empreinte répondrait d'abord.
	b.horloge.avancer(24 * time.Hour)
	if p := parcourir(t, b, expediteur, formulaireVoulu(2)); p.statut != http.StatusConflict || !strings.Contains(texte(p.corps), deja) {
		t.Fatalf("doublon en base : %d", p.statut)
	}

	// Deux sessions vérifiées avec le même numéro : la seconde à envoyer est refusée.
	autre, _ := numeroDeTest(8)
	n1, n2 := &navigateur{t: t, h: b.srv}, &navigateur{t: t, h: b.srv}
	verifier(t, b, n1, autre)
	verifier(t, b, n2, autre)
	if p := n1.post("/envoyer", formulaireVoulu(4)); p.statut != http.StatusSeeOther {
		t.Fatalf("premier envoi : %d", p.statut)
	}
	if p := n2.post("/envoyer", formulaireVoulu(5)); p.statut != http.StatusConflict || !strings.Contains(texte(p.corps), deja) {
		t.Fatalf("second envoi : %d", p.statut)
	}
	_ = b.tampon.Vider(context.Background())
	if np, nr := b.compter(t); np != 2 || nr != 2 {
		t.Errorf("%d participations, %d réponses ; 2 attendues", np, nr)
	}
}

func TestVerificationRefuseLesNumerosInvalides(t *testing.T) {
	b := nouveauBanc(t, map[string]string{})
	for expediteur, message := range map[string]string{
		"33612345678":  "n'est pas un numéro mobile sénégalais (+221)", // France
		"22177123456":  "n'est pas un numéro mobile sénégalais (+221)", // 8 chiffres
		"221331234567": "dont le préfixe ne correspond pas",            // fixe
		"221721234567": "dont le préfixe ne correspond pas",            // 72, hors liste
	} {
		n := &navigateur{t: t, h: b.srv}
		p := verifier(t, b, n, expediteur)
		if p.statut != http.StatusOK || !strings.Contains(texte(p.corps), message) || reJeton.MatchString(p.corps) {
			t.Errorf("%s : %d", expediteur, p.statut)
		}
		if strings.Contains(p.corps, expediteur[3:]) {
			t.Errorf("le numéro ne doit pas être affiché")
		}
		if q := n.get("/questionnaire"); q.statut != http.StatusSeeOther || q.location != "/verifier" {
			t.Errorf("%s : la session ne doit pas être vérifiée", expediteur)
		}
	}
	if got := b.verificateur.Metriques().NumerosRefuses.Load(); got != 4 {
		t.Errorf("%d refus comptés, 4 attendus", got)
	}
	// Le préfixe 72 devient accepté si la configuration l'autorise.
	b2 := nouveauBanc(t, map[string]string{"DIGUEE_PREFIXES_AUTORISES": "72,77"})
	n := &navigateur{t: t, h: b2.srv}
	if p := verifier(t, b2, n, "221721234567"); p.statut != http.StatusSeeOther || p.location != "/questionnaire" {
		t.Errorf("préfixe 72 autorisé : %d", p.statut)
	}
}

func TestParcoursGardeParLaSessionEtLaPhase(t *testing.T) {
	b := nouveauBanc(t, map[string]string{})
	n := &navigateur{t: t, h: b.srv}
	if p := n.get("/questionnaire"); p.statut != http.StatusSeeOther || p.location != "/verifier" {
		t.Errorf("questionnaire sans session : %d %q", p.statut, p.location)
	}
	if p := n.post("/questionnaire/recapitulatif", formulaireVoulu(1)); p.statut != http.StatusUnauthorized {
		t.Errorf("récapitulatif sans session : %d", p.statut)
	}
	// Consultation pas encore ouverte, ou close : retour à l'accueil.
	for _, ouverture := range []string{"2027-01-15", "2026-01-01", config.ACompleter} {
		b := nouveauBanc(t, map[string]string{"DIGUEE_OUVERTURE": ouverture})
		n := &navigateur{t: t, h: b.srv}
		if p := n.get("/verifier"); p.statut != http.StatusSeeOther || p.location != "/" {
			t.Errorf("ouverture %s : %d", ouverture, p.statut)
		}
		if p := n.get("/"); strings.Contains(p.corps, `href="/verifier"`) {
			t.Errorf("ouverture %s : l'accueil ne doit pas mener au parcours", ouverture)
		}
	}
}

func TestAccueilMeneAuParcours(t *testing.T) {
	b := nouveauBanc(t, map[string]string{})
	_, corps := get(t, b.srv, "/")
	if !strings.Contains(corps, `<a class="btn" href="/verifier">Répondre à la consultation</a>`) {
		t.Error("le bouton de l'accueil devrait être un lien vers le parcours")
	}
}

func TestPagesDuParcoursLegeres(t *testing.T) {
	b := nouveauBanc(t, map[string]string{"DIGUEE_NUMERO_WHATSAPP": "+221 77 000 00 00"})
	_, css := get(t, b.srv, "/statique/diguee.css")
	_, logo := get(t, b.srv, "/statique/logo.svg")
	_, js := get(t, b.srv, "/statique/verifier.js")
	_, jsAltcha := get(t, b.srv, "/statique/altcha.js")
	if !strings.Contains(jsAltcha, "crypto.subtle.digest") {
		t.Fatal("/statique/altcha.js absent")
	}
	n := &navigateur{t: t, h: b.srv}
	expediteur, _ := numeroDeTest(11)
	pages := map[string]page{"verifier": n.get("/verifier")}
	jeton := demanderJeton(t, n)
	pages["jeton"] = n.get("/verifier/whatsapp")
	pages["jeton pas encore reçu"] = n.post("/verifier/whatsapp", url.Values{})
	b.envoyerWhatsApp(t, expediteur, jeton)
	n.post("/verifier/whatsapp", url.Values{})
	pages["questionnaire"] = n.get("/questionnaire")
	pages["questionnaire avec erreurs"] = n.post("/questionnaire/recapitulatif", url.Values{})
	pages["recapitulatif"] = n.post("/questionnaire/recapitulatif", formulaireVoulu(9))
	_, caches := champs(pages["recapitulatif"].corps)
	n.post("/envoyer", caches)
	pages["merci"] = n.get("/merci")
	pages["deja"] = parcourir(t, b, expediteur, formulaireVoulu(9))
	pages["session expiree"] = n.post("/envoyer", caches)
	n2 := &navigateur{t: t, h: b.srv}
	demanderJeton(t, n2)
	b.horloge.avancer(11 * time.Minute)
	pages["jeton expiré"] = n2.get("/verifier/whatsapp")
	pages["simulation"] = n2.get("/dev/whatsapp")
	n3 := &navigateur{t: t, h: b.srv}
	pages["sans javascript"] = n3.get("/verifier/sans-javascript")
	pages["sms"] = n3.get("/verifier/sms")
	pages["captcha refusé"] = n3.post("/verifier", url.Values{altcha.Champ: {"x"}})
	// Code de secours (ticket #9).
	n4 := &navigateur{t: t, h: b.srv}
	pages["code"] = n4.get("/verifier/code")
	_, numeroCode := numeroDeTest(12)
	codeEnvoye(t, b, n4, numeroCode)
	pages["code saisie"] = n4.get("/verifier/code/saisie")
	pages["code faux"] = saisir(n4, faux(b.console.Dernier()))
	pages["code numéro refusé"] = demanderCode(t, &navigateur{t: t, h: b.srv}, "12")
	b.horloge.avancer(11 * time.Minute)
	pages["code expiré"] = n4.get("/verifier/code/saisie")
	// Code par SMS (ticket #10), sans JavaScript.
	n5 := &navigateur{t: t, h: b.srv}
	_, numeroSMS := numeroDeTest(13)
	smsEnvoye(t, b, n5, numeroSMS, false)
	pages["sms saisie"] = n5.get("/verifier/sms/saisie")
	pages["dev sms"] = n5.get("/dev/sms")
	_, autreSMS := numeroDeTest(14)
	smsEnvoye(t, b, &navigateur{t: t, h: b.srv}, autreSMS, false)
	pages["sms sans js refusé"] = demanderSMS(t, &navigateur{t: t, h: b.srv}, autreSMS, false)
	for b.coupeSMS.Autoriser() {
	}
	pages["sms suspendu"] = n5.get("/verifier/sms")
	for nom, p := range pages {
		// Deux pages seulement chargent un script, servi depuis le domaine,
		// et leur CSP est la seule à l'autoriser : la page du jeton
		// (interrogation) et la page de vérification (captcha ALTCHA, sans
		// connexion au serveur).
		script := map[string]string{"jeton": "verifier.js", "jeton pas encore reçu": "verifier.js",
			"verifier": "altcha.js", "captcha refusé": "altcha.js", "code": "altcha.js", "code numéro refusé": "altcha.js",
			"sms": "altcha.js", "sms sans js refusé": "altcha.js"}[nom]
		avecScript := script == "verifier.js"
		poids := len(p.corps) + len(css) + len(logo)
		switch script {
		case "verifier.js":
			poids += len(js)
		case "altcha.js":
			poids += len(jsAltcha)
		}
		if poids >= 100*1024 {
			t.Errorf("%s pèse %d octets", nom, poids)
		}
		bas := strings.ToLower(p.corps)
		scripts := strings.Count(bas, "<script")
		csp := p.entetes.Get("Content-Security-Policy")
		if script != "" {
			if scripts != 1 || !strings.Contains(p.corps, `<script src="/statique/`+script+`" defer></script>`) ||
				!strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe") {
				t.Errorf("%s : script ou CSP inattendus (%q)", nom, csp)
			}
			if script == "altcha.js" && strings.Contains(csp, "connect-src") {
				t.Errorf("%s : le captcha n'a besoin d'aucune connexion (%q)", nom, csp)
			}
		} else if scripts != 0 || strings.Contains(csp, "script-src") {
			t.Errorf("%s : aucun script attendu", nom)
		}
		for _, interdit := range []string{"javascript:", " onload=", " onclick=", " onsubmit=", " onchange="} {
			if strings.Contains(bas, interdit) {
				t.Errorf("%s contient %q", nom, interdit)
			}
		}
		// Seuls liens sortants : le partage WhatsApp de la page Merci, sans
		// identifiant, et le lien wa.me de la page du jeton.
		externes := strings.Count(bas, "https://") + strings.Count(bas, "http://")
		svg := strings.Count(bas, "http://www.w3.org/2000/svg")
		switch {
		case nom == "merci":
			if externes != 1 || !strings.Contains(p.corps, `href="https://wa.me/?text=`) {
				t.Errorf("merci : lien de partage attendu")
			}
		case avecScript:
			if externes-svg != 1 || !strings.Contains(p.corps, `href="https://wa.me/221770000000?text=DIGUEE%20`) {
				t.Errorf("%s : lien wa.me attendu", nom)
			}
		case externes != 0:
			t.Errorf("%s contient un lien sortant", nom)
		}
		if strings.Contains(p.corps, "⟦") {
			t.Errorf("%s utilise une clé absente du catalogue", nom)
		}
		if cc := p.entetes.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s : Cache-Control %q", nom, cc)
		}
	}
	for nom, attendu := range map[string]string{
		"jeton":                 "J'ai envoyé le message",
		"jeton pas encore reçu": "Nous n'avons pas encore reçu votre message.",
		"jeton expiré":          "Ce message de vérification a expiré.",
		"deja":                  "Ce numéro a déjà participé. Merci\u00a0!",
		"simulation":            "Simulation de WhatsApp",
		"verifier":              "votre navigateur fait un petit calcul",
		"captcha refusé":        "La vérification anti-robot n'a pas abouti.",
		"sans javascript":       "Vérifier mon numéro par SMS",
		"sms":                   "Recevoir un code par SMS",
		"code":                  "Recevoir mon code",
		"code saisie":           "Il vous reste 5 essais.",
		"code faux":             "Il vous reste 4 essais.",
		"code numéro refusé":    "n'est pas un numéro mobile sénégalais",
		"code expiré":           "Ce code n'est plus valable.",
		"sms saisie":            "Code reçu par SMS",
		"dev sms":               "Simulation des SMS",
		"sms sans js refusé":    "Trop de codes ont été demandés sans JavaScript",
		"sms suspendu":          "momentanément indisponible",
	} {
		if !strings.Contains(texte(pages[nom].corps), attendu) {
			t.Errorf("%s : %q attendu", nom, attendu)
		}
	}
	if !strings.Contains(pages["jeton"].corps, `<figure class="qr">`) {
		t.Error("QR code absent de la page du jeton")
	}
}

func TestCataloguesCouvrentLeQuestionnaire(t *testing.T) {
	cs, _ := i18n.Charger()
	fr := cs.Choisir("fr")
	q := questionnaire.Courant()
	cles := []string{q.CleMajeur, questionnaire.ErrObligatoire, questionnaire.ErrInvalide,
		questionnaire.ErrExclusif, questionnaire.ErrThemes, questionnaire.ErrMajeur}
	for _, x := range q.Questions {
		cles = append(cles, x.Cle)
		if x.Aide != "" {
			cles = append(cles, x.Aide)
		}
		for _, c := range x.Choix {
			cles = append(cles, c.Cle)
		}
	}
	for _, c := range cles {
		if !fr.Existe(c) {
			t.Errorf("clé %q absente du catalogue fr", c)
		}
	}
}

func TestSimulationWhatsAppInterditeEnProduction(t *testing.T) {
	cfg, _ := config.Charger(func(k string) string {
		return map[string]string{"DIGUEE_ENV": "production", "DIGUEE_POSTGRES_URL": "x"}[k]
	})
	cs, _ := i18n.Charger()
	wa, err := whatsapp.Construire(cfg, &integration.Registre{})
	if err != nil || !wa.Fausse() {
		t.Fatal(err)
	}
	if _, err := Nouveau(Options{Config: cfg, Catalogues: cs, WhatsApp: wa}); err == nil {
		t.Error("le mode fake de WhatsApp doit être refusé en production")
	}
}

// pgDump renvoie le dump complet du schéma de test.
func pgDump(t *testing.T, sc basetest.Schema) string {
	t.Helper()
	outil := os.Getenv("DIGUEE_TEST_PG_DUMP")
	if outil == "" {
		outil = "pg_dump"
	}
	if _, err := exec.LookPath(outil); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("pg_dump introuvable : %v", err)
		}
		t.Skipf("pg_dump introuvable (DIGUEE_TEST_PG_DUMP) : test sauté")
	}
	u, err := url.Parse(sc.URL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Del("search_path")
	u.RawQuery = q.Encode()
	out, err := exec.Command(outil, "--no-owner", "--schema="+sc.Nom, "--dbname="+u.String()).CombinedOutput()
	if err != nil {
		t.Fatalf("pg_dump : %v\n%s", err, out)
	}
	return string(out)
}

// lignesCopy renvoie les lignes de données du bloc COPY d'une table.
func lignesCopy(t *testing.T, dump, table string) (colonnes []string, lignes [][]string) {
	t.Helper()
	sc := bufio.NewScanner(strings.NewReader(dump))
	sc.Buffer(make([]byte, 0, 1<<16), 16<<20) // une ligne de publication dépasse 64 Ko
	dedans := false
	reEntete := regexp.MustCompile(`^COPY \S+\.` + table + ` \(([^)]*)\) FROM stdin;$`)
	for sc.Scan() {
		l := sc.Text()
		switch {
		case !dedans:
			if m := reEntete.FindStringSubmatch(l); m != nil {
				dedans = true
				colonnes = strings.Split(m[1], ", ")
			}
		case l == `\.`:
			return colonnes, lignes
		default:
			lignes = append(lignes, strings.Split(l, "\t"))
		}
	}
	t.Fatalf("bloc COPY de %s introuvable", table)
	return nil, nil
}

// T1 et T2 (spec §14) sur 200 envois faits par tout le parcours web.
func Test200EnvoisDumpSansNumeroNiIPNiHeureEtSansAppariement(t *testing.T) {
	b := nouveauBanc(t, map[string]string{})
	const nb = 200
	empreintes := map[string]int{} // empreinte (hex) -> ordre d'arrivée
	var numeros []string
	h := empreinte.NouveauHMACLocal(nil)
	for i := range nb {
		expediteur, norm := numeroDeTest(i)
		numeros = append(numeros, norm)
		e, _ := h.Calculer(context.Background(), norm)
		empreintes[hex.EncodeToString(e[:])] = i
		if p := parcourir(t, b, expediteur, formulaireVoulu(i)); p.statut != http.StatusOK {
			t.Fatalf("envoi %d : %d", i, p.statut)
		}
	}
	if b.tampon.Attente() != 0 {
		t.Fatalf("200 envois = 10 lots de 20 : %d en attente", b.tampon.Attente())
	}
	if np, nr := b.compter(t); np != nb || nr != nb {
		t.Fatalf("%d participations, %d réponses", np, nr)
	}
	dump := pgDump(t, b.schema)
	journal := b.journal.String()

	// T1 (et T3 pour le journal applicatif) : ni numéro, ni IP, ni heure.
	for _, src := range []struct{ nom, contenu string }{{"dump", dump}, {"journal", journal}} {
		for _, num := range numeros {
			for _, forme := range []string{num, num[4:], num[1:], num[4:6] + " " + num[6:9]} {
				if strings.Contains(src.contenu, forme) {
					t.Fatalf("%s : contient un numéro", src.nom)
				}
			}
		}
		if m := regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b|192\.0\.2\.|\[?::1\]?`).FindString(src.contenu); m != "" {
			t.Errorf("%s : adresse IP %q", src.nom, m)
		}
		// Ni jeton, ni corps de notification WhatsApp.
		for _, motif := range []string{"DIGUEE ", "whatsapp_business_account", "wamid", `"from"`} {
			if strings.Contains(src.contenu, motif) {
				t.Errorf("%s : contient %q", src.nom, motif)
			}
		}
	}
	donnees := regexp.MustCompile(`(?m)^--.*$`).ReplaceAllString(dump, "")
	// Seule exception : la table du journal public (spec §10), horodatée à la
	// minute parce que ses entrées sont publiques. Les envois n'y écrivent
	// rien : l'heure d'une entrée ne peut pas dater une réponse.
	reJournal := regexp.MustCompile(`(?s)CREATE TABLE \S+\.journal_public \(.*?\n\);\n`)
	if n := len(reJournal.FindAllString(donnees, -1)); n != 1 {
		t.Errorf("table journal_public trouvée %d fois dans le dump", n)
	}
	donnees = reJournal.ReplaceAllString(donnees, "")
	if _, lj := lignesCopy(t, dump, "journal_public"); len(lj) != 0 {
		t.Errorf("les envois ont écrit %d entrée(s) au journal public", len(lj))
	}
	for _, motif := range []string{`\d{1,2}:\d{2}`, `(?i)\btimestamp`, `(?i)\btime\b`, `(?i)\binterval\b`, `(?i)\bserial\b`, `(?i)nextval`} {
		if m := regexp.MustCompile(motif).FindString(donnees); m != "" {
			t.Errorf("dump : horodatage ou séquence %q", m)
		}
	}
	colsR, reps := lignesCopy(t, dump, "reponse")
	_, parts := lignesCopy(t, dump, "participation")
	if len(reps) != nb || len(parts) != nb {
		t.Fatalf("%d réponses, %d participations dans le dump", len(reps), len(parts))
	}
	col := map[string]int{}
	for i, c := range colsR {
		col[c] = i
	}
	if len(colsR) != 15 {
		t.Errorf("colonnes de reponse : %v", colsR)
	}
	jourAttendu := time.Now().UTC().Format(time.DateOnly)
	ordreReponses := make([]int, nb)
	for k, l := range reps {
		if j := l[col["jour"]]; j != jourAttendu && j != time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly) {
			t.Errorf("jour %q", j)
		}
		v := func(c string) int { n, _ := strconv.Atoi(l[col[c]]); return n }
		ordreReponses[k] = indiceReponse(v("q1"), v("q2"), v("q4"), v("q6"))
	}
	ordreParticipations := make([]int, nb)
	for k, l := range parts {
		i, ok := empreintes[strings.TrimPrefix(l[0], `\\x`)]
		if !ok {
			t.Fatalf("empreinte inconnue dans le dump : %q", l[0])
		}
		ordreParticipations[k] = i
	}

	// T2. L'attaquant connaît l'ordre d'arrivée des 200 envois et donc les
	// lots (envois 0-19, 20-39…). Au hasard, apparier la k-ième participation
	// du dump avec la k-ième réponse donne un bon appariement par lot en
	// moyenne (10 sur 200, loi proche de Poisson(10)) ; sans mélange, on en
	// aurait 200. Seuil : 30 (probabilité de dépassement au hasard < 1e-7).
	apparies, enPlaceP, enPlaceR := 0, 0, 0
	for k := range nb {
		if ordreParticipations[k] == ordreReponses[k] {
			apparies++
		}
		if ordreParticipations[k] == k {
			enPlaceP++
		}
		if ordreReponses[k] == k {
			enPlaceR++
		}
	}
	if apparies > 30 || enPlaceP > 30 || enPlaceR > 30 {
		t.Errorf("appariements %d, participations à leur place %d, réponses à leur place %d (≈ 10 attendus au hasard)",
			apparies, enPlaceP, enPlaceR)
	}
	// Dans chaque lot, l'ordre du dump ne suit pas l'ordre d'arrivée : parmi
	// les 1 900 paires d'envois d'un même lot, la part rangée dans le même
	// ordre est proche de 1/2 (écart type ≈ 0,026 ; bornes à plus de 5 écarts).
	for nom, ordre := range map[string][]int{"participations": ordreParticipations, "réponses": ordreReponses} {
		position := make([]int, nb)
		for k, i := range ordre {
			position[i] = k
		}
		concordantes, paires := 0, 0
		for i := range nb {
			for j := i + 1; j < nb && j/20 == i/20; j++ {
				paires++
				if position[i] < position[j] {
					concordantes++
				}
			}
		}
		if f := float64(concordantes) / float64(paires); f < 0.35 || f > 0.65 {
			t.Errorf("%s : %.2f des paires gardent l'ordre d'arrivée (0,5 attendu au hasard)", nom, f)
		}
	}

	// Ticket #11 : la table publication apparaît dans le dump. Une
	// publication sur ces 200 réponses (seuil abaissé à 100 pour qu'elle
	// contienne des tableaux) n'y ajoute que des agrégats datés au jour : ni
	// numéro, ni IP, ni heure, ni réponse individuelle (aucun identifiant de
	// réponse), et une seule entrée au journal public, la sienne.
	rendu, err := RendreInstantane(b.srv.opt.Catalogues, questionnaire.Courant())
	if err != nil {
		t.Fatal(err)
	}
	p := &publication.Publieur{Magasin: b.base, Questionnaire: questionnaire.Courant(), SeuilPublication: 100,
		SeuilCase: b.cfg.SeuilCase, Rendu: rendu}
	r, err := p.Publier(context.Background(), time.Now().UTC().AddDate(0, 0, 1))
	if err != nil || !r.Nouvelle || !r.Publication.ResultatsPublies || r.Publication.Reponses != nb {
		t.Fatalf("publication : %+v, %v", r.Publication, err)
	}
	dump = pgDump(t, b.schema)
	colsP, pubs := lignesCopy(t, dump, "publication")
	if len(pubs) != 1 || strings.Join(colsP, ",") != "jour,reponses,resultats_publies,instantane,html,empreinte" {
		t.Fatalf("publication dans le dump : %v, %d ligne(s)", colsP, len(pubs))
	}
	instantane := strings.Join(pubs[0], "\t")
	for _, num := range numeros {
		if strings.Contains(instantane, num[4:]) || strings.Contains(instantane, num[1:]) {
			t.Fatal("l'instantané contient un numéro")
		}
	}
	for _, motif := range []string{`\b(?:\d{1,3}\.){3}\d{1,3}\b`, `\d{1,2}:\d{2}`, `(?i)\btime`, `(?i)GMT`, `"id"`, `[0-9a-f]{8}-[0-9a-f]{4}-`} {
		if m := regexp.MustCompile(motif).FindString(instantane); m != "" {
			t.Errorf("instantané : %q (motif %s)", m, motif)
		}
	}
	donnees = regexp.MustCompile(`(?m)^--.*$`).ReplaceAllString(dump, "")
	donnees = reJournal.ReplaceAllString(donnees, "")
	// Seule exception, toujours : l'entrée du journal public, horodatée à la
	// minute (spec §10), écrite par la publication et non par un envoi.
	donnees = regexp.MustCompile(`(?s)COPY \S+\.journal_public .*?\n\\\.\n`).ReplaceAllString(donnees, "")
	for _, motif := range []string{`\d{1,2}:\d{2}`, `(?i)\btimestamp`, `(?i)\btime\b`, `(?i)\binterval\b`, `(?i)\bserial\b`, `(?i)nextval`} {
		if m := regexp.MustCompile(motif).FindString(donnees); m != "" {
			t.Errorf("dump après publication : horodatage ou séquence %q", m)
		}
	}
	if _, lj := lignesCopy(t, dump, "journal_public"); len(lj) != 1 || !strings.Contains(strings.Join(lj[0], "\t"), "publication_quotidienne") {
		t.Errorf("journal public après publication : %d entrée(s)", len(lj))
	}
}
