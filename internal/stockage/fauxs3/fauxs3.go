// Package fauxs3 imite un bucket S3 (Object Storage d'OVHcloud) pour les
// tests : il recalcule la signature AWS SigV4 de chaque requête, contrôle le
// SHA-256 du corps, et garde les objets en mémoire. Il connaît PUT, GET et
// DELETE d'un objet, ListObjectsV2 (par pages) et GetBucketVersioning.
package fauxs3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/2guntan/diguee/internal/stockage"
)

// Identifiants attendus par le faux serveur.
const (
	CleAcces     = "CLE-DE-TEST"
	CleSecrete   = "secret-de-test-1234567890"
	Region       = "gra"
	Compartiment = "compartiment"
)

// Serveur est le faux bucket.
type Serveur struct {
	mu      sync.Mutex
	objets  map[string]objet
	srv     *httptest.Server
	Requete []string // méthode et chemin de chaque requête reçue

	// Reglages, modifiables entre deux appels (sous Verrouiller).
	ParPage     int              // objets par page de liste (1000 si nul)
	Panne       bool             // répond 500 à tout
	Versionnage string           // "", "Enabled" ou "Suspended"
	Maintenant  func() time.Time // date de dépôt des objets (time.Now si nil)
}

type objet struct {
	contenu []byte
	modifie time.Time
}

// Nouveau démarre un faux bucket en HTTPS ; il s'arrête à la fin du test.
func Nouveau(t interface{ Cleanup(func()) }) *Serveur {
	s := &Serveur{objets: map[string]objet{}}
	s.srv = httptest.NewTLSServer(s)
	t.Cleanup(s.srv.Close)
	return s
}

// Client renvoie un client S3 réglé pour ce faux bucket.
func (s *Serveur) Client() *stockage.S3 {
	return &stockage.S3{PointAcces: s.srv.URL, Region: Region, Compartiment: Compartiment,
		CleAcces: CleAcces, CleSecrete: CleSecrete, Client: s.srv.Client()}
}

// Modifier applique f sous le verrou du serveur.
func (s *Serveur) Modifier(f func(*Serveur)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

// Objet renvoie une copie du contenu d'un objet.
func (s *Serveur) Objet(cle string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objets[cle]
	return bytes.Clone(o.contenu), ok
}

// Deposer range un objet directement (sans requête), daté de modifie.
func (s *Serveur) Deposer(cle string, contenu []byte, modifie time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objets[cle] = objet{contenu: bytes.Clone(contenu), modifie: modifie}
}

// Cles renvoie les clés présentes, triées.
func (s *Serveur) Cles() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var cles []string
	for k := range s.objets {
		cles = append(cles, k)
	}
	slices.Sort(cles)
	return cles
}

func erreur(w http.ResponseWriter, statut int, code string) {
	w.WriteHeader(statut)
	fmt.Fprintf(w, "<Error><Code>%s</Code><Message>%s</Message></Error>", code, CleSecrete)
}

func (s *Serveur) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Requete = append(s.Requete, r.Method+" "+r.URL.Path)
	if s.Panne {
		erreur(w, http.StatusInternalServerError, "InternalError")
		return
	}
	corps, _ := io.ReadAll(r.Body)
	hash := r.Header.Get("X-Amz-Content-Sha256")
	if h := sha256.Sum256(corps); hex.EncodeToString(h[:]) != hash {
		erreur(w, http.StatusBadRequest, "XAmzContentSHA256Mismatch")
		return
	}
	date, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if err != nil {
		erreur(w, http.StatusForbidden, "AccessDenied")
		return
	}
	copie := r.Clone(context.Background())
	copie.Header = http.Header{}
	for _, k := range []string{"Content-Type", "X-Amz-Date", "X-Amz-Content-Sha256"} {
		if v := r.Header.Get(k); v != "" {
			copie.Header.Set(k, v)
		}
	}
	copie.URL.Host = r.Host
	stockage.SignerV4(copie, hash, CleAcces, CleSecrete, Region, "s3", date)
	if copie.Header.Get("Authorization") != r.Header.Get("Authorization") {
		erreur(w, http.StatusForbidden, "SignatureDoesNotMatch")
		return
	}
	racine := "/" + Compartiment
	if r.URL.Path == racine {
		s.compartiment(w, r)
		return
	}
	cle, ok := strings.CutPrefix(r.URL.Path, racine+"/")
	if !ok {
		erreur(w, http.StatusNotFound, "NoSuchBucket")
		return
	}
	switch r.Method {
	case http.MethodPut:
		m := time.Now()
		if s.Maintenant != nil {
			m = s.Maintenant()
		}
		s.objets[cle] = objet{contenu: corps, modifie: m.UTC()}
	case http.MethodGet:
		o, ok := s.objets[cle]
		if !ok {
			erreur(w, http.StatusNotFound, "NoSuchKey")
			return
		}
		_, _ = w.Write(o.contenu)
	case http.MethodDelete:
		delete(s.objets, cle)
		w.WriteHeader(http.StatusNoContent)
	default:
		erreur(w, http.StatusMethodNotAllowed, "MethodNotAllowed")
	}
}

type contenuXML struct {
	Key          string
	LastModified string
	Size         int64
}

type listeXML struct {
	XMLName               xml.Name `xml:"ListBucketResult"`
	Xmlns                 string   `xml:"xmlns,attr"`
	Name                  string
	Prefix                string
	KeyCount              int
	IsTruncated           bool
	NextContinuationToken string `xml:",omitempty"`
	Contents              []contenuXML
}

func (s *Serveur) compartiment(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if r.Method != http.MethodGet {
		erreur(w, http.StatusMethodNotAllowed, "MethodNotAllowed")
		return
	}
	if _, ok := q["versioning"]; ok {
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
		if s.Versionnage != "" {
			fmt.Fprintf(w, "<Status>%s</Status>", s.Versionnage)
		}
		fmt.Fprint(w, "</VersioningConfiguration>")
		return
	}
	if q.Get("list-type") != "2" {
		erreur(w, http.StatusBadRequest, "InvalidArgument")
		return
	}
	prefixe := q.Get("prefix")
	var cles []string
	for k := range s.objets {
		if strings.HasPrefix(k, prefixe) {
			cles = append(cles, k)
		}
	}
	slices.Sort(cles)
	debut := 0
	if jeton := q.Get("continuation-token"); jeton != "" {
		// Jeton opaque, avec des caractères à encoder.
		n, err := strconv.Atoi(strings.TrimPrefix(jeton, "suite+/="))
		if err != nil {
			erreur(w, http.StatusBadRequest, "InvalidArgument")
			return
		}
		debut = n
	}
	parPage := s.ParPage
	if parPage <= 0 {
		parPage = 1000
	}
	fin := min(debut+parPage, len(cles))
	l := listeXML{Xmlns: "http://s3.amazonaws.com/doc/2006-03-01/", Name: Compartiment, Prefix: prefixe}
	for _, k := range cles[debut:fin] {
		o := s.objets[k]
		l.Contents = append(l.Contents, contenuXML{Key: k, LastModified: o.modifie.UTC().Format("2006-01-02T15:04:05.000Z"), Size: int64(len(o.contenu))})
	}
	l.KeyCount = len(l.Contents)
	if fin < len(cles) {
		l.IsTruncated = true
		l.NextContinuationToken = "suite+/=" + strconv.Itoa(fin)
	}
	w.Header().Set("Content-Type", "application/xml")
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(l)
}
