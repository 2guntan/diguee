// Package whatsapp porte la vérification inversée (spec §5.1, ADR 0009) : le
// site affiche un jeton, le citoyen l'envoie par WhatsApp à Diguee, et le
// webhook de l'API Cloud de Meta prouve qu'il détient le numéro expéditeur.
//
// Le paquet contient :
//   - le générateur de jetons et leur registre en mémoire (Verificateur) ;
//   - le gestionnaire du webhook de Meta (Webhook), qui vérifie la signature
//     X-Hub-Signature-256 et la poignée de main d'abonnement ;
//   - l'intégration choisie par DIGUEE_WHATSAPP (Construire) : « fake », avec
//     un secret de développement et une simulation (Simuler), ou « meta ».
//
// Diguee ne répond jamais dans WhatsApp. Ni le numéro, ni le jeton, ni le
// corps des requêtes ne sont journalisés.
package whatsapp

import (
	"crypto/rand"
	"errors"
	"math/big"
	"regexp"
	"strings"
)

// Le jeton a la forme « DIGUEE 4821-KX » : quatre chiffres et deux lettres,
// tirés dans des alphabets sans caractères ambigus (ni 0 ni 1, ni B, G, I, L,
// O, Q, S, Z), soit 8⁴ × 18² ≈ 1,3 million de jetons possibles. Un jeton ne
// vit que DIGUEE_JETON_DUREE (10 minutes), ne sert qu'une fois, et un
// jeton deviné ne ferait vérifier que le numéro de celui qui l'envoie.
const (
	chiffresJeton = "23456789"
	lettresJeton  = "ACDEFHJKMNPRTUVWXY"
	nbChiffres    = 4
	nbLettres     = 2
)

// PrefixeJeton ouvre chaque jeton ; il rend le message compréhensible pour
// la personne qui l'envoie.
const PrefixeJeton = "DIGUEE"

// motifJeton reconnaît un jeton dans le texte d'un message, même retouché :
// casse, espaces ou tiret manquants.
var motifJeton = regexp.MustCompile(`DIGUEE\s*([0-9]{4})\s*-?\s*([A-Z]{2})\b`)

// nouveauTexteJeton tire un jeton avec un générateur cryptographique.
func nouveauTexteJeton() (string, error) {
	var b strings.Builder
	b.WriteString(PrefixeJeton + " ")
	for i := range nbChiffres + nbLettres {
		alphabet := chiffresJeton
		if i >= nbChiffres {
			alphabet = lettresJeton
			if i == nbChiffres {
				b.WriteByte('-')
			}
		}
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		b.WriteByte(alphabet[n.Int64()])
	}
	return b.String(), nil
}

// ErrSansJeton signale un message qui ne contient aucun jeton.
var ErrSansJeton = errors.New("aucun jeton dans le message")

// extraireJeton retrouve la forme canonique du jeton contenu dans un message.
func extraireJeton(texte string) (string, error) {
	if len(texte) > 1024 {
		texte = texte[:1024]
	}
	m := motifJeton.FindStringSubmatch(strings.ToUpper(texte))
	if m == nil {
		return "", ErrSansJeton
	}
	return PrefixeJeton + " " + m[1] + "-" + m[2], nil
}
