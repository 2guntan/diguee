package empreinte

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/garde"
	"github.com/2guntan/diguee/internal/integration"
)

var prefixes = []string{"70", "75", "76", "77", "78"}

func TestNormaliser(t *testing.T) {
	bons := map[string]string{
		"771234567":         "+221771234567",
		"77 123 45 67":      "+221771234567",
		"+221 77 123 45 67": "+221771234567",
		"00221771234567":    "+221771234567",
		"221771234567":      "+221771234567",
		"  70.123.45.67 ":   "+221701234567",
		"+221-78-123-45-67": "+221781234567",
		"76 123 45 67":      "+221761234567",
	}
	for saisie, attendu := range bons {
		if got, err := Normaliser(saisie, prefixes); err != nil || got != attendu {
			t.Errorf("Normaliser(%q) = %q, %v ; attendu %q", saisie, got, err, attendu)
		}
	}
	mauvais := map[string]error{
		"":                  ErrFormat,
		"77123456":          ErrFormat,
		"7712345678":        ErrFormat,
		"+33612345678":      ErrFormat,
		"77 12a 45 67":      ErrFormat,
		"77+1234567":        ErrFormat,
		"331234567":         ErrPrefixe, // fixe
		"+221 33 123 45 67": ErrPrefixe,
		"791234567":         ErrPrefixe,
	}
	for saisie, attendu := range mauvais {
		if _, err := Normaliser(saisie, prefixes); !errors.Is(err, attendu) {
			t.Errorf("Normaliser(%q) : %v, attendu %v", saisie, err, attendu)
		}
	}
}

func TestErreursSansNumero(t *testing.T) {
	_, err := Normaliser("331234567", prefixes)
	if strings.Contains(err.Error(), "1234567") {
		t.Errorf("l'erreur cite le numéro : %v", err)
	}
}

func TestHMACLocal(t *testing.T) {
	ctx := context.Background()
	h := NouveauHMACLocal(nil)
	a, err := h.Calculer(ctx, "+221771234567")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := h.Calculer(ctx, "+221771234567")
	c, _ := h.Calculer(ctx, "+221771234568")
	if a != b || a == c || a == (Empreinte{}) {
		t.Errorf("empreintes : %x %x %x", a, b, c)
	}
	autre, _ := NouveauHMACLocal([]byte("autre clé")).Calculer(ctx, "+221771234567")
	if autre == a {
		t.Error("la clé devrait changer l'empreinte")
	}
	if _, err := h.Calculer(ctx, "771234567"); err == nil {
		t.Error("un numéro non normalisé devrait être refusé")
	}
	if err := VerifierDeterminisme(ctx, h); err != nil {
		t.Error(err)
	}
}

type aleatoire struct{ n byte }

func (a *aleatoire) Calculer(context.Context, string) (Empreinte, error) {
	a.n++
	return Empreinte{a.n}, nil
}

func TestDeterminismeExige(t *testing.T) {
	if err := VerifierDeterminisme(context.Background(), &aleatoire{}); err == nil {
		t.Error("un empreinteur non déterministe devrait être refusé")
	}
}

func charger(t *testing.T, m map[string]string) *config.Config {
	t.Helper()
	m["DIGUEE_POSTGRES_URL"] = "postgres://localhost/diguee"
	c, err := config.Charger(func(k string) string { return m[k] })
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// L'empreinteur de développement est déclaré « fake » : la garde refuse alors
// la production.
func TestEmpreinteurFauxInterditEnProduction(t *testing.T) {
	c := charger(t, map[string]string{
		"DIGUEE_ENV": "production", "DIGUEE_STRUCTURE_NOM": "S", "DIGUEE_STRUCTURE_ADRESSE": "A",
		"DIGUEE_CONTACT": "c", "DIGUEE_NUMERO_CDP": "n", "DIGUEE_NUMERO_WHATSAPP": "w",
		"DIGUEE_OUVERTURE": "2027-01-15",
	})
	var r integration.Registre
	if _, err := Construire(c, &r); err != nil {
		t.Fatal(err)
	}
	if fs := r.Fausses(); len(fs) != 1 || fs[0].Nom != "empreinteur" || fs[0].Variable != "DIGUEE_EMPREINTEUR" {
		t.Fatalf("déclarations : %+v", r.Declarations())
	}
	if err := garde.Verifier(c, &r); err == nil || !strings.Contains(err.Error(), "DIGUEE_EMPREINTEUR") {
		t.Fatalf("refus attendu : %v", err)
	}
}

func TestModeInconnu(t *testing.T) {
	m := map[string]string{"DIGUEE_POSTGRES_URL": "x", "DIGUEE_EMPREINTEUR": "ovh"}
	if _, err := config.Charger(func(k string) string { return m[k] }); err == nil {
		t.Error("le mode ovh n'existe pas encore et devrait être refusé")
	}
}
