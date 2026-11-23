package main

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/2guntan/diguee/internal/base/basetest"
	"github.com/2guntan/diguee/internal/empreinte"
	"github.com/2guntan/diguee/internal/empreinte/fauxkms"
)

func avecEnv(t *testing.T, env map[string]string) {
	for k, v := range env {
		t.Setenv(k, v)
	}
}

// T5 : avec un KMS qui signe de façon aléatoire (PS256), le serveur refuse de
// démarrer, avant même de se connecter à la base.
func TestDemarrageRefuseUneSignatureAleatoire(t *testing.T) {
	f := fauxkms.DemarrerOVH(t)
	f.Regler(fauxkms.Reglage{Aleatoire: true})
	valeursCompletes(t)
	avecEnv(t, f.Env())
	t.Setenv("DIGUEE_POSTGRES_URL", "postgres://personne@127.0.0.1:1/aucune")
	err := lancer(nil, silencieux())
	if !errors.Is(err, empreinte.ErrNonDeterministe) {
		t.Fatalf("refus attendu : %v", err)
	}
	if f.Requetes() != 2 {
		t.Errorf("%d signatures demandées, 2 attendues", f.Requetes())
	}
}

// Un KMS injoignable au démarrage empêche aussi de démarrer.
func TestDemarrageRefuseUnKMSInjoignable(t *testing.T) {
	f := fauxkms.DemarrerOVH(t)
	valeursCompletes(t)
	avecEnv(t, f.Env())
	f.Fermer()
	t.Setenv("DIGUEE_POSTGRES_URL", "postgres://personne@127.0.0.1:1/aucune")
	if err := lancer(nil, silencieux()); !errors.Is(err, empreinte.ErrKMS) {
		t.Fatalf("refus attendu : %v", err)
	}
}

type tamponSur struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *tamponSur) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *tamponSur) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// De bout en bout avec le faux KMS OVH : le parcours fonctionne, puis, KMS en
// panne (erreur qui recopie le numéro, puis serveur arrêté), la personne voit
// « service indisponible » et le journal ne contient jamais le numéro.
func TestParcoursAvecKMSOVHEtPanneSansNumeroAuJournal(t *testing.T) {
	_, sc := basetest.OuvrirSchema(t)
	f := fauxkms.DemarrerOVH(t)
	valeursCompletes(t)
	avecEnv(t, f.Env())
	t.Setenv("DIGUEE_POSTGRES_URL", sc.URL)
	t.Setenv("DIGUEE_KMS_DELAI", "500ms")
	journal := &tamponSur{}
	racine, arreter := demarrerAvec(t, slog.New(slog.NewJSONHandler(journal, &slog.HandlerOptions{Level: slog.LevelDebug})))
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	// Avec le KMS en panne, le webhook répond 503 (Meta réessaiera) et la
	// session n'est pas vérifiée.
	verifier := func(numero string) int {
		webhook, res, _ := verifierParWhatsApp(t, client, racine, numero)
		if webhook == http.StatusOK && res.StatusCode == http.StatusSeeOther && res.Header.Get("Location") == "/questionnaire" {
			return http.StatusSeeOther
		}
		if res.StatusCode != http.StatusOK {
			t.Fatalf("page du jeton : %d", res.StatusCode)
		}
		return webhook
	}
	if code := verifier("+221 77 765 43 21"); code != http.StatusSeeOther {
		t.Fatalf("vérification avec le KMS : %d", code)
	}
	if d := f.Defauts(); len(d) > 0 {
		t.Fatalf("requêtes non conformes : %v", d)
	}
	f.Regler(fauxkms.Reglage{Statut: 500, Corps: `{"errors":["échec pour {numero}"]}`})
	if code := verifier("+221 77 765 43 22"); code != http.StatusServiceUnavailable {
		t.Fatalf("KMS en erreur : %d", code)
	}
	f.Fermer()
	if code := verifier("+221 77 765 43 23"); code != http.StatusServiceUnavailable {
		t.Fatalf("KMS arrêté : %d", code)
	}
	if err := arreter(); err != nil {
		t.Fatal(err)
	}
	j := journal.String()
	if !strings.Contains(j, "KMS OVH") {
		t.Fatalf("les pannes du KMS devraient être journalisées : %s", j)
	}
	for _, n := range []string{"7654321", "7654322", "7654323", "765 43", "KzIyMTc3NjU0Mz"} {
		if strings.Contains(j, n) {
			t.Errorf("le journal contient le numéro (%q) :\n%s", n, j)
		}
	}
}
