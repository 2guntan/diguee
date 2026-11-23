package https

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const publique = "https://consultation.diguee.sn"

// transportInterdit fait échouer le test si autocert appelle l'annuaire.
type transportInterdit struct{ t *testing.T }

func (ti transportInterdit) RoundTrip(r *http.Request) (*http.Response, error) {
	ti.t.Errorf("appel inattendu à l'annuaire ACME : %s", r.URL)
	return nil, errors.New("interdit")
}

func construire(t *testing.T, journal *slog.Logger) *Serveurs {
	t.Helper()
	s, err := Construire(Options{
		URLPublique: publique, Cache: t.TempDir(), Journal: journal,
		ClientACME: &http.Client{Transport: transportInterdit{t}},
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "site") }))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRedirectionVersHTTPS(t *testing.T) {
	s := construire(t, nil)
	for _, c := range []struct {
		methode, cible, hote string
		statut               int
		location             string
	}{
		{"GET", "/", "consultation.diguee.sn", 301, publique + "/"},
		{"GET", "/resultats?jour=2026-12-01", "consultation.diguee.sn", 301, publique + "/resultats?jour=2026-12-01"},
		{"HEAD", "/journal", "consultation.diguee.sn", 301, publique + "/journal"},
		// L'en-tête Host reçu n'est jamais repris : pas de redirection ouverte.
		{"GET", "/verifier", "exemple.invalid", 301, publique + "/verifier"},
		{"GET", "//exemple.invalid/x", "consultation.diguee.sn", 301, publique + "/"},
		// Un formulaire envoyé en clair n'est pas relayé.
		{"POST", "/envoyer", "consultation.diguee.sn", 400, ""},
		// Le défi HTTP-01 : jeton inconnu (404), autre hôte refusé (403).
		{"GET", "/.well-known/acme-challenge/inconnu", "consultation.diguee.sn", 404, ""},
		{"GET", "/.well-known/acme-challenge/inconnu", "exemple.invalid", 403, ""},
	} {
		req := httptest.NewRequest(c.methode, "http://"+c.hote+c.cible, strings.NewReader("numero=+221771234567"))
		req.Host = c.hote
		rec := httptest.NewRecorder()
		s.HTTP.Handler.ServeHTTP(rec, req)
		if rec.Code != c.statut || rec.Header().Get("Location") != c.location {
			t.Errorf("%s %s (Host %s) : %d %q, attendu %d %q", c.methode, c.cible, c.hote, rec.Code, rec.Header().Get("Location"), c.statut, c.location)
		}
		if rec.Header().Get("Strict-Transport-Security") != "" {
			t.Errorf("HSTS sur HTTP : %s", c.cible)
		}
	}
}

func TestHSTSSurHTTPSSeulement(t *testing.T) {
	s := construire(t, nil)
	req := httptest.NewRequest("GET", publique+"/", nil)
	req.TLS = &tls.ConnectionState{}
	rec := httptest.NewRecorder()
	s.HTTPS.Handler.ServeHTTP(rec, req)
	if got := rec.Header().Get("Strict-Transport-Security"); got != ValeurHSTS || rec.Body.String() != "site" {
		t.Errorf("HSTS %q, corps %q", got, rec.Body.String())
	}
	if !strings.HasPrefix(ValeurHSTS, "max-age=31536000") {
		t.Errorf("HSTS : %s", ValeurHSTS)
	}
	req = httptest.NewRequest("GET", "http://localhost/", nil)
	rec = httptest.NewRecorder()
	HSTS(s.HTTPS.Handler).ServeHTTP(rec, req)
	if rec.Header().Get("Strict-Transport-Security") != "" {
		t.Error("HSTS sans TLS")
	}
}

// Seul le nom de DIGUEE_URL_PUBLIQUE obtient un certificat : un autre nom,
// ou une connexion sans SNI (par l'adresse IP), est refusé sans appel à
// l'annuaire ACME et sans rien journaliser.
func TestListeBlancheDesHotes(t *testing.T) {
	var buf bytes.Buffer
	s := construire(t, slog.New(slog.NewJSONHandler(&buf, nil)))
	for _, nom := range []string{"", "autre.diguee.sn", "diguee.sn", "consultation.diguee.sn.exemple.invalid", "203.0.113.7"} {
		if _, err := s.HTTPS.TLSConfig.GetCertificate(&tls.ClientHelloInfo{ServerName: nom}); err == nil {
			t.Errorf("certificat donné pour %q", nom)
		}
	}
	if buf.Len() != 0 {
		t.Errorf("journal : %s", buf.String())
	}
	pol := Politique("consultation.diguee.sn")
	for _, h := range []string{"consultation.diguee.sn", "Consultation.Diguee.SN", "consultation.diguee.sn:80", "consultation.diguee.sn."} {
		if err := pol(context.Background(), h); err != nil {
			t.Errorf("%q refusé : %v", h, err)
		}
	}
	if s.HTTPS.TLSConfig.MinVersion != tls.VersionTLS12 || s.Hote() != "consultation.diguee.sn" {
		t.Errorf("TLS min %x, hôte %s", s.HTTPS.TLSConfig.MinVersion, s.Hote())
	}
	if s.HTTPS.Addr != ":443" || s.HTTP.Addr != ":80" {
		t.Errorf("adresses %s %s", s.HTTPS.Addr, s.HTTP.Addr)
	}
	if s.Gestionnaire.Client.DirectoryURL != AnnuaireLetsEncrypt {
		t.Errorf("annuaire %s", s.Gestionnaire.Client.DirectoryURL)
	}
}

func TestConfigurationRefusee(t *testing.T) {
	site := http.NotFoundHandler()
	for _, o := range []Options{
		{URLPublique: "http://consultation.diguee.sn", Cache: "/tmp/x"},
		{URLPublique: "https://203.0.113.7", Cache: "/tmp/x"},
		{URLPublique: "https://[2001:db8::1]", Cache: "/tmp/x"},
		{URLPublique: publique},
	} {
		if _, err := Construire(o, site); err == nil {
			t.Errorf("%+v acceptée", o)
		}
	}
}

// Obtention réelle d'un certificat auprès de Pebble, le serveur ACME de test
// de Let's Encrypt, par TLS-ALPN-01 puis par HTTP-01 ; sauté si Pebble
// n'est pas installé. DIGUEE_TEST_PEBBLE : dossier qui contient les
// exécutables pebble et pebble-challtestsrv (faux DNS) ;
// DIGUEE_TEST_PEBBLE_DOSSIER : dossier des sources, pour ses certificats de
// test. Voir deploiement/README.md.
func TestPebble(t *testing.T) {
	bin := os.Getenv("DIGUEE_TEST_PEBBLE")
	sources := os.Getenv("DIGUEE_TEST_PEBBLE_DOSSIER")
	if bin == "" || sources == "" {
		t.Skip("DIGUEE_TEST_PEBBLE et DIGUEE_TEST_PEBBLE_DOSSIER absents : test sauté")
	}
	// Faux DNS : tout nom résout en 127.0.0.1 (autocert refuse « localhost »,
	// un nom sans point).
	adrDNS := portLibre(t)
	dns := exec.Command(filepath.Join(bin, "pebble-challtestsrv"), "-dns01", adrDNS, "-http01", "", "-https01", "",
		"-tlsalpn01", "", "-doh", "", "-management", portLibre(t), "-defaultIPv4", "127.0.0.1", "-defaultIPv6", "")
	if err := dns.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dns.Process.Kill(); dns.Wait() })
	for _, defi := range []string{"tls-alpn-01", "http-01"} {
		t.Run(defi, func(t *testing.T) { essayerPebble(t, filepath.Join(bin, "pebble"), sources, adrDNS, defi) })
	}
}

func portLibre(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func essayerPebble(t *testing.T, pebble, sources, adrDNS, defi string) {
	adrHTTPS, adrHTTP := portLibre(t), portLibre(t)
	_, portHTTPS, _ := net.SplitHostPort(adrHTTPS)
	_, portHTTP, _ := net.SplitHostPort(adrHTTP)
	adrACME, adrGestion := portLibre(t), portLibre(t)
	if defi == "http-01" {
		// Pebble ne peut valider TLS-ALPN-01 sur un port fermé : il passe à HTTP-01.
		portHTTPS = "1"
	}
	dir := t.TempDir()
	conf := map[string]any{"pebble": map[string]any{
		"listenAddress": adrACME, "managementListenAddress": adrGestion,
		"certificate": filepath.Join(sources, "test/certs/localhost/cert.pem"),
		"privateKey":  filepath.Join(sources, "test/certs/localhost/key.pem"),
		"httpPort":    atoi(portHTTP), "tlsPort": atoi(portHTTPS),
	}}
	b, _ := json.Marshal(conf)
	chemin := filepath.Join(dir, "pebble.json")
	if err := os.WriteFile(chemin, b, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(pebble, "-config", chemin, "-strict", "-dnsserver", adrDNS)
	cmd.Env = append(os.Environ(), "PEBBLE_VA_NOSLEEP=1", "PEBBLE_WFE_NONCEREJECT=0", "PEBBLE_AUTHZREUSE=0")
	var sortie bytes.Buffer
	cmd.Stdout, cmd.Stderr = &sortie, &sortie
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
		if t.Failed() || os.Getenv("DIGUEE_TEST_PEBBLE_BAVARD") != "" {
			t.Logf("pebble :\n%s", sortie.String())
		}
	})
	acPebble, err := os.ReadFile(filepath.Join(sources, "test/certs/pebble.minica.pem"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(acPebble)
	clientACME := &http.Client{Transport: &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{RootCAs: pool}}}
	attendre(t, "https://"+adrACME+"/dir", clientACME)

	s, err := Construire(Options{
		URLPublique: "https://" + nomTest, Cache: filepath.Join(dir, "acme"), Annuaire: "https://" + adrACME + "/dir",
		AdresseHTTPS: adrHTTPS, AdresseHTTP: adrHTTP, ClientACME: clientACME,
		Journal: slog.New(slog.NewTextHandler(ecrivainTest{t}, nil)),
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "accueil") }))
	if err != nil {
		t.Fatal(err)
	}
	erreurs, err := s.Lancer()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, annuler := context.WithTimeout(context.Background(), 5*time.Second)
		defer annuler()
		s.Arreter(ctx)
		<-erreurs
		<-erreurs
	})

	// Racine de la chaîne délivrée par Pebble (différente à chaque lancement).
	racines := x509.NewCertPool()
	gestion := &http.Client{Transport: &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	res, err := gestion.Get("https://" + adrGestion + "/roots/0")
	if err != nil {
		t.Fatal(err)
	}
	pem, _ := io.ReadAll(res.Body)
	res.Body.Close()
	racines.AppendCertsFromPEM(pem)

	client := &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{Proxy: nil,
		TLSClientConfig: &tls.Config{RootCAs: racines, ServerName: nomTest}}}
	res, err = client.Get("https://" + adrHTTPS + "/")
	if err != nil {
		t.Fatalf("HTTPS avec le certificat de Pebble : %v", err)
	}
	corps, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(corps) != "accueil" || res.Header.Get("Strict-Transport-Security") != ValeurHSTS {
		t.Errorf("réponse %q, HSTS %q", corps, res.Header.Get("Strict-Transport-Security"))
	}
	if n := res.TLS.PeerCertificates[0].DNSNames; len(n) != 1 || n[0] != nomTest {
		t.Errorf("noms du certificat : %v", n)
	}
	// Le certificat est dans le cache : un nouveau gestionnaire le relit
	// sans appeler l'annuaire.
	if _, err := os.Stat(filepath.Join(dir, "acme", nomTest)); err != nil {
		t.Errorf("certificat absent du cache : %v", err)
	}
	s2, err := Construire(Options{URLPublique: "https://" + nomTest, Cache: filepath.Join(dir, "acme"),
		ClientACME: &http.Client{Transport: transportInterdit{t}}}, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.HTTPS.TLSConfig.GetCertificate(&tls.ClientHelloInfo{ServerName: nomTest,
		CipherSuites: []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256}, SupportedVersions: []uint16{tls.VersionTLS13}}); err != nil {
		t.Errorf("relecture du cache : %v", err)
	}
	// La redirection du port 80 reste servie.
	nr := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err = nr.Get("http://" + adrHTTP + "/journal")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 301 || res.Header.Get("Location") != "https://"+nomTest+"/journal" {
		t.Errorf("redirection : %d %s", res.StatusCode, res.Header.Get("Location"))
	}
}

// nomTest est résolu en 127.0.0.1 par le faux DNS.
const nomTest = "consultation.diguee.test"

func atoi(s string) int {
	var n int
	fmt.Sscanf(s, "%d", &n)
	return n
}

func attendre(t *testing.T, adresse string, c *http.Client) {
	t.Helper()
	for range 100 {
		if res, err := c.Get(adresse); err == nil {
			res.Body.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s ne répond pas", adresse)
}

type ecrivainTest struct{ t *testing.T }

func (e ecrivainTest) Write(p []byte) (int, error) { e.t.Log(string(p)); return len(p), nil }
