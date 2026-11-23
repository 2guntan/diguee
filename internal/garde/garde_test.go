package garde

import (
	"strings"
	"testing"

	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/integration"
)

func charger(t *testing.T, m map[string]string) *config.Config {
	t.Helper()
	m["DIGUEE_POSTGRES_URL"] = "postgres://localhost/diguee"
	c, err := config.Charger(func(k string) string { return m[k] })
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func complete(env string) map[string]string {
	return map[string]string{
		"DIGUEE_ENV":               env,
		"DIGUEE_STRUCTURE_NOM":     "Structure SARL",
		"DIGUEE_STRUCTURE_ADRESSE": "Dakar",
		"DIGUEE_CONTACT":           "droits@example.org",
		"DIGUEE_NUMERO_CDP":        "CDP-0000",
		"DIGUEE_NUMERO_WHATSAPP":   "+221700000000",
		"DIGUEE_OUVERTURE":         "2027-01-15",
	}
}

func TestProductionRefuseLesValeursACompleter(t *testing.T) {
	m := complete("production")
	delete(m, "DIGUEE_NUMERO_CDP")
	err := Verifier(charger(t, m), &integration.Registre{})
	if err == nil || !strings.Contains(err.Error(), "DIGUEE_NUMERO_CDP") {
		t.Fatalf("refus attendu sur DIGUEE_NUMERO_CDP : %v", err)
	}
}

func TestProductionRefuseLesFaussesImplementations(t *testing.T) {
	var r integration.Registre
	_ = r.Declarer(integration.Declaration{Nom: "empreinteur", Mode: "fake", Variable: "DIGUEE_EMPREINTEUR"})
	err := Verifier(charger(t, complete("production")), &r)
	if err == nil || !strings.Contains(err.Error(), "DIGUEE_EMPREINTEUR") {
		t.Fatalf("refus attendu sur l'empreinteur : %v", err)
	}
}

func TestProductionComplete(t *testing.T) {
	var r integration.Registre
	_ = r.Declarer(integration.Declaration{Nom: "empreinteur", Mode: "ovh", Variable: "DIGUEE_EMPREINTEUR"})
	if err := Verifier(charger(t, complete("production")), &r); err != nil {
		t.Fatal(err)
	}
}

func TestDeveloppementToleré(t *testing.T) {
	var r integration.Registre
	_ = r.Declarer(integration.Declaration{Nom: "sms", Mode: "fake", Variable: "DIGUEE_SMS"})
	c := charger(t, map[string]string{})
	if err := Verifier(c, &r); err != nil {
		t.Fatal(err)
	}
	if n := len(Manques(c, &r)); n != 8 {
		t.Errorf("%d manques, attendu 8 (7 valeurs et 1 intégration)", n)
	}
}
