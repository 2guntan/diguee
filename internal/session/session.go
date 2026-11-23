// Package session garde en mémoire, et seulement en mémoire, l'état d'une
// personne pendant son parcours (spec §4) : ouverte avant la vérification
// (vérification inversée), puis l'empreinte de son numéro une fois vérifié. L'état expire après une période d'inactivité et est détruit à
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
	Verifiee  bool                // le numéro est vérifié
	Empreinte empreinte.Empreinte // empreinte du numéro vérifié (si Verifiee)
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
	_, err := m.nouvelle(w, Etat{Verifiee: true, Empreinte: e})
	return err
}

// Ouvrir renvoie l'identifiant de la session de la requête, ou en ouvre une
// nouvelle, non vérifiée, et pose le cookie. La vérification inversée lie
// son jeton à cet identifiant.
func (m *Magasin) Ouvrir(w http.ResponseWriter, r *http.Request) (string, error) {
	if id, ok := m.ID(r); ok {
		return id, nil
	}
	return m.nouvelle(w, Etat{})
}

// ID renvoie l'identifiant de la session de la requête, si elle existe et n'a
// pas expiré, et prolonge la session.
func (m *Magasin) ID(r *http.Request) (string, bool) {
	c, err := r.Cookie(NomCookie)
	if err != nil {
		return "", false
	}
	if _, ok := m.lire(c.Value); !ok {
		return "", false
	}
	return c.Value, true
}

// Verifier marque la session id comme vérifiée pour l'empreinte e. Il renvoie
// false si la session n'existe plus.
func (m *Magasin) Verifier(id string, e empreinte.Empreinte) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok || m.Maintenant().Sub(s.vu) > m.Duree {
		return false
	}
	s.Verifiee, s.Empreinte = true, e
	return true
}

// Renouveler donne un nouvel identifiant à la session de la requête, en
// gardant son état, et pose le nouveau cookie. Il sert quand la session
// devient vérifiée hors de la requête du navigateur (webhook) : l'ancien
// identifiant ne vaut plus rien.
func (m *Magasin) Renouveler(w http.ResponseWriter, r *http.Request) error {
	c, err := r.Cookie(NomCookie)
	if err != nil {
		return err
	}
	m.mu.Lock()
	s, ok := m.sessions[c.Value]
	delete(m.sessions, c.Value)
	m.mu.Unlock()
	if !ok {
		return http.ErrNoCookie
	}
	_, err = m.nouvelle(w, Etat{Verifiee: s.Verifiee, Empreinte: s.Empreinte})
	return err
}

func (m *Magasin) nouvelle(w http.ResponseWriter, e Etat) (string, error) {
	aleas := make([]byte, 32)
	if _, err := rand.Read(aleas); err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(aleas)
	m.mu.Lock()
	maintenant := m.Maintenant()
	for k, s := range m.sessions { // purge des sessions expirées
		if maintenant.Sub(s.vu) > m.Duree {
			delete(m.sessions, k)
		}
	}
	e.vu = maintenant
	m.sessions[id] = &e
	m.mu.Unlock()
	http.SetCookie(w, cookie(id, 0))
	return id, nil
}

// Lire renvoie l'état de la session de la requête, s'il existe et n'a pas
// expiré, et prolonge la session.
func (m *Magasin) Lire(r *http.Request) (Etat, bool) {
	c, err := r.Cookie(NomCookie)
	if err != nil {
		return Etat{}, false
	}
	return m.lire(c.Value)
}

func (m *Magasin) lire(id string) (Etat, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return Etat{}, false
	}
	maintenant := m.Maintenant()
	if maintenant.Sub(s.vu) > m.Duree {
		delete(m.sessions, id)
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
