// Package questionnaire décrit le questionnaire de la consultation (spec §3),
// versionné, et lit les réponses envoyées par le formulaire.
//
// Chaque choix a un code numérique, stocké dans la table reponse, et une clé
// du catalogue de langue pour son libellé : aucun texte visible n'est ici. Les
// définitions des versions sont dans v1.go (et les suivantes).
package questionnaire

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
)

// Type d'une question.
type Type int

const (
	Unique   Type = iota // un seul choix (boutons radio)
	Multiple             // un ou plusieurs choix (cases à cocher)
	Themes               // exactement NbThemes choix, ou « Je ne sais pas » seul
)

// NbThemes est le nombre de thèmes prioritaires à choisir (Q8).
const NbThemes = 3

// Partie du questionnaire où figure une question.
type Partie int

const (
	PartieQuestions Partie = iota
	PartieProfil
)

// Choix est une réponse possible à une question.
type Choix struct {
	Code int16  // valeur stockée
	Cle  string // clé du libellé dans le catalogue
	// Exclusif : ce choix (« Je ne sais pas ») ne se combine avec aucun autre.
	// Dans une question à choix multiple, il est stocké comme un tableau vide.
	Exclusif bool
}

// Question est une question du questionnaire ou un critère du profil.
type Question struct {
	Nom    string // nom du champ du formulaire et de la colonne, par exemple « q1 »
	Cle    string // clé du libellé dans le catalogue
	Aide   string // clé d'une indication sous le libellé ; facultative
	Type   Type
	Partie Partie
	Choix  []Choix
	// Provisoire : les choix restent à valider par le porteur (spec §15.1).
	// Le serveur refuse de démarrer en production tant qu'il en reste un.
	Provisoire bool
}

// Questionnaire est une version figée du questionnaire.
type Questionnaire struct {
	Version   int16
	Questions []Question
	// CleMajeur est la clé du libellé de la déclaration « 18 ans ou plus »,
	// obligatoire et jamais stockée.
	CleMajeur string
}

// ChampMajeur est le nom du champ de la déclaration « 18 ans ou plus ».
const ChampMajeur = "majeur"

// Reponse est le contenu d'un questionnaire rempli, sans aucun identifiant de
// la personne. Les tableaux vides signifient « Je ne sais pas ».
type Reponse struct {
	Version                           int16
	Q1, Q2, Q3, Q4, Q5, Q6            int16
	Q7                                []int16
	Themes                            []int16
	Region, TrancheAge, Sexe, Inscrit int16
}

// Erreurs associe au nom d'un champ la clé du message d'erreur à afficher.
type Erreurs map[string]string

// Clés des messages d'erreur, définies dans le catalogue.
const (
	ErrObligatoire = "erreur.questionnaire.obligatoire"
	ErrInvalide    = "erreur.questionnaire.invalide"
	ErrExclusif    = "erreur.questionnaire.exclusif"
	ErrThemes      = "erreur.questionnaire.themes"
	ErrMajeur      = "erreur.questionnaire.majeur"
)

// Question renvoie la question nommée, ou nil.
func (q *Questionnaire) Question(nom string) *Question {
	for i := range q.Questions {
		if q.Questions[i].Nom == nom {
			return &q.Questions[i]
		}
	}
	return nil
}

// Provisoires décrit les questions dont les choix restent à valider ; le
// paquet garde refuse la production tant que la liste n'est pas vide.
func (q *Questionnaire) Provisoires() []string {
	var ps []string
	for _, x := range q.Questions {
		if x.Provisoire {
			ps = append(ps, fmt.Sprintf("questionnaire v%d : les choix de %s sont provisoires "+
				"(catalogue %s.*, internal/questionnaire/v%d.go)", q.Version, x.Nom, x.Cle, q.Version))
		}
	}
	return ps
}

// Codes renvoie les codes choisis pour une question dans le formulaire, sans
// les valider.
func codes(form url.Values, nom string) ([]int16, bool) {
	var cs []int16
	for _, v := range form[nom] {
		n, err := strconv.ParseInt(v, 10, 16)
		if err != nil {
			return nil, false
		}
		if !slices.Contains(cs, int16(n)) {
			cs = append(cs, int16(n))
		}
	}
	return cs, true
}

// Lire valide le formulaire envoyé et renvoie la réponse, ou les erreurs par
// champ. Toutes les questions sont obligatoires : chacune propose « Je ne sais
// pas » ou « Je préfère ne pas répondre ».
func (q *Questionnaire) Lire(form url.Values) (Reponse, Erreurs) {
	r := Reponse{Version: q.Version}
	errs := Erreurs{}
	for _, x := range q.Questions {
		cs, ok := codes(form, x.Nom)
		if !ok {
			errs[x.Nom] = ErrInvalide
			continue
		}
		valeurs, cle := x.lire(cs)
		if cle != "" {
			errs[x.Nom] = cle
			continue
		}
		r.affecter(x.Nom, valeurs)
	}
	if form.Get(ChampMajeur) != "1" {
		errs[ChampMajeur] = ErrMajeur
	}
	if len(errs) > 0 {
		return Reponse{}, errs
	}
	return r, nil
}

// lire contrôle les codes choisis pour la question et renvoie les valeurs à
// stocker, ou la clé d'une erreur.
func (x Question) lire(cs []int16) ([]int16, string) {
	if len(cs) == 0 {
		return nil, ErrObligatoire
	}
	exclusif := false
	for _, c := range cs {
		ch := x.choix(c)
		if ch == nil {
			return nil, ErrInvalide
		}
		exclusif = exclusif || ch.Exclusif
	}
	switch x.Type {
	case Unique:
		if len(cs) != 1 {
			return nil, ErrInvalide
		}
		return cs, ""
	default:
		if exclusif {
			if len(cs) != 1 {
				return nil, ErrExclusif
			}
			return []int16{}, ""
		}
		if x.Type == Themes && len(cs) != NbThemes {
			return nil, ErrThemes
		}
		slices.Sort(cs)
		return cs, ""
	}
}

func (x Question) choix(code int16) *Choix {
	for i := range x.Choix {
		if x.Choix[i].Code == code {
			return &x.Choix[i]
		}
	}
	return nil
}

func (r *Reponse) affecter(nom string, v []int16) {
	switch nom {
	case "q1":
		r.Q1 = v[0]
	case "q2":
		r.Q2 = v[0]
	case "q3":
		r.Q3 = v[0]
	case "q4":
		r.Q4 = v[0]
	case "q5":
		r.Q5 = v[0]
	case "q6":
		r.Q6 = v[0]
	case "q7":
		r.Q7 = v
	case "themes":
		r.Themes = v
	case "region":
		r.Region = v[0]
	case "tranche_age":
		r.TrancheAge = v[0]
	case "sexe":
		r.Sexe = v[0]
	case "inscrit":
		r.Inscrit = v[0]
	default:
		panic("questionnaire : champ inconnu " + nom)
	}
}

// Valeurs renvoie, pour chaque champ, les codes choisis tels que le formulaire
// les envoie (un tableau vide redevient le code exclusif). Elle sert à
// préremplir le formulaire et à porter la réponse dans le récapitulatif.
func (q *Questionnaire) Valeurs(r Reponse) url.Values {
	v := url.Values{}
	for _, x := range q.Questions {
		var cs []int16
		switch x.Nom {
		case "q1":
			cs = []int16{r.Q1}
		case "q2":
			cs = []int16{r.Q2}
		case "q3":
			cs = []int16{r.Q3}
		case "q4":
			cs = []int16{r.Q4}
		case "q5":
			cs = []int16{r.Q5}
		case "q6":
			cs = []int16{r.Q6}
		case "q7":
			cs = r.Q7
		case "themes":
			cs = r.Themes
		case "region":
			cs = []int16{r.Region}
		case "tranche_age":
			cs = []int16{r.TrancheAge}
		case "sexe":
			cs = []int16{r.Sexe}
		case "inscrit":
			cs = []int16{r.Inscrit}
		}
		if len(cs) == 0 {
			for _, c := range x.Choix {
				if c.Exclusif {
					cs = []int16{c.Code}
				}
			}
		}
		for _, c := range cs {
			v.Add(x.Nom, strconv.Itoa(int(c)))
		}
	}
	v.Set(ChampMajeur, "1")
	return v
}
