// Package session garde en mémoire, et seulement en mémoire, l'état d'une
// personne pendant son parcours (spec §4) : l'empreinte de son numéro une fois
// vérifié. L'état expire après une période d'inactivité et est détruit à
// l'envoi. Le cookie ne porte qu'un identifiant aléatoire.
package session

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"sync"
	"time"

	"github.com/2guntan/diguee/internal/empreinte"
)

// NomCookie est le nom du cookie de session.
const NomCookie = "diguee_session"

// DureeInactivite est la durée après laquelle une session inutilisée expire.
const DureeInactivite = 30 * time.Minute

// Etat est ce que le serveur sait d'une session.
type Etat struct {
	Empreinte empreinte.Empreinte // empreinte du numéro vérifié
	vu        time.Time
}

// Magasin contient les sessions en cours.
type Magasin struct {
	Maintenant func() time.Time // time.Now par défaut
	Duree      time.Duration    // DureeInactivite par défaut

	mu       sync.Mutex
	sessions map[string]*Etat
}

// NouveauMagasin renvoie un magasin vide.
func NouveauMagasin() *Magasin {
	return &Magasin{Maintenant: time.Now, Duree: DureeInactivite, sessions: map[string]*Etat{}}
}

// Creer ouvre une session pour une empreinte vérifiée et pose le cookie.
func (m *Magasin) Creer(w http.ResponseWriter, e empreinte.Empreinte) error {
	aleas := make([]byte, 32)
	if _, err := rand.Read(aleas); err != nil {
		return err
	}
	id := base64.RawURLEncoding.EncodeToString(aleas)
	m.mu.Lock()
	maintenant := m.Maintenant()
	for k, s := range m.sessions { // purge des sessions expirées
		if maintenant.Sub(s.vu) > m.Duree {
			delete(m.sessions, k)
		}
	}
	m.sessions[id] = &Etat{Empreinte: e, vu: maintenant}
	m.mu.Unlock()
	http.SetCookie(w, cookie(id, 0))
	return nil
}

// Lire renvoie l'état de la session de la requête, s'il existe et n'a pas
// expiré, et prolonge la session.
func (m *Magasin) Lire(r *http.Request) (Etat, bool) {
	c, err := r.Cookie(NomCookie)
	if err != nil {
		return Etat{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[c.Value]
	if !ok {
		return Etat{}, false
	}
	maintenant := m.Maintenant()
	if maintenant.Sub(s.vu) > m.Duree {
		delete(m.sessions, c.Value)
		return Etat{}, false
	}
	s.vu = maintenant
	return *s, true
}

// Detruire supprime la session de la requête et efface le cookie.
func (m *Magasin) Detruire(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(NomCookie); err == nil {
		m.mu.Lock()
		delete(m.sessions, c.Value)
		m.mu.Unlock()
	}
	http.SetCookie(w, cookie("", -1))
}

// Nombre renvoie le nombre de sessions en mémoire (tests et métriques).
func (m *Magasin) Nombre() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

func cookie(valeur string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: NomCookie, Value: valeur, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	}
}
