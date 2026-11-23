package whatsapp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/empreinte"
	"github.com/2guntan/diguee/internal/integration"
)

func TestJetonsLisiblesEtAleatoires(t *testing.T) {
	forme := regexp.MustCompile(`^DIGUEE [2-9]{4}-[ACDEFHJKMNPRTUVWXY]{2}$`)
	vus := map[string]bool{}
	for range 2000 {
		j, err := nouveauTexteJeton()
		if err != nil {
			t.Fatal(err)
		}
		if !forme.MatchString(j) {
			t.Fatalf("jeton mal formé : %q", j)
		}
		vus[j] = true
	}
	// 2 000 tirages parmi 1,3 million : quelques collisions au plus.
	if len(vus) < 1990 {
		t.Errorf("%d jetons distincts sur 2 000", len(vus))
	}
}

func TestExtraireJeton(t *testing.T) {
	for texte, attendu := range map[string]string{
		"DIGUEE 4823-KX":                   "DIGUEE 4823-KX",
		"diguee 4823-kx":                   "DIGUEE 4823-KX",
		"  DIGUEE4823KX ":                  "DIGUEE 4823-KX",
		"Bonjour ! DIGUEE 4823 - KX merci": "DIGUEE 4823-KX",
	} {
		if got, err := extraireJeton(texte); err != nil || got != attendu {
			t.Errorf("%q : %q %v", texte, got, err)
		}
	}
	for _, texte := range []string{"", "Bonjour", "DIGUEE 482-KX", "DIGUEE 4823-KXY", "4823-KX"} {
		if _, err := extraireJeton(texte); err == nil {
			t.Errorf("%q ne contient pas de jeton", texte)
		}
	}
}

// RFC 4231, cas 1 : HMAC-SHA-256 avec une clé de 20 octets 0x0b.
func TestSignature(t *testing.T) {
	cle := bytes.Repeat([]byte{0x0b}, 20)
	attendu := "sha256=b0344c61d8db38535ca8afceaf0bf12b881dc200c9833da726e9376c2e32cff7"
	if got := Signature([]byte("Hi There"), cle); got != attendu {
		t.Errorf("signature %s", got)
	}
	if !signatureValide(attendu, []byte("Hi There"), cle) || signatureValide(attendu, []byte("Hi there"), cle) ||
		signatureValide(strings.ToUpper(attendu[7:]), []byte("Hi There"), cle) {
		t.Error("contrôle de signature")
	}
}

// Faux collaborateurs du vérificateur.
type faux struct {
	mu        sync.Mutex
	pannes    int // nombre d'échecs du KMS à simuler
	deja      map[empreinte.Empreinte]bool
	verifiees map[string]empreinte.Empreinte
	refuser   bool // limiteur
}

func (f *faux) Calculer(ctx context.Context, n string) (empreinte.Empreinte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pannes > 0 {
		f.pannes--
		return empreinte.Empreinte{}, errors.New("KMS indisponible")
	}
	return empreinte.NouveauHMACLocal(nil).Calculer(ctx, n)
}

func (f *faux) DejaParticipe(_ context.Context, e empreinte.Empreinte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deja[e], nil
}

func (f *faux) Verifier(id string, e empreinte.Empreinte) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id == "disparue" {
		return false
	}
	f.verifiees[id] = e
	return true
}

func (f *faux) Autoriser(empreinte.Empreinte) bool { return !f.refuser }

func nouveauVerif(t *testing.T, f *faux, maintenant *time.Time) *Verificateur {
	t.Helper()
	v, err := NouveauVerificateur(OptionsVerificateur{
		Empreinteur: f, Doublons: f, Sessions: f, Limiteur: f, Prefixes: []string{"70", "76", "77", "78"},
		Duree: 10 * time.Minute, Maintenant: func() time.Time { return *maintenant },
		Journal: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRecevoir(t *testing.T) {
	ctx := context.Background()
	maintenant := time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC)
	f := &faux{deja: map[empreinte.Empreinte]bool{}, verifiees: map[string]empreinte.Empreinte{}}
	v := nouveauVerif(t, f, &maintenant)

	// Panne passagère du KMS : erreur (Meta réessaiera), le jeton reste valable.
	j, _ := v.Creer("s1")
	f.pannes = 1
	if err := v.Recevoir(ctx, "221771234567", j.Texte); err == nil {
		t.Fatal("la panne du KMS doit être signalée")
	}
	if _, st := v.Etat("s1"); st != Attente {
		t.Fatalf("après la panne : %v", st)
	}
	if err := v.Recevoir(ctx, "221771234567", j.Texte); err != nil {
		t.Fatal(err)
	}
	if _, st := v.Etat("s1"); st != Verifiee {
		t.Fatalf("statut %v", st)
	}
	e, _ := empreinte.NouveauHMACLocal(nil).Calculer(ctx, "+221771234567")
	if f.verifiees["s1"] != e {
		t.Error("la session doit recevoir l'empreinte du numéro")
	}

	// Empreinte déjà connue.
	f.deja[e] = true
	j, _ = v.Creer("s2")
	_ = v.Recevoir(ctx, "221771234567", j.Texte)
	if _, st := v.Etat("s2"); st != DejaParticipe {
		t.Errorf("déjà participé : %v", st)
	}
	if _, ok := f.verifiees["s2"]; ok {
		t.Error("une empreinte connue ne doit pas vérifier la session")
	}

	// Refus : hors +221, préfixe, limite (ticket #8), session disparue.
	for exp, attendu := range map[string]Statut{"33612345678": NumeroRefuse, "221751234567": PrefixeRefuse} {
		j, _ := v.Creer("s3")
		_ = v.Recevoir(ctx, exp, j.Texte)
		if _, st := v.Etat("s3"); st != attendu {
			t.Errorf("%s : %v", exp, st)
		}
	}
	f.refuser = true
	j, _ = v.Creer("s4")
	_ = v.Recevoir(ctx, "221761234567", j.Texte)
	if _, st := v.Etat("s4"); st != Limitee {
		t.Errorf("limite : %v", st)
	}
	f.refuser = false
	j, _ = v.Creer("disparue")
	_ = v.Recevoir(ctx, "221761234567", j.Texte)
	if _, st := v.Etat("disparue"); st != Expiree {
		t.Errorf("session disparue : %v", st)
	}

	// Expiration, puis purge des jetons anciens.
	j, _ = v.Creer("s5")
	maintenant = maintenant.Add(10 * time.Minute)
	if _, st := v.Etat("s5"); st != Expiree {
		t.Errorf("expiration : %v", st)
	}
	_ = v.Recevoir(ctx, "221781234567", j.Texte)
	if _, ok := f.verifiees["s5"]; ok {
		t.Error("un jeton expiré ne doit rien vérifier")
	}
	maintenant = maintenant.Add(time.Hour)
	_, _ = v.Creer("s6")
	v.mu.Lock()
	n := len(v.parTexte)
	v.mu.Unlock()
	if n != 1 {
		t.Errorf("%d jetons en mémoire après la purge, 1 attendu", n)
	}
	v.Oublier("s6")
	if _, st := v.Etat("s6"); st != Aucun {
		t.Errorf("après Oublier : %v", st)
	}
}

func TestConstruire(t *testing.T) {
	charger := func(env map[string]string) *config.Config {
		env["DIGUEE_POSTGRES_URL"] = "x"
		c, err := config.Charger(func(k string) string { return env[k] })
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	r := &integration.Registre{}
	i, err := Construire(charger(map[string]string{}), r)
	if err != nil || !i.Fausse() || len(r.Fausses()) != 1 || r.Fausses()[0].Variable != "DIGUEE_WHATSAPP" {
		t.Fatalf("fake : %v %v", err, r.Fausses())
	}
	r = &integration.Registre{}
	i, err = Construire(charger(map[string]string{
		"DIGUEE_WHATSAPP": "meta", "DIGUEE_META_ID_NUMERO": "123456789012345",
		"DIGUEE_META_JETON_ACCES": "jeton-d-acces-graph-de-test-0123456789", "DIGUEE_META_MODELE_CODE": "diguee_code",
		"DIGUEE_META_SECRET_APP": "s3cr3t-de-l-application-meta", "DIGUEE_META_JETON_WEBHOOK": "jeton-de-verification",
	}), r)
	if err != nil || i.Fausse() || len(r.Fausses()) != 0 || r.Declarations()[0].Mode != "meta" {
		t.Fatalf("meta : %v", err)
	}
	if s := i.String(); strings.Contains(s, "s3cr3t") || strings.Contains(s, "jeton-de-verification") {
		t.Errorf("String montre un secret : %s", s)
	}
}
