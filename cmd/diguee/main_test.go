package main

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/altcha"
	"github.com/2guntan/diguee/internal/base/basetest"
	"github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/session"
	"github.com/2guntan/diguee/internal/version"
)

func valeursCompletes(t *testing.T) {
	for k, v := range map[string]string{
		"DIGUEE_STRUCTURE_NOM": "Structure SARL", "DIGUEE_STRUCTURE_ADRESSE": "Dakar",
		"DIGUEE_CONTACT": "droits@example.org", "DIGUEE_NUMERO_CDP": "CDP-0000",
		"DIGUEE_NUMERO_WHATSAPP": "+221700000000", "DIGUEE_OUVERTURE": "2026-11-01",
	} {
		t.Setenv(k, v)
	}
}

func silencieux() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Le serveur refuse de démarrer en production avec l'empreinteur de
// développement, avant même de se connecter à la base.
func TestProductionRefuseLEmpreinteurFactice(t *testing.T) {
	valeursCompletes(t)
	t.Setenv("DIGUEE_ENV", "production")
	t.Setenv("DIGUEE_POSTGRES_URL", "postgres://personne@127.0.0.1:1/aucune")
	err := lancer(nil, silencieux())
	if err == nil || !strings.Contains(err.Error(), "démarrage refusé en production") ||
		!strings.Contains(err.Error(), "l'intégration empreinteur est en mode fake (réglage : DIGUEE_EMPREINTEUR)") {
		t.Fatalf("refus attendu : %v", err)
	}
	if !strings.Contains(err.Error(), "l'intégration whatsapp est en mode fake (réglage : DIGUEE_WHATSAPP)") {
		t.Errorf("la simulation WhatsApp devrait aussi bloquer : %v", err)
	}
	if !strings.Contains(err.Error(), "l'intégration captcha_altcha est en mode fake (réglage : DIGUEE_CLE_ALTCHA)") {
		t.Errorf("la clé de développement du captcha devrait aussi bloquer : %v", err)
	}
	if !strings.Contains(err.Error(), "questionnaire v1") {
		t.Errorf("les choix provisoires du questionnaire devraient aussi bloquer : %v", err)
	}
}

func adresseLibre(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// À SIGTERM, le tampon est écrit même incomplet (spec §6.2).
func TestSIGTERMEcritLeTampon(t *testing.T) {
	b, sc := basetest.OuvrirSchema(t)
	valeursCompletes(t)
	t.Setenv("DIGUEE_POSTGRES_URL", sc.URL)
	t.Setenv("DIGUEE_TAMPON_TAILLE", "20")
	t.Setenv("DIGUEE_TAMPON_DELAI", "30m")

	racine, arreter := demarrer(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// Un envoi complet, qui reste dans le tampon (taille 20, délai 30 min).
	webhook, res, cookie := verifierParWhatsApp(t, client, racine, "+221 77 765 43 21")
	if webhook != http.StatusOK || res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/questionnaire" || cookie == nil {
		t.Fatalf("vérification : webhook %d, %d", webhook, res.StatusCode)
	}
	form := url.Values{"q1": {"1"}, "q2": {"1"}, "q3": {"2"}, "q4": {"1"}, "q5": {"1"}, "q6": {"1"},
		"q7": {"1"}, "themes": {"1", "2", "3"}, "region": {"1"}, "tranche_age": {"1"},
		"sexe": {"0"}, "inscrit": {"1"}, "majeur": {"1"}}
	req, _ := http.NewRequest(http.MethodPost, racine+"/envoyer", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/merci" {
		t.Fatalf("envoi : %d", res.StatusCode)
	}
	compter := func() (np, nr int) {
		_ = b.Pool.QueryRow(context.Background(),
			"SELECT (SELECT count(*) FROM participation), (SELECT count(*) FROM reponse)").Scan(&np, &nr)
		return
	}
	if np, nr := compter(); np != 0 || nr != 0 {
		t.Fatalf("rien ne doit être écrit avant l'arrêt : %d, %d", np, nr)
	}

	if err := arreter(); err != nil {
		t.Fatalf("arrêt : %v", err)
	}
	if np, nr := compter(); np != 1 || nr != 1 {
		t.Fatalf("le tampon devrait être écrit à l'arrêt : %d participation(s), %d réponse(s)", np, nr)
	}
}

// demarrer lance le serveur (variables d'environnement déjà fixées, sauf
// l'adresse) et attend qu'il réponde. arreter lui envoie SIGTERM et renvoie
// son erreur de sortie.
func demarrer(t *testing.T) (racine string, arreter func() error) {
	t.Helper()
	return demarrerAvec(t, silencieux())
}

func demarrerAvec(t *testing.T, j *slog.Logger) (racine string, arreter func() error) {
	t.Helper()
	adresse := adresseLibre(t)
	t.Setenv("DIGUEE_ADRESSE", adresse)
	fin := make(chan error, 1)
	go func() { fin <- lancer(nil, j) }()
	racine = "http://" + adresse
	pret := false
	for range 100 {
		if res, err := http.Get(racine + "/sante"); err == nil {
			res.Body.Close()
			pret = res.StatusCode == http.StatusOK
			if pret {
				break
			}
		}
		select {
		case err := <-fin:
			t.Fatalf("le serveur s'est arrêté : %v", err)
		case <-time.After(50 * time.Millisecond):
		}
	}
	if !pret {
		t.Fatal("le serveur n'a pas démarré")
	}
	return racine, func() error {
		if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
			return err
		}
		select {
		case err := <-fin:
			return err
		case <-time.After(20 * time.Second):
			return errors.New("le serveur ne s'est pas arrêté")
		}
	}
}

// Un démarrage sur une nouvelle version inscrit exactement une entrée « mise
// en production » ; un redémarrage sur la même version n'en inscrit pas.
func TestMiseEnProductionAuDemarrage(t *testing.T) {
	b, sc := basetest.OuvrirSchema(t)
	valeursCompletes(t)
	t.Setenv("DIGUEE_POSTGRES_URL", sc.URL)
	ctx := context.Background()
	jp := journal.Nouveau(b, nil)
	sha, err := version.SHA256Executable()
	if err != nil {
		t.Fatal(err)
	}

	var export string
	for i := range 3 {
		racine, arreter := demarrer(t)
		if i == 2 {
			res, err := http.Get(racine + "/journal.json")
			if err != nil {
				t.Fatal(err)
			}
			corps, _ := io.ReadAll(res.Body)
			res.Body.Close()
			export = string(corps)
			if res, err := http.Get(racine + "/journal"); err != nil || res.StatusCode != http.StatusOK {
				t.Fatalf("/journal : %v", err)
			} else {
				res.Body.Close()
			}
		}
		if err := arreter(); err != nil {
			t.Fatalf("arrêt %d : %v", i, err)
		}
	}
	es, err := jp.Toutes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 1 || es[0].Type != journal.MiseEnProduction {
		t.Fatalf("%d entrées, une seule mise en production attendue : %+v", len(es), es)
	}
	var c journal.ContenuMiseEnProduction
	if err := json.Unmarshal(es[0].Contenu, &c); err != nil || c.Version != version.Version() || c.SHA256Executable != sha {
		t.Errorf("contenu %s (version %s, sha %s)", es[0].Contenu, version.Version(), sha)
	}
	if _, rap, err := journal.LireExport(strings.NewReader(export)); err != nil || rap.Entrees != 1 {
		t.Errorf("export servi : %v, %+v", err, rap)
	}

	// Une autre version est déployée entre-temps (simulée), puis celle-ci
	// revient : nouvelle entrée.
	if _, ins, err := jp.InscrireMiseEnProduction(ctx, "autre-version", strings.Repeat("0", 64)); err != nil || !ins {
		t.Fatal(err)
	}
	_, arreter := demarrer(t)
	if err := arreter(); err != nil {
		t.Fatal(err)
	}
	if n, _ := jp.Compter(ctx); n != 3 {
		t.Errorf("%d entrées, 3 attendues", n)
	}
}

var reDefiAltcha = regexp.MustCompile(`data-altcha-algorithme="([^"]*)" data-altcha-defi="([^"]*)" data-altcha-max="([^"]*)" data-altcha-sel="([^"]*)" data-altcha-signature="([^"]*)"`)

// resoudreAltcha lit le défi ALTCHA de la page de vérification et le résout.
func resoudreAltcha(t *testing.T, page string) string {
	t.Helper()
	m := reDefiAltcha.FindStringSubmatch(page)
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

var reJetonPage = regexp.MustCompile(`<p class="jeton">(DIGUEE [2-9]{4}-[A-Z]{2})</p>`)

// verifierParWhatsApp fait la vérification inversée sur le serveur lancé, en
// mode DIGUEE_WHATSAPP=fake : jeton, message simulé par la page de
// développement (notification signée remise au vrai webhook), puis bouton
// « J'ai envoyé le message ». Il renvoie le statut rendu par le webhook, la
// réponse finale et le cookie de session.
func verifierParWhatsApp(t *testing.T, client *http.Client, racine, expediteur string) (webhook int, res *http.Response, cookie *http.Cookie) {
	t.Helper()
	faire := func(methode, chemin string, form url.Values) (*http.Response, string) {
		req, _ := http.NewRequest(methode, racine+chemin, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		r, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		corps, _ := io.ReadAll(r.Body)
		for _, c := range r.Cookies() {
			if c.Name == session.NomCookie {
				cookie = c
			}
		}
		return r, string(corps)
	}
	// Captcha ALTCHA : le test fait le calcul du navigateur (statique/altcha.js).
	_, corps := faire(http.MethodGet, "/verifier", nil)
	solution := resoudreAltcha(t, corps)
	if r, _ := faire(http.MethodPost, "/verifier", url.Values{altcha.Champ: {solution}}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("jeton : %d", r.StatusCode)
	}
	_, corps = faire(http.MethodGet, "/verifier/whatsapp", nil)
	m := reJetonPage.FindStringSubmatch(corps)
	if m == nil {
		t.Fatal("jeton absent de la page")
	}
	_, corps = faire(http.MethodPost, "/dev/whatsapp", url.Values{"numero": {expediteur}, "texte": {m[1]}})
	if m := regexp.MustCompile(`statut (\d+)`).FindStringSubmatch(corps); m != nil {
		webhook, _ = strconv.Atoi(m[1])
	}
	res, _ = faire(http.MethodPost, "/verifier/whatsapp", url.Values{})
	return webhook, res, cookie
}
