package stockage

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// S3 range les objets dans un bucket compatible S3, comme l'Object Storage
// d'OVHcloud (https://s3.<région>.io.cloud.ovh.net). Les requêtes sont
// adressées par chemin (<point d'accès>/<bucket>/<clé>) et signées en AWS
// Signature Version 4 (en-tête Authorization), corps compris : son SHA-256
// est aussi le nom de l'objet. La clé secrète n'est jamais écrite dans une
// erreur ni dans un journal.
type S3 struct {
	PointAcces   string // https://s3.gra.io.cloud.ovh.net, sans « / » final
	Region       string // gra, sbg… : la région de la signature
	Compartiment string // nom du bucket
	CleAcces     string
	CleSecrete   string
	Client       *http.Client     // http.DefaultClient si nil
	Maintenant   func() time.Time // time.Now si nil
	// TailleMax borne la lecture d'un objet (64 Mo si nul).
	TailleMax int64
}

func (s *S3) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return http.DefaultClient
}

func (s *S3) maintenant() time.Time {
	if s.Maintenant != nil {
		return s.Maintenant()
	}
	return time.Now()
}

func (s *S3) adresse(e Empreinte) string {
	return strings.TrimRight(s.PointAcces, "/") + "/" + s.Compartiment + "/" + Cle(e)
}

// Deposer implémente Stockage : PUT de l'objet. Le corps est signé (son
// SHA-256 dans x-amz-content-sha256) : le serveur refuse un contenu altéré
// en route.
func (s *S3) Deposer(ctx context.Context, typeContenu string, contenu []byte) (Empreinte, error) {
	e := Calculer(contenu)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.adresse(e), bytes.NewReader(contenu))
	if err != nil {
		return e, fmt.Errorf("stockage S3 : %w", err)
	}
	if typeContenu != "" {
		req.Header.Set("Content-Type", typeContenu)
	}
	s.signer(req, e.String())
	res, err := s.client().Do(req)
	if err != nil {
		return e, fmt.Errorf("stockage S3 : dépôt : %w", nettoyer(err))
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return e, erreurS3("dépôt", res)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
	return e, nil
}

// Lire implémente Stockage : GET de l'objet, puis contrôle du SHA-256.
func (s *S3) Lire(ctx context.Context, e Empreinte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.adresse(e), nil)
	if err != nil {
		return nil, fmt.Errorf("stockage S3 : %w", err)
	}
	s.signer(req, hashVide)
	res, err := s.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("stockage S3 : lecture : %w", nettoyer(err))
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, ErrIntrouvable
	default:
		return nil, erreurS3("lecture", res)
	}
	max := s.TailleMax
	if max <= 0 {
		max = 64 << 20
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, max+1))
	if err != nil {
		return nil, fmt.Errorf("stockage S3 : lecture : %w", err)
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("stockage S3 : objet de plus de %d octets", max)
	}
	return verifier(e, b)
}

// nettoyer retire l'adresse de l'erreur réseau (elle ne contient pas de
// secret, mais reste longue et inutile dans le journal).
func nettoyer(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// erreurS3 garde le statut HTTP et le code d'erreur S3 (par exemple
// SignatureDoesNotMatch), jamais le reste de la réponse.
func erreurS3(quoi string, res *http.Response) error {
	var corps struct {
		Code string `xml:"Code"`
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	_ = xml.Unmarshal(b, &corps)
	code := strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, corps.Code)
	if len(code) > 64 {
		code = code[:64]
	}
	if code != "" {
		return fmt.Errorf("stockage S3 : %s : statut %d (%s)", quoi, res.StatusCode, code)
	}
	return fmt.Errorf("stockage S3 : %s : statut %d", quoi, res.StatusCode)
}

// hashVide est le SHA-256 d'un corps vide.
const hashVide = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func (s *S3) signer(req *http.Request, hashCorps string) {
	SignerV4(req, hashCorps, s.CleAcces, s.CleSecrete, s.Region, "s3", s.maintenant())
}

// SignerV4 signe la requête en AWS Signature Version 4 : il pose
// x-amz-date, x-amz-content-sha256 (hashCorps, le SHA-256 du corps en
// hexadécimal) et Authorization. Tous les en-têtes déjà présents, plus
// Host, sont signés.
func SignerV4(req *http.Request, hashCorps, cleAcces, cleSecrete, region, service string, t time.Time) {
	amz := t.UTC().Format("20060102T150405Z")
	req.Header.Set("X-Amz-Date", amz)
	req.Header.Set("X-Amz-Content-Sha256", hashCorps)
	canonique, signes := requeteCanonique(req, hashCorps)
	portee := amz[:8] + "/" + region + "/" + service + "/aws4_request"
	signature := signatureV4(canonique, amz, portee, cleSecrete, region, service)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+cleAcces+"/"+portee+
		", SignedHeaders="+signes+", Signature="+signature)
}

// requeteCanonique construit la requête canonique de SigV4 et la liste des
// en-têtes signés.
func requeteCanonique(req *http.Request, hashCorps string) (canonique, signes string) {
	hote := req.Host
	if hote == "" {
		hote = req.URL.Host
	}
	entetes := map[string]string{"host": hote}
	for k, vs := range req.Header {
		k = strings.ToLower(k)
		if k == "authorization" {
			continue
		}
		var nettoyees []string
		for _, v := range vs {
			nettoyees = append(nettoyees, strings.Join(strings.Fields(v), " "))
		}
		entetes[k] = strings.Join(nettoyees, ",")
	}
	noms := make([]string, 0, len(entetes))
	for k := range entetes {
		noms = append(noms, k)
	}
	slices.Sort(noms)
	var b strings.Builder
	b.WriteString(req.Method + "\n")
	chemin := req.URL.Path
	if chemin == "" {
		chemin = "/"
	}
	b.WriteString(encoderURI(chemin, false) + "\n")
	b.WriteString(requeteCanoniqueQuery(req.URL.Query()) + "\n")
	for _, k := range noms {
		b.WriteString(k + ":" + entetes[k] + "\n")
	}
	signes = strings.Join(noms, ";")
	b.WriteString("\n" + signes + "\n" + hashCorps)
	return b.String(), signes
}

func requeteCanoniqueQuery(q url.Values) string {
	var paires []string
	for k, vs := range q {
		for _, v := range vs {
			paires = append(paires, encoderURI(k, true)+"="+encoderURI(v, true))
		}
	}
	slices.Sort(paires)
	return strings.Join(paires, "&")
}

// encoderURI encode comme l'exige SigV4 : seuls A-Z, a-z, 0-9, « - », « . »,
// « _ » et « ~ » restent tels quels ; « / » aussi dans un chemin.
func encoderURI(s string, barre bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '.', c == '_', c == '~':
			b.WriteByte(c)
		case c == '/' && !barre:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func hmacSHA256(cle []byte, donnee string) []byte {
	m := hmac.New(sha256.New, cle)
	m.Write([]byte(donnee))
	return m.Sum(nil)
}

func signatureV4(canonique, amz, portee, cleSecrete, region, service string) string {
	h := sha256.Sum256([]byte(canonique))
	aSigner := "AWS4-HMAC-SHA256\n" + amz + "\n" + portee + "\n" + hex.EncodeToString(h[:])
	k := hmacSHA256([]byte("AWS4"+cleSecrete), amz[:8])
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, service)
	k = hmacSHA256(k, "aws4_request")
	return hex.EncodeToString(hmacSHA256(k, aSigner))
}
