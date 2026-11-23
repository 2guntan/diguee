package codes

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/antifraude"
	"github.com/2guntan/diguee/internal/empreinte"
)

type envoyeurTest struct {
	mu      sync.Mutex
	numeros []string
	codes   []string
	err     error
}

func (e *envoyeurTest) Envoyer(_ context.Context, numero, code string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return e.err
	}
	e.numeros = append(e.numeros, numero)
	e.codes = append(e.codes, code)
	return nil
}

func (e *envoyeurTest) dernier() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.codes[len(e.codes)-1]
}

type doublonsTest struct {
	deja map[empreinte.Empreinte]bool
	err  error
}

func (d *doublonsTest) DejaParticipe(_ context.Context, e empreinte.Empreinte) (bool, error) {
	return d.deja[e], d.err
}

type sessionsTest struct {
	verifiees map[string]empreinte.Empreinte
	absentes  map[string]bool
}

func (s *sessionsTest) Verifier(id string, e empreinte.Empreinte) bool {
	if s.absentes[id] {
		return false
	}
	s.verifiees[id] = e
	return true
}

type bancCodes struct {
	v        *Verificateur
	envoyeur *envoyeurTest
	doublons *doublonsTest
	sessions *sessionsTest
	t        time.Time
	journal  *bytes.Buffer
	limite   *antifraude.LimiteEmpreinte
}

func nouveauBancCodes(t *testing.T, limite *antifraude.LimiteEmpreinte) *bancCodes {
	t.Helper()
	b := &bancCodes{envoyeur: &envoyeurTest{}, doublons: &doublonsTest{deja: map[empreinte.Empreinte]bool{}},
		sessions: &sessionsTest{verifiees: map[string]empreinte.Empreinte{}, absentes: map[string]bool{}},
		t:        time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC), journal: &bytes.Buffer{}, limite: limite}
	opt := Options{Canal: "WhatsApp", Envoyeur: b.envoyeur, Empreinteur: empreinte.NouveauHMACLocal(nil),
		Doublons: b.doublons, Sessions: b.sessions, Prefixes: []string{"70", "75", "76", "77", "78"},
		Duree: 10 * time.Minute, Essais: 5, Maintenant: func() time.Time { return b.t },
		Journal: slog.New(slog.NewJSONHandler(b.journal, nil))}
	if limite != nil {
		opt.Limiteur = limite
	}
	v, err := Nouveau(opt)
	if err != nil {
		t.Fatal(err)
	}
	b.v = v
	return b
}

func (b *bancCodes) demander(t *testing.T, session, numero string) Statut {
	t.Helper()
	s, err := b.v.Demander(context.Background(), session, numero)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (b *bancCodes) valider(t *testing.T, session, code string) (Statut, int) {
	t.Helper()
	s, n, err := b.v.Valider(context.Background(), session, code)
	if err != nil {
		t.Fatal(err)
	}
	return s, n
}

func autre(c string) string {
	if c == "999999" {
		return "999998"
	}
	return "999999"
}

func TestCodeJusteDansLesDixMinutes(t *testing.T) {
	b := nouveauBancCodes(t, nil)
	if s := b.demander(t, "s1", "77 123 45 67"); s != Envoye {
		t.Fatalf("demande : %v", s)
	}
	if b.envoyeur.numeros[0] != "+221771234567" {
		t.Fatalf("numéro envoyé : %q", b.envoyeur.numeros[0])
	}
	if s, n := b.v.Etat("s1"); s != Envoye || n != 5 {
		t.Fatalf("état : %v %d", s, n)
	}
	code := b.envoyeur.dernier()
	// Un code lié à la session : il ne vaut rien ailleurs.
	if s, _ := b.valider(t, "s2", code); s != Aucun {
		t.Fatalf("autre session : %v", s)
	}
	b.t = b.t.Add(10*time.Minute - time.Second)
	if s, _ := b.valider(t, "s1", code); s != Verifiee {
		t.Fatalf("code juste : %v", s)
	}
	attendue, _ := empreinte.NouveauHMACLocal(nil).Calculer(context.Background(), "+221771234567")
	if b.sessions.verifiees["s1"] != attendue {
		t.Fatal("session vérifiée avec une autre empreinte")
	}
	// Consommé.
	if s, _ := b.valider(t, "s1", code); s != Aucun {
		t.Fatalf("code rejoué : %v", s)
	}
}

func TestCodeExpire(t *testing.T) {
	b := nouveauBancCodes(t, nil)
	b.demander(t, "s", "771234567")
	b.t = b.t.Add(10 * time.Minute)
	if s, _ := b.v.Etat("s"); s != Expire {
		t.Fatalf("état : %v", s)
	}
	if s, _ := b.valider(t, "s", b.envoyeur.dernier()); s != Expire {
		t.Fatalf("code expiré : %v", s)
	}
	if s, _ := b.valider(t, "s", b.envoyeur.dernier()); s != Aucun || len(b.sessions.verifiees) != 0 {
		t.Fatalf("après expiration : %v", s)
	}
}

func TestCinqEssaisPuisInvalide(t *testing.T) {
	b := nouveauBancCodes(t, nil)
	b.demander(t, "s", "771234567")
	code := b.envoyeur.dernier()
	for i := 4; i >= 1; i-- {
		if s, n := b.valider(t, "s", autre(code)); s != Faux || n != i {
			t.Fatalf("essai faux : %v %d (attendu %d)", s, n, i)
		}
	}
	if s, _ := b.valider(t, "s", ""); s != Epuise {
		t.Fatalf("5e essai : %v", s)
	}
	if s, _ := b.valider(t, "s", code); s != Aucun || len(b.sessions.verifiees) != 0 {
		t.Fatalf("code juste après 5 essais : %v", s)
	}
	// Redemander donne un nouveau code, qui fonctionne.
	b.demander(t, "s", "771234567")
	if s, _ := b.valider(t, "s", b.envoyeur.dernier()); s != Verifiee {
		t.Fatalf("nouveau code : %v", s)
	}
}

func TestNouvelleDemandeRevoqueLAncienCode(t *testing.T) {
	b := nouveauBancCodes(t, nil)
	b.demander(t, "s", "771234567")
	ancien := b.envoyeur.dernier()
	b.demander(t, "s", "771234567")
	if nouveau := b.envoyeur.dernier(); nouveau != ancien {
		if s, _ := b.valider(t, "s", ancien); s != Faux {
			t.Fatalf("ancien code : %v", s)
		}
	}
	b.v.Oublier("s")
	if s, _ := b.v.Etat("s"); s != Aucun {
		t.Fatalf("après Oublier : %v", s)
	}
}

func TestNumerosRefuses(t *testing.T) {
	b := nouveauBancCodes(t, nil)
	for numero, attendu := range map[string]Statut{
		"+33612345678": NumeroRefuse, "77123": NumeroRefuse, "77 123 45 6x": NumeroRefuse,
		"721234567": PrefixeRefuse, "+221 33 123 45 67": PrefixeRefuse,
	} {
		if s := b.demander(t, "s", numero); s != attendu {
			t.Errorf("%s : %v", numero, s)
		}
	}
	if len(b.envoyeur.codes) != 0 {
		t.Fatal("aucun code ne doit partir")
	}
}

// La limite par empreinte est partagée : une même instance compte les codes
// de deux parcours (WhatsApp, SMS) et la vérification inversée.
func TestLimiteParEmpreintePartagee(t *testing.T) {
	limite := antifraude.NouvelleLimiteEmpreinte(3, nil)
	whatsapp := nouveauBancCodes(t, limite)
	sms := nouveauBancCodes(t, limite)
	emp, _ := empreinte.NouveauHMACLocal(nil).Calculer(context.Background(), "+221771234567")
	if !limite.Autoriser(emp) { // une vérification inversée
		t.Fatal("première tentative refusée")
	}
	if whatsapp.demander(t, "a", "771234567") != Envoye || sms.demander(t, "b", "771234567") != Envoye {
		t.Fatal("2e et 3e tentatives refusées")
	}
	if s := whatsapp.demander(t, "c", "+221 77 123 45 67"); s != Limitee {
		t.Fatalf("4e tentative : %v", s)
	}
	if len(whatsapp.envoyeur.codes) != 1 {
		t.Fatal("aucun code ne doit partir au-delà de la limite")
	}
	if whatsapp.demander(t, "d", "771234568") != Envoye {
		t.Fatal("un autre numéro n'est pas limité")
	}
}

func TestDejaParticipeSeulementApresUnCodeJuste(t *testing.T) {
	b := nouveauBancCodes(t, nil)
	emp, _ := empreinte.NouveauHMACLocal(nil).Calculer(context.Background(), "+221771234567")
	b.doublons.deja[emp] = true
	// La demande ne révèle rien : le code part comme pour un autre numéro.
	if s := b.demander(t, "s", "771234567"); s != Envoye {
		t.Fatalf("demande : %v", s)
	}
	if s, _ := b.valider(t, "s", autre(b.envoyeur.dernier())); s != Faux {
		t.Fatalf("code faux : %v", s)
	}
	if s, _ := b.valider(t, "s", b.envoyeur.dernier()); s != DejaParticipe || len(b.sessions.verifiees) != 0 {
		t.Fatalf("code juste : %v", s)
	}
}

func TestBaseIndisponibleGardeLeCode(t *testing.T) {
	b := nouveauBancCodes(t, nil)
	b.demander(t, "s", "771234567")
	b.doublons.err = errors.New("base indisponible")
	if _, _, err := b.v.Valider(context.Background(), "s", b.envoyeur.dernier()); err == nil {
		t.Fatal("erreur attendue")
	}
	b.doublons.err = nil
	if s, _ := b.valider(t, "s", b.envoyeur.dernier()); s != Verifiee {
		t.Fatalf("après rétablissement : %v", s)
	}
}

func TestSessionPerdue(t *testing.T) {
	b := nouveauBancCodes(t, nil)
	b.demander(t, "s", "771234567")
	b.sessions.absentes["s"] = true
	if s, _ := b.valider(t, "s", b.envoyeur.dernier()); s != SessionPerdue {
		t.Fatalf("session disparue : %v", s)
	}
}

// Un envoi raté ne laisse pas de code, et le journal ne contient ni le
// numéro ni le code.
func TestEnvoiImpossibleEtJournal(t *testing.T) {
	b := nouveauBancCodes(t, nil)
	b.envoyeur.err = errors.New("API Graph : statut 400, code d'erreur Meta 131026")
	if _, err := b.v.Demander(context.Background(), "s", "77 123 45 67"); !errors.Is(err, ErrEnvoi) {
		t.Fatalf("erreur : %v", err)
	}
	if s, _ := b.v.Etat("s"); s != Aucun {
		t.Fatalf("état : %v", s)
	}
	if b.v.Metriques().EchecsEnvoi.Load() != 1 {
		t.Error("échec non compté")
	}
	b.envoyeur.err = nil
	b.demander(t, "s", "77 123 45 67")
	code := b.envoyeur.dernier()
	b.valider(t, "s", autre(code))
	b.valider(t, "s", code)
	j := b.journal.String()
	if !strings.Contains(j, "131026") {
		t.Error("l'échec d'envoi devrait être journalisé")
	}
	for _, interdit := range []string{"771234567", "123 45 67", code} {
		if strings.Contains(j, interdit) {
			t.Errorf("le journal contient %q", interdit)
		}
	}
}

// Le code n'est gardé qu'en HMAC : l'entrée en mémoire n'a aucun champ texte.
func TestCodeJamaisGardeEnClair(t *testing.T) {
	ty := reflect.TypeOf(entree{})
	for i := range ty.NumField() {
		if k := ty.Field(i).Type.Kind(); k == reflect.String || k == reflect.Slice {
			t.Errorf("champ %s de type %s", ty.Field(i).Name, ty.Field(i).Type)
		}
	}
}

func TestTirage(t *testing.T) {
	vus := map[string]bool{}
	for range 2000 {
		c, err := tirer()
		if err != nil {
			t.Fatal(err)
		}
		if len(c) != Chiffres || strings.Trim(c, "0123456789") != "" {
			t.Fatalf("code %q", c)
		}
		vus[c] = true
	}
	if len(vus) < 1990 {
		t.Fatalf("%d codes distincts sur 2000", len(vus))
	}
}

func TestOptionsIncompletes(t *testing.T) {
	if _, err := Nouveau(Options{}); err == nil {
		t.Fatal("options vides acceptées")
	}
}

func TestConsole(t *testing.T) {
	if _, err := NouvelleConsole("WhatsApp", nil, true); !errors.Is(err, ErrConsoleEnProduction) {
		t.Fatalf("console en production : %v", err)
	}
	var sortie bytes.Buffer
	c, err := NouvelleConsole("WhatsApp", &sortie, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Envoyer(context.Background(), "+221771234567", "042137"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sortie.String(), "042137") || strings.Contains(sortie.String(), "771234567") || c.Dernier() != "042137" {
		t.Fatalf("console : %q", sortie.String())
	}
	ctx, annuler := context.WithCancel(context.Background())
	annuler()
	if c.Envoyer(ctx, "+221771234567", "1") == nil {
		t.Fatal("contexte annulé ignoré")
	}
}
