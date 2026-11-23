package config

import (
	"maps"
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
	if c.SMS != "fake" || c.SMSParEmpreinte24h != 2 || c.SMSCodeEssais != 3 || c.SMSIPParHeure != 10 || c.SMSParHeure != 100 ||
		c.SMSCoupeCircuitPlancher != 20 || c.SMSReactivation != time.Hour || c.SMSSansJSParHeure != 10 || c.SMSSansJSIPParHeure != 2 {
		t.Errorf("réglages du SMS : %+v", c)
	}
	if !slices.Equal(c.PrefixesAutorises, []string{"70", "75", "76", "77", "78"}) {
		t.Errorf("préfixes : %v", c.PrefixesAutorises)
	}
	if c.NumeroCDP.Valeur != ACompleter || !c.NumeroCDP.Provisoire() {
		t.Errorf("le numéro CDP devrait être provisoire : %q", c.NumeroCDP.Valeur)
	}
	attendus := []string{"DIGUEE_STRUCTURE_NOM", "DIGUEE_STRUCTURE_ADRESSE", "DIGUEE_CONTACT",
		"DIGUEE_NUMERO_CDP", "DIGUEE_NUMERO_WHATSAPP", "DIGUEE_POPULATION_ANSD", "DIGUEE_ADRESSE_REPONSE", "DIGUEE_OUVERTURE", "DIGUEE_CLOTURE"}
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
	for _, attendu := range []string{"DIGUEE_META_ID_NUMERO est obligatoire", "DIGUEE_META_SECRET_APP est trop court", "DIGUEE_META_JETON_WEBHOOK est obligatoire",
		"DIGUEE_META_JETON_ACCES est obligatoire", "DIGUEE_META_MODELE_CODE est obligatoire"} {
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
	m["DIGUEE_META_JETON_ACCES"] = strings.Repeat("EAAB", 10)
	m["DIGUEE_META_MODELE_CODE"] = "diguee_code"
	c, err = Charger(env(m))
	if err != nil || c.Meta.IDNumero != "106540352242922" || c.Meta.LangueModele != "fr" || c.Meta.VersionAPI != "v23.0" ||
		c.Meta.Delai != 10*time.Second || c.Meta.ModeleCode != "diguee_code" {
		t.Fatalf("meta : %v %+v", err, c.Meta)
	}
	for nom, valeur := range map[string]string{"DIGUEE_META_MODELE_CODE": "Code Diguee", "DIGUEE_META_LANGUE_MODELE": "français",
		"DIGUEE_META_VERSION_API": "23", "DIGUEE_META_JETON_ACCES": "court"} {
		m2 := maps.Clone(m)
		m2[nom] = valeur
		if _, err := Charger(env(m2)); err == nil || !strings.Contains(err.Error(), nom) {
			t.Errorf("%s=%q accepté : %v", nom, valeur, err)
		}
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

// L'ancrage est simulé par défaut en développement, réel par défaut en
// production ; ses réglages sont contrôlés.
func TestReglagesAncrage(t *testing.T) {
	charger := func(m map[string]string) (*Config, error) {
		m["DIGUEE_POSTGRES_URL"] = "postgres://localhost/diguee"
		return Charger(func(k string) string { return m[k] })
	}
	c, err := charger(map[string]string{})
	if err != nil || c.Ancrage.Mode != "fake" || len(c.Ancrage.Calendriers) != 3 || c.Ancrage.MinCalendriers != 2 ||
		c.Ancrage.Archive != "https://web.archive.org" || c.URLPublique != "https://consultation.diguee.sn" {
		t.Fatalf("défauts : %+v, %v", c.Ancrage, err)
	}
	if c, err := charger(map[string]string{"DIGUEE_ENV": "production"}); err != nil || c.Ancrage.Mode != "reel" {
		t.Fatalf("production : %+v, %v", c, err)
	}
	c, err = charger(map[string]string{"DIGUEE_ANCRAGE": "reel", "DIGUEE_ANCRAGE_CALENDRIERS": "https://a.example/, https://b.example",
		"DIGUEE_ANCRAGE_MIN_CALENDRIERS": "1", "DIGUEE_URL_PUBLIQUE": "https://essai.example/"})
	if err != nil || c.Ancrage.Calendriers[0] != "https://a.example" || c.Ancrage.MinCalendriers != 1 || c.URLPublique != "https://essai.example" {
		t.Fatalf("réglages : %+v, %v", c.Ancrage, err)
	}
	for nom, m := range map[string]map[string]string{
		"mode":         {"DIGUEE_ANCRAGE": "twitter"},
		"http":         {"DIGUEE_ANCRAGE_CALENDRIERS": "http://a.example"},
		"chemin":       {"DIGUEE_ANCRAGE_ARCHIVE": "https://web.archive.org/save"},
		"minimum":      {"DIGUEE_ANCRAGE_MIN_CALENDRIERS": "4"},
		"vide":         {"DIGUEE_ANCRAGE_CALENDRIERS": " , "},
		"décalage":     {"DIGUEE_ANCRAGE_DECALAGE": "25h"},
		"url publique": {"DIGUEE_URL_PUBLIQUE": "consultation.diguee.sn"},
	} {
		if _, err := charger(m); err == nil {
			t.Errorf("%s : accepté", nom)
		}
	}
}

// TLS (ticket #7) : HTTP simple en développement, ACME en production.
func TestTLS(t *testing.T) {
	c, err := Charger(env(minimal()))
	if err != nil || c.TLS.Mode != "non" || c.TLS.AdresseHTTPS != "" || c.PostgresURLMigrations != "" {
		t.Fatalf("développement : %+v, %v", c.TLS, err)
	}
	m := minimal()
	m["DIGUEE_ENV"] = "production"
	m["DIGUEE_POSTGRES_URL_MIGRATIONS"] = "postgres://proprietaire@localhost/diguee"
	c, err = Charger(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if c.TLS != (TLS{Mode: "acme", AdresseHTTPS: ":443", AdresseHTTP: ":80", Cache: "/srv/diguee-donnees/diguee/acme",
		Annuaire: "https://acme-v02.api.letsencrypt.org/directory"}) || c.PostgresURLMigrations != m["DIGUEE_POSTGRES_URL_MIGRATIONS"] {
		t.Errorf("production : %+v", c.TLS)
	}
	m = minimal()
	m["DIGUEE_TLS"] = "acme"
	m["DIGUEE_ACME_ANNUAIRE"] = "https://acme-staging-v02.api.letsencrypt.org/directory"
	m["DIGUEE_ACME_COURRIEL"] = "technique@diguee.sn"
	m["DIGUEE_ADRESSE_HTTPS"] = ":8443"
	if c, err = Charger(env(m)); err != nil || c.TLS.Annuaire != m["DIGUEE_ACME_ANNUAIRE"] || c.TLS.AdresseHTTPS != ":8443" || c.TLS.Courriel == "" {
		t.Errorf("réglages ACME : %+v, %v", c, err)
	}
	for k, v := range map[string]string{
		"DIGUEE_TLS": "oui", "DIGUEE_ACME_ANNUAIRE": "http://acme.example/dir", "DIGUEE_ACME_COURRIEL": "personne",
		"DIGUEE_URL_PUBLIQUE": "https://consultation.diguee.sn:8443",
	} {
		m := minimal()
		m["DIGUEE_TLS"] = "acme"
		m[k] = v
		if _, err := Charger(env(m)); err == nil || !strings.Contains(err.Error(), k) {
			t.Errorf("%s=%s accepté : %v", k, v, err)
		}
	}
}
