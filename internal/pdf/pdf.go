// Package pdf écrit des documents PDF 1.4 simples, en bibliothèque
// standard : du texte seulement, mis en page sur des pages A4, avec les
// polices standard Helvetica, Helvetica-Bold et Courier (jamais
// embarquées) en encodage WinAnsi, pour les accents du français.
//
// Le résultat est déterministe : les mêmes blocs donnent les mêmes octets
// (pas de date de création ; l'identifiant du fichier est le SHA-256 de son
// contenu). Le flux de chaque page n'est pas compressé.
package pdf

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Police est l'une des polices standard utilisées.
type Police int

const (
	Normale Police = iota // Helvetica
	Grasse                // Helvetica-Bold
	Fixe                  // Courier : adresses, empreintes
)

var nomsPolices = [...]string{"Helvetica", "Helvetica-Bold", "Courier"}

// Mise en page (points PDF, 1/72 de pouce) : A4 et marges de 2 cm.
const (
	largeurPage = 595
	hauteurPage = 842
	marge       = 56
	largeurUtil = largeurPage - 2*marge
	basPage     = marge + 24 // place du pied de page
)

type bloc struct {
	texte   string
	police  Police
	taille  float64
	avant   float64 // espace avant le bloc
	retrait float64
}

// Document est un document en cours de composition.
type Document struct {
	titre      string
	piedDePage func(page, pages int) string
	blocs      []bloc
}

// Nouveau commence un document. pied renvoie le texte du pied de chaque page
// (nil : aucun).
func Nouveau(titre string, pied func(page, pages int) string) *Document {
	return &Document{titre: titre, piedDePage: pied}
}

// Titre ajoute un titre de niveau 1.
func (d *Document) Titre(s string) { d.blocs = append(d.blocs, bloc{s, Grasse, 16, 6, 0}) }

// Intertitre ajoute un titre de section.
func (d *Document) Intertitre(s string) { d.blocs = append(d.blocs, bloc{s, Grasse, 12, 14, 0}) }

// Paragraphe ajoute un paragraphe ; les fins de ligne du texte sont gardées.
func (d *Document) Paragraphe(s string) { d.blocs = append(d.blocs, bloc{s, Normale, 10.5, 6, 0}) }

// Citation ajoute un paragraphe en retrait.
func (d *Document) Citation(s string) { d.blocs = append(d.blocs, bloc{s, Normale, 10.5, 6, 18}) }

// Gras ajoute un paragraphe en gras.
func (d *Document) Gras(s string) { d.blocs = append(d.blocs, bloc{s, Grasse, 10.5, 6, 0}) }

// Fixe ajoute un texte en police à chasse fixe (adresse, empreinte), coupé
// au caractère près s'il est trop long.
func (d *Document) Fixe(s string) { d.blocs = append(d.blocs, bloc{s, Fixe, 9, 2, 0}) }

// ligne est une ligne posée sur une page.
type ligne struct {
	x, y   float64
	police Police
	taille float64
	octets []byte // texte en WinAnsi
}

// Octets compose et écrit le document.
func (d *Document) Octets() []byte {
	var pages [][]ligne
	var page []ligne
	y := float64(hauteurPage - marge)
	for _, b := range d.blocs {
		interligne := b.taille * 1.35
		lignes := couper(b.texte, b.police, b.taille, largeurUtil-b.retrait)
		if len(page) > 0 {
			y -= b.avant
		}
		for i, l := range lignes {
			// Un titre ne reste pas seul en bas de page.
			besoin := interligne
			if i == 0 && b.police == Grasse && b.taille > 11 {
				besoin = 3 * interligne
			}
			if y-besoin < basPage && len(page) > 0 {
				pages, page, y = append(pages, page), nil, float64(hauteurPage-marge)
			}
			y -= b.taille
			page = append(page, ligne{x: marge + b.retrait, y: y, police: b.police, taille: b.taille, octets: l})
			y -= interligne - b.taille
		}
	}
	pages = append(pages, page)
	if d.piedDePage != nil {
		for i := range pages {
			pages[i] = append(pages[i], ligne{x: marge, y: marge, police: Normale, taille: 8,
				octets: winAnsi(d.piedDePage(i+1, len(pages)))})
		}
	}
	return ecrire(d.titre, pages)
}

// couper découpe un texte en lignes qui tiennent dans la largeur donnée :
// aux espaces, et au caractère près pour un mot trop long.
func couper(s string, p Police, taille, largeur float64) [][]byte {
	var lignes [][]byte
	for _, para := range strings.Split(s, "\n") {
		mots := strings.Fields(para)
		if len(mots) == 0 {
			lignes = append(lignes, nil)
			continue
		}
		var cour []byte
		for _, mot := range mots {
			m := winAnsi(mot)
			essai := m
			if len(cour) > 0 {
				essai = append(append(append([]byte{}, cour...), ' '), m...)
			}
			if largeurDe(essai, p, taille) <= largeur {
				cour = essai
				continue
			}
			if len(cour) > 0 {
				lignes = append(lignes, cour)
				cour = nil
			}
			for largeurDe(m, p, taille) > largeur {
				n := 1
				for n < len(m) && largeurDe(m[:n+1], p, taille) <= largeur {
					n++
				}
				lignes = append(lignes, m[:n])
				m = m[n:]
			}
			cour = m
		}
		lignes = append(lignes, cour)
	}
	return lignes
}

// largeurDe mesure un texte WinAnsi, en points.
func largeurDe(b []byte, p Police, taille float64) float64 {
	var u float64
	for _, c := range b {
		switch p {
		case Fixe:
			u += 600
		case Grasse:
			u += float64(chasse(c)) * 1.1 // un peu plus large que l'Helvetica
		default:
			u += float64(chasse(c))
		}
	}
	return u * taille / 1000
}

// chasse donne la largeur d'un caractère WinAnsi en Helvetica (millièmes de
// corps, d'après les métriques AFM d'Adobe), arrondie vers le haut pour les
// lettres accentuées.
func chasse(c byte) int {
	if c >= 32 && c < 127 {
		return chassesASCII[c-32]
	}
	switch {
	case c == 0x85 || c == 0x97 || c == 0x89 || c == 0x8C:
		return 1000
	case c == 0x9C || c == 0xE6:
		return 944
	case c == 0x91 || c == 0x92:
		return 222
	case c == 0x93 || c == 0x94:
		return 333
	case c >= 0xC0 && c <= 0xDE:
		return 778
	case c >= 0xE0:
		return 556
	}
	return 556
}

var chassesASCII = [95]int{
	278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278, // espace à /
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556, // 0 à ?
	1015, 667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778, // @ à O
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 278, 278, 278, 469, 556, // P à _
	333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556, // ` à o
	556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584, // p à ~
}

// winAnsiSpeciaux : les caractères de 0x80 à 0x9F du codage WinAnsi.
var winAnsiSpeciaux = map[rune]byte{
	'€': 0x80, '‚': 0x82, 'ƒ': 0x83, '„': 0x84, '…': 0x85, '†': 0x86, '‡': 0x87, 'ˆ': 0x88, '‰': 0x89,
	'Š': 0x8A, '‹': 0x8B, 'Œ': 0x8C, 'Ž': 0x8E, '‘': 0x91, '’': 0x92, '“': 0x93, '”': 0x94, '•': 0x95,
	'–': 0x96, '—': 0x97, '˜': 0x98, '™': 0x99, 'š': 0x9A, '›': 0x9B, 'œ': 0x9C, 'ž': 0x9E, 'Ÿ': 0x9F,
	' ': 0xA0, ' ': ' ', ' ': ' ', ' ': ' ', '‐': '-', '‑': '-',
}

// Remplacant est l'octet écrit à la place d'un caractère que WinAnsi ne
// peut pas représenter.
const Remplacant = '?'

// winAnsi convertit un texte UTF-8 en WinAnsi ; les caractères absents de
// ce codage deviennent Remplacant, les caractères de contrôle des espaces.
func winAnsi(s string) []byte {
	b := make([]byte, 0, len(s))
	for _, r := range s {
		switch {
		case r == utf8.RuneError:
			b = append(b, Remplacant)
		case r < 32 || r == 127:
			b = append(b, ' ')
		case r < 127 || (r >= 0xA0 && r <= 0xFF):
			b = append(b, byte(r))
		default:
			if c, ok := winAnsiSpeciaux[r]; ok {
				b = append(b, c)
			} else {
				b = append(b, Remplacant)
			}
		}
	}
	return b
}

// Representable indique si un texte s'écrit sans perte en WinAnsi.
func Representable(s string) bool {
	for _, r := range s {
		if r == '\n' || r == '\t' {
			continue
		}
		if winAnsi(string(r))[0] == Remplacant && r != Remplacant {
			return false
		}
	}
	return true
}

// chaine écrit une chaîne littérale PDF : parenthèses et barres obliques
// inverses échappées, octets hors ASCII en octal.
func chaine(b []byte) string {
	var s strings.Builder
	s.WriteByte('(')
	for _, c := range b {
		switch {
		case c == '(' || c == ')' || c == '\\':
			s.WriteByte('\\')
			s.WriteByte(c)
		case c < 32 || c > 126:
			fmt.Fprintf(&s, "\\%03o", c)
		default:
			s.WriteByte(c)
		}
	}
	s.WriteByte(')')
	return s.String()
}

func nombre(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// ecrire produit le fichier : catalogue, arbre des pages, polices, puis
// chaque page et son flux, table des références croisées et trailer.
func ecrire(titre string, pages [][]ligne) []byte {
	var objets []string // objets 1..n, sans « n 0 obj »
	ajouter := func(s string) int { objets = append(objets, s); return len(objets) }

	ajouter("") // 1 : catalogue, écrit à la fin
	ajouter("") // 2 : arbre des pages
	var polices []int
	for _, nom := range nomsPolices {
		polices = append(polices, ajouter("<< /Type /Font /Subtype /Type1 /BaseFont /"+nom+" /Encoding /WinAnsiEncoding >>"))
	}
	ressources := fmt.Sprintf("<< /Font << /F1 %d 0 R /F2 %d 0 R /F3 %d 0 R >> >>", polices[0], polices[1], polices[2])
	var enfants []string
	for _, p := range pages {
		var flux bytes.Buffer
		for _, l := range p {
			fmt.Fprintf(&flux, "BT /F%d %s Tf %s %s Td %s Tj ET\n", l.police+1, nombre(l.taille), nombre(l.x), nombre(l.y), chaine(l.octets))
		}
		contenu := ajouter(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", flux.Len(), flux.String()))
		page := ajouter(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] /Resources %s /Contents %d 0 R >>",
			largeurPage, hauteurPage, ressources, contenu))
		enfants = append(enfants, strconv.Itoa(page)+" 0 R")
	}
	objets[0] = "<< /Type /Catalog /Pages 2 0 R >>"
	objets[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(enfants, " "), len(pages))
	info := ajouter("<< /Title " + chaine(winAnsi(titre)) + " /Producer (Diguee) >>")

	var f bytes.Buffer
	f.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	positions := make([]int, len(objets))
	for i, o := range objets {
		positions[i] = f.Len()
		fmt.Fprintf(&f, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	id := sha256.Sum256(f.Bytes())
	xref := f.Len()
	fmt.Fprintf(&f, "xref\n0 %d\n0000000000 65535 f \n", len(objets)+1)
	for _, p := range positions {
		fmt.Fprintf(&f, "%010d 00000 n \n", p)
	}
	fmt.Fprintf(&f, "trailer\n<< /Size %d /Root 1 0 R /Info %d 0 R /ID [<%x> <%x>] >>\nstartxref\n%d\n%%%%EOF\n",
		len(objets)+1, info, id[:16], id[:16], xref)
	return f.Bytes()
}
