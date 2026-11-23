package journal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// EntierMax est le plus grand entier admis dans le JSON canonique (2^53 - 1) :
// au-delà, les implémentations de RFC 8785 qui passent par des nombres à
// virgule flottante ne retrouveraient pas la même écriture.
const EntierMax = 1<<53 - 1

// Canonique réécrit un document JSON sous sa forme canonique : celle de la
// RFC 8785 (JSON Canonicalization Scheme), restreinte aux nombres entiers.
//
//   - aucun espace ;
//   - les clés de chaque objet triées par unités UTF-16, sans doublon ;
//   - les chaînes en UTF-8, seuls « " », « \ » et les caractères de contrôle
//     sont échappés (\b, \t, \n, \f, \r, sinon \u00xx) ;
//   - les nombres sont des entiers écrits en décimal, entre -(2^53 - 1) et
//     2^53 - 1 ; les nombres à virgule ou à exposant sont refusés.
//
// N'importe quelle bibliothèque RFC 8785 donne donc les mêmes octets.
func Canonique(doc []byte) ([]byte, error) {
	if !utf8.Valid(doc) {
		return nil, errors.New("JSON canonique : UTF-8 invalide")
	}
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var buf bytes.Buffer
	if err := canoniser(dec, &buf); err != nil {
		return nil, fmt.Errorf("JSON canonique : %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("JSON canonique : contenu après la valeur")
	}
	return buf.Bytes(), nil
}

// CanoniqueDe encode v en JSON puis le met sous forme canonique.
func CanoniqueDe(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return Canonique(buf.Bytes())
}

func canoniser(dec *json.Decoder, buf *bytes.Buffer) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case '{':
			return canoniserObjet(dec, buf)
		case '[':
			buf.WriteByte('[')
			for i := 0; dec.More(); i++ {
				if i > 0 {
					buf.WriteByte(',')
				}
				if err := canoniser(dec, buf); err != nil {
					return err
				}
			}
			if _, err := dec.Token(); err != nil { // ']'
				return err
			}
			buf.WriteByte(']')
			return nil
		}
		return fmt.Errorf("délimiteur inattendu %q", v)
	case string:
		ecrireChaine(buf, v)
	case json.Number:
		n, err := entierCanonique(v)
		if err != nil {
			return err
		}
		buf.WriteString(n)
	case bool:
		buf.WriteString(strconv.FormatBool(v))
	case nil:
		buf.WriteString("null")
	default:
		return fmt.Errorf("jeton inattendu %T", tok)
	}
	return nil
}

func canoniserObjet(dec *json.Decoder, buf *bytes.Buffer) error {
	type membre struct {
		cle    string
		valeur []byte
	}
	var membres []membre
	vues := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		cle, ok := tok.(string)
		if !ok {
			return fmt.Errorf("clé d'objet attendue, reçu %v", tok)
		}
		if vues[cle] {
			return fmt.Errorf("clé %q en double", cle)
		}
		vues[cle] = true
		var v bytes.Buffer
		if err := canoniser(dec, &v); err != nil {
			return err
		}
		membres = append(membres, membre{cle, v.Bytes()})
	}
	if _, err := dec.Token(); err != nil { // '}'
		return err
	}
	slices.SortFunc(membres, func(a, b membre) int { return compareUTF16(a.cle, b.cle) })
	buf.WriteByte('{')
	for i, m := range membres {
		if i > 0 {
			buf.WriteByte(',')
		}
		ecrireChaine(buf, m.cle)
		buf.WriteByte(':')
		buf.Write(m.valeur)
	}
	buf.WriteByte('}')
	return nil
}

// entierCanonique vérifie qu'un nombre JSON est un entier admis et renvoie son
// écriture décimale.
func entierCanonique(n json.Number) (string, error) {
	s := n.String()
	i, err := strconv.ParseInt(s, 10, 64)
	if err != nil || i > EntierMax || i < -EntierMax {
		return "", fmt.Errorf("nombre %s refusé : seuls les entiers entre -(2^53-1) et 2^53-1 sont admis", s)
	}
	// « -0 » et les zéros de tête ne sont pas canoniques ; le décodeur JSON
	// refuse déjà les zéros de tête.
	return strconv.FormatInt(i, 10), nil
}

// ecrireChaine écrit une chaîne JSON selon la RFC 8785.
func ecrireChaine(buf *bytes.Buffer, s string) {
	const hex = "0123456789abcdef"
	buf.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\t':
			buf.WriteString(`\t`)
		case '\n':
			buf.WriteString(`\n`)
		case '\f':
			buf.WriteString(`\f`)
		case '\r':
			buf.WriteString(`\r`)
		default:
			if r < 0x20 {
				buf.WriteString(`\u00`)
				buf.WriteByte(hex[r>>4])
				buf.WriteByte(hex[r&0xf])
			} else {
				buf.WriteRune(r)
			}
		}
	}
	buf.WriteByte('"')
}

// compareUTF16 compare deux chaînes par unités de code UTF-16 (RFC 8785 §3.2.3).
func compareUTF16(a, b string) int {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	return slices.Compare(ua, ub)
}
