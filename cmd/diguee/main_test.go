package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/base/basetest"
	"github.com/2guntan/diguee/internal/session"
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
	adresse := adresseLibre(t)
	t.Setenv("DIGUEE_POSTGRES_URL", sc.URL)
	t.Setenv("DIGUEE_ADRESSE", adresse)
	t.Setenv("DIGUEE_TAMPON_TAILLE", "20")
	t.Setenv("DIGUEE_TAMPON_DELAI", "30m")

	fin := make(chan error, 1)
	go func() { fin <- lancer(nil, silencieux()) }()

	racine := "http://" + adresse
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	pret := false
	for range 100 {
		if res, err := client.Get(racine + "/sante"); err == nil {
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

	// Un envoi complet, qui reste dans le tampon (taille 20, délai 30 min).
	res, err := client.PostForm(racine+"/verifier", url.Values{"numero": {"77 765 43 21"}})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	var cookie *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == session.NomCookie {
			cookie = c
		}
	}
	if res.StatusCode != http.StatusSeeOther || cookie == nil {
		t.Fatalf("vérification : %d", res.StatusCode)
	}
	form := url.Values{"q1": {"1"}, "q2": {"1"}, "q3": {"2"}, "q4": {"1"}, "q5": {"1"}, "q6": {"1"},
		"q7": {"1"}, "themes": {"1", "2", "3"}, "region": {"1"}, "tranche_age": {"1"},
		"sexe": {"0"}, "inscrit": {"1"}, "majeur": {"1"}}
	req, _ := http.NewRequest(http.MethodPost, racine+"/envoyer", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	res, err = client.Do(req)
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

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-fin:
		if err != nil {
			t.Fatalf("arrêt : %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("le serveur ne s'est pas arrêté")
	}
	if np, nr := compter(); np != 1 || nr != 1 {
		t.Fatalf("le tampon devrait être écrit à l'arrêt : %d participation(s), %d réponse(s)", np, nr)
	}
}
