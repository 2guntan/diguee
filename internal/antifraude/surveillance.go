package antifraude

import (
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
)

// Dimensions des métriques agrégées (spec §7, « Surveillance »).
const (
	// DimensionPrefixe compte les numéros acceptés par préfixe (« 77 »), au
	// moment où le numéro est contrôlé, avant tout calcul d'empreinte.
	DimensionPrefixe = "prefixe"
	// DimensionRegion compte les réponses par code de région déclaré, au
	// moment où le tampon écrit un lot (jamais à l'envoi d'une réponse : le
	// mélange du tampon, ADR 0003, reste entier).
	DimensionRegion = "region"
)

// Règle du pic anormal, simple et publiée :
//
//	une série (dimension, valeur) est en pic quand le nombre de l'heure
//	courante dépasse FacteurPic fois la moyenne horaire des heures pleines
//	précédentes (au plus HistoriqueMax, soit 7 jours, depuis le démarrage),
//	avec au moins HistoriqueMin heures pleines d'historique et au moins
//	PicMin événements dans l'heure.
//
// Une heure sans événement compte pour zéro dans la moyenne. Une alerte au
// plus par série et par heure est journalisée ; elle ne contient que la
// dimension, la valeur (un préfixe ou un code de région) et des nombres.
const (
	FacteurPic     = 3.0
	PicMin         = 20
	HistoriqueMin  = 24
	HistoriqueMax  = 7 * 24
	maxSeries      = 256 // les valeurs sont des listes fermées ; garde-fou
	dureeHeure     = time.Hour
	longueurValeur = 16
)

// Surveillance tient les métriques agrégées par heure, en mémoire seulement.
// Elle ne reçoit jamais de donnée individuelle : seulement une dimension et
// une valeur d'une liste fermée (préfixe autorisé, code de région).
type Surveillance struct {
	maintenant func() time.Time
	journal    *slog.Logger
	debut      int64 // heure (Unix/3600) de création

	mu     sync.Mutex
	series map[cleSerie]*serie
	pics   int64
}

type cleSerie struct{ dimension, valeur string }

type serie struct {
	heures       [HistoriqueMax + 1]int64 // anneau indexé par heure % len
	derniere     int64                    // dernière heure mise à jour
	total        int64
	alerteeHeure int64 // dernière heure qui a donné une alerte, -1 sinon
}

// NouvelleSurveillance prépare une surveillance vide ; maintenant vaut
// time.Now et journal slog.Default() si nil.
func NouvelleSurveillance(maintenant func() time.Time, journal *slog.Logger) *Surveillance {
	if maintenant == nil {
		maintenant = time.Now
	}
	if journal == nil {
		journal = slog.Default()
	}
	return &Surveillance{maintenant: maintenant, journal: journal, debut: heure(maintenant()), series: map[cleSerie]*serie{}}
}

func heure(t time.Time) int64 { return t.Unix() / int64(dureeHeure/time.Second) }

// avancer remet à zéro les heures écoulées depuis la dernière mise à jour.
func (s *serie) avancer(h int64) {
	if h <= s.derniere {
		return
	}
	for x := s.derniere + 1; x <= h && x <= s.derniere+int64(len(s.heures)); x++ {
		s.heures[x%int64(len(s.heures))] = 0
	}
	s.derniere = h
}

// Compter ajoute un événement à la série (dimension, valeur) pour l'heure
// courante, puis applique la règle du pic anormal.
func (s *Surveillance) Compter(dimension, valeur string) {
	if dimension == "" || valeur == "" || len(valeur) > longueurValeur {
		return
	}
	h := heure(s.maintenant())
	s.mu.Lock()
	defer s.mu.Unlock()
	k := cleSerie{dimension, valeur}
	se, ok := s.series[k]
	if !ok {
		if len(s.series) >= maxSeries {
			return
		}
		se = &serie{derniere: h, alerteeHeure: -1}
		s.series[k] = se
	}
	se.avancer(h)
	n := int64(len(se.heures))
	se.heures[h%n]++
	se.total++
	courant := se.heures[h%n]

	pleines := min(h-s.debut, HistoriqueMax)
	if pleines < HistoriqueMin || courant < PicMin || se.alerteeHeure == h {
		return
	}
	var somme int64
	for x := h - pleines; x < h; x++ {
		somme += se.heures[x%n]
	}
	moyenne := float64(somme) / float64(pleines)
	if float64(courant) <= FacteurPic*moyenne {
		return
	}
	se.alerteeHeure = h
	s.pics++
	s.journal.Warn("anti-fraude : pic anormal", "dimension", dimension, "valeur", valeur,
		"heure_courante", courant, "moyenne_horaire", moyenne, "facteur", FacteurPic)
}

// Serie est l'instantané d'une série agrégée.
type Serie struct {
	Dimension, Valeur string
	HeureCourante     int64 // événements de l'heure en cours
	Total             int64 // depuis le démarrage
}

// Instantane renvoie les séries, triées par dimension puis valeur.
func (s *Surveillance) Instantane() []Serie {
	h := heure(s.maintenant())
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Serie, 0, len(s.series))
	for k, se := range s.series {
		se.avancer(h)
		out = append(out, Serie{Dimension: k.dimension, Valeur: k.valeur, HeureCourante: se.heures[h%int64(len(se.heures))], Total: se.total})
	}
	slices.SortFunc(out, func(a, b Serie) int {
		if c := strings.Compare(a.Dimension, b.Dimension); c != 0 {
			return c
		}
		return strings.Compare(a.Valeur, b.Valeur)
	})
	return out
}

// Pics renvoie le nombre d'alertes de pic depuis le démarrage.
func (s *Surveillance) Pics() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pics
}
