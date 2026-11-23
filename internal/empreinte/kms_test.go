package empreinte

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/empreinte/fauxkms"
	"github.com/2guntan/diguee/internal/garde"
	"github.com/2guntan/diguee/internal/integration"
)

const (
	numeroA = "+221771234567"
	numeroB = "+221771234568"
)

// sansNumero échoue si le texte contient le numéro sous une forme reconnaissable.
func sansNumero(t *testing.T, texte, numero string) {
	t.Helper()
	for _, forme := range []string{numero, numero[4:], numero[1:], base64.StdEncoding.EncodeToString([]byte(numero)),
		base64.RawURLEncoding.EncodeToString([]byte(numero))} {
		if strings.Contains(texte, forme) {
			t.Errorf("le texte contient le numéro (%q) : %s", forme, texte)
		}
	}
}

func configAvec(t *testing.T, env map[string]string) *config.Config {
	t.Helper()
	env["DIGUEE_POSTGRES_URL"] = "postgres://localhost/diguee"
	c, err := config.Charger(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func construireOVH(t *testing.T, f *fauxkms.OVH, extra map[string]string) Empreinteur {
	t.Helper()
	env := f.Env()
	for k, v := range extra {
		env[k] = v
	}
	var r integration.Registre
	e, err := Construire(configAvec(t, env), &r)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// La requête envoyée respecte l'API d'OVHcloud (chemin, format raw, RS256,
// isdigest=false, message en base64, certificat client), et l'empreinte est
// SHA-256 de la signature PKCS#1 v1.5.
func TestOVHRequeteEtEmpreinte(t *testing.T) {
	f := fauxkms.DemarrerOVH(t)
	e := construireOVH(t, f, nil)
	ctx := context.Background()
	a, err := e.Calculer(ctx, numeroA)
	if err != nil {
		t.Fatal(err)
	}
	if d := f.Defauts(); len(d) > 0 {
		t.Fatalf("requête non conforme : %v", d)
	}
	if want := sha256.Sum256(f.Signature(numeroA)); a != want {
		t.Errorf("empreinte %x, attendu SHA-256(signature) %x", a, want)
	}
	a2, _ := e.Calculer(ctx, numeroA)
	b, _ := e.Calculer(ctx, numeroB)
	if a2 != a || b == a {
		t.Errorf("déterminisme ou distinction en défaut : %x %x %x", a, a2, b)
	}
	if err := VerifierDeterminisme(ctx, e); err != nil {
		t.Error(err)
	}
	if _, err := e.Calculer(ctx, "771234567"); !errors.Is(err, ErrFormat) {
		t.Errorf("numéro non normalisé : %v", err)
	}
	if f.Requetes() != 6 {
		t.Errorf("%d requêtes, attendu 6 (le numéro non normalisé n'est pas envoyé)", f.Requetes())
	}
}

// T5 : un KMS qui signe en PS256 (aléatoire) est détecté au démarrage.
func TestOVHAleatoireRefuse(t *testing.T) {
	f := fauxkms.DemarrerOVH(t)
	f.Regler(fauxkms.Reglage{Aleatoire: true})
	err := VerifierDeterminisme(context.Background(), construireOVH(t, f, nil))
	if !errors.Is(err, ErrNonDeterministe) {
		t.Fatalf("refus attendu : %v", err)
	}
}

// Sans certificat client, le KMS refuse la connexion.
func TestOVHSansCertificatClient(t *testing.T) {
	f := fauxkms.DemarrerOVH(t)
	ac, err := autorites(f.AC)
	if err != nil {
		t.Fatal(err)
	}
	e, err := NouveauOVH(OptionsOVH{PointAcces: f.URL, KMSID: f.KMSID, CleID: f.CleID,
		TLS: tlsAvecAC(ac)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Calculer(context.Background(), numeroA); !errors.Is(err, ErrKMS) {
		t.Fatalf("échec attendu : %v", err)
	}
}

// Aucune panne du KMS ne fait apparaître le numéro dans l'erreur, même quand
// le serveur le renvoie dans sa réponse.
func TestOVHPannesSansNumero(t *testing.T) {
	cas := map[string]struct {
		reglage fauxkms.Reglage
		motif   string
	}{
		"403 qui recopie le numéro": {fauxkms.Reglage{Statut: 403, Corps: `{"error_code":17125378,"error_id":"a","errors":["Forbidden: sign {numero} ` +
			`(message 4oCvKzIyMTc3MTIzNDU2Nw== / KzIyMTc3MTIzNDU2Nw==)"],"request_id":"79b66eba"}`}, "réponse HTTP 403"},
		"400 avec le numéro sans +": {fauxkms.Reglage{Statut: 400, Corps: `{"errors":["bad message 221771234567 / 77 123 45 67"]}`}, "réponse HTTP 400"},
		"500 non JSON":              {fauxkms.Reglage{Statut: 500, Corps: "panne +221771234567"}, "réponse HTTP 500"},
		"503":                       {fauxkms.Reglage{Statut: 503, Corps: `{}`}, "réponse HTTP 503"},
		"délai dépassé":             {fauxkms.Reglage{Attente: 2 * time.Second}, "délai d'attente dépassé"},
		"JSON invalide":             {fauxkms.Reglage{Brute: "{+221771234567"}, "réponse JSON invalide"},
		"signature non base64":      {fauxkms.Reglage{Brute: `{"signature":"+221771234567!"}`}, "signature illisible"},
		"signature trop courte":     {fauxkms.Reglage{Brute: `{"signature":"KzIyMTc3MTIzNDU2Nw=="}`}, "2048 bits"},
		"réponse sans signature":    {fauxkms.Reglage{Brute: `{}`}, "signature de 0 octets"},
	}
	for nom, c := range cas {
		t.Run(nom, func(t *testing.T) {
			f := fauxkms.DemarrerOVH(t)
			f.Regler(c.reglage)
			e := construireOVH(t, f, map[string]string{"DIGUEE_KMS_DELAI": "300ms"})
			_, err := e.Calculer(context.Background(), numeroA)
			if !errors.Is(err, ErrKMS) || !strings.Contains(err.Error(), c.motif) {
				t.Fatalf("erreur %v, attendu %q", err, c.motif)
			}
			sansNumero(t, err.Error(), numeroA)
			// L'erreur de démarrage ne le cite pas non plus.
			sansNumero(t, VerifierDeterminisme(context.Background(), e).Error(), numeroDeControle)
		})
	}
	t.Run("serveur arrêté", func(t *testing.T) {
		f := fauxkms.DemarrerOVH(t)
		e := construireOVH(t, f, nil)
		f.Fermer()
		_, err := e.Calculer(context.Background(), numeroA)
		if !errors.Is(err, ErrKMS) || !strings.Contains(err.Error(), "appel impossible") {
			t.Fatalf("erreur %v", err)
		}
		sansNumero(t, err.Error(), numeroA)
	})
	t.Run("contexte annulé", func(t *testing.T) {
		f := fauxkms.DemarrerOVH(t)
		f.Regler(fauxkms.Reglage{Attente: 2 * time.Second})
		ctx, annuler := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer annuler()
		_, err := construireOVH(t, f, nil).Calculer(ctx, numeroA)
		if !errors.Is(err, ErrKMS) || !strings.Contains(err.Error(), "délai d'attente dépassé") {
			t.Fatalf("erreur %v", err)
		}
		sansNumero(t, err.Error(), numeroA)
	})
}

// Les modes ovh et gcp ne sont pas des fausses implémentations : la garde les
// admet en production.
func TestModesReelsAdmisEnProduction(t *testing.T) {
	prod := map[string]string{
		"DIGUEE_ENV": "production", "DIGUEE_STRUCTURE_NOM": "S", "DIGUEE_STRUCTURE_ADRESSE": "A",
		"DIGUEE_CONTACT": "c", "DIGUEE_NUMERO_CDP": "n", "DIGUEE_NUMERO_WHATSAPP": "w",
		"DIGUEE_OUVERTURE": "2027-01-15", "DIGUEE_CLE_TOTP": strings.Repeat("ab", 32),
	}
	for nom, env := range map[string]map[string]string{
		"ovh": fauxkms.DemarrerOVH(t).Env(), "gcp": fauxkms.DemarrerGCP(t).Env(),
	} {
		for k, v := range prod {
			env[k] = v
		}
		c := configAvec(t, env)
		var r integration.Registre
		if _, err := Construire(c, &r); err != nil {
			t.Fatal(err)
		}
		ds := r.Declarations()
		if len(ds) != 1 || ds[0].Mode != nom || ds[0].Fausse() {
			t.Errorf("%s : déclarations %+v", nom, ds)
		}
		if err := garde.Verifier(c, &r); err != nil {
			t.Errorf("%s : %v", nom, err)
		}
	}
}

// Configuration : PS256 n'est pas réglable, les réglages manquants sont
// signalés, et les fichiers illisibles empêchent la construction.
func TestConfigurationOVH(t *testing.T) {
	m := map[string]string{"DIGUEE_POSTGRES_URL": "x", "DIGUEE_EMPREINTEUR": "ovh"}
	_, err := config.Charger(func(k string) string { return m[k] })
	for _, v := range []string{"DIGUEE_OVH_KMS_POINT_ACCES", "DIGUEE_OVH_KMS_ID", "DIGUEE_OVH_KMS_CLE_ID",
		"DIGUEE_OVH_KMS_CERTIFICAT", "DIGUEE_OVH_KMS_CLE_CLIENT"} {
		if err == nil || !strings.Contains(err.Error(), v) {
			t.Errorf("%s manquante non signalée : %v", v, err)
		}
	}
	m["DIGUEE_OVH_KMS_POINT_ACCES"] = "http://eu-west-par.okms.ovh.net"
	m["DIGUEE_OVH_KMS_ID"] = "pas-un-uuid"
	if _, err := config.Charger(func(k string) string { return m[k] }); err == nil ||
		!strings.Contains(err.Error(), "https://") || !strings.Contains(err.Error(), "UUID") {
		t.Errorf("http:// et UUID invalide devraient être refusés : %v", err)
	}
	m["DIGUEE_EMPREINTEUR"] = "PS256"
	if _, err := config.Charger(func(k string) string { return m[k] }); err == nil {
		t.Error("mode inconnu accepté")
	}

	f := fauxkms.DemarrerOVH(t)
	env := f.Env()
	env["DIGUEE_OVH_KMS_CLE_CLIENT"] = f.AC // pas une clé privée
	var r integration.Registre
	if _, err := Construire(configAvec(t, env), &r); err == nil || !strings.Contains(err.Error(), "DIGUEE_OVH_KMS_CLE_CLIENT") {
		t.Errorf("clé client illisible acceptée : %v", err)
	}
	if algorithmeOVH != "RS256" {
		t.Fatal("seul RS256 est permis")
	}
}

func construireGCP(t *testing.T, f *fauxkms.GCP, extra map[string]string) Empreinteur {
	t.Helper()
	env := f.Env()
	for k, v := range extra {
		env[k] = v
	}
	var r integration.Registre
	e, err := Construire(configAvec(t, env), &r)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestGCPRequeteEtEmpreinte(t *testing.T) {
	f := fauxkms.DemarrerGCP(t)
	e := construireGCP(t, f, nil)
	ctx := context.Background()
	a, err := e.Calculer(ctx, numeroA)
	if err != nil {
		t.Fatal(err)
	}
	if d := f.Defauts(); len(d) > 0 {
		t.Fatalf("requête non conforme : %v", d)
	}
	if want := sha256.Sum256(f.Mac(numeroA)); a != want {
		t.Errorf("empreinte %x, attendu %x", a, want)
	}
	if err := VerifierDeterminisme(ctx, e); err != nil {
		t.Error(err)
	}
	b, _ := e.Calculer(ctx, numeroB)
	if a == b {
		t.Error("deux numéros, une seule empreinte")
	}
}

func TestGCPPannesSansNumeroNiJeton(t *testing.T) {
	cas := map[string]struct {
		reglage fauxkms.Reglage
		motif   string
	}{
		"401":                   {fauxkms.Reglage{Statut: 401, Corps: `{"error":{"code":401,"message":"bad data {numero}","status":"UNAUTHENTICATED"}}`}, "UNAUTHENTICATED"},
		"délai dépassé":         {fauxkms.Reglage{Attente: 2 * time.Second}, "délai d'attente dépassé"},
		"CRC du HMAC faux":      {fauxkms.Reglage{MauvaisCRC: true}, "CRC32C du HMAC"},
		"données non vérifiées": {fauxkms.Reglage{NonVerifiee: true}, "CRC32C des données"},
		"HMAC trop court":       {fauxkms.Reglage{Brute: `{"mac":"AAAA","verifiedDataCrc32c":true}`}, "HMAC_SHA256"},
	}
	for nom, c := range cas {
		t.Run(nom, func(t *testing.T) {
			f := fauxkms.DemarrerGCP(t)
			f.Regler(c.reglage)
			_, err := construireGCP(t, f, map[string]string{"DIGUEE_KMS_DELAI": "300ms"}).Calculer(context.Background(), numeroA)
			if !errors.Is(err, ErrKMS) || !strings.Contains(err.Error(), c.motif) {
				t.Fatalf("erreur %v, attendu %q", err, c.motif)
			}
			sansNumero(t, err.Error(), numeroA)
			if strings.Contains(err.Error(), f.Jeton()) {
				t.Errorf("l'erreur contient le jeton : %v", err)
			}
		})
	}
	t.Run("fichier du jeton absent", func(t *testing.T) {
		f := fauxkms.DemarrerGCP(t)
		_, err := construireGCP(t, f, map[string]string{"DIGUEE_GCP_KMS_JETON_FICHIER": t.TempDir() + "/absent"}).
			Calculer(context.Background(), numeroA)
		if !errors.Is(err, ErrKMS) || f.Requetes() != 0 {
			t.Fatalf("erreur %v, %d requêtes", err, f.Requetes())
		}
	})
}

func TestConfigurationGCP(t *testing.T) {
	m := map[string]string{"DIGUEE_POSTGRES_URL": "x", "DIGUEE_EMPREINTEUR": "gcp", "DIGUEE_GCP_KMS_CLE": "projects/p/locations/europe-west9/keyRings/k/cryptoKeys/c"}
	_, err := config.Charger(func(k string) string { return m[k] })
	if err == nil || !strings.Contains(err.Error(), "DIGUEE_GCP_KMS_CLE") || !strings.Contains(err.Error(), "DIGUEE_GCP_KMS_JETON_FICHIER") {
		t.Errorf("version de clé incomplète et jeton absent devraient être signalés : %v", err)
	}
	m["DIGUEE_GCP_KMS_CLE"] += "/cryptoKeyVersions/1"
	m["DIGUEE_GCP_KMS_JETON_FICHIER"] = "/run/diguee/jeton"
	c, err := config.Charger(func(k string) string { return m[k] })
	if err != nil || c.GCPKMS.PointAcces != "https://cloudkms.googleapis.com" {
		t.Fatalf("%v %+v", err, c)
	}
}

func tlsAvecAC(p *x509.CertPool) *tls.Config { return &tls.Config{RootCAs: p} }

func TestMasquer(t *testing.T) {
	got := masquer("a +221771234567 b KzIyMTc3MTIzNDU2Nw== c 77 123 45 67 d 77-123-45-67 e 12345678", numeroA)
	if strings.Count(got, "[masqué]") != 4 || !strings.Contains(got, "12345678") {
		t.Errorf("masquage : %s", got)
	}
}
