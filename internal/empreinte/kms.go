package empreinte

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// Ce fichier réunit ce qui est commun aux deux KMS (OVHcloud et Google
// Cloud) : un client HTTPS minimal, écrit avec la bibliothèque standard, et
// des erreurs qui ne citent jamais le numéro.
//
// Règles :
//   - le corps des requêtes (qui contient le numéro, en base64) n'est jamais
//     journalisé ni recopié dans une erreur ;
//   - le corps d'une réponse d'erreur n'est repris qu'après masquage du numéro
//     sous toutes ses formes et de toute suite de 9 chiffres ou plus ;
//   - pas de mandataire (HTTPS_PROXY est ignoré) ni de nouvel essai : un appel
//     qui échoue renvoie une erreur, et la page « service indisponible »
//     s'affiche.

// ErrKMS est la cause commune de tous les échecs d'appel à un KMS.
var ErrKMS = errors.New("KMS indisponible")

// tailleMaxReponse borne la lecture d'une réponse du KMS.
const tailleMaxReponse = 64 << 10

// delaiParDefaut est le délai d'attente d'un appel si la configuration n'en
// donne pas.
const delaiParDefaut = 5 * time.Second

// clientKMS appelle l'API JSON d'un KMS.
type clientKMS struct {
	nom  string // « KMS OVH » ou « KMS Google Cloud », pour les erreurs
	http *http.Client
}

func nouveauClientKMS(nom string, tlsCfg *tls.Config, delai time.Duration) *clientKMS {
	if delai <= 0 {
		delai = delaiParDefaut
	}
	if tlsCfg == nil {
		tlsCfg = &tls.Config{}
	}
	tlsCfg = tlsCfg.Clone()
	if tlsCfg.MinVersion < tls.VersionTLS12 {
		tlsCfg.MinVersion = tls.VersionTLS12
	}
	return &clientKMS{nom: nom, http: &http.Client{
		Timeout: delai,
		Transport: &http.Transport{
			Proxy:               nil,
			TLSClientConfig:     tlsCfg,
			TLSHandshakeTimeout: delai,
			MaxIdleConnsPerHost: 8,
			IdleConnTimeout:     90 * time.Second,
		},
		// Le KMS ne redirige jamais : une redirection est refusée.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// erreurKMS est une erreur d'appel à un KMS. Son texte ne contient jamais le
// numéro (voir masquer).
type erreurKMS struct {
	nom    string
	detail string
}

func (e *erreurKMS) Error() string { return e.nom + " : " + e.detail }
func (e *erreurKMS) Unwrap() error { return ErrKMS }

// poster envoie corps (JSON) en POST à adresse et décode la réponse 200 dans
// reponse. numero ne sert qu'à masquer le numéro dans les éventuels messages
// d'erreur.
func (c *clientKMS) poster(ctx context.Context, adresse string, entetes http.Header, corps, reponse any, numero string) error {
	echec := func(format string, args ...any) error {
		return &erreurKMS{nom: c.nom, detail: masquer(fmt.Sprintf(format, args...), numero)}
	}
	charge, err := json.Marshal(corps)
	if err != nil {
		return echec("encodage de la requête impossible")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, adresse, bytes.NewReader(charge))
	if err != nil {
		return echec("requête impossible")
	}
	for k, vs := range entetes {
		req.Header[k] = vs
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return echec("appel impossible : %s", causeReseau(err))
	}
	defer res.Body.Close()
	lu, err := io.ReadAll(io.LimitReader(res.Body, tailleMaxReponse+1))
	if err != nil {
		return echec("réponse illisible (HTTP %d) : %s", res.StatusCode, causeReseau(err))
	}
	if len(lu) > tailleMaxReponse {
		return echec("réponse trop longue (HTTP %d)", res.StatusCode)
	}
	if res.StatusCode != http.StatusOK {
		return echec("réponse HTTP %d%s", res.StatusCode, resumeErreur(lu))
	}
	if err := json.Unmarshal(lu, reponse); err != nil {
		return echec("réponse JSON invalide")
	}
	return nil
}

// causeReseau décrit une erreur de transport sans l'adresse complète de la
// requête (url.Error la recopie) : seuls la cause et le dépassement de délai
// sont gardés.
func causeReseau(err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return "délai d'attente dépassé"
	}
	if errors.Is(err, context.Canceled) {
		return "requête annulée"
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return "délai d'attente dépassé"
		}
		err = ue.Err
	}
	return err.Error()
}

// resumeErreur extrait d'une réponse d'erreur JSON les champs utiles au
// diagnostic (code, identifiant de requête, messages), sans jamais recopier
// le corps brut.
func resumeErreur(corps []byte) string {
	var r struct {
		// OVHcloud (ErrorResponse du SDK okms).
		ErrorCode *int64   `json:"error_code"`
		ErrorID   *string  `json:"error_id"`
		Errors    []string `json:"errors"`
		RequestID *string  `json:"request_id"`
		// Google Cloud.
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if json.Unmarshal(corps, &r) != nil {
		return ""
	}
	var parts []string
	if r.ErrorCode != nil {
		parts = append(parts, fmt.Sprintf("code %d", *r.ErrorCode))
	}
	if r.ErrorID != nil {
		parts = append(parts, "erreur "+tronquer(*r.ErrorID))
	}
	if r.RequestID != nil {
		parts = append(parts, "requête "+tronquer(*r.RequestID))
	}
	for _, m := range r.Errors {
		parts = append(parts, tronquer(m))
	}
	if r.Error != nil {
		parts = append(parts, tronquer(r.Error.Status), tronquer(r.Error.Message))
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, " ; ") + ")"
}

func tronquer(s string) string {
	if r := []rune(s); len(r) > 200 {
		return string(r[:200]) + "…"
	}
	return s
}

// suiteDeChiffres repère tout ce qui ressemble à un numéro (9 chiffres ou
// plus, éventuellement séparés par des espaces, points ou tirets).
var suiteDeChiffres = regexp.MustCompile(`\d(?:[ .\-]?\d){8,}`)

// masquer retire d'un message le numéro sous ses formes probables (texte,
// base64 standard et URL) et toute suite de 9 chiffres ou plus.
func masquer(message, numero string) string {
	if numero != "" {
		for _, forme := range []string{
			numero,
			base64.StdEncoding.EncodeToString([]byte(numero)),
			base64.RawStdEncoding.EncodeToString([]byte(numero)),
			base64.URLEncoding.EncodeToString([]byte(numero)),
			base64.RawURLEncoding.EncodeToString([]byte(numero)),
		} {
			message = strings.ReplaceAll(message, forme, "[masqué]")
		}
	}
	return suiteDeChiffres.ReplaceAllString(message, "[masqué]")
}

// decoderBase64 accepte les deux alphabets base64, avec ou sans bourrage.
func decoderBase64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("base64 invalide")
}

// autorites charge un fichier PEM d'autorités de certification. Un chemin
// vide désigne les autorités du système.
func autorites(chemin string) (*x509.CertPool, error) {
	if chemin == "" {
		return nil, nil
	}
	pem, err := os.ReadFile(chemin)
	if err != nil {
		return nil, fmt.Errorf("lecture des autorités de certification : %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%s : aucun certificat PEM", chemin)
	}
	return pool, nil
}
