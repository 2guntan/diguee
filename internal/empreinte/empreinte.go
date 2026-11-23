// Package empreinte normalise les numéros mobiles sénégalais et calcule leur
// empreinte (spec §5.4, ADR 0002).
//
// Le calcul passe par l'interface Empreinteur. La vraie implémentation (KMS
// d'OVHcloud, RS256 puis SHA-256) arrive au ticket #5. En attendant, HMACLocal
// calcule un HMAC-SHA-256 avec une clé de développement écrite dans le code :
// elle est déclarée « fake » dans le registre des intégrations, donc refusée
// en production par le paquet garde.
//
// Règle commune : dès que le numéro est vérifié, on calcule son empreinte et
// on oublie le numéro. Ce paquet ne journalise jamais rien.
package empreinte

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
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

// numeroDeControle sert au test de déterminisme ; ce n'est pas un vrai numéro
// (le préfixe 00 n'est attribué à aucun opérateur).
const numeroDeControle = "+221000000000"

// VerifierDeterminisme calcule deux fois l'empreinte d'une valeur de contrôle
// et renvoie une erreur si les résultats diffèrent (spec §5.4, test T5).
func VerifierDeterminisme(ctx context.Context, e Empreinteur) error {
	a, err := e.Calculer(ctx, numeroDeControle)
	if err != nil {
		return fmt.Errorf("empreinte de contrôle : %w", err)
	}
	b, err := e.Calculer(ctx, numeroDeControle)
	if err != nil {
		return fmt.Errorf("empreinte de contrôle : %w", err)
	}
	if !bytes.Equal(a[:], b[:]) {
		return errors.New("l'empreinteur n'est pas déterministe : les doublons ne seraient pas détectés")
	}
	return nil
}

// Construire choisit l'empreinteur d'après DIGUEE_EMPREINTEUR et le déclare
// dans le registre des intégrations.
func Construire(c *config.Config, r *integration.Registre) (Empreinteur, error) {
	var e Empreinteur
	switch c.Empreinteur {
	case integration.ModeFake:
		e = NouveauHMACLocal(nil)
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
