// Package journal tient le Journal public (spec §10, ADR 0010) : la suite
// chaînée de tout ce que Diguee publie ou change.
//
// Chaque entrée contient un numéro (1, 2, 3…), un horodatage à la minute (UTC),
// un type, un contenu (un objet JSON) et l'empreinte de l'entrée précédente :
//
//	H(entrée_n) = SHA-256(JSON canonique de
//	  {"contenu":…,"empreinte_precedente":H(entrée_n-1),"horodatage":…,"numero":n,"type":…})
//
// Le JSON canonique est celui de la RFC 8785, restreint aux entiers (voir
// Canonique). L'empreinte précédente de la première entrée est faite de 32
// octets nuls. Les empreintes s'écrivent en hexadécimal minuscule et
// l'horodatage sous la forme 2026-11-23T06:00:00Z (secondes toujours nulles).
//
// Le journal est en ajout seul : le paquet n'offre aucune modification ni
// suppression, et la base les refuse elle aussi (migration 0002).
//
// Anonymat : une entrée ne contient que des agrégats, jamais une réponse, une
// empreinte de numéro ou une donnée d'une personne. Et puisque son horodatage
// est public, une entrée n'est jamais écrite à cause d'un envoi ou d'un lot du
// tampon : seulement à heure fixe (publication quotidienne) ou sur une action
// de l'équipe ou de l'exploitation (statut, méthodologie, mise en production…).
package journal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"
)

// Type est le type d'une entrée.
type Type string

// Types d'entrées (spec §10).
const (
	PublicationQuotidienne Type = "publication_quotidienne" // agrégats du jour et empreinte de l'instantané
	StatutPublie           Type = "statut_publie"           // statut d'un engagement dans l'observatoire
	DroitDeReponse         Type = "droit_de_reponse"        // droit de réponse publié
	MiseEnProduction       Type = "mise_en_production"      // version du code et SHA-256 de l'exécutable
	Methodologie           Type = "methodologie"            // modification de la méthodologie ou de ses paramètres
	Ouverture              Type = "ouverture"               // ouverture de la consultation
	Cloture                Type = "cloture"                 // clôture de la consultation
	ProcesVerbal           Type = "proces_verbal"           // procès-verbal de destruction
)

// Types renvoie les types connus, dans l'ordre de la spec.
func Types() []Type {
	return []Type{PublicationQuotidienne, StatutPublie, DroitDeReponse, MiseEnProduction,
		Methodologie, Ouverture, Cloture, ProcesVerbal}
}

// Connu indique si le type fait partie de la liste de la spec.
func (t Type) Connu() bool { return slices.Contains(Types(), t) }

var reType = regexp.MustCompile(`^[a-z][a-z_]*$`)

// Empreinte est un SHA-256.
type Empreinte [sha256.Size]byte

// String écrit l'empreinte en hexadécimal minuscule.
func (e Empreinte) String() string { return hex.EncodeToString(e[:]) }

// LireEmpreinte lit une empreinte en hexadécimal (64 caractères minuscules).
func LireEmpreinte(s string) (Empreinte, error) {
	var e Empreinte
	if len(s) != 2*len(e) || s != string(bytes.ToLower([]byte(s))) {
		return e, fmt.Errorf("empreinte %q : 64 caractères hexadécimaux minuscules attendus", s)
	}
	if _, err := hex.Decode(e[:], []byte(s)); err != nil {
		return e, fmt.Errorf("empreinte %q : %w", s, err)
	}
	return e, nil
}

// FormatHorodatage est l'écriture de l'horodatage (UTC, à la minute).
const FormatHorodatage = "2006-01-02T15:04:00Z"

// Entree est une entrée du journal.
type Entree struct {
	Numero              int64
	Horodatage          time.Time // UTC, à la minute
	Type                Type
	Contenu             json.RawMessage // objet JSON, sous forme canonique
	EmpreintePrecedente Empreinte
	Empreinte           Empreinte // H(entrée), calculée par Sceller
}

// Hachee est la partie hachée d'une entrée, telle qu'elle est canonisée.
type hachee struct {
	Numero              int64           `json:"numero"`
	Horodatage          string          `json:"horodatage"`
	Type                Type            `json:"type"`
	Contenu             json.RawMessage `json:"contenu"`
	EmpreintePrecedente string          `json:"empreinte_precedente"`
}

// JSONCanonique renvoie les octets hachés de l'entrée : le JSON canonique de
// ses cinq champs (le champ Empreinte n'en fait pas partie).
func (e Entree) JSONCanonique() ([]byte, error) {
	if e.Numero < 1 {
		return nil, fmt.Errorf("numéro %d : les numéros commencent à 1", e.Numero)
	}
	if !reType.MatchString(string(e.Type)) {
		return nil, fmt.Errorf("type %q invalide", e.Type)
	}
	if h := e.Horodatage; h.Location() != time.UTC || !h.Equal(h.Truncate(time.Minute)) {
		return nil, fmt.Errorf("horodatage %v : UTC à la minute attendu", h)
	}
	if t := bytes.TrimSpace(e.Contenu); len(t) == 0 || t[0] != '{' {
		return nil, errors.New("le contenu doit être un objet JSON")
	}
	return CanoniqueDe(hachee{
		Numero: e.Numero, Horodatage: e.Horodatage.Format(FormatHorodatage), Type: e.Type,
		Contenu: e.Contenu, EmpreintePrecedente: e.EmpreintePrecedente.String(),
	})
}

// Calculer renvoie H(entrée), le SHA-256 de son JSON canonique.
func (e Entree) Calculer() (Empreinte, error) {
	c, err := e.JSONCanonique()
	if err != nil {
		return Empreinte{}, err
	}
	return sha256.Sum256(c), nil
}

// Suivante prépare l'entrée qui suit derniere (nil pour la première) :
// numéro, empreinte précédente, horodatage tronqué à la minute, contenu mis
// sous forme canonique, empreinte calculée. Si l'horloge recule, l'horodatage
// reprend celui de l'entrée précédente, pour que le journal reste dans l'ordre.
func Suivante(derniere *Entree, maintenant time.Time, t Type, contenu json.RawMessage) (*Entree, error) {
	c, err := Canonique(contenu)
	if err != nil {
		return nil, err
	}
	e := &Entree{Numero: 1, Horodatage: maintenant.UTC().Truncate(time.Minute), Type: t, Contenu: c}
	if derniere != nil {
		e.Numero = derniere.Numero + 1
		e.EmpreintePrecedente = derniere.Empreinte
		if e.Horodatage.Before(derniere.Horodatage) {
			e.Horodatage = derniere.Horodatage
		}
	}
	if e.Empreinte, err = e.Calculer(); err != nil {
		return nil, err
	}
	return e, nil
}

// Vue donne accès, pendant une écriture et sous le même verrou, aux entrées
// déjà inscrites.
type Vue interface {
	// Derniere renvoie la dernière entrée, ou nil si le journal est vide.
	Derniere(ctx context.Context) (*Entree, error)
	// DerniereDuType renvoie la dernière entrée du type, ou nil.
	DerniereDuType(ctx context.Context, t Type) (*Entree, error)
}

// Magasin conserve les entrées. Implémenté par base.Base (PostgreSQL) et, pour
// les tests, par journaltest.Memoire.
type Magasin interface {
	// AjouterAuJournal appelle preparer sous un verrou d'écriture exclusif,
	// puis inscrit l'entrée renvoyée, dans la même transaction. Si preparer
	// renvoie nil, rien n'est inscrit et AjouterAuJournal renvoie nil.
	AjouterAuJournal(ctx context.Context, preparer func(context.Context, Vue) (*Entree, error)) (*Entree, error)
	// CompterJournal renvoie le nombre d'entrées, qui est aussi le dernier numéro.
	CompterJournal(ctx context.Context) (int64, error)
	// LireJournal renvoie, dans l'ordre, les entrées de numéro premier à
	// dernier compris.
	LireJournal(ctx context.Context, premier, dernier int64) ([]Entree, error)
}

// Journal est l'API d'écriture et de lecture du journal public.
type Journal struct {
	m          Magasin
	maintenant func() time.Time
}

// Nouveau renvoie le journal tenu par le magasin. maintenant vaut time.Now par
// défaut.
func Nouveau(m Magasin, maintenant func() time.Time) *Journal {
	if maintenant == nil {
		maintenant = time.Now
	}
	return &Journal{m: m, maintenant: maintenant}
}

// Inscrire ajoute une entrée du type donné. contenu est encodé en JSON (une
// structure, une map ou un json.RawMessage) et doit donner un objet, sans
// nombre à virgule. Deux appels simultanés, ici ou dans un autre processus,
// sont inscrits l'un après l'autre sans jamais casser la chaîne.
//
// Les entrées de la consultation ne contiennent que des agrégats : un contenu
// qui ressemble à un numéro de téléphone ou à une adresse IP est refusé.
func (j *Journal) Inscrire(ctx context.Context, t Type, contenu any) (Entree, error) {
	e, err := j.inscrireSi(ctx, t, contenu, nil)
	if err != nil {
		return Entree{}, err
	}
	return *e, nil
}

// InscrireSi ajoute une entrée du type donné si condition, appelée sous le
// verrou d'écriture du journal, renvoie vrai : la vérification et l'écriture
// ne peuvent pas être séparées par une autre écriture, même d'un autre
// processus. Elle renvoie nil (et aucune erreur) si rien n'est inscrit.
func (j *Journal) InscrireSi(ctx context.Context, t Type, contenu any, condition func(context.Context, Vue) (bool, error)) (*Entree, error) {
	if condition == nil {
		return nil, errors.New("journal : condition absente")
	}
	return j.inscrireSi(ctx, t, contenu, condition)
}

// ContenuMiseEnProduction est le contenu d'une entrée « mise en production ».
type ContenuMiseEnProduction struct {
	Version          string `json:"version"`
	SHA256Executable string `json:"sha256_executable"`
}

// InscrireMiseEnProduction inscrit une entrée « mise en production » si la
// dernière entrée de ce type ne porte pas déjà cette version et ce SHA-256 :
// un redémarrage sur la même version n'inscrit rien. Un retour à une version
// antérieure est une nouvelle mise en production, donc inscrit. La
// vérification et l'écriture se font sous le même verrou : deux démarrages
// simultanés n'inscrivent qu'une entrée.
func (j *Journal) InscrireMiseEnProduction(ctx context.Context, version, sha256Executable string) (e Entree, inscrite bool, err error) {
	if version == "" {
		return Entree{}, false, errors.New("mise en production : version vide")
	}
	if _, err := LireEmpreinte(sha256Executable); err != nil {
		return Entree{}, false, fmt.Errorf("mise en production : SHA-256 de l'exécutable : %w", err)
	}
	c := ContenuMiseEnProduction{Version: version, SHA256Executable: sha256Executable}
	var deja *Entree
	ins, err := j.inscrireSi(ctx, MiseEnProduction, c, func(ctx context.Context, v Vue) (bool, error) {
		d, err := v.DerniereDuType(ctx, MiseEnProduction)
		if err != nil || d == nil {
			return d == nil, err
		}
		var prec ContenuMiseEnProduction
		if err := json.Unmarshal(d.Contenu, &prec); err != nil {
			return false, fmt.Errorf("entrée %d : %w", d.Numero, err)
		}
		if prec == c {
			deja = d
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return Entree{}, false, err
	}
	if ins == nil {
		return *deja, false, nil
	}
	return *ins, true, nil
}

func (j *Journal) inscrireSi(ctx context.Context, t Type, contenu any,
	condition func(context.Context, Vue) (bool, error)) (*Entree, error) {
	if !t.Connu() {
		return nil, fmt.Errorf("journal : type %q inconnu", t)
	}
	brut, err := CanoniqueDe(contenu)
	if err != nil {
		return nil, fmt.Errorf("journal : contenu : %w", err)
	}
	if len(brut) == 0 || brut[0] != '{' {
		return nil, errors.New("journal : le contenu doit être un objet JSON")
	}
	if err := verifierAgregats(brut); err != nil {
		return nil, err
	}
	return j.m.AjouterAuJournal(ctx, func(ctx context.Context, v Vue) (*Entree, error) {
		if condition != nil {
			ok, err := condition(ctx, v)
			if err != nil || !ok {
				return nil, err
			}
		}
		d, err := v.Derniere(ctx)
		if err != nil {
			return nil, err
		}
		return Suivante(d, j.maintenant(), t, brut)
	})
}

var (
	// Numéro mobile sénégalais, avec ou sans indicatif ni séparateurs.
	reTelephone = regexp.MustCompile(`(?:^|[^\w])(?:\+?221[ .-]?)?7[05-8](?:[ .-]?\d){7}(?:$|[^\w])`)
	reIPv4      = regexp.MustCompile(`(?:^|[^\w.])(?:\d{1,3}\.){3}\d{1,3}(?:$|[^\w.])`)
)

// verifierAgregats refuse un contenu où traîne une donnée personnelle
// évidente : numéro de téléphone ou adresse IPv4 (spec E2, E6). Ce n'est qu'un
// garde-fou : c'est à l'appelant de n'écrire que des agrégats.
func verifierAgregats(canonique []byte) error {
	var v any
	if err := json.Unmarshal(canonique, &v); err != nil {
		return err
	}
	var err error
	var parcourir func(x any)
	parcourir = func(x any) {
		switch y := x.(type) {
		case map[string]any:
			for k, z := range y {
				parcourir(k)
				parcourir(z)
			}
		case []any:
			for _, z := range y {
				parcourir(z)
			}
		case string:
			if err == nil && (reTelephone.MatchString(y) || reIPv4.MatchString(y)) {
				err = errors.New("journal : le contenu ressemble à un numéro de téléphone ou à une adresse IP ; seuls des agrégats sont admis")
			}
		}
	}
	parcourir(v)
	return err
}

// Compter renvoie le nombre d'entrées.
func (j *Journal) Compter(ctx context.Context) (int64, error) { return j.m.CompterJournal(ctx) }

// Lire renvoie les entrées de numéro premier à dernier compris, dans l'ordre.
func (j *Journal) Lire(ctx context.Context, premier, dernier int64) ([]Entree, error) {
	return j.m.LireJournal(ctx, premier, dernier)
}

// Toutes renvoie toutes les entrées, dans l'ordre.
func (j *Journal) Toutes(ctx context.Context) ([]Entree, error) {
	return j.m.LireJournal(ctx, 1, 1<<62)
}

// EstHex indique si s n'est fait que de chiffres hexadécimaux minuscules.
func EstHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return s != ""
}
