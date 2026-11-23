package antifraude

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"
)

// Réglages du ralentissement au-delà de la limite par adresse.
const (
	// PasRalentissement est le délai ajouté par demande au-delà de la limite :
	// la 31ᵉ demande de l'heure attend 0,5 s, la 32ᵉ 1 s, etc.
	PasRalentissement = 500 * time.Millisecond
	// RalentissementMax borne le délai : bien en dessous du délai d'écriture
	// du serveur (30 s), la demande aboutit toujours.
	RalentissementMax = 10 * time.Second
	// FenetreAdresse est la fenêtre de la limite par adresse.
	FenetreAdresse = time.Hour
)

// OptionsRalentisseur règle un Ralentisseur.
type OptionsRalentisseur struct {
	ParHeure   int              // DIGUEE_IP_PAR_HEURE : 30
	Maintenant func() time.Time // time.Now par défaut
	// Attendre patiente d, ou moins si la requête est abandonnée. Les tests
	// en injectent une qui ne dort pas.
	Attendre func(ctx context.Context, d time.Duration)
	Journal  *slog.Logger // slog.Default() par défaut
}

// Ralentisseur applique la limite par adresse (spec §7) : au-delà de
// ParHeure demandes dans l'heure glissante depuis une même adresse, chaque
// demande attend un délai qui croît de PasRalentissement par demande en trop,
// borné à RalentissementMax, et une alerte est journalisée. Aucune demande
// n'est bloquée : derrière le CGNAT des opérateurs, des centaines de
// personnes partagent une adresse.
//
// L'adresse est celle de la connexion TCP (aucun proxy devant le serveur,
// ADR 0007 ; X-Forwarded-For n'est jamais lu), remplacée aussitôt par un
// HMAC à sel quotidien (Anonymiseur). Le compteur est en mémoire seulement.
// L'alerte ne contient ni l'adresse, ni son HMAC.
type Ralentisseur struct {
	opt      OptionsRalentisseur
	adresses Anonymiseur
	fenetre  *Fenetre

	ralenties atomic.Int64
	alertes   atomic.Int64
}

// NouveauRalentisseur prépare un ralentisseur.
func NouveauRalentisseur(opt OptionsRalentisseur) *Ralentisseur {
	if opt.ParHeure <= 0 {
		opt.ParHeure = 30
	}
	if opt.Maintenant == nil {
		opt.Maintenant = time.Now
	}
	if opt.Attendre == nil {
		opt.Attendre = attendre
	}
	if opt.Journal == nil {
		opt.Journal = slog.Default()
	}
	paliers := int(RalentissementMax / PasRalentissement)
	return &Ralentisseur{opt: opt, fenetre: NouvelleFenetre(opt.ParHeure, opt.ParHeure+paliers+1, FenetreAdresse)}
}

func attendre(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// Delai renvoie le délai d'une demande qui est la n-ième de l'heure.
func (r *Ralentisseur) Delai(n int) time.Duration {
	trop := n - r.opt.ParHeure
	if trop <= 0 {
		return 0
	}
	return min(time.Duration(trop)*PasRalentissement, RalentissementMax)
}

// Freiner compte la demande, la fait attendre si l'adresse a dépassé la
// limite, et renvoie le délai appliqué. Il n'empêche jamais la demande.
func (r *Ralentisseur) Freiner(req *http.Request) time.Duration {
	n := r.fenetre.Ajouter(r.adresses.Cle(req.RemoteAddr, r.opt.Maintenant()), r.opt.Maintenant())
	d := r.Delai(n)
	if d == 0 {
		return 0
	}
	r.ralenties.Add(1)
	if n == r.opt.ParHeure+1 {
		// Une alerte par adresse quand elle franchit la limite, pas une par
		// demande : le journal reste lisible pendant une attaque.
		r.alertes.Add(1)
		r.opt.Journal.Warn("anti-fraude : une adresse dépasse la limite de demandes par heure ; demandes ralenties",
			"limite_par_heure", r.opt.ParHeure)
	}
	r.opt.Attendre(req.Context(), d)
	return d
}

// Ralenties renvoie le nombre de demandes ralenties depuis le démarrage.
func (r *Ralentisseur) Ralenties() int64 { return r.ralenties.Load() }

// Alertes renvoie le nombre d'alertes émises depuis le démarrage.
func (r *Ralentisseur) Alertes() int64 { return r.alertes.Load() }
