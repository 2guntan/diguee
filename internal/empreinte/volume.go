package empreinte

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Clé du volume chiffré (ticket #7, spec §11) : les données de PostgreSQL
// sont sur un volume LUKS dont la clé (64 octets aléatoires) n'est jamais
// écrite en clair sur le disque. Elle est gardée chiffrée par une clé
// symétrique du KMS d'OVHcloud (chiffrement d'enveloppe) et déchiffrée au
// démarrage de la machine par « diguee cle-volume dechiffrer », qui la passe
// directement à cryptsetup par un tube.
//
// L'appel est celui de l'API REST du KMS, avec le même client mTLS que
// l'empreinte (kms.go) :
//
//	POST /api/{okmsId}/v1/servicekey/{keyId}/encrypt  {"plaintext": base64, "context": …} → {"ciphertext": JWE}
//	POST /api/{okmsId}/v1/servicekey/{keyId}/decrypt  {"ciphertext": JWE, "context": …}  → {"plaintext": base64}
//
// (EncryptRequest et DecryptRequest du SDK ovh/okms-sdk-go v0.5.5.) La clé
// du KMS est une clé de service symétrique (AES-256, opérations encrypt et
// decrypt), distincte de la clé RSA de l'empreinte, avec son propre
// certificat d'accès, qui n'a que le droit de déchiffrer une fois le volume
// créé.

// ContexteVolume est la donnée authentifiée associée au chiffrement : un
// texte chiffré pour un autre usage de la même clé n'est pas accepté.
const ContexteVolume = "diguee:volume-luks:v1"

// TailleCleVolume est la taille de la clé du volume, en octets.
const TailleCleVolume = 64

// CleVolume chiffre et déchiffre la clé du volume avec le KMS d'OVHcloud.
type CleVolume struct {
	chiffrer, dechiffrer string // URL complètes des deux opérations
	client               *clientKMS
}

// OptionsCleVolume configure NouvelleCleVolume.
type OptionsCleVolume struct {
	PointAcces string        // https://eu-west-par.okms.ovh.net par exemple
	KMSID      string        // identifiant du KMS (okmsId)
	CleID      string        // identifiant de la clé symétrique (keyId)
	TLS        *tls.Config   // certificat client et, au besoin, autorités
	Delai      time.Duration // délai d'attente d'un appel ; 5 s par défaut
}

// NouvelleCleVolume prépare le client ; il ne fait aucun appel.
func NouvelleCleVolume(o OptionsCleVolume) (*CleVolume, error) {
	base, err := url.Parse(strings.TrimRight(o.PointAcces, "/"))
	if err != nil || base.Scheme != "https" || base.Host == "" {
		return nil, fmt.Errorf("KMS OVH : point d'accès https:// attendu")
	}
	if o.KMSID == "" || o.CleID == "" {
		return nil, fmt.Errorf("KMS OVH : identifiants du KMS et de la clé du volume obligatoires")
	}
	racine := base.String() + "/api/" + url.PathEscape(o.KMSID) + "/v1/servicekey/" + url.PathEscape(o.CleID)
	return &CleVolume{
		chiffrer: racine + "/encrypt", dechiffrer: racine + "/decrypt",
		client: nouveauClientKMS("KMS OVH (clé du volume)", o.TLS, o.Delai),
	}, nil
}

type requeteChiffrerOVH struct {
	Plaintext []byte `json:"plaintext"`
	Context   string `json:"context"`
}

type reponseChiffrerOVH struct {
	Ciphertext string `json:"ciphertext"`
}

type requeteDechiffrerOVH struct {
	Ciphertext string `json:"ciphertext"`
	Context    string `json:"context"`
}

type reponseDechiffrerOVH struct {
	Plaintext string `json:"plaintext"`
}

// Chiffrer renvoie le texte chiffré (JWE compact) de cle.
func (c *CleVolume) Chiffrer(ctx context.Context, cle []byte) (string, error) {
	var rep reponseChiffrerOVH
	if err := c.client.poster(ctx, c.chiffrer, http.Header{}, requeteChiffrerOVH{Plaintext: cle, Context: ContexteVolume}, &rep, ""); err != nil {
		return "", err
	}
	ct := strings.TrimSpace(rep.Ciphertext)
	if ct == "" || strings.ContainsAny(ct, " \n\r\t") {
		return "", &erreurKMS{nom: c.client.nom, detail: "texte chiffré absent ou mal formé"}
	}
	return ct, nil
}

// Dechiffrer renvoie la clé en clair. Elle n'est jamais citée dans une
// erreur.
func (c *CleVolume) Dechiffrer(ctx context.Context, chiffre string) ([]byte, error) {
	chiffre = strings.TrimSpace(chiffre)
	if chiffre == "" {
		return nil, fmt.Errorf("texte chiffré vide")
	}
	var rep reponseDechiffrerOVH
	if err := c.client.poster(ctx, c.dechiffrer, http.Header{}, requeteDechiffrerOVH{Ciphertext: chiffre, Context: ContexteVolume}, &rep, ""); err != nil {
		return nil, err
	}
	cle, err := decoderBase64(rep.Plaintext)
	if err != nil {
		return nil, &erreurKMS{nom: c.client.nom, detail: "clé déchiffrée illisible (base64 attendu)"}
	}
	if len(cle) != TailleCleVolume {
		return nil, &erreurKMS{nom: c.client.nom, detail: fmt.Sprintf("clé déchiffrée de %d octets, %d attendus", len(cle), TailleCleVolume)}
	}
	return cle, nil
}

// VerifierAllerRetour chiffre puis déchiffre cle et vérifie qu'on retrouve
// la même valeur ; il renvoie le texte chiffré.
func (c *CleVolume) VerifierAllerRetour(ctx context.Context, cle []byte) (string, error) {
	ct, err := c.Chiffrer(ctx, cle)
	if err != nil {
		return "", err
	}
	retour, err := c.Dechiffrer(ctx, ct)
	if err != nil {
		return "", err
	}
	if !bytes.Equal(retour, cle) {
		return "", &erreurKMS{nom: c.client.nom, detail: "le déchiffrement ne redonne pas la clé chiffrée"}
	}
	return ct, nil
}

// ChargerCertificat lit le certificat client mTLS et les autorités
// facultatives, pour NouvelleCleVolume.
func ChargerCertificat(certificat, cleClient, ac string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certificat, cleClient)
	if err != nil {
		return nil, fmt.Errorf("certificat client illisible : %w", err)
	}
	pool, err := autorites(ac)
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: pool, MinVersion: tls.VersionTLS12}, nil
}
