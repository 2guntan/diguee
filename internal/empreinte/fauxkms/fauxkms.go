// Package fauxkms fournit, pour les tests seulement, de faux serveurs HTTPS
// qui imitent l'API du KMS d'OVHcloud (opération sign, avec certificat client)
// et celle de Google Cloud KMS (macSign). Ils vérifient chaque requête reçue
// (chemin, algorithme, encodage du message, authentification) et peuvent
// simuler les pannes : signature aléatoire (PS256), erreurs HTTP, lenteur,
// réponses mal formées.
//
// Ce paquet n'est importé que par des tests : il n'entre pas dans l'exécutable.
package fauxkms

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Reglage décrit le comportement du faux serveur.
type Reglage struct {
	Aleatoire   bool          // OVH : signe en PSS (aléatoire) au lieu de PKCS#1 v1.5
	Statut      int           // si non nul, répond ce code HTTP avec Corps
	Corps       string        // corps de la réponse d'erreur ; « {numero} » est remplacé par le numéro reçu
	Attente     time.Duration // attend avant de répondre
	Brute       string        // si non vide, renvoyé tel quel avec le code 200
	MauvaisCRC  bool          // GCP : somme CRC32C du HMAC fausse
	NonVerifiee bool          // GCP : verifiedDataCrc32c à false
}

type commun struct {
	t        testing.TB
	srv      *httptest.Server
	mu       sync.Mutex
	reglage  Reglage
	requetes int
	defauts  []string
	URL      string
	AC       string // fichier PEM du certificat du serveur
}

// Regler change le comportement du serveur.
func (c *commun) Regler(r Reglage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reglage = r
}

// Requetes renvoie le nombre de requêtes reçues.
func (c *commun) Requetes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requetes
}

// Defauts renvoie les écarts constatés entre les requêtes reçues et l'API.
func (c *commun) Defauts() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.defauts...)
}

// Fermer arrête le serveur (les appels suivants échouent).
func (c *commun) Fermer() { c.srv.Close() }

func (c *commun) defaut(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.defauts = append(c.defauts, fmt.Sprintf(format, args...))
}

// debut compte la requête et applique les réglages communs. Il renvoie false
// si la réponse a déjà été écrite.
func (c *commun) debut(w http.ResponseWriter, r *http.Request, numero string) (Reglage, bool) {
	c.mu.Lock()
	c.requetes++
	rg := c.reglage
	c.mu.Unlock()
	if rg.Attente > 0 {
		select {
		case <-time.After(rg.Attente):
		case <-r.Context().Done():
			return rg, false
		}
	}
	if rg.Statut != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rg.Statut)
		io.WriteString(w, strings.ReplaceAll(rg.Corps, "{numero}", numero))
		return rg, false
	}
	if rg.Brute != "" {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, rg.Brute)
		return rg, false
	}
	return rg, true
}

func ecrirePEM(t testing.TB, dir, nom, typ string, der []byte) string {
	t.Helper()
	chemin := filepath.Join(dir, nom)
	if err := os.WriteFile(chemin, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return chemin
}

var (
	cleRSAUnique sync.Once
	cleRSA       *rsa.PrivateKey
)

func cleRSATest(t testing.TB) *rsa.PrivateKey {
	cleRSAUnique.Do(func() {
		var err error
		if cleRSA, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
	})
	return cleRSA
}

// OVH imite l'API REST du KMS d'OVHcloud.
type OVH struct {
	commun
	KMSID, CleID          string
	Certificat, CleClient string // fichiers PEM du certificat client accepté
	cle                   *rsa.PrivateKey
}

// DemarrerOVH démarre un faux KMS OVH qui exige un certificat client signé
// par une autorité de test. Il est arrêté à la fin du test.
func DemarrerOVH(t testing.TB) *OVH {
	t.Helper()
	dir := t.TempDir()
	cleAC, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	modeleAC := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "AC de test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	derAC, err := x509.CreateCertificate(rand.Reader, modeleAC, modeleAC, &cleAC.PublicKey, cleAC)
	if err != nil {
		t.Fatal(err)
	}
	ac, _ := x509.ParseCertificate(derAC)
	cleCli, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	modeleCli := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "diguee"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, KeyUsage: x509.KeyUsageDigitalSignature,
	}
	derCli, err := x509.CreateCertificate(rand.Reader, modeleCli, ac, &cleCli.PublicKey, cleAC)
	if err != nil {
		t.Fatal(err)
	}
	derCleCli, _ := x509.MarshalPKCS8PrivateKey(cleCli)

	o := &OVH{
		KMSID: "7745c93b-8ed3-4eef-921c-87bd8bbdd01b", CleID: "2dab95dc-d7d3-482b-a07b-6b4dfae89d58",
		Certificat: ecrirePEM(t, dir, "client.pem", "CERTIFICATE", derCli),
		CleClient:  ecrirePEM(t, dir, "client.key", "PRIVATE KEY", derCleCli),
		cle:        cleRSATest(t),
	}
	o.t = t
	pool := x509.NewCertPool()
	pool.AddCert(ac)
	o.srv = httptest.NewUnstartedServer(http.HandlerFunc(o.servir))
	o.srv.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
	o.srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	o.srv.StartTLS()
	t.Cleanup(o.srv.Close)
	o.URL = o.srv.URL
	o.AC = ecrirePEM(t, dir, "ac.pem", "CERTIFICATE", o.srv.Certificate().Raw)
	return o
}

// Env renvoie les variables DIGUEE_* qui désignent ce faux KMS.
func (o *OVH) Env() map[string]string {
	return map[string]string{
		"DIGUEE_EMPREINTEUR":         "ovh",
		"DIGUEE_OVH_KMS_POINT_ACCES": o.URL,
		"DIGUEE_OVH_KMS_ID":          o.KMSID,
		"DIGUEE_OVH_KMS_CLE_ID":      o.CleID,
		"DIGUEE_OVH_KMS_CERTIFICAT":  o.Certificat,
		"DIGUEE_OVH_KMS_CLE_CLIENT":  o.CleClient,
		"DIGUEE_OVH_KMS_AC":          o.AC,
	}
}

// Signature renvoie la signature RS256 que le vrai KMS donnerait au message.
func (o *OVH) Signature(message string) []byte {
	h := sha256.Sum256([]byte(message))
	s, err := rsa.SignPKCS1v15(nil, o.cle, crypto.SHA256, h[:])
	if err != nil {
		panic(err)
	}
	return s
}

func (o *OVH) servir(w http.ResponseWriter, r *http.Request) {
	var corps struct {
		Alg      *string `json:"alg"`
		IsDigest *bool   `json:"isdigest"`
		Message  []byte  `json:"message"`
	}
	brut, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(brut, &corps); err != nil {
		o.defaut("corps JSON invalide")
	}
	numero := string(corps.Message)
	rg, suite := o.debut(w, r, numero)
	if !suite {
		return
	}
	chemin := "/api/" + o.KMSID + "/v1/servicekey/" + o.CleID + "/sign"
	switch {
	case r.Method != http.MethodPost:
		o.defaut("méthode %s", r.Method)
	case r.URL.Path != chemin:
		o.defaut("chemin %s", r.URL.Path)
	case r.URL.Query().Get("format") != "raw":
		o.defaut("format %q", r.URL.Query().Get("format"))
	case r.Header.Get("Content-Type") != "application/json":
		o.defaut("Content-Type %q", r.Header.Get("Content-Type"))
	case r.TLS == nil || len(r.TLS.PeerCertificates) == 0:
		o.defaut("pas de certificat client")
	case corps.Alg == nil || *corps.Alg != "RS256":
		o.defaut("algorithme %v", corps.Alg)
	case corps.IsDigest == nil || *corps.IsDigest:
		o.defaut("isdigest absent ou vrai")
	case !strings.Contains(string(brut), `"message":"`+base64.StdEncoding.EncodeToString(corps.Message)+`"`):
		o.defaut("message non encodé en base64 standard")
	}
	var sig []byte
	if rg.Aleatoire {
		h := sha256.Sum256(corps.Message)
		var err error
		if sig, err = rsa.SignPSS(rand.Reader, o.cle, crypto.SHA256, h[:], nil); err != nil {
			panic(err)
		}
	} else {
		sig = o.Signature(numero)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"signature": base64.StdEncoding.EncodeToString(sig)})
}

// GCP imite la méthode macSign de l'API REST de Google Cloud KMS.
type GCP struct {
	commun
	Cle          string // nom de la version de clé
	JetonFichier string // fichier qui contient le jeton attendu
	jeton        string
	secret       []byte
}

// DemarrerGCP démarre un faux Google Cloud KMS. Il est arrêté à la fin du test.
func DemarrerGCP(t testing.TB) *GCP {
	t.Helper()
	dir := t.TempDir()
	g := &GCP{
		Cle:    "projects/diguee/locations/europe-west9/keyRings/diguee/cryptoKeys/empreinte/cryptoKeyVersions/1",
		jeton:  "ya29.jeton-de-test",
		secret: []byte("secret HMAC du faux KMS"),
	}
	g.t = t
	g.JetonFichier = filepath.Join(dir, "jeton")
	if err := os.WriteFile(g.JetonFichier, []byte(g.jeton+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	g.srv = httptest.NewUnstartedServer(http.HandlerFunc(g.servir))
	g.srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	g.srv.StartTLS()
	t.Cleanup(g.srv.Close)
	g.URL = g.srv.URL
	g.AC = ecrirePEM(t, dir, "ac.pem", "CERTIFICATE", g.srv.Certificate().Raw)
	return g
}

// Env renvoie les variables DIGUEE_* qui désignent ce faux KMS.
func (g *GCP) Env() map[string]string {
	return map[string]string{
		"DIGUEE_EMPREINTEUR":           "gcp",
		"DIGUEE_GCP_KMS_POINT_ACCES":   g.URL,
		"DIGUEE_GCP_KMS_CLE":           g.Cle,
		"DIGUEE_GCP_KMS_JETON_FICHIER": g.JetonFichier,
		"DIGUEE_GCP_KMS_AC":            g.AC,
	}
}

// Jeton renvoie le jeton attendu (pour vérifier qu'il n'apparaît dans aucune erreur).
func (g *GCP) Jeton() string { return g.jeton }

// Mac renvoie le HMAC que le faux KMS calcule.
func (g *GCP) Mac(message string) []byte {
	m := hmac.New(sha256.New, g.secret)
	m.Write([]byte(message))
	return m.Sum(nil)
}

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

func crc(b []byte) string { return strconv.FormatUint(uint64(crc32.Checksum(b, castagnoli)), 10) }

func (g *GCP) servir(w http.ResponseWriter, r *http.Request) {
	var corps struct {
		Data       []byte `json:"data"`
		DataCrc32c string `json:"dataCrc32c"`
	}
	if err := json.NewDecoder(r.Body).Decode(&corps); err != nil {
		g.defaut("corps JSON invalide")
	}
	rg, suite := g.debut(w, r, string(corps.Data))
	if !suite {
		return
	}
	switch {
	case r.Method != http.MethodPost:
		g.defaut("méthode %s", r.Method)
	case r.URL.Path != "/v1/"+g.Cle+":macSign":
		g.defaut("chemin %s", r.URL.Path)
	case r.Header.Get("Authorization") != "Bearer "+g.jeton:
		g.defaut("jeton absent ou faux")
	case corps.DataCrc32c != crc(corps.Data):
		g.defaut("dataCrc32c faux")
	}
	mac := g.Mac(string(corps.Data))
	somme := crc(mac)
	if rg.MauvaisCRC {
		somme = "1"
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"name": g.Cle, "mac": base64.StdEncoding.EncodeToString(mac),
		"macCrc32c": somme, "verifiedDataCrc32c": !rg.NonVerifiee, "protectionLevel": "HSM",
	})
}
