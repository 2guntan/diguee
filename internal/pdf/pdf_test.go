package pdf

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/2guntan/diguee/internal/pdf/pdftest"
)

func TestDocumentValideEtLisible(t *testing.T) {
	d := Nouveau("Essai (accents) é", func(p, n int) string { return "page " + strconv.Itoa(p) + " sur " + strconv.Itoa(n) })
	d.Titre("Préavis à l'élu")
	d.Paragraphe("« Nous construirons dix écoles » : œuvre, Ç, à, ô, ï, ù, €, 3–4 — fin… (parenthèses) \\ barre.")
	d.Fixe("https://exemple.sn/" + strings.Repeat("a", 200))
	long := strings.Repeat("Une phrase assez longue pour remplir les pages du document. ", 400)
	d.Paragraphe(long)
	b := d.Octets()
	pdftest.Verifier(t, b)
	if !bytes.Equal(b, d.Octets()) {
		t.Error("le document n'est pas déterministe")
	}
	if n := bytes.Count(b, []byte("/Type /Page ")); n < 3 {
		t.Errorf("%d page(s), plusieurs attendues", n)
	}
	texte, ok := pdftest.TexteDe(t, b)
	if !ok {
		t.Skip("pdftotext absent : extraction du texte non vérifiée")
	}
	for _, attendu := range []string{"Préavis à l'élu", "« Nous construirons dix écoles » : œuvre, Ç, à, ô, ï, ù, €, 3–4 — fin… (parenthèses) \\ barre.",
		"https://exemple.sn/aaaa", "page 1 sur", "Une phrase assez longue"} {
		if !strings.Contains(texte, attendu) {
			t.Errorf("%q absent du texte extrait", attendu)
		}
	}
}

func TestWinAnsi(t *testing.T) {
	if got := winAnsi("é€œ’ŋ \t"); !bytes.Equal(got, []byte{0xE9, 0x80, 0x9C, 0x92, '?', 0xA0, ' '}) {
		t.Errorf("%x", got)
	}
	if !Representable("Élu à Thiès, « œuvre »\n") || Representable("Ŋaañ") {
		t.Error("Representable")
	}
	if got := chaine([]byte("a(b)\\\xe9")); got != `(a\(b\)\\\351)` {
		t.Errorf("%s", got)
	}
}

func TestCoupureDesMotsTropLongs(t *testing.T) {
	for _, l := range couper(strings.Repeat("x", 500)+" fin", Fixe, 9, 200) {
		if largeurDe(l, Fixe, 9) > 200 {
			t.Errorf("ligne trop large : %d caractères", len(l))
		}
	}
}
