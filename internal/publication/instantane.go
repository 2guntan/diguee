// Package publication produit la Publication quotidienne (spec §8, ADR
// 0010) : chaque jour à DIGUEE_HEURE_PUBLICATION, un instantané figé des
// résultats, calculé sur les réponses dont le jour est antérieur au jour de
// publication, puis inscrit au journal public avec ses totaux et son
// empreinte.
//
// Règles appliquées (spec §8) :
//
//   - en dessous du Seuil de publication, l'instantané ne contient que le
//     compteur global, sans aucune répartition ;
//   - au-delà, chaque tableau croise au plus deux critères : une question
//     seule, un critère du profil seul, une question × un critère du profil,
//     ou deux critères du profil ; jamais deux questions, jamais trois
//     critères ;
//   - une case de moins de SeuilCase réponses est masquée (« moins de 30
//     réponses ») ; si une seule case d'une rangée est masquée, la plus
//     petite des autres l'est aussi (masquage complémentaire) ;
//   - les cases sont des pourcentages de la rangée, arrondis à l'unité ; seuls
//     les effectifs des rangées et le total sont donnés, arrondis à la
//     dizaine inférieure (voir Arrondir) ;
//   - une région de moins de SeuilCase réponses est regroupée dans « Autres
//     régions » ;
//   - pas de pondération : l'instantané donne la composition de l'échantillon
//     et, quand elle est fournie, celle de la population d'après l'ANSD.
//
// Aucune réponse individuelle n'est jamais exportée : l'instantané ne
// contient que des agrégats, datés au jour.
package publication

import (
	"strconv"
	"time"
)

// FormatInstantane est la version du format JSON de l'instantané.
const FormatInstantane = 1

// Catégories particulières des rangées et des colonnes.
const (
	Ensemble      = "ensemble"       // rangée unique d'un tableau à un seul critère
	AutresRegions = "autres_regions" // régions de moins de SeuilCase réponses, regroupées
)

// Raisons du masquage d'une case ou d'un effectif.
const (
	MasqueSeuil          = "moins_de_seuil" // moins de SeuilCase réponses
	MasqueComplementaire = "complementaire" // masquée pour qu'on ne retrouve pas une case voisine par différence
)

// Instantane est le contenu figé d'une publication, publié tel quel en JSON
// (seulement des entiers, des booléens et des chaînes).
type Instantane struct {
	Format int `json:"format"`
	// Jour est le jour de publication (AAAA-MM-JJ) ; l'instantané porte sur
	// les réponses écrites jusqu'à DernierJourInclus, la veille.
	Jour                 string `json:"jour"`
	DernierJourInclus    string `json:"dernier_jour_inclus"`
	VersionQuestionnaire int16  `json:"version_questionnaire"`
	SeuilPublication     int    `json:"seuil_publication"`
	SeuilCase            int    `json:"seuil_case"`
	// Reponses est le compteur global, arrondi à la dizaine inférieure.
	Reponses int `json:"reponses"`
	// ResultatsPublies : le Seuil de publication est atteint, les tableaux
	// suivent. Sinon, rien d'autre que le compteur n'est publié.
	ResultatsPublies bool `json:"resultats_publies"`
	// Ponderation vaut toujours « aucune » : les résultats sont bruts.
	Ponderation string `json:"ponderation,omitempty"`
	// AutresRegions liste les codes des régions regroupées.
	AutresRegions []string `json:"autres_regions,omitempty"`
	// SourcePopulation cite la source des chiffres de population ; vide tant
	// qu'ils ne sont pas fournis (DIGUEE_POPULATION_ANSD).
	SourcePopulation string `json:"source_population,omitempty"`
	// Composition de l'échantillon : un tableau par critère du profil, avec
	// la part de la population quand elle est connue.
	Composition []Tableau `json:"composition,omitempty"`
	// Tableaux : chaque question seule, puis croisée avec chaque critère, puis
	// les critères croisés deux à deux.
	Tableaux []Tableau `json:"tableaux,omitempty"`
}

// Tableau répartit les réponses selon un critère en colonnes et, au plus, un
// critère en rangées.
type Tableau struct {
	Colonnes string `json:"colonnes"`         // question ou critère réparti en colonnes
	Lignes   string `json:"lignes,omitempty"` // critère réparti en rangées ; vide : une seule rangée « ensemble »
	// ChoixMultiples : chaque case est la part des répondants de la rangée qui
	// ont choisi l'option (Q7, thèmes) ; le total dépasse 100.
	ChoixMultiples bool     `json:"choix_multiples,omitempty"`
	Options        []string `json:"options"` // codes des colonnes, dans l'ordre (« 0 » : je ne sais pas, ou préfère ne pas répondre)
	Rangees        []Rangee `json:"rangees"`
	// Population : part de chaque option dans la population (ANSD), en
	// pourcentage arrondi ; null si sans objet ou non fournie. Seulement dans
	// la composition de l'échantillon.
	Population []*int `json:"population,omitempty"`
}

// Rangee est une rangée d'un tableau.
type Rangee struct {
	Categorie string `json:"categorie"` // code de la catégorie, Ensemble ou AutresRegions
	// Effectif : nombre de réponses de la rangée, arrondi à la dizaine
	// inférieure ; null si la rangée compte moins de SeuilCase réponses.
	Effectif *int   `json:"effectif"`
	Cases    []Case `json:"cases"`
}

// Case est une case d'un tableau : un pourcentage, ou une raison de masquage.
type Case struct {
	Pourcentage *int   `json:"pourcentage,omitempty"`
	Masque      string `json:"masque,omitempty"`
}

// Masquee indique que la case n'est pas chiffrée.
func (c Case) Masquee() bool { return c.Masque != "" }

// Arrondir arrondit un effectif à la dizaine inférieure. Le même arrondi sert
// partout (compteur, effectifs des rangées) : deux arrondis différents d'un
// même nombre en diraient plus long que chacun d'eux. Vers le bas, le
// compteur n'annonce jamais plus de réponses qu'il n'y en a, ni le Seuil de
// publication avant qu'il soit atteint.
func Arrondir(n int) int { return n / 10 * 10 }

// Pourcentage arrondit 100 × c / n à l'unité la plus proche (0,5 vers le
// haut), en calcul entier.
func Pourcentage(c, n int) int {
	if n <= 0 {
		return 0
	}
	return (200*c + n) / (2 * n)
}

// Jour renvoie le jour (minuit UTC) de t : l'heure de Dakar est GMT.
func Jour(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// JourDu renvoie le jour de la dernière publication due à l'instant t, avec
// une publication par jour à l'heure donnée (depuis minuit UTC) : à 06:00
// le 24, c'est le 24 ; à 05:59, encore le 23.
func JourDu(t time.Time, heure time.Duration) time.Time {
	return Jour(t.UTC().Add(-heure))
}

// Prochaine renvoie l'instant de la première publication due après t.
func Prochaine(t time.Time, heure time.Duration) time.Time {
	return JourDu(t, heure).AddDate(0, 0, 1).Add(heure)
}

func code(c int16) string { return strconv.Itoa(int(c)) }
