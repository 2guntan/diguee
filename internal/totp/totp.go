// Package totp implémente les mots de passe à usage unique fondés sur le temps
// (TOTP, RFC 6238) au-dessus de HOTP (RFC 4226), avec HMAC-SHA-1, 6 chiffres
// et une période de 30 secondes : les réglages que lisent toutes les
// applications d'authentification. Le code est court pour rester facile à
// auditer (spec §11) ; il ne dépend que de la bibliothèque standard.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"hash"
	"net/url"
	"strings"
	"time"
)

const (
	// Periode est la durée de validité d'un pas.
	Periode = 30 * time.Second
	// Chiffres est la longueur des codes.
	Chiffres = 6
	// TailleSecret est la longueur du secret partagé, en octets (160 bits,
	// recommandé par la RFC 4226 pour HMAC-SHA-1).
	TailleSecret = 20
	// Tolerance est le nombre de pas acceptés avant et après le pas courant,
	// pour absorber le décalage d'horloge du téléphone.
	Tolerance = 1
)

// encodage est le base32 sans remplissage des URI otpauth.
var encodage = base32.StdEncoding.WithPadding(base32.NoPadding)

// NouveauSecret tire un secret avec le générateur cryptographique.
func NouveauSecret() ([]byte, error) {
	s := make([]byte, TailleSecret)
	if _, err := rand.Read(s); err != nil {
		return nil, err
	}
	return s, nil
}

// Pas renvoie le numéro du pas de temps qui contient t (T de la RFC 6238,
// avec T0 = 0).
func Pas(t time.Time) int64 {
	return t.Unix() / int64(Periode/time.Second)
}

// HOTP calcule le code de la RFC 4226 pour un compteur, avec la fonction de
// hachage h et le nombre de chiffres voulus (de 6 à 9).
func HOTP(h func() hash.Hash, secret []byte, compteur int64, chiffres int) string {
	m := hmac.New(h, secret)
	var c [8]byte
	binary.BigEndian.PutUint64(c[:], uint64(compteur))
	m.Write(c[:])
	somme := m.Sum(nil)
	decalage := somme[len(somme)-1] & 0x0f
	valeur := binary.BigEndian.Uint32(somme[decalage:decalage+4]) & 0x7fffffff
	modulo := uint32(1)
	for range chiffres {
		modulo *= 10
	}
	s := make([]byte, chiffres)
	v := valeur % modulo
	for i := chiffres - 1; i >= 0; i-- {
		s[i] = byte('0' + v%10)
		v /= 10
	}
	return string(s)
}

// Code renvoie le code TOTP (SHA-1, 6 chiffres) du pas donné.
func Code(secret []byte, pas int64) string {
	return HOTP(sha1.New, secret, pas, Chiffres)
}

// Verifier cherche le code saisi parmi les pas voisins de t (± Tolerance) et
// strictement postérieurs à dejaUtilise, le dernier pas accepté pour ce
// secret : un code ne sert ainsi qu'une fois (anti-rejeu). Elle renvoie le pas
// reconnu. La comparaison se fait en temps constant.
func Verifier(secret []byte, saisie string, t time.Time, dejaUtilise int64) (int64, bool) {
	saisie = strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' {
			return -1
		}
		return r
	}, saisie)
	if len(saisie) != Chiffres {
		return 0, false
	}
	courant := Pas(t)
	var trouve int64
	ok := 0
	for p := courant - Tolerance; p <= courant+Tolerance; p++ {
		egal := subtle.ConstantTimeCompare([]byte(Code(secret, p)), []byte(saisie))
		if egal == 1 && p > dejaUtilise && ok == 0 {
			trouve, ok = p, 1
		}
	}
	return trouve, ok == 1
}

// Base32 écrit le secret comme le demandent les applications
// d'authentification (saisie manuelle).
func Base32(secret []byte) string { return encodage.EncodeToString(secret) }

// URI renvoie l'URI otpauth://totp/… du secret, à transformer en QR code ou à
// coller dans une application d'authentification.
func URI(emetteur, compte string, secret []byte) string {
	q := url.Values{}
	q.Set("secret", Base32(secret))
	q.Set("issuer", emetteur)
	q.Set("algorithm", "SHA1")
	q.Set("digits", "6")
	q.Set("period", "30")
	u := url.URL{Scheme: "otpauth", Host: "totp", Path: "/" + emetteur + ":" + compte, RawQuery: q.Encode()}
	return u.String()
}
