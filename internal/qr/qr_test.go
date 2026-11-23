package qr

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// Exemple « HELLO WORLD » 1-M (tutoriel de Thonky, d'après la norme) : mots de
// données et mots de correction attendus.
func TestReedSolomon(t *testing.T) {
	donnees := []byte{32, 91, 11, 120, 209, 114, 220, 77, 67, 64, 236, 17, 236, 17, 236, 17}
	attendu := []byte{196, 35, 39, 119, 235, 215, 231, 226, 93, 23}
	if got := reedSolomon(donnees, 10); !bytes.Equal(got, attendu) {
		t.Errorf("correction %v, attendu %v", got, attendu)
	}
}

// Tables C.1 et D.1 de la norme.
func TestFormatEtVersion(t *testing.T) {
	formats := []string{"101010000010010", "101000100100101", "101111001111100", "101101101001011",
		"100010111111001", "100000011001110", "100111110010111", "100101010100000"}
	for m, f := range formats {
		if got := fmt.Sprintf("%015b", bitsFormat(m)); got != f {
			t.Errorf("format M, masque %d : %s, attendu %s", m, got, f)
		}
	}
	for v, f := range map[int]string{7: "000111110010010100", 8: "001000010110111100", 9: "001001101010011001", 10: "001010010011010011"} {
		if got := fmt.Sprintf("%018b", v<<12|resteBCH(v, 0x1F25, 12)); got != f {
			t.Errorf("version %d : %s, attendu %s", v, got, f)
		}
	}
}

func matrice(c *Code) []string {
	var ls []string
	for y := range c.Taille {
		var b strings.Builder
		for x := range c.Taille {
			if c.Sombre(x, y) {
				b.WriteByte('#')
			} else {
				b.WriteByte('.')
			}
		}
		ls = append(ls, b.String())
	}
	return ls
}

func TestVecteursDeReference(t *testing.T) {
	for _, cas := range []struct {
		texte           string
		version, masque int
		golden          []string
	}{{lienWaMe, 4, 3, goldenWaMe}, {texteV7, 7, 1, goldenV7}} {
		c, err := Encoder([]byte(cas.texte))
		if err != nil {
			t.Fatal(err)
		}
		if c.Version != cas.version || c.Masque != cas.masque {
			t.Errorf("version %d masque %d, attendu %d et %d", c.Version, c.Masque, cas.version, cas.masque)
		}
		got := matrice(c)
		for y := range got {
			if got[y] != cas.golden[y] {
				t.Errorf("v%d, ligne %d :\n%s\n%s", cas.version, y, got[y], cas.golden[y])
			}
		}
	}
}

// relire décode un code par le chemin inverse : format (deux copies),
// démasquage, lecture en zigzag, désentrelacement, contrôle de la correction,
// mode octet. Il vérifie aussi les motifs fixes.
func relire(t *testing.T, c *Code) []byte {
	t.Helper()
	ref := nouveauCode(c.Version)
	t1, t2 := 0, 0
	for i := 0; i <= 5; i++ {
		t1 |= b2i(c.Sombre(8, i)) << i
	}
	t1 |= b2i(c.Sombre(8, 7))<<6 | b2i(c.Sombre(8, 8))<<7 | b2i(c.Sombre(7, 8))<<8
	for i := 9; i < 15; i++ {
		t1 |= b2i(c.Sombre(14-i, 8)) << i
	}
	for i := 0; i < 8; i++ {
		t2 |= b2i(c.Sombre(c.Taille-1-i, 8)) << i
	}
	for i := 8; i < 15; i++ {
		t2 |= b2i(c.Sombre(8, c.Taille-15+i)) << i
	}
	if t1 != t2 || t1 != bitsFormat(c.Masque) {
		t.Fatalf("format illisible : %015b %015b", t1, t2)
	}
	// Motifs de repérage, de synchronisation et d'alignement, module sombre.
	for y := range c.Taille {
		for x := range c.Taille {
			formatOuVersion := (x == 8 && (y < 9 || y >= c.Taille-8)) || (y == 8 && (x < 9 || x >= c.Taille-8)) ||
				(c.Version >= 7 && ((x < 6 && y >= c.Taille-11 && y < c.Taille-8) || (y < 6 && x >= c.Taille-11 && x < c.Taille-8)))
			if ref.reserve[y][x] && !formatOuVersion && ref.modules[y][x] != c.Sombre(x, y) {
				t.Fatalf("motif fixe altéré en (%d, %d)", x, y)
			}
		}
	}
	if !c.Sombre(8, c.Taille-8) {
		t.Fatal("module sombre absent")
	}
	var bits []bool
	for droite := c.Taille - 1; droite >= 1; droite -= 2 {
		if droite == 6 {
			droite = 5
		}
		for v := range c.Taille {
			y := v
			if (droite+1)&2 == 0 {
				y = c.Taille - 1 - v
			}
			for j := range 2 {
				x := droite - j
				if !ref.reserve[y][x] {
					bits = append(bits, c.Sombre(x, y) != conditionMasque(c.Masque, x, y))
				}
			}
		}
	}
	b := versions[c.Version]
	nb := b.blocs1 + b.blocs2
	total := b.octetsDonnees() + nb*b.correction
	mots := make([]byte, total)
	for i := range 8 * total {
		if bits[i] {
			mots[i/8] |= 0x80 >> (i % 8)
		}
	}
	for _, r := range bits[8*total:] {
		if r {
			t.Fatal("bits de reste sombres après démasquage")
		}
	}
	blocs := make([][]byte, nb)
	k := 0
	for i := range max(b.donn1, b.donn2) {
		for n := range nb {
			if taille := map[bool]int{true: b.donn1, false: b.donn2}[n < b.blocs1]; i < taille {
				blocs[n] = append(blocs[n], mots[k])
				k++
			}
		}
	}
	corr := make([][]byte, nb)
	for i := 0; i < b.correction; i++ {
		for n := range nb {
			corr[n] = append(corr[n], mots[k])
			k++
		}
	}
	var donnees []byte
	for n := range nb {
		if !bytes.Equal(reedSolomon(blocs[n], b.correction), corr[n]) {
			t.Fatalf("bloc %d : correction incohérente", n)
		}
		donnees = append(donnees, blocs[n]...)
	}
	lire := func(pos, n int) int {
		v := 0
		for i := pos; i < pos+n; i++ {
			v = v<<1 | int(donnees[i/8]>>(7-i%8)&1)
		}
		return v
	}
	if lire(0, 4) != 0b0100 {
		t.Fatal("mode octet attendu")
	}
	cc := bitsCompte(c.Version)
	n := lire(4, cc)
	var sortie []byte
	for i := range n {
		sortie = append(sortie, byte(lire(4+cc+8*i, 8)))
	}
	return sortie
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestAllerRetour(t *testing.T) {
	for n := 0; n <= 213; n += 7 {
		texte := []byte(strings.Repeat("wa.me/221?text=DIGUEE é", 10)[:n])
		c, err := Encoder(texte)
		if err != nil {
			t.Fatal(n, err)
		}
		if c.Taille != 17+4*c.Version {
			t.Fatal("taille")
		}
		if got := relire(t, c); !bytes.Equal(got, texte) {
			t.Fatalf("%d octets : relu %q", n, got)
		}
		for m := range 8 { // chaque masque reste lisible
			cm, _ := encoder(texte, c.Version, m)
			if got := relire(t, cm); !bytes.Equal(got, texte) {
				t.Fatalf("%d octets, masque %d : relu %q", n, m, got)
			}
		}
	}
	if _, err := Encoder(make([]byte, 214)); err != ErrTropLong {
		t.Errorf("214 octets : %v", err)
	}
}

func TestSVG(t *testing.T) {
	c, _ := Encoder([]byte(lienWaMe))
	s := c.SVG(`QR "<code>"`)
	if !strings.HasPrefix(s, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 41 41"`) ||
		!strings.Contains(s, `aria-label="QR &quot;&lt;code&gt;&quot;"`) || !strings.HasSuffix(s, `"/></svg>`) {
		t.Errorf("SVG inattendu : %.200s", s)
	}
	// Le premier module sombre est le coin du motif de repérage, après la marge.
	if !strings.Contains(s, `d="M4 4h7v1h-7z`) {
		t.Errorf("chemin inattendu : %.300s", s)
	}
	if len(s) > 12000 {
		t.Errorf("SVG trop lourd : %d octets", len(s))
	}
}
