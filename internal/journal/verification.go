package journal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// FormatExport identifie le format de l'export JSON.
const FormatExport = "diguee-journal/1"

// Export est le document JSON publié sur /journal.json.
type Export struct {
	Format            string         `json:"format"`
	Hachage           string         `json:"hachage"`
	EmpreinteInitiale string         `json:"empreinte_precedente_premiere"` // 32 octets nuls
	Verifier          string         `json:"verifier"`
	NombreEntrees     int64          `json:"nombre_entrees"`
	Tete              string         `json:"tete"` // empreinte de la dernière entrée (vide si le journal est vide)
	Entrees           []EntreeExport `json:"entrees"`
}

// EntreeExport est une entrée telle qu'elle est exportée.
type EntreeExport struct {
	Numero              int64           `json:"numero"`
	Horodatage          string          `json:"horodatage"`
	Type                Type            `json:"type"`
	Contenu             json.RawMessage `json:"contenu"`
	EmpreintePrecedente string          `json:"empreinte_precedente"`
	Empreinte           string          `json:"empreinte"`
}

// DescriptionHachage décrit, dans l'export, comment l'empreinte est calculée.
const DescriptionHachage = "empreinte = SHA-256 du JSON canonique (RFC 8785, entiers seulement) de " +
	"{numero, horodatage, type, contenu, empreinte_precedente}, en hexadécimal minuscule"

// CommandeVerification est la commande publiée pour vérifier un export
// téléchargé (elle accepte aussi une adresse https://…/journal.json).
const CommandeVerification = "go run github.com/2guntan/diguee/cmd/diguee@latest verifier-journal journal.json"

// Exporter écrit l'export JSON des entrées.
func Exporter(w io.Writer, es []Entree) error {
	x := Export{
		Format: FormatExport, Hachage: DescriptionHachage, Verifier: CommandeVerification,
		Entrees: make([]EntreeExport, 0, len(es)), NombreEntrees: int64(len(es)),
		EmpreinteInitiale: Empreinte{}.String(),
	}
	for _, e := range es {
		x.Entrees = append(x.Entrees, EntreeExport{
			Numero: e.Numero, Horodatage: e.Horodatage.Format(FormatHorodatage), Type: e.Type,
			Contenu: e.Contenu, EmpreintePrecedente: e.EmpreintePrecedente.String(), Empreinte: e.Empreinte.String(),
		})
	}
	if len(es) > 0 {
		x.Tete = es[len(es)-1].Empreinte.String()
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	return enc.Encode(x)
}

// Rapport résume une vérification réussie.
type Rapport struct {
	Entrees int64
	Tete    Empreinte // empreinte de la dernière entrée ; nulle si le journal est vide
}

// ErreurChaine signale l'entrée où la vérification a échoué.
type ErreurChaine struct {
	Position int64 // rang de l'entrée dans l'export, à partir de 1
	Raison   string
}

func (e *ErreurChaine) Error() string {
	return fmt.Sprintf("entrée %d : %s", e.Position, e.Raison)
}

// LireExport lit et vérifie un export JSON : format, champs, chaînage et
// empreintes. Il renvoie les entrées et le rapport, ou la première erreur.
func LireExport(r io.Reader) ([]Entree, Rapport, error) {
	brut, err := io.ReadAll(r)
	if err != nil {
		return nil, Rapport{}, err
	}
	var x struct {
		Format  string            `json:"format"`
		Entrees []json.RawMessage `json:"entrees"`
		Tete    *string           `json:"tete"`
		Nombre  *int64            `json:"nombre_entrees"`
	}
	if err := json.Unmarshal(brut, &x); err != nil {
		return nil, Rapport{}, fmt.Errorf("export illisible : %w", err)
	}
	if x.Format != FormatExport {
		return nil, Rapport{}, fmt.Errorf("format %q inconnu (attendu %q)", x.Format, FormatExport)
	}
	if x.Entrees == nil {
		return nil, Rapport{}, errors.New("export sans liste « entrees »")
	}
	es := make([]Entree, 0, len(x.Entrees))
	for i, b := range x.Entrees {
		e, err := lireEntree(b)
		if err != nil {
			return nil, Rapport{}, &ErreurChaine{Position: int64(i + 1), Raison: err.Error()}
		}
		es = append(es, e)
	}
	rap, err := VerifierChaine(es)
	if err != nil {
		return nil, Rapport{}, err
	}
	// La tête et le nombre annoncés doivent être ceux qu'on recalcule.
	if x.Tete != nil && len(es) > 0 && *x.Tete != rap.Tete.String() {
		return nil, Rapport{}, fmt.Errorf("tête annoncée %s, recalculée %s", *x.Tete, rap.Tete)
	}
	if x.Nombre != nil && *x.Nombre != rap.Entrees {
		return nil, Rapport{}, fmt.Errorf("%d entrées annoncées, %d trouvées", *x.Nombre, rap.Entrees)
	}
	return es, rap, nil
}

func lireEntree(b []byte) (Entree, error) {
	// Refuse les clés en double et les nombres non entiers.
	if _, err := Canonique(b); err != nil {
		return Entree{}, err
	}
	var x EntreeExport
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields() // un champ non haché serait trompeur
	if err := dec.Decode(&x); err != nil {
		return Entree{}, err
	}
	var vus map[string]json.RawMessage
	if err := json.Unmarshal(b, &vus); err != nil {
		return Entree{}, err
	}
	for _, champ := range []string{"numero", "horodatage", "type", "contenu", "empreinte_precedente", "empreinte"} {
		if _, ok := vus[champ]; !ok {
			return Entree{}, fmt.Errorf("champ %q absent", champ)
		}
	}
	h, err := time.Parse(time.RFC3339, x.Horodatage)
	if err != nil || h.Format(FormatHorodatage) != x.Horodatage {
		return Entree{}, fmt.Errorf("horodatage %q : attendu AAAA-MM-JJTHH:MM:00Z", x.Horodatage)
	}
	c, err := Canonique(x.Contenu)
	if err != nil {
		return Entree{}, err
	}
	e := Entree{Numero: x.Numero, Horodatage: h.UTC(), Type: x.Type, Contenu: c}
	if e.EmpreintePrecedente, err = LireEmpreinte(x.EmpreintePrecedente); err != nil {
		return Entree{}, err
	}
	if e.Empreinte, err = LireEmpreinte(x.Empreinte); err != nil {
		return Entree{}, err
	}
	return e, nil
}

// VerifierChaine vérifie une suite complète d'entrées, depuis la première :
// numéros consécutifs à partir de 1, horodatages dans l'ordre, chaque
// empreinte recalculée, et chaque empreinte précédente égale à celle de
// l'entrée d'avant (32 octets nuls pour la première).
func VerifierChaine(es []Entree) (Rapport, error) {
	var prec Empreinte
	var hPrec time.Time
	for i, e := range es {
		pos := int64(i + 1)
		fail := func(format string, args ...any) (Rapport, error) {
			return Rapport{}, &ErreurChaine{Position: pos, Raison: fmt.Sprintf(format, args...)}
		}
		if e.Numero != pos {
			return fail("numéro %d, attendu %d (entrée ajoutée, retirée ou déplacée)", e.Numero, pos)
		}
		if e.EmpreintePrecedente != prec {
			return fail("l'empreinte précédente %s ne correspond pas à l'entrée %d (%s)", e.EmpreintePrecedente, pos-1, prec)
		}
		if i > 0 && e.Horodatage.Before(hPrec) {
			return fail("horodatage %s antérieur à celui de l'entrée précédente", e.Horodatage.Format(FormatHorodatage))
		}
		h, err := e.Calculer()
		if err != nil {
			return fail("%v", err)
		}
		if h != e.Empreinte {
			return fail("empreinte annoncée %s, recalculée %s : l'entrée a été modifiée", e.Empreinte, h)
		}
		prec, hPrec = h, e.Horodatage
	}
	return Rapport{Entrees: int64(len(es)), Tete: prec}, nil
}

// VerifierPrefixe vérifie que le journal ancien (un export téléchargé plus
// tôt) est le début exact du journal actuel : aucune entrée passée n'a changé.
func VerifierPrefixe(ancien, actuel []Entree) error {
	if len(ancien) > len(actuel) {
		return fmt.Errorf("le journal actuel a %d entrées, l'ancien en avait %d : des entrées ont disparu", len(actuel), len(ancien))
	}
	for i, a := range ancien {
		if a.Empreinte != actuel[i].Empreinte {
			return &ErreurChaine{Position: int64(i + 1), Raison: "différente de l'export ancien : une entrée passée a été réécrite"}
		}
	}
	return nil
}

// Contient indique si l'une des entrées a l'empreinte donnée, par exemple une
// empreinte de tête publiée hors de nos serveurs (OpenTimestamps, archive.org, X).
func Contient(es []Entree, h Empreinte) bool {
	for _, e := range es {
		if e.Empreinte == h {
			return true
		}
	}
	return false
}
