package ancrage

import (
	"bytes"
	"crypto/sha1" // opération du format OpenTimestamps, sans usage de sécurité
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"

	"golang.org/x/crypto/ripemd160" // opération du format OpenTimestamps
)

// Format des preuves OpenTimestamps (fichiers .ots), tel que l'écrivent et le
// lisent le client de référence (python-opentimestamps, commande ots) et les
// calendriers publics :
//
//	fichier     = en-tête magique, version (varuint 1), opération de hachage
//	              du fichier (0x08 : SHA-256), condensat (32 octets), horodatage
//	horodatage  = (0xff élément)* élément ; plusieurs éléments = branches
//	élément     = 0x00 attestation | opération horodatage
//	opération   = étiquette d'un octet, suivie pour append (0xf0) et prepend
//	              (0xf1) de son argument (varbytes)
//	attestation = étiquette de 8 octets, charge (varbytes) ; en attente :
//	              charge = varbytes(URI du calendrier) ; Bitcoin : charge =
//	              varuint(hauteur du bloc)
//
// Un horodatage s'applique à un message : chaque opération transforme le
// message de son nœud, et une attestation dit que le message de son nœud a
// été inscrit (Bitcoin) ou le sera (calendrier en attente).

// EnTeteOTS est l'en-tête magique d'un fichier .ots.
var EnTeteOTS = []byte("\x00OpenTimestamps\x00\x00Proof\x00\xbf\x89\xe2\xe8\x84\xe8\x92\x94")

// VersionOTS est la seule version du format.
const VersionOTS = 1

// Étiquettes des opérations.
const (
	OpSHA1      byte = 0x02
	OpRIPEMD160 byte = 0x03
	OpSHA256    byte = 0x08
	OpKeccak256 byte = 0x67
	OpAppend    byte = 0xf0
	OpPrepend   byte = 0xf1
	OpReverse   byte = 0xf2
	OpHexlify   byte = 0xf3
)

const (
	marqueAttestation byte = 0x00
	marqueFourche     byte = 0xff
)

// Étiquettes des attestations.
var (
	TagEnAttente = [8]byte{0x83, 0xdf, 0xe3, 0x0d, 0x2e, 0xf9, 0x0c, 0x8e}
	TagBitcoin   = [8]byte{0x05, 0x88, 0x96, 0x0d, 0x73, 0xd7, 0x19, 0x01}
)

// Limites du client de référence : elles bornent ce qu'une réponse de
// calendrier (donnée extérieure) peut faire consommer.
const (
	maxMessage     = 4096 // longueur d'un message ou d'un argument
	maxCharge      = 8192 // charge d'une attestation
	maxURI         = 1000 // URI d'un calendrier
	maxProfondeur  = 256  // imbrication des horodatages
	maxTailleOTS   = 64 << 10
	maxAttestation = 1024 // attestations dans une preuve
)

// Op est une opération : son étiquette et, pour append et prepend, son argument.
type Op struct {
	Tag byte
	Arg []byte
}

func (o Op) binaire() bool { return o.Tag == OpAppend || o.Tag == OpPrepend }

// Appliquer renvoie le résultat de l'opération sur msg.
func (o Op) Appliquer(msg []byte) ([]byte, error) {
	var r []byte
	switch o.Tag {
	case OpAppend:
		r = append(slices.Clone(msg), o.Arg...)
	case OpPrepend:
		r = append(slices.Clone(o.Arg), msg...)
	case OpSHA256:
		s := sha256.Sum256(msg)
		r = s[:]
	case OpSHA1:
		s := sha1.Sum(msg)
		r = s[:]
	case OpRIPEMD160:
		h := ripemd160.New()
		h.Write(msg)
		r = h.Sum(nil)
	case OpKeccak256:
		// Réservée aux attestations Ethereum, que ni les calendriers ni
		// Bitcoin n'emploient ; la calculer demanderait une dépendance de plus.
		return nil, errors.New("ots : opération keccak256 non prise en charge")
	case OpReverse:
		if len(msg) == 0 {
			return nil, errors.New("ots : reverse d'un message vide")
		}
		r = slices.Clone(msg)
		slices.Reverse(r)
	case OpHexlify:
		if len(msg) == 0 || 2*len(msg) > maxMessage {
			return nil, errors.New("ots : hexlify hors limites")
		}
		r = []byte(hex.EncodeToString(msg))
	default:
		return nil, fmt.Errorf("ots : opération 0x%02x inconnue", o.Tag)
	}
	if len(r) > maxMessage {
		return nil, errors.New("ots : message trop long")
	}
	return r, nil
}

func comparerOps(a, b Op) int {
	if a.Tag != b.Tag {
		return int(a.Tag) - int(b.Tag)
	}
	return bytes.Compare(a.Arg, b.Arg)
}

// Attestation est une attestation : son étiquette et sa charge brute.
type Attestation struct {
	Tag    [8]byte
	Charge []byte
}

// EnAttente renvoie une attestation « en attente » du calendrier uri.
func EnAttente(uri string) Attestation {
	return Attestation{Tag: TagEnAttente, Charge: varbytes(nil, []byte(uri))}
}

// Bitcoin renvoie une attestation Bitcoin au bloc de hauteur donnée.
func Bitcoin(hauteur uint64) Attestation {
	return Attestation{Tag: TagBitcoin, Charge: varuint(nil, hauteur)}
}

// URI renvoie l'URI d'une attestation en attente.
func (a Attestation) URI() (string, bool) {
	if a.Tag != TagEnAttente {
		return "", false
	}
	l := &lecteur{b: a.Charge}
	u, err := l.varbytes(maxURI)
	if err != nil || len(l.b) != 0 || !uriValide(u) {
		return "", false
	}
	return string(u), true
}

// Hauteur renvoie la hauteur du bloc d'une attestation Bitcoin.
func (a Attestation) Hauteur() (uint64, bool) {
	if a.Tag != TagBitcoin {
		return 0, false
	}
	l := &lecteur{b: a.Charge}
	h, err := l.varuint()
	if err != nil || len(l.b) != 0 {
		return 0, false
	}
	return h, true
}

// uriValide reprend les caractères admis par le client de référence.
func uriValide(u []byte) bool {
	if len(u) == 0 || len(u) > maxURI {
		return false
	}
	for _, c := range u {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '.' || c == '-' || c == '_' || c == '/' || c == ':'
		if !ok {
			return false
		}
	}
	return true
}

// comparerAttestations suit l'ordre du client de référence : par étiquette,
// puis par URI (en attente), par hauteur (Bitcoin) ou par charge.
func comparerAttestations(a, b Attestation) int {
	if c := bytes.Compare(a.Tag[:], b.Tag[:]); c != 0 {
		return c
	}
	if ua, ok := a.URI(); ok {
		if ub, ok := b.URI(); ok {
			return bytes.Compare([]byte(ua), []byte(ub))
		}
	}
	if ha, ok := a.Hauteur(); ok {
		if hb, ok := b.Hauteur(); ok {
			switch {
			case ha < hb:
				return -1
			case ha > hb:
				return 1
			}
			return 0
		}
	}
	return bytes.Compare(a.Charge, b.Charge)
}

// Horodatage est un nœud de l'arbre : un message, ses attestations, et les
// opérations qui en partent.
type Horodatage struct {
	Message      []byte
	Attestations []Attestation
	Branches     []Branche
}

// Branche est une opération et l'horodatage de son résultat.
type Branche struct {
	Op    Op
	Suite *Horodatage
}

// Ajouter ajoute l'opération o (ou reprend la branche existante) et renvoie
// l'horodatage de son résultat.
func (h *Horodatage) Ajouter(o Op) (*Horodatage, error) {
	for _, b := range h.Branches {
		if comparerOps(b.Op, o) == 0 {
			return b.Suite, nil
		}
	}
	r, err := o.Appliquer(h.Message)
	if err != nil {
		return nil, err
	}
	s := &Horodatage{Message: r}
	h.Branches = append(h.Branches, Branche{Op: o, Suite: s})
	return s, nil
}

// Attester ajoute une attestation, sauf si elle y est déjà.
func (h *Horodatage) Attester(a Attestation) {
	for _, x := range h.Attestations {
		if x.Tag == a.Tag && bytes.Equal(x.Charge, a.Charge) {
			return
		}
	}
	h.Attestations = append(h.Attestations, a)
}

// Fusionner ajoute à h les attestations et les branches de autre, qui doit
// porter sur le même message.
func (h *Horodatage) Fusionner(autre *Horodatage) error {
	if !bytes.Equal(h.Message, autre.Message) {
		return errors.New("ots : fusion d'horodatages de messages différents")
	}
	for _, a := range autre.Attestations {
		h.Attester(a)
	}
	for _, b := range autre.Branches {
		s, err := h.Ajouter(b.Op)
		if err != nil {
			return err
		}
		if err := s.Fusionner(b.Suite); err != nil {
			return err
		}
	}
	return nil
}

// Feuille est une attestation et le message qu'elle atteste.
type Feuille struct {
	Noeud       *Horodatage
	Attestation Attestation
}

// Feuilles renvoie toutes les attestations de l'arbre, en profondeur.
func (h *Horodatage) Feuilles() []Feuille {
	var fs []Feuille
	var parcourir func(*Horodatage)
	parcourir = func(n *Horodatage) {
		for _, a := range n.Attestations {
			fs = append(fs, Feuille{Noeud: n, Attestation: a})
		}
		for _, b := range n.Branches {
			parcourir(b.Suite)
		}
	}
	parcourir(h)
	return fs
}

// Blocs renvoie les hauteurs des blocs Bitcoin attestés, sans doublon, triées.
func (h *Horodatage) Blocs() []uint64 {
	var hs []uint64
	for _, f := range h.Feuilles() {
		if n, ok := f.Attestation.Hauteur(); ok && !slices.Contains(hs, n) {
			hs = append(hs, n)
		}
	}
	slices.Sort(hs)
	return hs
}

// Calendriers renvoie les URI des attestations en attente, sans doublon.
func (h *Horodatage) Calendriers() []string {
	var us []string
	for _, f := range h.Feuilles() {
		if u, ok := f.Attestation.URI(); ok && !slices.Contains(us, u) {
			us = append(us, u)
		}
	}
	return us
}

// Encoder sérialise l'horodatage (sans en-tête), comme le client de
// référence : attestations puis opérations, chacune triée, toutes sauf la
// dernière précédées de 0xff.
func (h *Horodatage) Encoder() ([]byte, error) { return h.encoder(nil, 0) }

func (h *Horodatage) encoder(b []byte, prof int) ([]byte, error) {
	if len(h.Attestations) == 0 && len(h.Branches) == 0 {
		return nil, errors.New("ots : horodatage vide")
	}
	if prof > maxProfondeur {
		return nil, errors.New("ots : horodatage trop profond")
	}
	as := slices.Clone(h.Attestations)
	slices.SortFunc(as, comparerAttestations)
	bs := slices.Clone(h.Branches)
	slices.SortFunc(bs, func(x, y Branche) int { return comparerOps(x.Op, y.Op) })
	n := len(as) + len(bs)
	i := 0
	var err error
	for _, a := range as {
		if i++; i < n {
			b = append(b, marqueFourche)
		}
		b = append(b, marqueAttestation)
		b = append(b, a.Tag[:]...)
		b = varbytes(b, a.Charge)
	}
	for _, br := range bs {
		if i++; i < n {
			b = append(b, marqueFourche)
		}
		b = append(b, br.Op.Tag)
		if br.Op.binaire() {
			b = varbytes(b, br.Op.Arg)
		}
		if b, err = br.Suite.encoder(b, prof+1); err != nil {
			return nil, err
		}
	}
	return b, nil
}

// LireHorodatage lit un horodatage sérialisé (la réponse d'un calendrier, par
// exemple) qui s'applique à msg. Tous les octets doivent être consommés.
func LireHorodatage(donnees, msg []byte) (*Horodatage, error) {
	l := &lecteur{b: donnees}
	h, err := l.horodatage(slices.Clone(msg), 0)
	if err != nil {
		return nil, err
	}
	if len(l.b) != 0 {
		return nil, errors.New("ots : octets en trop après l'horodatage")
	}
	return h, nil
}

// Fichier est une preuve .ots complète.
type Fichier struct {
	OpFichier  byte   // hachage du fichier : OpSHA256 ici
	Condensat  []byte // condensat du fichier (ici : l'empreinte de tête du journal)
	Horodatage *Horodatage
}

// Encoder écrit le fichier .ots.
func (f *Fichier) Encoder() ([]byte, error) {
	if f.Horodatage == nil || !bytes.Equal(f.Horodatage.Message, f.Condensat) {
		return nil, errors.New("ots : l'horodatage ne porte pas sur le condensat")
	}
	b := append(slices.Clone(EnTeteOTS), VersionOTS, f.OpFichier)
	b = append(b, f.Condensat...)
	return f.Horodatage.encoder(b, 0)
}

// LireFichier lit un fichier .ots.
func LireFichier(donnees []byte) (*Fichier, error) {
	if len(donnees) > maxTailleOTS {
		return nil, errors.New("ots : fichier trop grand")
	}
	if !bytes.HasPrefix(donnees, EnTeteOTS) {
		return nil, errors.New("ots : en-tête magique absent")
	}
	l := &lecteur{b: donnees[len(EnTeteOTS):]}
	v, err := l.varuint()
	if err != nil {
		return nil, err
	}
	if v != VersionOTS {
		return nil, fmt.Errorf("ots : version %d inconnue", v)
	}
	op, err := l.octet()
	if err != nil {
		return nil, err
	}
	var taille int
	switch op {
	case OpSHA256, OpKeccak256:
		taille = 32
	case OpSHA1, OpRIPEMD160:
		taille = 20
	default:
		return nil, fmt.Errorf("ots : hachage du fichier 0x%02x inconnu", op)
	}
	c, err := l.lire(taille)
	if err != nil {
		return nil, err
	}
	h, err := l.horodatage(slices.Clone(c), 0)
	if err != nil {
		return nil, err
	}
	if len(l.b) != 0 {
		return nil, errors.New("ots : octets en trop en fin de fichier")
	}
	return &Fichier{OpFichier: op, Condensat: slices.Clone(c), Horodatage: h}, nil
}

type lecteur struct {
	b            []byte
	attestations int
}

func (l *lecteur) octet() (byte, error) {
	if len(l.b) == 0 {
		return 0, errors.New("ots : fin de données inattendue")
	}
	c := l.b[0]
	l.b = l.b[1:]
	return c, nil
}

func (l *lecteur) lire(n int) ([]byte, error) {
	if n < 0 || len(l.b) < n {
		return nil, errors.New("ots : fin de données inattendue")
	}
	r := l.b[:n]
	l.b = l.b[n:]
	return r, nil
}

// varuint : entier non signé LEB128 (7 bits par octet, poids faibles d'abord).
func (l *lecteur) varuint() (uint64, error) {
	var v uint64
	for dec := uint(0); ; dec += 7 {
		if dec > 63 {
			return 0, errors.New("ots : entier trop grand")
		}
		c, err := l.octet()
		if err != nil {
			return 0, err
		}
		v |= uint64(c&0x7f) << dec
		if c&0x80 == 0 {
			return v, nil
		}
	}
}

func (l *lecteur) varbytes(max int) ([]byte, error) {
	n, err := l.varuint()
	if err != nil {
		return nil, err
	}
	if n > uint64(max) {
		return nil, errors.New("ots : donnée trop longue")
	}
	return l.lire(int(n))
}

func (l *lecteur) horodatage(msg []byte, prof int) (*Horodatage, error) {
	if prof > maxProfondeur {
		return nil, errors.New("ots : horodatage trop profond")
	}
	h := &Horodatage{Message: msg}
	for {
		tag, err := l.octet()
		if err != nil {
			return nil, err
		}
		fourche := tag == marqueFourche
		if fourche {
			if tag, err = l.octet(); err != nil {
				return nil, err
			}
		}
		if err := l.element(h, tag, prof); err != nil {
			return nil, err
		}
		if !fourche {
			return h, nil
		}
	}
}

func (l *lecteur) element(h *Horodatage, tag byte, prof int) error {
	if tag == marqueAttestation {
		if l.attestations++; l.attestations > maxAttestation {
			return errors.New("ots : trop d'attestations")
		}
		t, err := l.lire(8)
		if err != nil {
			return err
		}
		c, err := l.varbytes(maxCharge)
		if err != nil {
			return err
		}
		a := Attestation{Charge: slices.Clone(c)}
		copy(a.Tag[:], t)
		if a.Tag == TagEnAttente {
			if _, ok := a.URI(); !ok {
				return errors.New("ots : URI de calendrier invalide")
			}
		}
		if a.Tag == TagBitcoin {
			if _, ok := a.Hauteur(); !ok {
				return errors.New("ots : attestation Bitcoin invalide")
			}
		}
		h.Attester(a)
		return nil
	}
	o := Op{Tag: tag}
	if o.binaire() {
		arg, err := l.varbytes(maxMessage)
		if err != nil {
			return err
		}
		if len(arg) == 0 {
			return errors.New("ots : argument vide")
		}
		o.Arg = slices.Clone(arg)
	}
	r, err := o.Appliquer(h.Message)
	if err != nil {
		return err
	}
	s, err := l.horodatage(r, prof+1)
	if err != nil {
		return err
	}
	for _, b := range h.Branches {
		if comparerOps(b.Op, o) == 0 {
			return b.Suite.Fusionner(s)
		}
	}
	h.Branches = append(h.Branches, Branche{Op: o, Suite: s})
	return nil
}

func varuint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func varbytes(b, d []byte) []byte { return append(varuint(b, uint64(len(d))), d...) }
