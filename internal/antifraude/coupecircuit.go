package antifraude

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Raisons d'une suspension par le coupe-circuit.
const (
	// RaisonPic : le volume de l'heure dépasse Facteur fois la moyenne
	// horaire glissante (spec §7).
	RaisonPic = "pic"
	// RaisonPlafond : le volume de l'heure atteint le plafond absolu.
	RaisonPlafond = "plafond"
)

// OptionsCoupeCircuit règle un CoupeCircuit.
type OptionsCoupeCircuit struct {
	// Facteur : DIGUEE_SMS_COUPE_CIRCUIT (3x). Le circuit coupe quand le
	// volume de l'heure en cours dépasserait Facteur fois la moyenne horaire
	// des heures pleines précédentes (au plus 7 jours).
	Facteur float64
	// Plancher : DIGUEE_SMS_COUPE_CIRCUIT_PLANCHER (20). La règle du pic
	// n'agit qu'à partir de ce nombre d'envois dans l'heure.
	Plancher int
	// HistoriqueMin : heures pleines d'historique avant que la règle du pic
	// agisse (HistoriqueMin du paquet, 24, si nul) : au démarrage, la
	// moyenne n'a pas de sens.
	HistoriqueMin int
	// PlafondHoraire : DIGUEE_SMS_PAR_HEURE (100). Plafond absolu : il coupe
	// toujours, même sans historique.
	PlafondHoraire int
	// Reactivation : DIGUEE_SMS_REACTIVATION (1h). Délai après lequel le
	// circuit se referme de lui-même ; un administrateur peut le refermer
	// avant (espace équipe).
	Reactivation time.Duration
	Maintenant   func() time.Time // time.Now par défaut
	Journal      *slog.Logger     // slog.Default() par défaut
}

// CoupeCircuit est le coupe-circuit financier du parcours SMS (spec §5.3 et
// §7, ticket #10). Il compte les envois par heure, en mémoire seulement, et
// suspend le parcours :
//
//   - quand un envoi porterait le volume de l'heure au-delà de Facteur fois
//     la moyenne horaire glissante (heures pleines précédentes, au plus 7
//     jours, une heure sans envoi comptant pour zéro), avec au moins
//     HistoriqueMin heures d'historique et au moins Plancher envois dans
//     l'heure ;
//   - ou quand il dépasserait PlafondHoraire, quel que soit l'historique.
//
// L'envoi qui déclenche la coupure est refusé. Une fois coupé, Autoriser
// refuse tout envoi jusqu'à la réactivation : automatique après
// Reactivation, ou manuelle (Reactiver). L'alerte est un avertissement du
// journal qui ne contient que des nombres et la raison.
//
// Tout est en mémoire : un redémarrage efface l'historique (la règle du pic
// attend de nouveau HistoriqueMin heures, le plafond absolu reste actif) et
// referme le circuit.
type CoupeCircuit struct {
	opt   OptionsCoupeCircuit
	debut int64 // heure (Unix/3600) de création

	mu       sync.Mutex
	volume   serie
	suspendu bool
	raison   string
	depuis   time.Time
	jusqua   time.Time

	declenchements atomic.Int64
	refuses        atomic.Int64
}

// NouveauCoupeCircuit prépare un coupe-circuit fermé (envois permis).
func NouveauCoupeCircuit(opt OptionsCoupeCircuit) *CoupeCircuit {
	if opt.Maintenant == nil {
		opt.Maintenant = time.Now
	}
	if opt.Journal == nil {
		opt.Journal = slog.Default()
	}
	if opt.HistoriqueMin <= 0 {
		opt.HistoriqueMin = HistoriqueMin
	}
	h := heure(opt.Maintenant())
	return &CoupeCircuit{opt: opt, debut: h, volume: serie{derniere: h, alerteeHeure: -1}}
}

// EtatCoupeCircuit est l'instantané du coupe-circuit (espace équipe).
type EtatCoupeCircuit struct {
	Suspendu       bool
	Raison         string    // RaisonPic ou RaisonPlafond, si suspendu
	Depuis         time.Time // début de la suspension
	Jusqua         time.Time // réactivation automatique prévue
	HeureCourante  int64     // envois de l'heure en cours
	MoyenneHoraire float64   // moyenne des heures pleines précédentes
	Heures         int       // heures pleines d'historique (au plus 7 jours)
	Facteur        float64
	Plancher       int
	PlafondHoraire int
	Reactivation   time.Duration
	Declenchements int64 // coupures depuis le démarrage
	Refuses        int64 // envois refusés depuis le démarrage
}

// moyenne renvoie la moyenne horaire des heures pleines avant h et leur
// nombre. c.mu est tenu et c.volume avancé à h.
func (c *CoupeCircuit) moyenne(h int64) (float64, int64) {
	pleines := min(h-c.debut, HistoriqueMax)
	if pleines <= 0 {
		return 0, 0
	}
	n := int64(len(c.volume.heures))
	var somme int64
	for x := h - pleines; x < h; x++ {
		somme += c.volume.heures[x%n]
	}
	return float64(somme) / float64(pleines), pleines
}

// reactiverSiEchu referme le circuit si le délai est passé. c.mu est tenu.
func (c *CoupeCircuit) reactiverSiEchu(t time.Time) {
	if c.suspendu && !t.Before(c.jusqua) {
		c.suspendu = false
		c.opt.Journal.Info("coupe-circuit SMS : parcours SMS réactivé automatiquement", "raison", c.raison)
		c.raison = ""
	}
}

// Suspendu dit si le parcours est suspendu, sans rien compter.
func (c *CoupeCircuit) Suspendu() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reactiverSiEchu(c.opt.Maintenant())
	return c.suspendu
}

// Autoriser dit si un envoi peut partir maintenant et, si oui, le compte.
// Il coupe le circuit (et refuse l'envoi) si cet envoi déclenchait l'une
// des deux règles.
func (c *CoupeCircuit) Autoriser() bool {
	t := c.opt.Maintenant()
	h := heure(t)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reactiverSiEchu(t)
	if c.suspendu {
		c.refuses.Add(1)
		return false
	}
	c.volume.avancer(h)
	n := int64(len(c.volume.heures))
	apres := c.volume.heures[h%n] + 1
	moyenne, pleines := c.moyenne(h)
	switch {
	case c.opt.PlafondHoraire > 0 && apres > int64(c.opt.PlafondHoraire):
		c.couper(t, RaisonPlafond, apres-1, moyenne)
		return false
	case pleines >= int64(c.opt.HistoriqueMin) && apres >= int64(c.opt.Plancher) &&
		float64(apres) > c.opt.Facteur*moyenne:
		c.couper(t, RaisonPic, apres-1, moyenne)
		return false
	}
	c.volume.heures[h%n] = apres
	c.volume.total++
	return true
}

// couper suspend le parcours et émet l'alerte. c.mu est tenu.
func (c *CoupeCircuit) couper(t time.Time, raison string, courant int64, moyenne float64) {
	c.suspendu, c.raison, c.depuis, c.jusqua = true, raison, t, t.Add(c.opt.Reactivation)
	c.declenchements.Add(1)
	c.refuses.Add(1)
	// Alerte : aucune donnée individuelle, seulement des nombres.
	c.opt.Journal.Warn("alerte : coupe-circuit SMS déclenché, parcours SMS suspendu",
		"raison", raison, "envois_heure_courante", courant, "moyenne_horaire", moyenne,
		"facteur", c.opt.Facteur, "plafond_horaire", c.opt.PlafondHoraire,
		"reactivation_automatique", c.jusqua.UTC().Format(time.RFC3339))
}

// Reactiver referme le circuit à la main (action d'un administrateur). Il
// dit si le parcours était suspendu.
func (c *CoupeCircuit) Reactiver() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.suspendu {
		return false
	}
	c.suspendu, c.raison = false, ""
	c.opt.Journal.Info("coupe-circuit SMS : parcours SMS réactivé à la main")
	return true
}

// Etat renvoie l'instantané du coupe-circuit.
func (c *CoupeCircuit) Etat() EtatCoupeCircuit {
	t := c.opt.Maintenant()
	h := heure(t)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reactiverSiEchu(t)
	c.volume.avancer(h)
	moyenne, pleines := c.moyenne(h)
	e := EtatCoupeCircuit{
		Suspendu: c.suspendu, HeureCourante: c.volume.heures[h%int64(len(c.volume.heures))],
		MoyenneHoraire: moyenne, Heures: int(pleines),
		Facteur: c.opt.Facteur, Plancher: c.opt.Plancher, PlafondHoraire: c.opt.PlafondHoraire,
		Reactivation:   c.opt.Reactivation,
		Declenchements: c.declenchements.Load(), Refuses: c.refuses.Load(),
	}
	if c.suspendu {
		e.Raison, e.Depuis, e.Jusqua = c.raison, c.depuis, c.jusqua
	}
	return e
}
