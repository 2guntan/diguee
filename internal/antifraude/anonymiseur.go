package antifraude

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/netip"
	"sync"
	"time"
)

// Anonymiseur remplace une adresse par un HMAC-SHA-256 dont le sel, tiré au
// hasard et gardé en mémoire seulement, change chaque jour (UTC, spec §7) :
// l'adresse elle-même n'est ni conservée, ni journalisée, et deux jours ne
// peuvent pas être rapprochés. La valeur zéro est prête à l'emploi.
type Anonymiseur struct {
	mu   sync.Mutex
	jour string
	sel  []byte
}

// Cle renvoie le HMAC de l'adresse pour le jour de t. adresse est l'adresse
// de la connexion TCP (http.Request.RemoteAddr), port compris ou non ; les
// en-têtes comme X-Forwarded-For ne sont jamais lus, car aucun proxy ne se
// place devant le serveur (ADR 0007). Une adresse IPv6 est réduite à son
// préfixe /64, qu'un abonné reçoit en entier : sinon, changer d'adresse dans
// son propre préfixe suffirait à échapper au compteur.
func (a *Anonymiseur) Cle(adresse string, t time.Time) string {
	h := Hote(adresse)
	a.mu.Lock()
	defer a.mu.Unlock()
	if j := t.UTC().Format(time.DateOnly); j != a.jour || a.sel == nil {
		a.jour, a.sel = j, make([]byte, 32)
		_, _ = rand.Read(a.sel)
	}
	m := hmac.New(sha256.New, a.sel)
	m.Write([]byte(h))
	return hex.EncodeToString(m.Sum(nil))
}

// Hote retire le port d'une adresse de connexion et réduit une adresse IPv6
// à son préfixe /64. Une adresse illisible est rendue telle quelle.
func Hote(adresse string) string {
	h := adresse
	if hh, _, err := net.SplitHostPort(adresse); err == nil {
		h = hh
	}
	ip, err := netip.ParseAddr(h)
	if err != nil {
		return h
	}
	ip = ip.Unmap()
	if ip.Is6() {
		p, err := ip.Prefix(64)
		if err == nil {
			return p.String()
		}
	}
	return ip.String()
}
