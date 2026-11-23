package equipe

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"sync"
	"time"
)

// NomCookie est le cookie de session de l'espace équipe, limité à /equipe.
const NomCookie = "diguee_equipe"

// DureeSession est la durée d'une session, fixe depuis la connexion (spec §9).
const DureeSession = 8 * time.Hour

// sessionEquipe est l'état d'une session, en mémoire seulement : un
// redémarrage du serveur ferme toutes les sessions.
type sessionEquipe struct {
	membre string
	csrf   string
	expire time.Time
}

// Sessions contient les sessions ouvertes de l'espace équipe.
type Sessions struct {
	Maintenant func() time.Time
	Duree      time.Duration

	mu       sync.Mutex
	sessions map[string]*sessionEquipe
}

// NouvellesSessions renvoie un magasin vide.
func NouvellesSessions(maintenant func() time.Time) *Sessions {
	if maintenant == nil {
		maintenant = time.Now
	}
	return &Sessions{Maintenant: maintenant, Duree: DureeSession, sessions: map[string]*sessionEquipe{}}
}

func aleatoire() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Ouvrir crée une session neuve pour le membre et pose le cookie. Une
// session que la requête portait déjà est fermée : l'identifiant change à
// chaque connexion.
func (s *Sessions) Ouvrir(w http.ResponseWriter, r *http.Request, membreID string) error {
	id, err := aleatoire()
	if err != nil {
		return err
	}
	csrf, err := aleatoire()
	if err != nil {
		return err
	}
	s.mu.Lock()
	if c, err := r.Cookie(NomCookie); err == nil {
		delete(s.sessions, c.Value)
	}
	t := s.Maintenant()
	for k, v := range s.sessions {
		if !t.Before(v.expire) {
			delete(s.sessions, k)
		}
	}
	s.sessions[id] = &sessionEquipe{membre: membreID, csrf: csrf, expire: t.Add(s.Duree)}
	s.mu.Unlock()
	http.SetCookie(w, cookieEquipe(NomCookie, id, int(s.Duree/time.Second)))
	return nil
}

// Lire renvoie le membre et le jeton CSRF de la session de la requête, si
// elle existe et n'a pas expiré. La session n'est pas prolongée.
func (s *Sessions) Lire(r *http.Request) (membreID, csrf string, ok bool) {
	c, err := r.Cookie(NomCookie)
	if err != nil {
		return "", "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.sessions[c.Value]
	if !ok {
		return "", "", false
	}
	if !s.Maintenant().Before(v.expire) {
		delete(s.sessions, c.Value)
		return "", "", false
	}
	return v.membre, v.csrf, true
}

// Fermer supprime la session de la requête et efface le cookie.
func (s *Sessions) Fermer(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(NomCookie); err == nil {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, cookieEquipe(NomCookie, "", -1))
}

// FermerMembre ferme toutes les sessions du membre et renvoie leur nombre.
func (s *Sessions) FermerMembre(membreID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for k, v := range s.sessions {
		if v.membre == membreID {
			delete(s.sessions, k)
			n++
		}
	}
	return n
}

// Nombre renvoie le nombre de sessions ouvertes du membre (tests).
func (s *Sessions) Nombre(membreID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, v := range s.sessions {
		if v.membre == membreID {
			n++
		}
	}
	return n
}

func cookieEquipe(nom, valeur string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: nom, Value: valeur, Path: "/equipe", MaxAge: maxAge,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	}
}

// egal compare deux jetons en temps constant ; un jeton vide n'est jamais égal.
func egal(a, b string) bool {
	return a != "" && b != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
