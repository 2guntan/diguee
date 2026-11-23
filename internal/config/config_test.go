package config

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func minimal() map[string]string {
	return map[string]string{"DIGUEE_POSTGRES_URL": "postgres://localhost/diguee"}
}

func TestValeursParDefaut(t *testing.T) {
	c, err := Charger(env(minimal()))
	if err != nil {
		t.Fatal(err)
	}
	if c.Env != Developpement || c.Adresse != ":8080" {
		t.Errorf("env=%q adresse=%q", c.Env, c.Adresse)
	}
	if c.SeuilPublication != 1000 || c.SeuilCase != 30 || c.TamponTaille != 20 || c.TamponDelai != 30*time.Minute {
		t.Errorf("seuils ou tampon inattendus : %+v", c)
	}
	if c.HeurePublication != 6*time.Hour || c.SMSCoupeCircuit != 3 || c.PreavisJours != 15 {
		t.Errorf("heure=%v coupe-circuit=%v préavis=%d", c.HeurePublication, c.SMSCoupeCircuit, c.PreavisJours)
	}
	if !slices.Equal(c.PrefixesAutorises, []string{"70", "75", "76", "77", "78"}) {
		t.Errorf("préfixes : %v", c.PrefixesAutorises)
	}
	if c.NumeroCDP.Valeur != ACompleter || !c.NumeroCDP.Provisoire() {
		t.Errorf("le numéro CDP devrait être provisoire : %q", c.NumeroCDP.Valeur)
	}
	attendus := []string{"DIGUEE_STRUCTURE_NOM", "DIGUEE_STRUCTURE_ADRESSE", "DIGUEE_CONTACT",
		"DIGUEE_NUMERO_CDP", "DIGUEE_NUMERO_WHATSAPP", "DIGUEE_OUVERTURE", "DIGUEE_CLOTURE"}
	if got := c.Provisoires(); !slices.Equal(got, attendus) {
		t.Errorf("provisoires = %v, attendu %v", got, attendus)
	}
}

func TestClotureSixMoisApresOuverture(t *testing.T) {
	m := minimal()
	m["DIGUEE_OUVERTURE"] = "2027-01-15"
	c, err := Charger(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Cloture.Jour.Format(time.DateOnly); got != "2027-07-15" {
		t.Errorf("clôture = %s", got)
	}
	if c.Cloture.Provisoire() || slices.Contains(c.Provisoires(), "DIGUEE_CLOTURE") {
		t.Error("la clôture ne devrait plus être provisoire")
	}
}

func TestPhase(t *testing.T) {
	m := minimal()
	c, _ := Charger(env(m))
	if p := c.Phase(time.Now()); p != PhaseAVenir {
		t.Errorf("sans date d'ouverture : %s", p)
	}
	m["DIGUEE_OUVERTURE"] = "2027-01-15"
	c, _ = Charger(env(m))
	jour := func(s string) time.Time { j, _ := time.Parse(time.DateOnly, s); return j }
	for s, attendu := range map[string]Phase{
		"2027-01-14": PhaseAVenir, "2027-01-15": PhaseOuverte, "2027-07-14": PhaseOuverte, "2027-07-15": PhaseClose,
	} {
		if p := c.Phase(jour(s)); p != attendu {
			t.Errorf("%s : %s, attendu %s", s, p, attendu)
		}
	}
}

func TestErreursRegroupees(t *testing.T) {
	_, err := Charger(env(map[string]string{
		"DIGUEE_ENV":                "prod",
		"DIGUEE_SEUIL_CASE":         "trente",
		"DIGUEE_OUVERTURE":          "15/01/2027",
		"DIGUEE_HEURE_PUBLICATION":  "6h",
		"DIGUEE_PREFIXES_AUTORISES": "77,7a",
		"DIGUEE_SMS_COUPE_CIRCUIT":  "3",
	}))
	if err == nil {
		t.Fatal("erreur attendue")
	}
	for _, nom := range []string{"DIGUEE_ENV", "DIGUEE_POSTGRES_URL", "DIGUEE_SEUIL_CASE", "DIGUEE_OUVERTURE",
		"DIGUEE_HEURE_PUBLICATION", "DIGUEE_PREFIXES_AUTORISES", "DIGUEE_SMS_COUPE_CIRCUIT"} {
		if !strings.Contains(err.Error(), nom) {
			t.Errorf("l'erreur ne mentionne pas %s : %v", nom, err)
		}
	}
}

func TestClotureAvantOuvertureRefusee(t *testing.T) {
	m := minimal()
	m["DIGUEE_OUVERTURE"] = "2027-01-15"
	m["DIGUEE_CLOTURE"] = "2027-01-01"
	if _, err := Charger(env(m)); err == nil || !strings.Contains(err.Error(), "DIGUEE_CLOTURE") {
		t.Errorf("erreur attendue sur DIGUEE_CLOTURE : %v", err)
	}
}

func TestCleTOTP(t *testing.T) {
	c, err := Charger(env(minimal()))
	if err != nil || c.CleTOTP != nil {
		t.Fatalf("sans DIGUEE_CLE_TOTP, la clé doit être absente : %v %v", c, err)
	}
	m := minimal()
	m["DIGUEE_CLE_TOTP"] = strings.Repeat("0a", 32)
	if c, err = Charger(env(m)); err != nil || len(c.CleTOTP) != 32 || c.CleTOTP[0] != 0x0a {
		t.Fatalf("clé valide refusée : %v", err)
	}
	for _, v := range []string{"zz", strings.Repeat("0a", 16), strings.Repeat("secret", 11)} {
		m["DIGUEE_CLE_TOTP"] = v
		_, err := Charger(env(m))
		if err == nil || !strings.Contains(err.Error(), "DIGUEE_CLE_TOTP") || strings.Contains(err.Error(), v) {
			t.Errorf("%q : erreur attendue, sans la valeur : %v", v, err)
		}
	}
}

func TestCleAltcha(t *testing.T) {
	c, err := Charger(env(minimal()))
	if err != nil || c.CleAltcha != nil || c.AltchaDifficulte != 100000 {
		t.Fatalf("sans DIGUEE_CLE_ALTCHA, la clé doit être absente : %v", err)
	}
	m := minimal()
	m["DIGUEE_CLE_ALTCHA"] = strings.Repeat("0b", 32)
	if c, err = Charger(env(m)); err != nil || len(c.CleAltcha) != 32 || c.CleAltcha[0] != 0x0b {
		t.Fatalf("clé valide refusée : %v", err)
	}
	m["DIGUEE_CLE_ALTCHA"] = strings.Repeat("secret", 11)
	if _, err := Charger(env(m)); err == nil || !strings.Contains(err.Error(), "DIGUEE_CLE_ALTCHA") || strings.Contains(err.Error(), "secret") {
		t.Errorf("erreur attendue, sans la valeur : %v", err)
	}
}

func TestWhatsApp(t *testing.T) {
	c, err := Charger(env(minimal()))
	if err != nil || c.WhatsApp != "fake" || c.JetonDuree != 10*time.Minute {
		t.Fatalf("défaut : %v %q", err, c.WhatsApp)
	}
	m := minimal()
	m["DIGUEE_WHATSAPP"] = "meta"
	m["DIGUEE_META_SECRET_APP"] = "court"
	_, err = Charger(env(m))
	for _, attendu := range []string{"DIGUEE_META_ID_NUMERO est obligatoire", "DIGUEE_META_SECRET_APP est trop court", "DIGUEE_META_JETON_WEBHOOK est obligatoire"} {
		if err == nil || !strings.Contains(err.Error(), attendu) {
			t.Errorf("erreur attendue %q : %v", attendu, err)
		}
	}
	if err != nil && strings.Contains(err.Error(), "court\"") {
		t.Error("le message d'erreur ne doit pas citer le secret")
	}
	m["DIGUEE_META_ID_NUMERO"] = "106540352242922"
	m["DIGUEE_META_SECRET_APP"] = "0123456789abcdef0123456789abcdef"
	m["DIGUEE_META_JETON_WEBHOOK"] = "jeton-de-verification-du-webhook"
	c, err = Charger(env(m))
	if err != nil || c.Meta.IDNumero != "106540352242922" {
		t.Fatalf("meta : %v", err)
	}
	m["DIGUEE_WHATSAPP"] = "bot"
	if _, err := Charger(env(m)); err == nil {
		t.Error("mode inconnu accepté")
	}
}

func TestNumeroWhatsApp(t *testing.T) {
	for saisie, attendu := range map[string]string{"+221 77 123 45 67": "221771234567", "221771234567": "221771234567"} {
		m := minimal()
		m["DIGUEE_NUMERO_WHATSAPP"] = saisie
		c, err := Charger(env(m))
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := c.NumeroWhatsAppChiffres(); got != attendu {
			t.Errorf("%q : %q", saisie, got)
		}
	}
	for _, saisie := range []string{"00221771234567", "+221-77", "wa.me/221771234567", "1234"} {
		m := minimal()
		m["DIGUEE_NUMERO_WHATSAPP"] = saisie
		if _, err := Charger(env(m)); err == nil || !strings.Contains(err.Error(), "DIGUEE_NUMERO_WHATSAPP") {
			t.Errorf("%q devrait être refusé : %v", saisie, err)
		}
	}
	c, _ := Charger(env(minimal()))
	if _, err := c.NumeroWhatsAppChiffres(); err == nil {
		t.Error("« À COMPLÉTER » n'est pas un numéro")
	}
}
