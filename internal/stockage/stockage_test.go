package stockage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/integration"
)

// Clés de l'exemple de la documentation d'AWS (« Signature Calculations for
// the Authorization Header: Transferring Payload in a Single Chunk »).
const (
	cleExemple    = "AKIAIOSFODNN7EXAMPLE"
	secretExemple = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
)

var dateExemple = time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)

func signatureDe(t *testing.T, req *http.Request) string {
	t.Helper()
	a := req.Header.Get("Authorization")
	i := strings.Index(a, "Signature=")
	if i < 0 {
		t.Fatalf("Authorization : %q", a)
	}
	return a[i+len("Signature="):]
}

// Vecteurs de la documentation d'AWS : GET Object (avec Range) et PUT Object
// (clé à encoder, en-têtes Date et x-amz-storage-class).
func TestSignatureV4VecteursAWS(t *testing.T) {
	get, _ := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	get.Header.Set("Range", "bytes=0-9")
	SignerV4(get, hashVide, cleExemple, secretExemple, "us-east-1", "s3", dateExemple)
	canon, _ := requeteCanonique(get, hashVide)
	if h := sha256.Sum256([]byte(canon)); hex.EncodeToString(h[:]) != "7344ae5b7ee6c3e7e6b0fe0640412a37625d1fbfff95c48bbb2dc43964946972" {
		t.Errorf("requête canonique GET :\n%s", canon)
	}
	if s := signatureDe(t, get); s != "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41" {
		t.Errorf("signature GET %s", s)
	}
	if a := get.Header.Get("Authorization"); !strings.HasPrefix(a, "AWS4-HMAC-SHA256 Credential="+cleExemple+"/20130524/us-east-1/s3/aws4_request, SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, Signature=") {
		t.Errorf("Authorization %q", a)
	}

	corps := "Welcome to Amazon S3."
	h := sha256.Sum256([]byte(corps))
	put, _ := http.NewRequest(http.MethodPut, "https://examplebucket.s3.amazonaws.com/test$file.text", strings.NewReader(corps))
	put.Header.Set("Date", "Fri, 24 May 2013 00:00:00 GMT")
	put.Header.Set("X-Amz-Storage-Class", "REDUCED_REDUNDANCY")
	SignerV4(put, hex.EncodeToString(h[:]), cleExemple, secretExemple, "us-east-1", "s3", dateExemple)
	if s := signatureDe(t, put); s != "98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd" {
		canon, _ := requeteCanonique(put, hex.EncodeToString(h[:]))
		t.Errorf("signature PUT %s\n%s", s, canon)
	}
}

// fauxS3 imite un bucket : il recalcule la signature de chaque requête avec
// la clé secrète qu'il connaît, contrôle le SHA-256 du corps, et garde les
// objets en mémoire.
type fauxS3 struct {
	t        *testing.T
	mu       sync.Mutex
	objets   map[string][]byte
	types    map[string]string
	requetes int
	alterer  bool // renvoie un contenu modifié
}

func (f *fauxS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requetes++
	corps, _ := io.ReadAll(r.Body)
	hash := r.Header.Get("X-Amz-Content-Sha256")
	if h := sha256.Sum256(corps); hex.EncodeToString(h[:]) != hash {
		http.Error(w, "<Error><Code>XAmzContentSHA256Mismatch</Code></Error>", http.StatusBadRequest)
		return
	}
	date, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if err != nil {
		http.Error(w, "<Error><Code>AccessDenied</Code></Error>", http.StatusForbidden)
		return
	}
	recue := r.Header.Get("Authorization")
	copie := r.Clone(context.Background())
	copie.Header = http.Header{}
	for _, k := range []string{"Content-Type", "X-Amz-Date", "X-Amz-Content-Sha256"} {
		if v := r.Header.Get(k); v != "" {
			copie.Header.Set(k, v)
		}
	}
	copie.URL.Host = r.Host
	SignerV4(copie, hash, "CLE-DE-TEST", "secret-de-test-1234567890", "gra", "s3", date)
	if copie.Header.Get("Authorization") != recue {
		http.Error(w, "<Error><Code>SignatureDoesNotMatch</Code><Message>secret-de-test</Message></Error>", http.StatusForbidden)
		return
	}
	cle := strings.TrimPrefix(r.URL.Path, "/compartiment/")
	switch r.Method {
	case http.MethodPut:
		f.objets[cle] = corps
		f.types[cle] = r.Header.Get("Content-Type")
	case http.MethodGet:
		b, ok := f.objets[cle]
		if !ok {
			http.Error(w, "<Error><Code>NoSuchKey</Code></Error>", http.StatusNotFound)
			return
		}
		if f.alterer {
			b = append(bytes.Clone(b), '!')
		}
		_, _ = w.Write(b)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func TestS3ContreUnFauxServeur(t *testing.T) {
	f := &fauxS3{t: t, objets: map[string][]byte{}, types: map[string]string{}}
	srv := httptest.NewTLSServer(f)
	defer srv.Close()
	s := &S3{PointAcces: srv.URL, Region: "gra", Compartiment: "compartiment", CleAcces: "CLE-DE-TEST",
		CleSecrete: "secret-de-test-1234567890", Client: srv.Client()}
	ctx := context.Background()
	contenu := []byte("%PDF-1.7 contenu de test")
	e, err := s.Deposer(ctx, "application/pdf", contenu)
	if err != nil {
		t.Fatal(err)
	}
	if e != sha256.Sum256(contenu) {
		t.Error("empreinte")
	}
	cle := "archives/" + e.String()
	if !bytes.Equal(f.objets[cle], contenu) || f.types[cle] != "application/pdf" {
		t.Fatalf("objets %v", f.objets)
	}
	if lu, err := s.Lire(ctx, e); err != nil || !bytes.Equal(lu, contenu) {
		t.Fatalf("lecture %q, %v", lu, err)
	}
	if _, err := s.Lire(ctx, Calculer([]byte("autre"))); !errors.Is(err, ErrIntrouvable) {
		t.Errorf("objet absent : %v", err)
	}
	f.alterer = true
	if _, err := s.Lire(ctx, e); !errors.Is(err, ErrAltere) {
		t.Errorf("objet altéré : %v", err)
	}
	f.alterer = false

	// Mauvaise clé secrète : refus, et l'erreur ne garde que le statut et le
	// code S3, jamais le message du serveur ni la clé.
	mauvais := *s
	mauvais.CleSecrete = "une-autre-cle-secrete"
	_, err = mauvais.Deposer(ctx, "application/pdf", contenu)
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "SignatureDoesNotMatch") ||
		strings.Contains(err.Error(), "secret") {
		t.Errorf("erreur %v", err)
	}
}

func TestDossier(t *testing.T) {
	d, err := NouveauDossier(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	contenu := []byte("\x89PNG\r\n\x1a\nimage")
	e, err := d.Deposer(ctx, "image/png", contenu)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Deposer(ctx, "image/png", contenu); err != nil {
		t.Fatal(err)
	}
	if lu, err := d.Lire(ctx, e); err != nil || !bytes.Equal(lu, contenu) {
		t.Fatalf("%q %v", lu, err)
	}
	if err := os.WriteFile(filepath.Join(d.Chemin, "archives", e.String()), []byte("modifié"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Lire(ctx, e); !errors.Is(err, ErrAltere) {
		t.Errorf("altéré : %v", err)
	}
	if _, err := d.Lire(ctx, Calculer(nil)); !errors.Is(err, ErrIntrouvable) {
		t.Errorf("absent : %v", err)
	}
}

func TestLireEmpreinte(t *testing.T) {
	e := Calculer([]byte("x"))
	if lu, err := LireEmpreinte(e.String()); err != nil || lu != e {
		t.Error(err)
	}
	for _, s := range []string{"", strings.ToUpper(e.String()), e.String()[:62], "../" + e.String()[3:]} {
		if _, err := LireEmpreinte(s); err == nil {
			t.Errorf("%q accepté", s)
		}
	}
}

func TestConstruireDeclareLeMode(t *testing.T) {
	cfg, err := config.Charger(func(k string) string {
		return map[string]string{"DIGUEE_POSTGRES_URL": "postgres://x", "DIGUEE_STOCKAGE_DOSSIER": t.TempDir()}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &integration.Registre{}
	st, err := Construire(cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.(*Dossier); !ok || len(r.Fausses()) != 1 || r.Fausses()[0].Nom != "stockage" {
		t.Errorf("%T %v", st, r.Fausses())
	}
	cfg, err = config.Charger(func(k string) string {
		return map[string]string{"DIGUEE_POSTGRES_URL": "postgres://x", "DIGUEE_STOCKAGE": "ovh",
			"DIGUEE_OVH_S3_POINT_ACCES": "https://s3.gra.io.cloud.ovh.net", "DIGUEE_OVH_S3_REGION": "gra",
			"DIGUEE_OVH_S3_COMPARTIMENT": "diguee-archives", "DIGUEE_OVH_S3_CLE_ACCES": "0123456789abcdef",
			"DIGUEE_OVH_S3_CLE_SECRETE": "0123456789abcdef0123456789abcdef"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	r = &integration.Registre{}
	st, err = Construire(cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := st.(*S3); !ok || len(r.Fausses()) != 0 || s.TailleMax != 10<<20 || s.Compartiment != "diguee-archives" {
		t.Errorf("%T %v", st, r.Fausses())
	}
	if _, err := config.Charger(func(k string) string {
		return map[string]string{"DIGUEE_POSTGRES_URL": "postgres://x", "DIGUEE_STOCKAGE": "ovh"}[k]
	}); err == nil || !strings.Contains(err.Error(), "DIGUEE_OVH_S3_CLE_SECRETE est obligatoire") {
		t.Errorf("réglages manquants : %v", err)
	}
}
