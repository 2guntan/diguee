package whatsapp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/codes"
	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/integration"
)

var _ codes.Envoyeur = (*EnvoyeurMeta)(nil)

const jetonAccesTest = "EAAG-jeton-d-acces-graph-de-test-0123456789"

func envoyeurTest(t *testing.T, h http.HandlerFunc, delai time.Duration) (*EnvoyeurMeta, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	e, err := NouvelEnvoyeurMeta(OptionsEnvoyeurMeta{
		PointAcces: srv.URL, VersionAPI: "v23.0", IDNumero: "106540352242922", JetonAcces: jetonAccesTest,
		Modele: "diguee_code", Langue: "fr", Delai: delai,
	})
	if err != nil {
		t.Fatal(err)
	}
	return e, srv
}

// Le faux serveur de l'API Graph vérifie la requête : méthode, chemin,
// jeton d'accès, type, corps au format des modèles d'authentification (code
// dans le corps et dans le bouton « Copier le code »).
func TestEnvoyeurMetaRequete(t *testing.T) {
	var appels atomic.Int32
	e, _ := envoyeurTest(t, func(w http.ResponseWriter, r *http.Request) {
		appels.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v23.0/106540352242922/messages" || r.URL.RawQuery != "" {
			t.Errorf("requête %s %s", r.Method, r.URL)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+jetonAccesTest {
			t.Errorf("Authorization %q", got)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type %q", ct)
		}
		brut, _ := io.ReadAll(r.Body)
		var m map[string]any
		if err := json.Unmarshal(brut, &m); err != nil {
			t.Fatal(err)
		}
		attendu := map[string]any{
			"messaging_product": "whatsapp", "recipient_type": "individual", "to": "221771234567", "type": "template",
			"template": map[string]any{
				"name": "diguee_code", "language": map[string]any{"code": "fr"},
				"components": []any{
					map[string]any{"type": "body", "parameters": []any{map[string]any{"type": "text", "text": "042137"}}},
					map[string]any{"type": "button", "sub_type": "url", "index": "0",
						"parameters": []any{map[string]any{"type": "text", "text": "042137"}}},
				},
			},
		}
		a, _ := json.Marshal(attendu)
		g, _ := json.Marshal(m)
		if string(a) != string(g) {
			t.Errorf("corps :\n%s\nattendu :\n%s", g, a)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"messaging_product":"whatsapp","contacts":[{"input":"221771234567","wa_id":"221771234567"}],"messages":[{"id":"wamid.HBgM"}]}`)
	}, time.Second)
	if err := e.Envoyer(context.Background(), "+221771234567", "042137"); err != nil {
		t.Fatal(err)
	}
	if appels.Load() != 1 {
		t.Fatalf("%d appels", appels.Load())
	}
	if s := e.String(); strings.Contains(s, jetonAccesTest) {
		t.Errorf("String montre le jeton : %s", s)
	}
}

// Les erreurs ne citent ni le numéro, ni le code, ni le jeton, ni le corps
// de la réponse : seulement le statut et le code d'erreur de Meta.
func TestEnvoyeurMetaErreursSansDonnees(t *testing.T) {
	interdits := []string{"771234567", "042137", jetonAccesTest, "Recipient"}
	verifier := func(nom string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s : erreur attendue", nom)
		}
		for _, x := range interdits {
			if strings.Contains(err.Error(), x) {
				t.Errorf("%s : l'erreur cite %q : %v", nom, x, err)
			}
		}
	}
	e, _ := envoyeurTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"Recipient 221771234567 code 042137 `+jetonAccesTest+`","type":"OAuthException","code":131026,"fbtrace_id":"x"}}`)
	}, time.Second)
	err := e.Envoyer(context.Background(), "+221771234567", "042137")
	verifier("400", err)
	if ee, ok := err.(*ErreurEnvoi); !ok || ee.Statut != 400 || ee.Code != 131026 {
		t.Errorf("erreur : %#v", err)
	}

	e, _ = envoyeurTest(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"messages":[]} 221771234567`)
	}, time.Second)
	verifier("réponse inattendue", e.Envoyer(context.Background(), "+221771234567", "042137"))

	bloque := make(chan struct{})
	e, _ = envoyeurTest(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-bloque:
		case <-r.Context().Done():
		}
	}, 50*time.Millisecond)
	debut := time.Now()
	err = e.Envoyer(context.Background(), "+221771234567", "042137")
	close(bloque)
	verifier("délai", err)
	if time.Since(debut) > 5*time.Second {
		t.Error("le délai d'attente n'est pas respecté")
	}

	e, srv := envoyeurTest(t, func(http.ResponseWriter, *http.Request) {}, time.Second)
	srv.Close()
	verifier("connexion", e.Envoyer(context.Background(), "+221771234567", "042137"))
}

func TestEnvoyeurMetaOptions(t *testing.T) {
	if _, err := NouvelEnvoyeurMeta(OptionsEnvoyeurMeta{VersionAPI: "v23.0"}); err == nil {
		t.Fatal("options incomplètes acceptées")
	}
	e, err := NouvelEnvoyeurMeta(OptionsEnvoyeurMeta{VersionAPI: "v23.0", IDNumero: "1", JetonAcces: "j", Modele: "m", Langue: "fr"})
	if err != nil || e.url != "https://graph.facebook.com/v23.0/1/messages" {
		t.Fatalf("%v %v", err, e)
	}
}

// En mode « fake », l'intégration écrit les codes dans la console, sauf en
// production, où elle n'a pas d'envoyeur (et la garde refuse le démarrage).
func TestIntegrationEnvoyeur(t *testing.T) {
	charger := func(env map[string]string) *config.Config {
		env["DIGUEE_POSTGRES_URL"] = "x"
		c, err := config.Charger(func(k string) string { return env[k] })
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	i, err := Construire(charger(map[string]string{}), &integration.Registre{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := i.Envoyeur.(*codes.Console); !ok {
		t.Fatalf("fake : envoyeur %T", i.Envoyeur)
	}
	i, err = Construire(charger(map[string]string{"DIGUEE_ENV": "production"}), &integration.Registre{})
	if err != nil || i.Envoyeur != nil {
		t.Fatalf("fake en production : %v %T", err, i.Envoyeur)
	}
	i, err = Construire(charger(map[string]string{
		"DIGUEE_WHATSAPP": "meta", "DIGUEE_META_ID_NUMERO": "123456789012345",
		"DIGUEE_META_JETON_ACCES": jetonAccesTest, "DIGUEE_META_MODELE_CODE": "diguee_code",
		"DIGUEE_META_SECRET_APP": "s3cr3t-de-l-application-meta", "DIGUEE_META_JETON_WEBHOOK": "jeton-de-verification",
	}), &integration.Registre{})
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := i.Envoyeur.(*EnvoyeurMeta); !ok || e.url != "https://graph.facebook.com/v23.0/123456789012345/messages" {
		t.Fatalf("meta : envoyeur %T", i.Envoyeur)
	}
}
