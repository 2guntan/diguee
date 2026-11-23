package totp

import (
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"hash"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Vecteurs de l'annexe B de la RFC 6238 (8 chiffres).
func TestVecteursRFC6238(t *testing.T) {
	graine := func(n int) []byte { return []byte(strings.Repeat("1234567890", 7)[:n]) }
	algos := []struct {
		nom    string
		h      func() hash.Hash
		secret []byte
	}{
		{"SHA1", sha1.New, graine(20)},
		{"SHA256", sha256.New, graine(32)},
		{"SHA512", sha512.New, graine(64)},
	}
	cas := []struct {
		t     int64
		codes [3]string
	}{
		{59, [3]string{"94287082", "46119246", "90693936"}},
		{1111111109, [3]string{"07081804", "68084774", "25091201"}},
		{1111111111, [3]string{"14050471", "67062674", "99943326"}},
		{1234567890, [3]string{"89005924", "91819424", "93441116"}},
		{2000000000, [3]string{"69279037", "90698825", "38618901"}},
		{20000000000, [3]string{"65353130", "77737706", "47863826"}},
	}
	for _, c := range cas {
		for i, a := range algos {
			if got := HOTP(a.h, a.secret, Pas(time.Unix(c.t, 0)), 8); got != c.codes[i] {
				t.Errorf("%s t=%d : %s, attendu %s", a.nom, c.t, got, c.codes[i])
			}
		}
	}
}

// Vecteurs de l'annexe D de la RFC 4226 (HOTP, 6 chiffres).
func TestVecteursRFC4226(t *testing.T) {
	secret := []byte("12345678901234567890")
	attendus := []string{"755224", "287082", "359152", "969429", "338314", "254676", "287922", "162583", "399871", "520489"}
	for i, a := range attendus {
		if got := HOTP(sha1.New, secret, int64(i), 6); got != a {
			t.Errorf("compteur %d : %s, attendu %s", i, got, a)
		}
	}
}

func TestVerifierToleranceEtAntiRejeu(t *testing.T) {
	secret, err := NouveauSecret()
	if err != nil {
		t.Fatal(err)
	}
	maintenant := time.Unix(1_800_000_015, 0)
	p := Pas(maintenant)
	for _, d := range []int64{-1, 0, 1} {
		if got, ok := Verifier(secret, Code(secret, p+d), maintenant, 0); !ok || got != p+d {
			t.Errorf("décalage %d refusé", d)
		}
	}
	for _, d := range []int64{-3, -2, 2, 3} {
		if _, ok := Verifier(secret, Code(secret, p+d), maintenant, 0); ok {
			t.Errorf("décalage %d accepté", d)
		}
	}
	// Un code déjà utilisé, ou plus ancien que le dernier utilisé, est refusé.
	if _, ok := Verifier(secret, Code(secret, p), maintenant, p); ok {
		t.Error("code rejoué accepté")
	}
	if _, ok := Verifier(secret, Code(secret, p-1), maintenant, p); ok {
		t.Error("code plus ancien que le dernier accepté")
	}
	if _, ok := Verifier(secret, Code(secret, p+1), maintenant, p); !ok {
		t.Error("code suivant refusé")
	}
	for _, s := range []string{"", "12345", "1234567", "abcdef"} {
		if _, ok := Verifier(secret, s, maintenant, 0); ok {
			t.Errorf("saisie %q acceptée", s)
		}
	}
	c := Code(secret, p)
	if _, ok := Verifier(secret, c[:3]+" "+c[3:], maintenant, 0); !ok {
		t.Error("code avec espace refusé")
	}
}

func TestURI(t *testing.T) {
	secret := []byte("12345678901234567890")
	if got := Base32(secret); got != "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" {
		t.Errorf("base32 : %s", got)
	}
	u, err := url.Parse(URI("Diguee", "awa.diop", secret))
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "otpauth" || u.Host != "totp" || u.Path != "/Diguee:awa.diop" {
		t.Errorf("URI : %s", u)
	}
	q := u.Query()
	if q.Get("secret") != Base32(secret) || q.Get("issuer") != "Diguee" || q.Get("digits") != "6" || q.Get("period") != "30" {
		t.Errorf("paramètres : %v", q)
	}
}
