package web

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/whatsapp"
)

// webhook envoie un corps au webhook avec l'en-tête de signature donné
// (aucun en-tête s'il est vide).
func (b *banc) webhook(t *testing.T, corps []byte, signature string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, whatsapp.CheminWebhook, bytes.NewReader(corps))
	req.Header.Set("Content-Type", "application/json")
	if signature != "" {
		req.Header.Set("X-Hub-Signature-256", signature)
	}
	rec := httptest.NewRecorder()
	b.srv.ServeHTTP(rec, req)
	r, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, string(r)
}

func (b *banc) signer(corps []byte) string { return whatsapp.Signature(corps, b.whatsapp.SecretApp) }

func estVerifiee(n *navigateur) bool {
	p := n.get("/questionnaire")
	return p.statut == http.StatusOK
}

// T6 : un webhook dont la signature est invalide est rejeté, et rien n'est
// vérifié.
func TestWebhookSignatureInvalideRejetee(t *testing.T) {
	b := nouveauBanc(t, map[string]string{})
	n := &navigateur{t: t, h: b.srv}
	jeton := demanderJeton(t, n)
	expediteur, _ := numeroDeTest(21)
	corps := whatsapp.Notification(b.whatsapp.IDNumero, expediteur, jeton)
	autre := whatsapp.Notification(b.whatsapp.IDNumero, "221770000001", jeton)
	for nom, sig := range map[string]string{
		"sans signature":     "",
		"autre secret":       whatsapp.Signature(corps, []byte("un autre secret d'application")),
		"sans préfixe":       strings.TrimPrefix(b.signer(corps), "sha256="),
		"hexadécimal faux":   "sha256=zz" + b.signer(corps)[9:],
		"tronquée":           b.signer(corps)[:40],
		"d'un autre corps":   b.signer(autre),
		"algorithme inconnu": "sha1=" + b.signer(corps)[7:],
	} {
		if statut, _ := b.webhook(t, corps, sig); statut != http.StatusUnauthorized {
			t.Errorf("%s : statut %d", nom, statut)
		}
	}
	if estVerifiee(n) {
		t.Fatal("une notification mal signée ne doit rien vérifier")
	}
	if statut, _ := b.webhook(t, corps, b.signer(corps)); statut != http.StatusOK {
		t.Fatalf("notification bien signée : %d", statut)
	}
	if p := n.get("/verifier/whatsapp"); p.statut != http.StatusSeeOther || p.location != "/questionnaire" {
		t.Fatalf("après la notification : %d %q", p.statut, p.location)
	}
	j := b.journal.String()
	for _, interdit := range []string{expediteur, expediteur[3:], jeton, "whatsapp_business_account"} {
		if strings.Contains(j, interdit) {
			t.Errorf("le journal contient %q", interdit)
		}
	}
	if !strings.Contains(j, "signature invalide") {
		t.Error("les rejets devraient être journalisés (sans le corps)")
	}
}

func TestPoigneeDeMainDuWebhook(t *testing.T) {
	b := nouveauBanc(t, map[string]string{})
	n := &navigateur{t: t, h: b.srv}
	q := url.Values{"hub.mode": {"subscribe"}, "hub.verify_token": {b.whatsapp.JetonWebhook}, "hub.challenge": {"1158201444"}}
	if p := n.get(whatsapp.CheminWebhook + "?" + q.Encode()); p.statut != http.StatusOK || p.corps != "1158201444" {
		t.Errorf("poignée de main : %d %q", p.statut, p.corps)
	}
	for nom, modif := range map[string][2]string{
		"mauvais jeton":   {"hub.verify_token", "autre"},
		"mode inconnu":    {"hub.mode", "unsubscribe"},
		"défi suspect":    {"hub.challenge", "<script>"},
		"défi manquant":   {"hub.challenge", ""},
		"jeton manquant":  {"hub.verify_token", ""},
		"jeton tronqué":   {"hub.verify_token", b.whatsapp.JetonWebhook[:10]},
		"jeton prolongé":  {"hub.verify_token", b.whatsapp.JetonWebhook + "x"},
		"mode en majuscu": {"hub.mode", "SUBSCRIBE"},
	} {
		q2 := url.Values{}
		for k, v := range q {
			q2[k] = v
		}
		q2.Set(modif[0], modif[1])
		if p := n.get(whatsapp.CheminWebhook + "?" + q2.Encode()); p.statut != http.StatusForbidden {
			t.Errorf("%s : %d", nom, p.statut)
		}
	}
	if p := n.faire(httptest.NewRequest(http.MethodPut, whatsapp.CheminWebhook, nil)); p.statut != http.StatusMethodNotAllowed {
		t.Errorf("PUT : %d", p.statut)
	}
}

// Un jeton expiré, révoqué ou déjà utilisé ne vérifie rien ; un même jeton
// reçu de deux expéditeurs ne compte que pour le premier.
func TestJetonExpireOuDejaUtilise(t *testing.T) {
	b := nouveauBanc(t, map[string]string{})
	a, _ := numeroDeTest(31)
	c, _ := numeroDeTest(32)

	// Expiré : DIGUEE_JETON_DUREE (10 minutes) écoulée.
	n := &navigateur{t: t, h: b.srv}
	jeton := demanderJeton(t, n)
	b.horloge.avancer(10 * time.Minute)
	b.envoyerWhatsApp(t, a, jeton)
	if p := n.post("/verifier/whatsapp", url.Values{}); p.statut != http.StatusOK ||
		!strings.Contains(texte(p.corps), "Ce message de vérification a expiré.") || reJeton.MatchString(p.corps) {
		t.Errorf("jeton expiré : %d", p.statut)
	}
	if estVerifiee(n) {
		t.Fatal("un jeton expiré ne doit rien vérifier")
	}

	// Révoqué : un nouveau message a été demandé.
	ancien := demanderJeton(t, n)
	nouveau := demanderJeton(t, n)
	if ancien == nouveau {
		t.Fatal("deux jetons identiques")
	}
	b.envoyerWhatsApp(t, a, ancien)
	if estVerifiee(n) {
		t.Fatal("un jeton révoqué ne doit rien vérifier")
	}

	// Utilisé : le premier expéditeur compte, le second est ignoré et compté.
	b.envoyerWhatsApp(t, a, "Bonjour, voici mon code : "+strings.ToLower(nouveau))
	b.envoyerWhatsApp(t, a, nouveau) // Meta livre parfois deux fois
	if got := b.verificateur.Metriques().SecondExpediteur.Load(); got != 0 {
		t.Errorf("une nouvelle livraison n'est pas un incident : %d", got)
	}
	b.envoyerWhatsApp(t, c, nouveau)
	if got := b.verificateur.Metriques().SecondExpediteur.Load(); got != 1 {
		t.Errorf("incident « second expéditeur » compté %d fois, 1 attendu", got)
	}
	if !estVerifiee(n) {
		t.Fatal("le premier expéditeur doit vérifier la session")
	}
	// La session vérifiée porte l'empreinte du premier expéditeur : après
	// l'envoi, c'est lui, et pas le second, qui a déjà participé.
	_, caches := champs(n.post("/questionnaire/recapitulatif", formulaireVoulu(3)).corps)
	if p := n.post("/envoyer", caches); p.statut != http.StatusSeeOther {
		t.Fatalf("envoi : %d", p.statut)
	}
	if p := parcourir(t, b, c, formulaireVoulu(4)); p.statut != http.StatusOK {
		t.Errorf("le second expéditeur n'a pas participé : %d", p.statut)
	}
	if p := parcourir(t, b, a, formulaireVoulu(5)); p.statut != http.StatusConflict {
		t.Errorf("le premier expéditeur a déjà participé : %d", p.statut)
	}

	// Un jeton déjà utilisé ne vérifie pas une autre session.
	n2 := &navigateur{t: t, h: b.srv}
	demanderJeton(t, n2)
	b.envoyerWhatsApp(t, c, nouveau)
	if estVerifiee(n2) {
		t.Fatal("un jeton déjà utilisé ne doit rien vérifier")
	}
	if m := b.verificateur.Metriques(); m.JetonsInconnus.Load() < 2 || m.Verifications.Load() != 2 {
		t.Errorf("métriques : %d inconnus, %d vérifications", m.JetonsInconnus.Load(), m.Verifications.Load())
	}
}

// La page détecte la vérification par l'interrogation (JavaScript) et
// change l'identifiant de session avant le questionnaire.
func TestDetectionParInterrogation(t *testing.T) {
	b := nouveauBanc(t, map[string]string{})
	n := &navigateur{t: t, h: b.srv}
	if p := n.get("/verifier/etat"); p.corps != "{\"etat\":\"fin\"}\n" {
		t.Errorf("sans session : %q", p.corps)
	}
	jeton := demanderJeton(t, n)
	p := n.get("/verifier/etat")
	if p.statut != http.StatusOK || p.corps != "{\"etat\":\"attente\"}\n" || p.entetes.Get("Cache-Control") != "no-store" ||
		!strings.HasPrefix(p.entetes.Get("Content-Type"), "application/json") {
		t.Fatalf("attente : %d %q", p.statut, p.corps)
	}
	expediteur, _ := numeroDeTest(41)
	b.envoyerWhatsApp(t, expediteur, jeton)
	if p := n.get("/verifier/etat"); p.corps != "{\"etat\":\"fin\"}\n" {
		t.Fatalf("après réception : %q", p.corps)
	}
	ancien := n.cookie
	p = n.get("/verifier/whatsapp") // rechargement par le script
	if p.statut != http.StatusSeeOther || p.location != "/questionnaire" || n.cookie == nil || n.cookie.Value == ancien.Value {
		t.Fatalf("passage au questionnaire : %d %q", p.statut, p.location)
	}
	if !estVerifiee(n) {
		t.Fatal("la session devrait être vérifiée")
	}
	vieux := &navigateur{t: t, h: b.srv, cookie: ancien}
	if estVerifiee(vieux) {
		t.Error("l'ancien identifiant de session ne doit plus rien valoir")
	}
	// Le script est servi depuis le domaine.
	if res, js := get(t, b.srv, "/statique/verifier.js"); res.StatusCode != http.StatusOK ||
		!strings.Contains(js, `"/verifier/etat"`) || !strings.Contains(js, "3000") || strings.Contains(js, "http") {
		t.Errorf("script : %d", res.StatusCode)
	}
}

// Les notifications valides mais sans rapport (statuts, autres numéros,
// autres types) reçoivent 200 et ne vérifient rien.
func TestNotificationsNonPertinentes(t *testing.T) {
	b := nouveauBanc(t, map[string]string{})
	n := &navigateur{t: t, h: b.srv}
	jeton := demanderJeton(t, n)
	expediteur, _ := numeroDeTest(51)
	statuts := []byte(`{"object":"whatsapp_business_account","entry":[{"id":"1","changes":[{"field":"messages","value":` +
		`{"messaging_product":"whatsapp","metadata":{"phone_number_id":"` + b.whatsapp.IDNumero + `"},` +
		`"statuses":[{"id":"wamid.x","status":"delivered","recipient_id":"221770000000"}]}}]}]}`)
	image := bytes.Replace(whatsapp.Notification(b.whatsapp.IDNumero, expediteur, jeton), []byte(`"type":"text"`), []byte(`"type":"image"`), 1)
	for nom, corps := range map[string][]byte{
		"statuts":       statuts,
		"autre numéro":  whatsapp.Notification("999999999999999", expediteur, jeton),
		"image":         image,
		"sans jeton":    whatsapp.Notification(b.whatsapp.IDNumero, expediteur, "Bonjour"),
		"autre objet":   bytes.Replace(statuts, []byte("whatsapp_business_account"), []byte("page"), 1),
		"autre champ":   bytes.Replace(whatsapp.Notification(b.whatsapp.IDNumero, expediteur, jeton), []byte(`"field":"messages"`), []byte(`"field":"account_update"`), 1),
		"jeton inconnu": whatsapp.Notification(b.whatsapp.IDNumero, expediteur, "DIGUEE 2345-AC"),
	} {
		if statut, rep := b.webhook(t, corps, b.signer(corps)); statut != http.StatusOK || rep != "" {
			t.Errorf("%s : %d %q", nom, statut, rep)
		}
	}
	if statut, _ := b.webhook(t, []byte("{pas du json"), b.signer([]byte("{pas du json"))); statut != http.StatusBadRequest {
		t.Errorf("JSON invalide signé : %d", statut)
	}
	if estVerifiee(n) {
		t.Fatal("aucune de ces notifications ne doit vérifier la session")
	}
}

// En mode meta, la page de simulation n'existe pas et le secret de
// développement ne signe rien.
func TestModeMetaSansSimulation(t *testing.T) {
	b := nouveauBanc(t, map[string]string{
		"DIGUEE_WHATSAPP": "meta", "DIGUEE_META_ID_NUMERO": "123456789012345",
		"DIGUEE_META_SECRET_APP": "0123456789abcdef0123456789abcdef", "DIGUEE_META_JETON_WEBHOOK": "un-jeton-de-verification-long",
	})
	n := &navigateur{t: t, h: b.srv}
	if p := n.get("/dev/whatsapp"); p.statut != http.StatusNotFound {
		t.Errorf("page de simulation en mode meta : %d", p.statut)
	}
	jeton := demanderJeton(t, n)
	if strings.Contains(n.get("/verifier/whatsapp").corps, "/dev/whatsapp") {
		t.Error("lien vers la simulation en mode meta")
	}
	if _, err := b.whatsapp.Simuler(b.srv, "221771234567", jeton); err == nil {
		t.Error("la simulation doit être refusée en mode meta")
	}
	expediteur, _ := numeroDeTest(61)
	corps := whatsapp.Notification("123456789012345", expediteur, jeton)
	if statut, _ := b.webhook(t, corps, whatsapp.Signature(corps, []byte("diguee : secret d'application de developpement, interdit en production"))); statut != http.StatusUnauthorized {
		t.Errorf("secret de développement en mode meta : %d", statut)
	}
	if statut, _ := b.webhook(t, corps, whatsapp.Signature(corps, []byte("0123456789abcdef0123456789abcdef"))); statut != http.StatusOK {
		t.Errorf("secret de l'application : %d", statut)
	}
	if p := n.post("/verifier/whatsapp", url.Values{}); p.statut != http.StatusSeeOther || p.location != "/questionnaire" {
		t.Errorf("vérification en mode meta : %d", p.statut)
	}
}

// La page de simulation remet au webhook une notification signée.
func TestPageDeSimulation(t *testing.T) {
	b := nouveauBanc(t, map[string]string{})
	n := &navigateur{t: t, h: b.srv}
	jeton := demanderJeton(t, n)
	p := n.get("/dev/whatsapp")
	if !strings.Contains(p.corps, `value="`+jeton+`"`) {
		t.Error("le jeton de la session devrait être prérempli")
	}
	p = n.post("/dev/whatsapp", url.Values{"numero": {"+221 76 987 65 43"}, "texte": {jeton}})
	if p.statut != http.StatusOK || !strings.Contains(texte(p.corps), "statut 200") || strings.Contains(p.corps, "987 65 43") {
		t.Fatalf("simulation : %d", p.statut)
	}
	if !estVerifiee(n) {
		t.Fatal("la simulation devrait vérifier la session")
	}
	if strings.Contains(b.journal.String(), "769876543") {
		t.Error("le journal contient le numéro")
	}
}
