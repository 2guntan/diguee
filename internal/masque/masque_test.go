package masque

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestTexte(t *testing.T) {
	for _, c := range []struct{ entree, sortie string }{
		{"dial tcp 203.0.113.7:443: connect: connection refused", "dial tcp [masqué]:443: connect: connection refused"},
		{`{"ip":"192.0.2.1"}`, `{"ip":"[masqué]"}`},
		{"de 10.0.0.1.", "de [masqué]."},
		{"[2001:db8::1]:443", "[[masqué]]:443"},
		{"fe80::1ff:fe23:4567:890a%eth0", "[masqué]%eth0"},
		{"2001:0db8:0000:0000:0000:ff00:0042:8329 fin", "[masqué] fin"},
		{"::ffff:198.51.100.2", "::ffff:[masqué]"}, // la partie IPv4 suffit
		{"numéro +221771234567 refusé", "numéro [masqué] refusé"},
		{"numéro +221 77 123 45 67", "numéro [masqué]"},
		{"00221 76-123-45-67", "[masqué]"},
		{"221781234567", "[masqué]"},
		{"tel=701234567;", "tel=[masqué];"},
		{"77.123.45.67", "[masqué]"}, // IPv4 et numéro à la fois
		// Rien à masquer.
		{"2026-11-23T05:21:32Z 12:34:56", "2026-11-23T05:21:32Z 12:34:56"},
		{"sha256 ab7712345678cd 9771234567890", "sha256 ab7712345678cd 9771234567890"},
		{"version v1.2.3 adresse :443 :80", "version v1.2.3 adresse :443 :80"},
		{"1.2.3.4.5 et 999.1.1.1", "1.2.3.4.5 et 999.1.1.1"},
		{"52:54:00:12:34:56", "52:54:00:12:34:56"},
		{"duree_ms 123456789", "duree_ms 123456789"},
		{"std::string", "std::string"},
	} {
		if got := Texte(c.entree); got != c.sortie {
			t.Errorf("Texte(%q) = %q, attendu %q", c.entree, got, c.sortie)
		}
	}
}

func TestGestionnaire(t *testing.T) {
	var buf bytes.Buffer
	j := slog.New(Nouveau(slog.NewJSONHandler(&buf, nil))).With("hote", "198.51.100.9")
	j.WithGroup("kms").Error("appel au KMS 203.0.113.5 impossible",
		"erreur", errors.New("dial tcp 203.0.113.5:443: i/o timeout"),
		"detail", slog.GroupValue(slog.String("numero", "+221 77 765 43 21")),
		"chemin", "/resultats", "statut", 503, "duree_ms", 771234567)
	sortie := buf.String()
	for _, interdit := range []string{"203.0.113.5", "198.51.100.9", "77 765 43 21"} {
		if strings.Contains(sortie, interdit) {
			t.Errorf("%q dans %s", interdit, sortie)
		}
	}
	for _, garde := range []string{`"chemin":"/resultats"`, `"statut":503`, `"msg":"appel au KMS [masqué] impossible"`} {
		if !strings.Contains(sortie, garde) {
			t.Errorf("%q absent de %s", garde, sortie)
		}
	}
}
