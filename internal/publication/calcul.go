package publication

import (
	"slices"
	"time"

	"github.com/2guntan/diguee/internal/questionnaire"
)

// Critère du profil dont les catégories de moins de SeuilCase réponses sont
// regroupées (spec §8, règle 5).
const critereRegion = "region"

// Parametres règle le calcul d'un instantané.
type Parametres struct {
	Jour             time.Time // jour de publication
	SeuilPublication int       // DIGUEE_SEUIL_PUBLICATION
	SeuilCase        int       // DIGUEE_SEUIL_CASE
	Population       *Population
}

// dimension est une question ou un critère du profil.
type dimension struct {
	nom      string
	critere  bool // critère du profil
	multiple bool // plusieurs choix par réponse (Q7, thèmes)
	codes    []int16
}

// Agregateur compte les réponses, une par une, sans jamais en garder une :
// seuls des compteurs par catégorie (un critère) et par paire de catégories
// (deux critères) sont tenus.
type Agregateur struct {
	q     *questionnaire.Questionnaire
	dims  []dimension
	total int
	// simple[d][code] : réponses qui ont la catégorie code pour d (pour un
	// choix multiple : qui ont choisi l'option).
	simple []map[int16]int
	// croise[paire][codes] : pour une rangée (critère) et une colonne
	// (question ou critère), réponses qui ont les deux catégories.
	croise map[paire]map[[2]int16]int
	paires []paire
}

// paire est un croisement de deux dimensions : la première en rangées (un
// critère du profil), la seconde en colonnes. Il n'existe aucun croisement de
// trois dimensions.
type paire struct{ ligne, colonne int }

// NouvelAgregateur prépare le comptage pour un questionnaire.
func NouvelAgregateur(q *questionnaire.Questionnaire) *Agregateur {
	a := &Agregateur{q: q, croise: map[paire]map[[2]int16]int{}}
	for _, x := range q.Questions {
		d := dimension{nom: x.Nom, critere: x.Partie == questionnaire.PartieProfil, multiple: x.Type != questionnaire.Unique}
		for _, c := range x.Choix {
			d.codes = append(d.codes, c.Code)
		}
		a.dims = append(a.dims, d)
		a.simple = append(a.simple, map[int16]int{})
	}
	var criteres, questions []int
	for i, d := range a.dims {
		if d.critere {
			criteres = append(criteres, i)
		} else {
			questions = append(questions, i)
		}
	}
	// Une question × un critère, puis deux critères entre eux (spec §8, règle 1).
	for _, qi := range questions {
		for _, ci := range criteres {
			a.paires = append(a.paires, paire{ci, qi})
		}
	}
	for i, ci := range criteres {
		for _, cj := range criteres[i+1:] {
			a.paires = append(a.paires, paire{ci, cj})
		}
	}
	for _, p := range a.paires {
		a.croise[p] = map[[2]int16]int{}
	}
	return a
}

// valeurs renvoie les codes d'une réponse pour une dimension ; un choix
// multiple vide (« Je ne sais pas ») vaut le code exclusif 0.
func valeurs(r *questionnaire.Reponse, nom string) []int16 {
	var v int16
	switch nom {
	case "q1":
		v = r.Q1
	case "q2":
		v = r.Q2
	case "q3":
		v = r.Q3
	case "q4":
		v = r.Q4
	case "q5":
		v = r.Q5
	case "q6":
		v = r.Q6
	case "q7", "themes":
		cs := r.Q7
		if nom == "themes" {
			cs = r.Themes
		}
		if len(cs) == 0 {
			return []int16{questionnaire.CodeNSP}
		}
		return cs
	case "region":
		v = r.Region
	case "tranche_age":
		v = r.TrancheAge
	case "sexe":
		v = r.Sexe
	case "inscrit":
		v = r.Inscrit
	default:
		panic("publication : dimension inconnue " + nom)
	}
	return []int16{v}
}

// Ajouter compte une réponse. Une réponse d'une autre version du
// questionnaire est ignorée (ses codes pourraient différer).
func (a *Agregateur) Ajouter(r questionnaire.Reponse) {
	if r.Version != a.q.Version {
		return
	}
	a.total++
	vs := make([][]int16, len(a.dims))
	for i, d := range a.dims {
		vs[i] = valeurs(&r, d.nom)
		for _, c := range vs[i] {
			a.simple[i][c]++
		}
	}
	for _, p := range a.paires {
		m := a.croise[p]
		l := vs[p.ligne][0] // un critère n'a qu'une valeur
		for _, c := range vs[p.colonne] {
			m[[2]int16{l, c}]++
		}
	}
}

// Total renvoie le nombre exact de réponses comptées (jamais publié tel quel).
func (a *Agregateur) Total() int { return a.total }

// categorie est une colonne ou une rangée : un code, ou plusieurs régions
// regroupées.
type categorie struct {
	nom   string
	codes []int16
}

// categories renvoie les catégories d'une dimension, dans l'ordre du
// questionnaire ; pour la région, les régions regroupées forment « Autres
// régions », placée avant « Je préfère ne pas répondre ».
func (a *Agregateur) categories(d int, regroupees []int16) []categorie {
	var cs []categorie
	var autres []int16
	for _, c := range a.dims[d].codes {
		if a.dims[d].nom == critereRegion && slices.Contains(regroupees, c) {
			autres = append(autres, c)
			continue
		}
		if c == questionnaire.CodeRefus && len(autres) > 0 && a.dims[d].critere {
			cs = append(cs, categorie{AutresRegions, autres})
			autres = nil
		}
		cs = append(cs, categorie{code(c), []int16{c}})
	}
	if len(autres) > 0 {
		cs = append(cs, categorie{AutresRegions, autres})
	}
	return cs
}

// regroupees renvoie les régions (et la diaspora) de moins de seuil réponses
// au total (spec §8, règle 5). « Je préfère ne pas répondre » n'est pas une
// région et n'est jamais regroupé.
func (a *Agregateur) regroupees(seuil int) []int16 {
	var rs []int16
	for i, d := range a.dims {
		if d.nom != critereRegion {
			continue
		}
		for _, c := range d.codes {
			if c != questionnaire.CodeRefus && a.simple[i][c] < seuil {
				rs = append(rs, c)
			}
		}
	}
	return rs
}

// Instantane calcule l'instantané publié. Il ne contient que des agrégats
// masqués et arrondis.
func (a *Agregateur) Instantane(p Parametres) *Instantane {
	jour := Jour(p.Jour)
	inst := &Instantane{
		Format: FormatInstantane, Jour: jour.Format(time.DateOnly),
		DernierJourInclus:    jour.AddDate(0, 0, -1).Format(time.DateOnly),
		VersionQuestionnaire: a.q.Version, SeuilPublication: p.SeuilPublication, SeuilCase: p.SeuilCase,
		Reponses: Arrondir(a.total), ResultatsPublies: a.total >= p.SeuilPublication,
	}
	if !inst.ResultatsPublies {
		return inst // le compteur seul, sans aucune répartition
	}
	inst.Ponderation = "aucune"
	regroupees := a.regroupees(p.SeuilCase)
	for _, c := range regroupees {
		inst.AutresRegions = append(inst.AutresRegions, code(c))
	}
	if p.Population != nil {
		inst.SourcePopulation = p.Population.Source
	}
	for d, dim := range a.dims {
		if dim.critere {
			t := a.tableau(-1, d, regroupees, p.SeuilCase)
			t.Population = a.population(d, regroupees, p.Population)
			inst.Composition = append(inst.Composition, t)
		}
	}
	for d, dim := range a.dims {
		if dim.critere {
			continue
		}
		inst.Tableaux = append(inst.Tableaux, a.tableau(-1, d, regroupees, p.SeuilCase))
		for _, pr := range a.paires {
			if pr.colonne == d {
				inst.Tableaux = append(inst.Tableaux, a.tableau(pr.ligne, d, regroupees, p.SeuilCase))
			}
		}
	}
	for _, pr := range a.paires {
		if a.dims[pr.colonne].critere {
			inst.Tableaux = append(inst.Tableaux, a.tableau(pr.ligne, pr.colonne, regroupees, p.SeuilCase))
		}
	}
	return inst
}

// tableau construit le tableau de la dimension col, réparti selon la
// dimension ligne (-1 : une seule rangée, l'ensemble des réponses).
func (a *Agregateur) tableau(ligne, col int, regroupees []int16, seuil int) Tableau {
	t := Tableau{Colonnes: a.dims[col].nom, ChoixMultiples: a.dims[col].multiple}
	colonnes := a.categories(col, regroupees)
	for _, c := range colonnes {
		t.Options = append(t.Options, c.nom)
	}
	if ligne < 0 {
		cs := make([]int, len(colonnes))
		for k, c := range colonnes {
			for _, x := range c.codes {
				cs[k] += a.simple[col][x]
			}
		}
		t.Rangees = []Rangee{masquer(Ensemble, a.total, cs, t.ChoixMultiples, seuil)}
		return t
	}
	t.Lignes = a.dims[ligne].nom
	m := a.croise[paire{ligne, col}]
	for _, l := range a.categories(ligne, regroupees) {
		n := 0
		for _, x := range l.codes {
			n += a.simple[ligne][x]
		}
		cs := make([]int, len(colonnes))
		for k, c := range colonnes {
			for _, x := range l.codes {
				for _, y := range c.codes {
					cs[k] += m[[2]int16{x, y}]
				}
			}
		}
		t.Rangees = append(t.Rangees, masquer(l.nom, n, cs, t.ChoixMultiples, seuil))
	}
	return t
}

// masquer applique à une rangée de n réponses, de comptes cs, les règles 2 à
// 4 de la spec §8.
//
// Pour un choix multiple, chaque case est une option choisie ou non : elle
// est masquée si les répondants qui l'ont choisie, ou ceux qui ne l'ont pas
// choisie, sont moins de seuil (sinon « 99 % » d'une rangée de 40 dirait
// qu'une seule personne ne l'a pas choisie).
func masquer(cat string, n int, cs []int, multiple bool, seuil int) Rangee {
	r := Rangee{Categorie: cat, Cases: make([]Case, len(cs))}
	if n >= seuil {
		e := Arrondir(n)
		r.Effectif = &e
	}
	masquees := 0
	for k, c := range cs {
		if c < seuil || (multiple && n-c < seuil) {
			r.Cases[k].Masque = MasqueSeuil
			masquees++
		}
	}
	if masquees == 1 {
		// Masquage complémentaire : la plus petite des autres cases.
		plusPetite := -1
		for k, c := range cs {
			if !r.Cases[k].Masquee() && (plusPetite < 0 || c < cs[plusPetite]) {
				plusPetite = k
			}
		}
		if plusPetite >= 0 {
			r.Cases[plusPetite].Masque = MasqueComplementaire
		}
	}
	for k, c := range cs {
		if !r.Cases[k].Masquee() {
			p := Pourcentage(c, n)
			r.Cases[k].Pourcentage = &p
		}
	}
	return r
}

// population renvoie, pour la composition d'un critère, la part de chaque
// catégorie dans la population, ou nil si elle n'est pas connue.
func (a *Agregateur) population(d int, regroupees []int16, pop *Population) []*int {
	if pop == nil {
		return nil
	}
	effectifs, ok := pop.Effectifs[a.dims[d].nom]
	if !ok {
		return nil
	}
	total := int64(0)
	for _, n := range effectifs {
		total += n
	}
	var parts []*int
	for _, c := range a.categories(d, regroupees) {
		var n int64
		connue := false
		for _, x := range c.codes {
			if v, ok := effectifs[x]; ok {
				n += v
				connue = true
			}
		}
		if !connue || total == 0 {
			parts = append(parts, nil)
			continue
		}
		p := int((200*n + total) / (2 * total))
		parts = append(parts, &p)
	}
	return parts
}
