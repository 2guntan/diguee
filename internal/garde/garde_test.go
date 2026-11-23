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
		"DIGUEE_POPULATION_ANSD":   "/etc/diguee/population-ansd.json",
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

// Les chiffres de population de l'ANSD (ticket #11) sont exigés en production.
func TestProductionRefuseLaPopulationACompleter(t *testing.T) {
	m := complete("production")
	delete(m, "DIGUEE_POPULATION_ANSD")
	err := Verifier(charger(t, m), &integration.Registre{})
	if err == nil || !strings.Contains(err.Error(), "DIGUEE_POPULATION_ANSD vaut « À COMPLÉTER »") {
		t.Fatalf("refus attendu sur DIGUEE_POPULATION_ANSD : %v", err)
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
	if n := len(Manques(c, &r)); n != 9 {
		t.Errorf("%d manques, attendu 9 (8 valeurs et 1 intégration)", n)
	}
}

type provisoires []string

func (p provisoires) Provisoires() []string { return p }

func TestProductionRefuseLesAutresProvisoires(t *testing.T) {
	c := charger(t, complete("production"))
	err := Verifier(c, &integration.Registre{}, provisoires{"questionnaire v1 : q5 provisoire"})
	if err == nil || !strings.Contains(err.Error(), "q5") {
		t.Fatalf("refus attendu : %v", err)
	}
	if err := Verifier(c, &integration.Registre{}, provisoires{}); err != nil {
		t.Fatal(err)
	}
}
