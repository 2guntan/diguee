package equipe

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// Le mot de passe est tiré par la plateforme (100 bits d'aléa), jamais choisi
// par le membre : il ne peut donc être ni faible ni réutilisé ailleurs. Il
// est affiché une seule fois, à la création ou à la réinitialisation de
// l'accès, et seule son empreinte PBKDF2-SHA-256 est conservée.

// alphabetMotDePasse : 32 signes sans ambiguïté à la lecture (ni l, ni o, ni 0, ni 1).
const alphabetMotDePasse = "abcdefghijkmnpqrstuvwxyz23456789"

// iterationsPBKDF2 suit la recommandation de l'OWASP pour PBKDF2-SHA-256. Le
// nombre est écrit dans chaque empreinte : il peut être relevé plus tard.
var iterationsPBKDF2 = 600_000

const prefixePBKDF2 = "pbkdf2-sha256"

// NouveauMotDePasse tire un mot de passe de 20 signes en 4 groupes de 5.
func NouveauMotDePasse() (string, error) {
	aleas := make([]byte, 20)
	if _, err := rand.Read(aleas); err != nil {
		return "", err
	}
	var b strings.Builder
	for i, a := range aleas {
		if i > 0 && i%5 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(alphabetMotDePasse[a%32]) // 256 est un multiple de 32 : aucun biais
	}
	return b.String(), nil
}

// normaliserMotDePasse tolère les espaces, tirets et majuscules à la saisie.
func normaliserMotDePasse(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '-' || r == ' ':
			return -1
		case r >= 'A' && r <= 'Z':
			return r + 'a' - 'A'
		}
		return r
	}, s)
}

// EmpreinteMotDePasse calcule l'empreinte à stocker.
func EmpreinteMotDePasse(mdp string) (string, error) {
	sel := make([]byte, 16)
	if _, err := rand.Read(sel); err != nil {
		return "", err
	}
	cle, err := pbkdf2.Key(sha256.New, normaliserMotDePasse(mdp), sel, iterationsPBKDF2, 32)
	if err != nil {
		return "", err
	}
	enc := base64.RawStdEncoding
	return fmt.Sprintf("%s$%d$%s$%s", prefixePBKDF2, iterationsPBKDF2, enc.EncodeToString(sel), enc.EncodeToString(cle)), nil
}

// VerifierMotDePasse compare en temps constant une saisie à une empreinte.
func VerifierMotDePasse(empreinte, saisie string) bool {
	parties := strings.Split(empreinte, "$")
	if len(parties) != 4 || parties[0] != prefixePBKDF2 {
		return false
	}
	iter, err := strconv.Atoi(parties[1])
	if err != nil || iter < 1 {
		return false
	}
	enc := base64.RawStdEncoding
	sel, err1 := enc.DecodeString(parties[2])
	attendu, err2 := enc.DecodeString(parties[3])
	if err := errors.Join(err1, err2); err != nil || len(attendu) != 32 {
		return false
	}
	cle, err := pbkdf2.Key(sha256.New, normaliserMotDePasse(saisie), sel, iter, 32)
	return err == nil && subtle.ConstantTimeCompare(cle, attendu) == 1
}

// empreinteLeurre sert quand l'identifiant est inconnu : on calcule quand
// même une empreinte, pour que la durée de la réponse ne dise pas si le
// compte existe.
var empreinteLeurre = sync.OnceValue(func() string {
	e, err := EmpreinteMotDePasse("leurre")
	if err != nil {
		return prefixePBKDF2 + "$1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	}
	return e
})
