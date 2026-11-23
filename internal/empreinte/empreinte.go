// Package empreinte normalise les numéros mobiles sénégalais et calcule leur
// empreinte (spec §5.4, ADR 0002).
//
// Le calcul passe par l'interface Empreinteur, choisie par DIGUEE_EMPREINTEUR :
//   - ovh : KMS d'OVHcloud, signature RS256 puis SHA-256 (ADR 0002 et 0006) ;
//   - gcp : repli sur Google Cloud KMS, HMAC natif puis SHA-256 ;
//   - fake : HMACLocal, un HMAC-SHA-256 avec une clé de développement écrite
//     dans le code, déclaré « fake » dans le registre des intégrations, donc
//     refusé en production par le paquet garde.
//
// Règle commune : dès que le numéro est vérifié, on calcule son empreinte et
// on oublie le numéro. Ce paquet ne journalise jamais rien.
package empreinte

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/integration"
)

// Empreinte est la valeur de 32 octets conservée à la place du numéro.
type Empreinte [32]byte

// Empreinteur calcule l'empreinte d'un numéro normalisé (« +221 » suivi de
// 9 chiffres). Le résultat doit être déterministe : un même numéro donne
// toujours la même empreinte, sinon les doublons passeraient.
type Empreinteur interface {
	Calculer(ctx context.Context, numero string) (Empreinte, error)
}

// Erreurs de Normaliser. Elles ne citent jamais le numéro.
var (
	ErrFormat  = errors.New("numéro mal formé : attendu un numéro mobile sénégalais à 9 chiffres")
	ErrPrefixe = errors.New("préfixe mobile non autorisé")
)

// Normaliser met une saisie libre (« 77 123 45 67 », « +221771234567 »,
// « 00221 77… ») sous la forme « +221XXXXXXXXX » et vérifie que son préfixe est
// dans la liste autorisée (DIGUEE_PREFIXES_AUTORISES).
func Normaliser(saisie string, prefixes []string) (string, error) {
	var b strings.Builder
	for i, r := range strings.TrimSpace(saisie) {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && i == 0:
			b.WriteString("00")
		case r == ' ' || r == '.' || r == '-' || r == ' ' || r == ' ':
		default:
			return "", ErrFormat
		}
	}
	chiffres := b.String()
	switch {
	case len(chiffres) == 14 && strings.HasPrefix(chiffres, "00221"):
		chiffres = chiffres[5:]
	case len(chiffres) == 12 && strings.HasPrefix(chiffres, "221"):
		chiffres = chiffres[3:]
	}
	if len(chiffres) != 9 {
		return "", ErrFormat
	}
	if !slices.Contains(prefixes, chiffres[:2]) {
		return "", ErrPrefixe
	}
	return "+221" + chiffres, nil
}

// valide indique si numero est déjà normalisé.
func valide(numero string) bool {
	if len(numero) != 13 || !strings.HasPrefix(numero, "+221") {
		return false
	}
	return strings.Trim(numero[4:], "0123456789") == ""
}

// cleDeveloppement est la clé de HMACLocal. Elle est publique : les empreintes
// calculées avec elle ne protègent rien, d'où l'interdiction en production.
var cleDeveloppement = []byte("diguee : cle de developpement, interdite en production")

// HMACLocal est la fausse implémentation de développement et de test.
type HMACLocal struct {
	cle []byte
}

// NouveauHMACLocal renvoie l'empreinteur de développement. Une clé vide
// désigne la clé de développement par défaut.
func NouveauHMACLocal(cle []byte) *HMACLocal {
	if len(cle) == 0 {
		cle = cleDeveloppement
	}
	return &HMACLocal{cle: slices.Clone(cle)}
}

// Calculer renvoie HMAC-SHA-256(clé, numéro).
func (h *HMACLocal) Calculer(_ context.Context, numero string) (Empreinte, error) {
	if !valide(numero) {
		return Empreinte{}, ErrFormat
	}
	m := hmac.New(sha256.New, h.cle)
	m.Write([]byte(numero))
	var e Empreinte
	copy(e[:], m.Sum(nil))
	return e, nil
}

// Les numéros de contrôle servent au test de déterminisme ; ce ne sont pas de
// vrais numéros (le préfixe 00 n'est attribué à aucun opérateur).
const (
	numeroDeControle      = "+221000000000"
	autreNumeroDeControle = "+221000000001"
)

// ErrNonDeterministe signale un empreinteur qui ne donne pas deux fois la même
// empreinte pour le même numéro (PS256 au lieu de RS256, par exemple).
var ErrNonDeterministe = errors.New("l'empreinteur n'est pas déterministe : les doublons ne seraient pas détectés (RS256 attendu, jamais PS256)")

// VerifierDeterminisme calcule deux fois l'empreinte d'une valeur de contrôle
// et renvoie une erreur si les résultats diffèrent (spec §5.4, test T5). Il
// vérifie aussi que deux valeurs différentes donnent deux empreintes
// différentes. Le serveur l'appelle au démarrage, quel que soit le mode.
func VerifierDeterminisme(ctx context.Context, e Empreinteur) error {
	calculer := func(n string) (Empreinte, error) {
		v, err := e.Calculer(ctx, n)
		if err != nil {
			return v, fmt.Errorf("empreinte de contrôle : %w", err)
		}
		return v, nil
	}
	a, err := calculer(numeroDeControle)
	if err != nil {
		return err
	}
	b, err := calculer(numeroDeControle)
	if err != nil {
		return err
	}
	if !bytes.Equal(a[:], b[:]) {
		return ErrNonDeterministe
	}
	c, err := calculer(autreNumeroDeControle)
	if err != nil {
		return err
	}
	if bytes.Equal(a[:], c[:]) || a == (Empreinte{}) {
		return errors.New("l'empreinteur donne la même empreinte à deux numéros différents")
	}
	return nil
}

// Construire choisit l'empreinteur d'après DIGUEE_EMPREINTEUR et le déclare
// dans le registre des intégrations. Les modes ovh et gcp ne sont pas
// « fake » : ils sont admis en production. Construire ne fait aucun appel au
// KMS ; VerifierDeterminisme s'en charge au démarrage.
func Construire(c *config.Config, r *integration.Registre) (Empreinteur, error) {
	var e Empreinteur
	switch c.Empreinteur {
	case integration.ModeFake:
		e = NouveauHMACLocal(nil)
	case "ovh":
		o := c.OVHKMS
		cert, err := tls.LoadX509KeyPair(o.Certificat, o.CleClient)
		if err != nil {
			return nil, fmt.Errorf("KMS OVH : certificat client (DIGUEE_OVH_KMS_CERTIFICAT, DIGUEE_OVH_KMS_CLE_CLIENT) illisible : %w", err)
		}
		ac, err := autorites(o.AC)
		if err != nil {
			return nil, fmt.Errorf("KMS OVH (DIGUEE_OVH_KMS_AC) : %w", err)
		}
		e, err = NouveauOVH(OptionsOVH{
			PointAcces: o.PointAcces, KMSID: o.ID, CleID: o.CleID, Delai: c.KMSDelai,
			TLS: &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: ac, MinVersion: tls.VersionTLS12},
		})
		if err != nil {
			return nil, err
		}
	case "gcp":
		g := c.GCPKMS
		ac, err := autorites(g.AC)
		if err != nil {
			return nil, fmt.Errorf("KMS Google Cloud (DIGUEE_GCP_KMS_AC) : %w", err)
		}
		e, err = NouveauGCP(OptionsGCP{
			PointAcces: g.PointAcces, Cle: g.Cle, JetonFichier: g.JetonFichier, Delai: c.KMSDelai,
			TLS: &tls.Config{RootCAs: ac, MinVersion: tls.VersionTLS12},
		})
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("DIGUEE_EMPREINTEUR : mode %q non pris en charge", c.Empreinteur)
	}
	if err := r.Declarer(integration.Declaration{
		Nom: "empreinteur", Mode: c.Empreinteur, Variable: "DIGUEE_EMPREINTEUR",
	}); err != nil {
		return nil, err
	}
	return e, nil
}
