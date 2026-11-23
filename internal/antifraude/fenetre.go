package antifraude

import (
	"sync"
	"time"
)

// purgeMin est la taille à partir de laquelle la table d'une fenêtre est
// purgée de ses clés périmées ; la purge suivante attend que la table ait
// doublé, pour un coût amorti constant.
const purgeMin = 10_000

// Fenetre compte, en mémoire seulement, des événements par clé dans une
// fenêtre glissante de durée Duree. Une clé garde au plus Plafond dates : la
// mémoire reste bornée par le nombre de clés actives dans la fenêtre.
type Fenetre struct {
	Max     int           // seuil de Atteint
	Plafond int           // dates gardées par clé (au moins Max)
	Duree   time.Duration // largeur de la fenêtre

	mu         sync.Mutex
	evenements map[string][]time.Time
	seuilPurge int
}

// NouvelleFenetre renvoie une fenêtre vide ; plafond est ramené à max s'il
// est plus petit.
func NouvelleFenetre(max, plafond int, duree time.Duration) *Fenetre {
	if plafond < max {
		plafond = max
	}
	return &Fenetre{Max: max, Plafond: plafond, Duree: duree, evenements: map[string][]time.Time{}, seuilPurge: purgeMin}
}

// recents garde les événements encore dans la fenêtre (verrou tenu).
func (f *Fenetre) recents(cle string, t time.Time) []time.Time {
	es := f.evenements[cle]
	i := 0
	for i < len(es) && t.Sub(es[i]) >= f.Duree {
		i++
	}
	es = es[i:]
	if len(es) == 0 {
		delete(f.evenements, cle)
		return nil
	}
	f.evenements[cle] = es
	return es
}

// Compte renvoie le nombre d'événements de la clé dans la fenêtre (au plus
// Plafond).
func (f *Fenetre) Compte(cle string, t time.Time) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.recents(cle, t))
}

// Atteint indique si la clé compte déjà Max événements dans la fenêtre.
func (f *Fenetre) Atteint(cle string, t time.Time) bool {
	return f.Compte(cle, t) >= f.Max
}

// Ajouter enregistre un événement pour la clé et renvoie le nombre
// d'événements dans la fenêtre, celui-ci compris. Au plafond, l'événement
// n'allonge plus la liste et le compte reste au plafond.
func (f *Fenetre) Ajouter(cle string, t time.Time) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ajouter(cle, t)
}

// ajouter fait le travail de Ajouter (verrou tenu).
func (f *Fenetre) ajouter(cle string, t time.Time) int {
	if len(f.evenements) >= f.seuilPurge {
		for k := range f.evenements {
			f.recents(k, t)
		}
		f.seuilPurge = max(purgeMin, 2*len(f.evenements))
	}
	es := f.recents(cle, t)
	if len(es) >= f.Plafond {
		return len(es)
	}
	es = append(es, t)
	f.evenements[cle] = es
	return len(es)
}

// AjouterSiPossible enregistre un événement seulement si la clé n'a pas
// encore atteint Max ; il dit si l'événement a été accepté. Le test et
// l'ajout sont faits sous le même verrou.
func (f *Fenetre) AjouterSiPossible(cle string, t time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.recents(cle, t)) >= f.Max {
		return false
	}
	f.ajouter(cle, t)
	return true
}

// Effacer oublie les événements de la clé.
func (f *Fenetre) Effacer(cle string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.evenements, cle)
}

// Cles renvoie le nombre de clés en mémoire (tests et supervision).
func (f *Fenetre) Cles() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.evenements)
}
