package empreinte

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// algorithmeOVH est l'unique algorithme de signature utilisé. Il n'est pas
// configurable : RS256 (RSASSA-PKCS1-v1_5 avec SHA-256) est déterministe,
// alors que PS256 (RSASSA-PSS) tire un sel aléatoire à chaque signature et
// casserait la détection des doublons (spec §5.4).
const algorithmeOVH = "RS256"

// tailleMinSignature est la taille minimale d'une signature RSA acceptée :
// 256 octets, soit une clé d'au moins 2048 bits.
const tailleMinSignature = 256

// OVH calcule l'empreinte avec le KMS d'OVHcloud :
//
//	signature = KMS.sign(clé, alg = RS256, message = numéro normalisé)
//	empreinte = SHA-256(signature)
//
// La clé RSA est créée dans le KMS et ne peut pas en sortir ; le serveur
// n'utilise que l'opération de signature (permission kms:serviceKey/sign).
//
// L'appel est fait directement sur l'API REST du KMS
// (POST /api/{okmsId}/v1/servicekey/{keyId}/sign?format=raw), avec un
// certificat client (mTLS) : voir le message du commit du ticket #5 pour le
// choix d'un client HTTP minimal plutôt que du SDK okms-sdk-go.
type OVH struct {
	adresse string // URL complète de l'opération sign
	client  *clientKMS
}

// OptionsOVH configure NouveauOVH.
type OptionsOVH struct {
	PointAcces string        // https://eu-west-par.okms.ovh.net par exemple
	KMSID      string        // identifiant du KMS (okmsId)
	CleID      string        // identifiant de la clé RSA (keyId)
	TLS        *tls.Config   // certificat client et, au besoin, autorités
	Delai      time.Duration // délai d'attente d'un appel ; 5 s par défaut
}

// NouveauOVH renvoie l'empreinteur du KMS d'OVHcloud. Il ne fait aucun appel :
// c'est VerifierDeterminisme, au démarrage, qui le met à l'épreuve.
func NouveauOVH(o OptionsOVH) (*OVH, error) {
	base, err := url.Parse(strings.TrimRight(o.PointAcces, "/"))
	if err != nil || base.Scheme != "https" || base.Host == "" {
		return nil, fmt.Errorf("KMS OVH : point d'accès https:// attendu")
	}
	if o.KMSID == "" || o.CleID == "" {
		return nil, fmt.Errorf("KMS OVH : identifiants du KMS et de la clé obligatoires")
	}
	base.Path = "/api/" + url.PathEscape(o.KMSID) + "/v1/servicekey/" + url.PathEscape(o.CleID) + "/sign"
	base.RawQuery = url.Values{"format": {"raw"}}.Encode()
	return &OVH{adresse: base.String(), client: nouveauClientKMS("KMS OVH", o.TLS, o.Delai)}, nil
}

// requeteSignOVH est le corps de l'opération sign (SignRequest du SDK) :
// le message est encodé en base64 par encoding/json.
type requeteSignOVH struct {
	Alg      string `json:"alg"`
	IsDigest bool   `json:"isdigest"`
	Message  []byte `json:"message"`
}

type reponseSignOVH struct {
	Signature string `json:"signature"`
}

// Calculer renvoie SHA-256(signature RS256 du numéro).
func (o *OVH) Calculer(ctx context.Context, numero string) (Empreinte, error) {
	if !valide(numero) {
		return Empreinte{}, ErrFormat
	}
	var rep reponseSignOVH
	req := requeteSignOVH{Alg: algorithmeOVH, IsDigest: false, Message: []byte(numero)}
	if err := o.client.poster(ctx, o.adresse, http.Header{}, req, &rep, numero); err != nil {
		return Empreinte{}, err
	}
	sig, err := decoderBase64(rep.Signature)
	if err != nil {
		return Empreinte{}, &erreurKMS{nom: o.client.nom, detail: "signature illisible (base64 attendu)"}
	}
	if len(sig) < tailleMinSignature {
		return Empreinte{}, &erreurKMS{nom: o.client.nom, detail: fmt.Sprintf(
			"signature de %d octets : une clé RSA d'au moins 2048 bits est attendue", len(sig))}
	}
	return sha256.Sum256(sig), nil
}
