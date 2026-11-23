package empreinte

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"hash/crc32"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// GCP est la solution de repli de l'ADR 0006 : Google Cloud KMS, région
// Paris (europe-west9), qui calcule nativement un HMAC-SHA-256 avec une clé
// non exportable (méthode macSign) :
//
//	mac       = KMS.macSign(version de clé HMAC_SHA256, données = numéro normalisé)
//	empreinte = SHA-256(mac)
//
// Le SHA-256 final n'ajoute pas de sécurité, mais garde la même forme que
// pour OVHcloud (SHA-256 de la sortie du KMS) et une taille fixe.
//
// Seule l'API REST est utilisée, en bibliothèque standard, sans le SDK Google
// Cloud. Le jeton d'accès OAuth 2 est lu dans un fichier à chaque appel : il
// expire au bout d'une heure et doit être renouvelé par un processus
// extérieur (voir « À faire par le porteur », §1.1). Le compte de service n'a
// que le rôle « Cloud KMS CryptoKey Signer » (cloudkms.cryptoKeyVersions.useToSign)
// sur cette clé.
//
// Attention : changer d'empreinteur change toutes les empreintes. La bascule
// n'est possible qu'avant l'ouverture, ou en acceptant de perdre la
// détection des doublons avec les participations déjà enregistrées.
type GCP struct {
	adresse      string
	jetonFichier string
	client       *clientKMS
}

// OptionsGCP configure NouveauGCP.
type OptionsGCP struct {
	PointAcces   string        // https://cloudkms.googleapis.com
	Cle          string        // projects/…/cryptoKeyVersions/N
	JetonFichier string        // fichier qui contient le jeton d'accès
	TLS          *tls.Config   // autorités, au besoin
	Delai        time.Duration // délai d'attente d'un appel ; 5 s par défaut
}

// NouveauGCP renvoie l'empreinteur de Google Cloud KMS. Il ne fait aucun appel.
func NouveauGCP(o OptionsGCP) (*GCP, error) {
	base, err := url.Parse(strings.TrimRight(o.PointAcces, "/"))
	if err != nil || base.Scheme != "https" || base.Host == "" {
		return nil, fmt.Errorf("KMS Google Cloud : point d'accès https:// attendu")
	}
	if o.Cle == "" || o.JetonFichier == "" {
		return nil, fmt.Errorf("KMS Google Cloud : nom de la version de clé et fichier du jeton obligatoires")
	}
	// Le nom de la clé ne contient que des segments séparés par « / » ; le
	// « : » de la méthode n'est pas échappé.
	segments := strings.Split(o.Cle, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	base.Path = "/v1/" + strings.Join(segments, "/") + ":macSign"
	return &GCP{adresse: base.String(), jetonFichier: o.JetonFichier,
		client: nouveauClientKMS("KMS Google Cloud", o.TLS, o.Delai)}, nil
}

type requeteMacSign struct {
	Data       []byte `json:"data"`
	DataCrc32c string `json:"dataCrc32c"`
}

type reponseMacSign struct {
	Mac                string `json:"mac"`
	MacCrc32c          string `json:"macCrc32c"`
	VerifiedDataCrc32c bool   `json:"verifiedDataCrc32c"`
}

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// Calculer renvoie SHA-256(HMAC-SHA-256 du numéro calculé par le KMS).
// Les sommes CRC32C recommandées par Google sont vérifiées dans les deux sens.
func (g *GCP) Calculer(ctx context.Context, numero string) (Empreinte, error) {
	if !valide(numero) {
		return Empreinte{}, ErrFormat
	}
	echec := func(detail string) error { return &erreurKMS{nom: g.client.nom, detail: detail} }
	jeton, err := os.ReadFile(g.jetonFichier)
	if err != nil {
		return Empreinte{}, echec("lecture du fichier du jeton d'accès impossible")
	}
	j := strings.TrimSpace(string(jeton))
	if j == "" || strings.ContainsAny(j, "\r\n") {
		return Empreinte{}, echec("fichier du jeton d'accès vide ou mal formé")
	}
	donnees := []byte(numero)
	req := requeteMacSign{Data: donnees, DataCrc32c: strconv.FormatUint(uint64(crc32.Checksum(donnees, castagnoli)), 10)}
	var rep reponseMacSign
	if err := g.client.poster(ctx, g.adresse, http.Header{"Authorization": {"Bearer " + j}}, req, &rep, numero); err != nil {
		return Empreinte{}, err
	}
	if !rep.VerifiedDataCrc32c {
		return Empreinte{}, echec("le KMS n'a pas confirmé la somme CRC32C des données")
	}
	mac, err := decoderBase64(rep.Mac)
	if err != nil {
		return Empreinte{}, echec("HMAC illisible (base64 attendu)")
	}
	if len(mac) != sha256.Size {
		return Empreinte{}, echec(fmt.Sprintf("HMAC de %d octets : une clé HMAC_SHA256 est attendue", len(mac)))
	}
	if rep.MacCrc32c != strconv.FormatUint(uint64(crc32.Checksum(mac, castagnoli)), 10) {
		return Empreinte{}, echec("somme CRC32C du HMAC incorrecte")
	}
	return sha256.Sum256(mac), nil
}
