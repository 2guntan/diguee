package equipe

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// Limiteur compte, en mémoire seulement, les échecs de connexion par clé dans
// une fenêtre glissante. Au-delà de Max échecs dans la fenêtre, la clé est
// bloquée jusqu'à ce que le plus ancien sorte de la fenêtre.
type Limiteur struct {
	Max     int
	Fenetre time.Duration

	mu     sync.Mutex
	echecs map[string][]time.Time
}

// NouveauLimiteur renvoie un limiteur vide.
func NouveauLimiteur(max int, fenetre time.Duration) *Limiteur {
	return &Limiteur{Max: max, Fenetre: fenetre, echecs: map[string][]time.Time{}}
}

// recents garde les échecs encore dans la fenêtre (verrou tenu).
func (l *Limiteur) recents(cle string, t time.Time) []time.Time {
	es := l.echecs[cle]
	i := 0
	for i < len(es) && t.Sub(es[i]) >= l.Fenetre {
		i++
	}
	es = es[i:]
	if len(es) == 0 {
		delete(l.echecs, cle)
		return nil
	}
	l.echecs[cle] = es
	return es
}

// Bloque indique si la clé a atteint le nombre maximal d'échecs.
func (l *Limiteur) Bloque(cle string, t time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recents(cle, t)) >= l.Max
}

// Echec enregistre un échec pour la clé.
func (l *Limiteur) Echec(cle string, t time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.echecs) > 10_000 { // purge, pour que la mémoire reste bornée
		for k := range l.echecs {
			l.recents(k, t)
		}
	}
	es := l.recents(cle, t)
	if len(es) >= l.Max { // déjà bloquée : inutile d'allonger la liste
		return
	}
	l.echecs[cle] = append(es, t)
}

// Effacer oublie les échecs de la clé (après une connexion réussie).
func (l *Limiteur) Effacer(cle string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.echecs, cle)
}

// selSource remplace l'adresse de la requête par un HMAC dont le sel, tiré au
// hasard et gardé en mémoire, change chaque jour (spec §7) : l'adresse
// elle-même n'est ni conservée, ni journalisée.
type selSource struct {
	mu   sync.Mutex
	jour string
	sel  []byte
}

func (s *selSource) cle(adresse string, t time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j := t.UTC().Format(time.DateOnly); j != s.jour || s.sel == nil {
		s.jour, s.sel = j, make([]byte, 32)
		_, _ = rand.Read(s.sel)
	}
	m := hmac.New(sha256.New, s.sel)
	m.Write([]byte(adresse))
	return hex.EncodeToString(m.Sum(nil))
}
