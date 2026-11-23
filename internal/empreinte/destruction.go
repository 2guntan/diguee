package empreinte

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Destruction de la clé de l'empreinte à la Clôture (ticket #16, spec §12
// étape 5, ADR 0002). Sans cette clé, plus personne ne peut recalculer
// l'empreinte d'un numéro : même une copie oubliée de la table participation
// ne permettrait plus de savoir qui a participé.
//
// API REST du KMS d'OVHcloud (SDK ovh/okms-sdk-go v0.5.5, apis.go :
// DeactivateServiceKey, DeleteServiceKey, GetServiceKey) :
//
//	GET    /api/{okmsId}/v1/servicekey/{keyId}             état de la clé
//	POST   /api/{okmsId}/v1/servicekey/{keyId}/deactivate  {"reason": "cessation_of_operation"}
//	DELETE /api/{okmsId}/v1/servicekey/{keyId}             suppression définitive (clé désactivée)
//
// Le SDK tient pour réussi tout code inférieur à 400. La suppression
// « ne peut pas être annulée » et échoue si la clé n'est pas désactivée
// (documentation du SDK). Après la suppression, la clé est relue : elle doit
// être introuvable (HTTP 404), ou dans un état détruit.
//
// À CONFIRMER SUR LE VRAI KMS : le code et le corps de la relecture d'une
// clé supprimée (404, ou 200 avec un état « destroyed »), et les noms des
// actions IAM de l'identité de clôture (deploiement/README.md §12). Testé
// seulement contre le faux KMS (fauxkms).
//
// L'appel se fait avec une identité propre à la Clôture (certificat
// DIGUEE_CLOTURE_KMS_CERTIFICAT), créée par le porteur au moment de la
// Clôture et seule à avoir le droit de détruire la clé : celle du serveur ne
// peut que signer.

// RaisonDesactivation est la raison donnée au KMS (RevocationReasons du SDK).
const RaisonDesactivation = "cessation_of_operation"

// DestructeurOVH détruit la clé RSA de l'empreinte dans le KMS d'OVHcloud.
type DestructeurOVH struct {
	cle    string // URL de la clé
	chemin string // chemin de la clé, pour le compte rendu
	client *clientKMS
}

// OptionsDestructeur configure NouveauDestructeurOVH.
type OptionsDestructeur struct {
	PointAcces string        // https://eu-west-par.okms.ovh.net par exemple
	KMSID      string        // identifiant du KMS
	CleID      string        // identifiant de la clé de l'empreinte (DIGUEE_OVH_KMS_CLE_ID)
	TLS        *tls.Config   // certificat client de l'identité de clôture
	Delai      time.Duration // délai d'attente d'un appel ; 5 s par défaut
}

// NouveauDestructeurOVH prépare le client ; il ne fait aucun appel.
func NouveauDestructeurOVH(o OptionsDestructeur) (*DestructeurOVH, error) {
	base, err := url.Parse(strings.TrimRight(o.PointAcces, "/"))
	if err != nil || base.Scheme != "https" || base.Host == "" {
		return nil, fmt.Errorf("KMS OVH : point d'accès https:// attendu")
	}
	if o.KMSID == "" || o.CleID == "" {
		return nil, fmt.Errorf("KMS OVH : identifiants du KMS et de la clé obligatoires")
	}
	chemin := "/api/" + url.PathEscape(o.KMSID) + "/v1/servicekey/" + url.PathEscape(o.CleID)
	return &DestructeurOVH{cle: base.String() + chemin, chemin: chemin,
		client: nouveauClientKMS("KMS OVH (destruction de la clé de l'empreinte)", o.TLS, o.Delai)}, nil
}

// BilanDestruction rend compte de la destruction.
type BilanDestruction struct {
	DejaDetruite bool     // la clé était déjà introuvable avant l'appel (reprise)
	EtatFinal    string   // « introuvable (HTTP 404) » ou l'état lu
	Operations   []string // requêtes et codes HTTP, dans l'ordre
}

// etatsDetruits sont les états d'une clé qui ne peut plus servir (KeyStates du SDK).
var etatsDetruits = map[string]bool{"destroyed": true, "destroyed_compromised": true}

// lireEtat relit la clé : introuvable (404), ou son état (attributes.state
// ou state), « inconnu » s'il n'est pas donné.
func (d *DestructeurOVH) lireEtat(ctx context.Context, b *BilanDestruction) (introuvable bool, etat string, err error) {
	code, corps, err := d.client.appeler(ctx, http.MethodGet, d.cle, nil)
	if err != nil {
		return false, "", err
	}
	b.Operations = append(b.Operations, fmt.Sprintf("GET %s → HTTP %d", d.chemin, code))
	switch {
	case code == http.StatusNotFound:
		return true, "introuvable (HTTP 404)", nil
	case code != http.StatusOK:
		return false, "", &erreurKMS{nom: d.client.nom, detail: fmt.Sprintf("lecture de la clé : réponse HTTP %d%s", code, resumeErreur(corps))}
	}
	var r struct {
		State      string `json:"state"`
		Attributes struct {
			State string `json:"state"`
		} `json:"attributes"`
	}
	_ = json.Unmarshal(corps, &r)
	etat = r.Attributes.State
	if etat == "" {
		etat = r.State
	}
	if etat == "" {
		etat = "inconnu"
	}
	return false, etat, nil
}

// Etat relit la clé sans la modifier : « introuvable (HTTP 404) » ou son
// état (active, deactivated…). Sert au contrôle préalable de la Clôture :
// l'identité de clôture est acceptée par le KMS.
func (d *DestructeurOVH) Etat(ctx context.Context) (string, error) {
	_, etat, err := d.lireEtat(ctx, &BilanDestruction{})
	return etat, err
}

// Detruire désactive puis supprime définitivement la clé, et vérifie
// qu'elle a disparu. Reprenable : une clé déjà introuvable n'est pas une
// erreur (DejaDetruite), une clé déjà désactivée n'est pas désactivée deux
// fois.
func (d *DestructeurOVH) Detruire(ctx context.Context) (BilanDestruction, error) {
	var b BilanDestruction
	introuvable, etat, err := d.lireEtat(ctx, &b)
	if err != nil {
		return b, err
	}
	if introuvable || etatsDetruits[etat] {
		b.DejaDetruite, b.EtatFinal = true, etat
		return b, nil
	}
	if etat != "deactivated" {
		code, corps, err := d.client.appeler(ctx, http.MethodPost, d.cle+"/deactivate", map[string]string{"reason": RaisonDesactivation})
		if err != nil {
			return b, err
		}
		b.Operations = append(b.Operations, fmt.Sprintf("POST %s/deactivate (%s) → HTTP %d", d.chemin, RaisonDesactivation, code))
		if code >= 400 {
			return b, &erreurKMS{nom: d.client.nom, detail: fmt.Sprintf("désactivation : réponse HTTP %d%s", code, resumeErreur(corps))}
		}
	}
	code, corps, err := d.client.appeler(ctx, http.MethodDelete, d.cle, nil)
	if err != nil {
		return b, err
	}
	b.Operations = append(b.Operations, fmt.Sprintf("DELETE %s → HTTP %d", d.chemin, code))
	if code >= 400 {
		return b, &erreurKMS{nom: d.client.nom, detail: fmt.Sprintf("suppression : réponse HTTP %d%s", code, resumeErreur(corps))}
	}
	introuvable, etat, err = d.lireEtat(ctx, &b)
	if err != nil {
		return b, err
	}
	if !introuvable && !etatsDetruits[etat] {
		return b, &erreurKMS{nom: d.client.nom, detail: "la clé existe encore après sa suppression (état " + etat + ")"}
	}
	b.EtatFinal = etat
	return b, nil
}
