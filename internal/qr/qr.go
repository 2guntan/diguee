// Package qr encode un court texte en QR code (ISO/IEC 18004), sans
// dépendance : mode octet, correction d'erreur M (15 % environ), versions 1
// à 10 (jusqu'à 213 octets). Il sert à afficher, sur ordinateur, le lien
// wa.me de la vérification inversée (spec §5.1) : l'image est calculée par le
// serveur et rendue en SVG dans la page, sans JavaScript ni service tiers.
package qr

import (
	"errors"
	"fmt"
	"strings"
)

// ErrTropLong signale un texte qui ne tient pas dans un QR code de version 10.
var ErrTropLong = errors.New("qr : texte trop long (213 octets au plus)")

// Code est un QR code : Taille × Taille modules, true pour un module sombre.
type Code struct {
	Version int
	Masque  int
	Taille  int
	modules [][]bool
	reserve [][]bool // modules des motifs fixes (repérage, synchronisation, format…)
}

// Sombre indique si le module de la colonne x et de la ligne y est sombre.
func (c *Code) Sombre(x, y int) bool {
	if x < 0 || y < 0 || x >= c.Taille || y >= c.Taille {
		return false
	}
	return c.modules[y][x]
}

// bloc décrit la structure des blocs d'une version au niveau M : nombre de
// codes de correction par bloc, puis (nombre de blocs, octets de données par
// bloc) pour les deux groupes.
type bloc struct {
	correction    int
	blocs1, donn1 int
	blocs2, donn2 int
	alignement    []int
}

// Table 9 de la norme (niveau M) et annexe E (centres des motifs d'alignement).
var versions = [...]bloc{
	1:  {10, 1, 16, 0, 0, nil},
	2:  {16, 1, 28, 0, 0, []int{6, 18}},
	3:  {26, 1, 44, 0, 0, []int{6, 22}},
	4:  {18, 2, 32, 0, 0, []int{6, 26}},
	5:  {24, 2, 43, 0, 0, []int{6, 30}},
	6:  {16, 4, 27, 0, 0, []int{6, 34}},
	7:  {18, 4, 31, 0, 0, []int{6, 22, 38}},
	8:  {22, 2, 38, 2, 39, []int{6, 24, 42}},
	9:  {22, 3, 36, 2, 37, []int{6, 26, 46}},
	10: {26, 4, 43, 1, 44, []int{6, 28, 50}},
}

const versionMax = len(versions) - 1

func (b bloc) octetsDonnees() int { return b.blocs1*b.donn1 + b.blocs2*b.donn2 }

// bitsCompte est la taille du champ « nombre de caractères » en mode octet.
func bitsCompte(version int) int {
	if version <= 9 {
		return 8
	}
	return 16
}

// Encoder encode les octets donnés dans le plus petit QR code possible, avec
// le masque de plus faible pénalité.
func Encoder(donnees []byte) (*Code, error) {
	for v := 1; v <= versionMax; v++ {
		if 4+bitsCompte(v)+8*len(donnees) <= 8*versions[v].octetsDonnees() {
			return encoder(donnees, v, -1)
		}
	}
	return nil, ErrTropLong
}

// encoder construit le QR code d'une version donnée ; masque vaut -1 pour
// choisir le meilleur masque.
func encoder(donnees []byte, version, masque int) (*Code, error) {
	if version < 1 || version > versionMax {
		return nil, fmt.Errorf("qr : version %d non prise en charge", version)
	}
	mots, err := motsDeCode(donnees, version)
	if err != nil {
		return nil, err
	}
	c := nouveauCode(version)
	c.placerDonnees(mots)
	if masque < 0 {
		meilleure := -1
		for m := range 8 {
			c.appliquerMasque(m)
			c.placerFormat(m)
			if p := c.penalite(); meilleure < 0 || p < meilleure {
				meilleure, masque = p, m
			}
			c.appliquerMasque(m) // le masque est un OU exclusif : on l'annule
		}
	}
	c.Masque = masque
	c.appliquerMasque(masque)
	c.placerFormat(masque)
	return c, nil
}

// motsDeCode prépare la suite finale des mots de code : données en mode
// octet, bourrage, correction Reed-Solomon par bloc, puis entrelacement.
func motsDeCode(donnees []byte, version int) ([]byte, error) {
	b := versions[version]
	capacite := b.octetsDonnees()
	var e ecrivainBits
	e.ecrire(0b0100, 4) // mode octet
	e.ecrire(len(donnees), bitsCompte(version))
	for _, o := range donnees {
		e.ecrire(int(o), 8)
	}
	if e.n > 8*capacite {
		return nil, ErrTropLong
	}
	e.ecrire(0, min(4, 8*capacite-e.n)) // terminateur
	e.ecrire(0, (8-e.n%8)%8)
	for i := 0; len(e.octets) < capacite; i++ {
		e.ecrire([]int{0xEC, 0x11}[i%2], 8)
	}

	var blocs, corrections [][]byte
	reste := e.octets
	for i := range b.blocs1 + b.blocs2 {
		n := b.donn1
		if i >= b.blocs1 {
			n = b.donn2
		}
		blocs = append(blocs, reste[:n])
		corrections = append(corrections, reedSolomon(reste[:n], b.correction))
		reste = reste[n:]
	}
	var mots []byte
	for i := range max(b.donn1, b.donn2) {
		for _, bl := range blocs {
			if i < len(bl) {
				mots = append(mots, bl[i])
			}
		}
	}
	for i := range b.correction {
		for _, co := range corrections {
			mots = append(mots, co[i])
		}
	}
	return mots, nil
}

type ecrivainBits struct {
	octets []byte
	n      int // nombre de bits écrits
}

func (e *ecrivainBits) ecrire(valeur, bits int) {
	for i := bits - 1; i >= 0; i-- {
		if e.n%8 == 0 {
			e.octets = append(e.octets, 0)
		}
		if valeur>>i&1 == 1 {
			e.octets[e.n/8] |= 0x80 >> (e.n % 8)
		}
		e.n++
	}
}

// Corps de Galois GF(256), polynôme primitif x⁸ + x⁴ + x³ + x² + 1 (0x11D).
var expGF, logGF = func() (exp [512]byte, log [256]int) {
	x := 1
	for i := range 255 {
		exp[i] = byte(x)
		log[x] = i
		x <<= 1
		if x&0x100 != 0 {
			x ^= 0x11D
		}
	}
	for i := 255; i < 512; i++ {
		exp[i] = exp[i-255]
	}
	return
}()

func mulGF(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return expGF[logGF[a]+logGF[b]]
}

// reedSolomon renvoie les n mots de correction du bloc : le reste de la
// division de donnees·xⁿ par le polynôme générateur ∏(x − αⁱ), i < n.
func reedSolomon(donnees []byte, n int) []byte {
	gen := []byte{1} // coefficients, du plus haut degré au plus bas
	for i := range n {
		suivant := make([]byte, len(gen)+1)
		for j, c := range gen {
			suivant[j] ^= c
			suivant[j+1] ^= mulGF(c, expGF[i])
		}
		gen = suivant
	}
	reste := make([]byte, n)
	for _, d := range donnees {
		facteur := d ^ reste[0]
		copy(reste, reste[1:])
		reste[n-1] = 0
		for j := range n {
			reste[j] ^= mulGF(gen[j+1], facteur)
		}
	}
	return reste
}

func nouveauCode(version int) *Code {
	taille := 17 + 4*version
	c := &Code{Version: version, Taille: taille}
	c.modules = make([][]bool, taille)
	c.reserve = make([][]bool, taille)
	for y := range taille {
		c.modules[y] = make([]bool, taille)
		c.reserve[y] = make([]bool, taille)
	}
	// Motifs de synchronisation.
	for i := range taille {
		c.fixer(6, i, i%2 == 0)
		c.fixer(i, 6, i%2 == 0)
	}
	// Motifs de repérage et leurs séparateurs.
	for _, p := range [][2]int{{3, 3}, {taille - 4, 3}, {3, taille - 4}} {
		for dy := -4; dy <= 4; dy++ {
			for dx := -4; dx <= 4; dx++ {
				x, y := p[0]+dx, p[1]+dy
				if x < 0 || y < 0 || x >= taille || y >= taille {
					continue
				}
				d := max(abs(dx), abs(dy))
				c.fixer(x, y, d != 2 && d != 4)
			}
		}
	}
	// Motifs d'alignement, sauf ceux qui chevauchent un motif de repérage.
	al := versions[version].alignement
	for i, cx := range al {
		for j, cy := range al {
			if (i == 0 && j == 0) || (i == 0 && j == len(al)-1) || (i == len(al)-1 && j == 0) {
				continue
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					c.fixer(cx+dx, cy+dy, max(abs(dx), abs(dy)) != 1)
				}
			}
		}
	}
	// Zones du format (remplies plus tard) et module sombre.
	c.placerFormat(0)
	// Informations de version (version 7 et plus).
	if version >= 7 {
		bits := version<<12 | resteBCH(version, 0x1F25, 12)
		for i := range 18 {
			sombre := bits>>i&1 == 1
			a, b := taille-11+i%3, i/3
			c.fixer(a, b, sombre)
			c.fixer(b, a, sombre)
		}
	}
	return c
}

func (c *Code) fixer(x, y int, sombre bool) {
	c.modules[y][x] = sombre
	c.reserve[y][x] = true
}

// resteBCH renvoie le reste de la division de valeur·x^degre par le
// polynôme générateur (codes BCH du format et de la version).
func resteBCH(valeur, generateur, degre int) int {
	r := valeur << degre
	for i := 30; i >= degre; i-- {
		if r>>i&1 == 1 {
			r ^= generateur << (i - degre)
		}
	}
	return r
}

// bitsFormat renvoie les 15 bits du format pour le niveau M et un masque.
func bitsFormat(masque int) int {
	donnees := 0b00<<3 | masque // niveau M : 00
	return (donnees<<10 | resteBCH(donnees, 0x537, 10)) ^ 0x5412
}

// placerFormat écrit les deux copies du format et le module sombre.
func (c *Code) placerFormat(masque int) {
	bits := bitsFormat(masque)
	bit := func(i int) bool { return bits>>i&1 == 1 }
	t := c.Taille
	for i := 0; i <= 5; i++ {
		c.fixer(8, i, bit(i))
	}
	c.fixer(8, 7, bit(6))
	c.fixer(8, 8, bit(7))
	c.fixer(7, 8, bit(8))
	for i := 9; i < 15; i++ {
		c.fixer(14-i, 8, bit(i))
	}
	for i := 0; i < 8; i++ {
		c.fixer(t-1-i, 8, bit(i))
	}
	for i := 8; i < 15; i++ {
		c.fixer(8, t-15+i, bit(i))
	}
	c.fixer(8, t-8, true)
}

// placerDonnees dépose les bits des mots de code en zigzag, par colonnes de
// deux modules, de bas en haut puis de haut en bas, depuis le coin inférieur
// droit, en sautant les modules réservés et la colonne de synchronisation.
func (c *Code) placerDonnees(mots []byte) {
	i, t := 0, c.Taille
	for droite := t - 1; droite >= 1; droite -= 2 {
		if droite == 6 {
			droite = 5
		}
		montant := (droite+1)&2 == 0
		for v := range t {
			y := v
			if montant {
				y = t - 1 - v
			}
			for j := range 2 {
				x := droite - j
				if c.reserve[y][x] {
					continue
				}
				if i < 8*len(mots) {
					c.modules[y][x] = mots[i/8]>>(7-i%8)&1 == 1
					i++
				}
				// Les bits de reste (versions 2 à 6) restent clairs.
			}
		}
	}
}

// conditionMasque renvoie la condition d'inversion du masque m (x : colonne,
// y : ligne).
func conditionMasque(m, x, y int) bool {
	switch m {
	case 0:
		return (x+y)%2 == 0
	case 1:
		return y%2 == 0
	case 2:
		return x%3 == 0
	case 3:
		return (x+y)%3 == 0
	case 4:
		return (x/3+y/2)%2 == 0
	case 5:
		return x*y%2+x*y%3 == 0
	case 6:
		return (x*y%2+x*y%3)%2 == 0
	default:
		return ((x+y)%2+x*y%3)%2 == 0
	}
}

func (c *Code) appliquerMasque(m int) {
	for y := range c.Taille {
		for x := range c.Taille {
			if !c.reserve[y][x] && conditionMasque(m, x, y) {
				c.modules[y][x] = !c.modules[y][x]
			}
		}
	}
}

// penalite évalue un masque selon les quatre règles de la norme (§7.8.3).
func (c *Code) penalite() int {
	t, p := c.Taille, 0
	ligne := func(i int, horizontal bool) []bool {
		l := make([]bool, t)
		for k := range t {
			if horizontal {
				l[k] = c.modules[i][k]
			} else {
				l[k] = c.modules[k][i]
			}
		}
		return l
	}
	motifA := []bool{true, false, true, true, true, false, true, false, false, false, false}
	motifB := []bool{false, false, false, false, true, false, true, true, true, false, true}
	for i := range t {
		for _, h := range []bool{true, false} {
			l := ligne(i, h)
			// Règle 1 : suites d'au moins 5 modules de même couleur.
			suite := 1
			for k := 1; k <= t; k++ {
				if k < t && l[k] == l[k-1] {
					suite++
					continue
				}
				if suite >= 5 {
					p += 3 + suite - 5
				}
				suite = 1
			}
			// Règle 3 : motifs 1:1:3:1:1 bordés de 4 modules clairs.
			for k := 0; k+11 <= t; k++ {
				if egal(l[k:k+11], motifA) || egal(l[k:k+11], motifB) {
					p += 40
				}
			}
		}
	}
	// Règle 2 : carrés 2 × 2 de même couleur.
	sombres := 0
	for y := range t {
		for x := range t {
			if c.modules[y][x] {
				sombres++
			}
			if x+1 < t && y+1 < t {
				v := c.modules[y][x]
				if c.modules[y][x+1] == v && c.modules[y+1][x] == v && c.modules[y+1][x+1] == v {
					p += 3
				}
			}
		}
	}
	// Règle 4 : écart à 50 % de modules sombres, par tranche de 5 %.
	p += 10 * (abs(sombres*100/(t*t)-50) / 5)
	return p
}

func egal(a, b []bool) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// MargeModules est la zone claire laissée autour du code (4 modules, norme).
const MargeModules = 4

// SVG rend le code en SVG, marge comprise, avec un chemin par suite
// horizontale de modules sombres. Le résultat ne contient que des chiffres et
// des attributs fixes ; libelle est échappé.
func (c *Code) SVG(libelle string) string {
	n := c.Taille + 2*MargeModules
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges" role="img" aria-label="%s">`,
		n, n, echapper(libelle))
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="`, n, n)
	for y := range c.Taille {
		for x := 0; x < c.Taille; {
			if !c.modules[y][x] {
				x++
				continue
			}
			debut := x
			for x < c.Taille && c.modules[y][x] {
				x++
			}
			fmt.Fprintf(&b, "M%d %dh%dv1h-%dz", debut+MargeModules, y+MargeModules, x-debut, x-debut)
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String()
}

func echapper(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;").Replace(s)
}
