// Package tampon garde les envois en mémoire et les écrit par lots mélangés
// (ADR 0003, spec §6.2).
//
// Un envoi est une participation (l'empreinte) et une réponse, qui ne sont
// jamais reliées l'une à l'autre. Le tampon est écrit en une seule
// transaction dès qu'il compte Taille envois, ou au plus tard Delai après le
// premier envoi, et à l'arrêt propre (Fermer). Les participations et les
// réponses sont mélangées séparément avec le générateur aléatoire
// cryptographique, de sorte que l'ordre d'écriture ne dise rien de l'ordre
// d'arrivée.
//
// Le serveur tourne sur une seule instance (ADR 0003) : la détection des
// doublons consulte la base puis le tampon, sous le même verrou que
// l'écriture d'un lot, pour qu'une empreinte ne puisse pas passer entre les
// deux.
package tampon

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"math/big"
	"strconv"
	"sync"
	"time"

	"github.com/2guntan/diguee/internal/empreinte"
	"github.com/2guntan/diguee/internal/questionnaire"
)

// ErrDejaParticipe signale une empreinte déjà en base ou dans le tampon.
var ErrDejaParticipe = errors.New("ce numéro a déjà participé")

// ErrFerme signale un envoi après l'arrêt du tampon.
var ErrFerme = errors.New("tampon fermé")

// Ecrivain est la base de données vue par le tampon.
type Ecrivain interface {
	ParticipationExiste(ctx context.Context, e empreinte.Empreinte) (bool, error)
	EcrireLot(ctx context.Context, jour time.Time, empreintes []empreinte.Empreinte, reponses []questionnaire.Reponse) error
}

// Minuterie lance f après d et renvoie une fonction qui l'annule
// (time.AfterFunc en pratique ; les tests en injectent une manuelle).
type Minuterie func(d time.Duration, f func()) (annuler func() bool)

// Options règle le tampon.
type Options struct {
	Ecrivain   Ecrivain
	Taille     int           // DIGUEE_TAMPON_TAILLE : 20
	Delai      time.Duration // DIGUEE_TAMPON_DELAI : 30m
	Maintenant func() time.Time
	Minuterie  Minuterie
	Journal    *slog.Logger
	// DelaiEcriture borne la durée d'une écriture déclenchée par la minuterie.
	DelaiEcriture time.Duration
	// Surveillance reçoit, après l'écriture de chaque lot, le code de région
	// de chaque réponse du lot (antifraude.Surveillance, spec §7). Compter au
	// moment du lot, et non de l'envoi, ne crée aucun lien entre une réponse
	// et le moment où elle a été donnée. Facultative.
	Surveillance Surveillance
}

// Surveillance reçoit les métriques agrégées (antifraude.Surveillance).
type Surveillance interface {
	Compter(dimension, valeur string)
}

// DimensionRegion est la dimension des métriques par région
// (antifraude.DimensionRegion).
const DimensionRegion = "region"

// Tampon est sûr pour un usage concurrent.
type Tampon struct {
	opt Options

	mu         sync.Mutex
	empreintes []empreinte.Empreinte
	presentes  map[empreinte.Empreinte]struct{}
	reponses   []questionnaire.Reponse
	annuler    func() bool
	generation uint64 // invalide les minuteries d'un lot déjà écrit
	ferme      bool
}

// Nouveau renvoie un tampon vide.
func Nouveau(opt Options) *Tampon {
	if opt.Taille <= 0 {
		opt.Taille = 20
	}
	if opt.Delai <= 0 {
		opt.Delai = 30 * time.Minute
	}
	if opt.Maintenant == nil {
		opt.Maintenant = time.Now
	}
	if opt.Minuterie == nil {
		opt.Minuterie = func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }
	}
	if opt.Journal == nil {
		opt.Journal = slog.Default()
	}
	if opt.DelaiEcriture <= 0 {
		opt.DelaiEcriture = time.Minute
	}
	return &Tampon{opt: opt, presentes: map[empreinte.Empreinte]struct{}{}}
}

// DejaParticipe indique si l'empreinte est en base ou dans le tampon.
func (t *Tampon) DejaParticipe(ctx context.Context, e empreinte.Empreinte) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dejaParticipe(ctx, e)
}

func (t *Tampon) dejaParticipe(ctx context.Context, e empreinte.Empreinte) (bool, error) {
	if existe, err := t.opt.Ecrivain.ParticipationExiste(ctx, e); err != nil || existe {
		return existe, err
	}
	_, ok := t.presentes[e]
	return ok, nil
}

// Ajouter place un envoi dans le tampon. Elle renvoie ErrDejaParticipe si
// l'empreinte est déjà en base ou dans le tampon. Si le tampon atteint sa
// taille, il est écrit aussitôt ; en cas d'échec de l'écriture, l'envoi reste
// accepté et le lot sera réessayé.
func (t *Tampon) Ajouter(ctx context.Context, e empreinte.Empreinte, r questionnaire.Reponse) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ferme {
		return ErrFerme
	}
	deja, err := t.dejaParticipe(ctx, e)
	if err != nil {
		return err
	}
	if deja {
		return ErrDejaParticipe
	}
	t.empreintes = append(t.empreintes, e)
	t.presentes[e] = struct{}{}
	t.reponses = append(t.reponses, r)
	if len(t.empreintes) >= t.opt.Taille {
		if err := t.vider(ctx); err != nil {
			t.opt.Journal.Error("écriture du tampon (taille atteinte)", "erreur", err, "envois", len(t.empreintes))
		}
	}
	if len(t.empreintes) > 0 && t.annuler == nil {
		t.armer()
	}
	return nil
}

// armer lance la minuterie du lot en cours. Verrou tenu.
func (t *Tampon) armer() {
	gen := t.generation
	t.annuler = t.opt.Minuterie(t.opt.Delai, func() { t.echeance(gen) })
}

// echeance est appelée par la minuterie.
func (t *Tampon) echeance(gen uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if gen != t.generation || t.ferme {
		return
	}
	t.annuler = nil
	ctx, fin := context.WithTimeout(context.Background(), t.opt.DelaiEcriture)
	defer fin()
	if err := t.vider(ctx); err != nil {
		t.opt.Journal.Error("écriture du tampon (délai atteint)", "erreur", err, "envois", len(t.empreintes))
		t.armer() // nouvel essai au prochain délai
	}
}

// Vider écrit le tampon tout de suite, même incomplet.
func (t *Tampon) Vider(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.vider(ctx)
}

// Fermer refuse les envois suivants et écrit le tampon (arrêt propre).
func (t *Tampon) Fermer(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ferme = true
	return t.vider(ctx)
}

// Attente renvoie le nombre d'envois pas encore écrits.
func (t *Tampon) Attente() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.empreintes)
}

// vider écrit le lot en cours, mélangé. Verrou tenu. En cas d'échec, le lot
// reste dans le tampon.
func (t *Tampon) vider(ctx context.Context) error {
	if len(t.empreintes) == 0 {
		return nil
	}
	es := append([]empreinte.Empreinte(nil), t.empreintes...)
	rs := append([]questionnaire.Reponse(nil), t.reponses...)
	if err := melanger(len(es), func(i, j int) { es[i], es[j] = es[j], es[i] }); err != nil {
		return err
	}
	if err := melanger(len(rs), func(i, j int) { rs[i], rs[j] = rs[j], rs[i] }); err != nil {
		return err
	}
	if err := t.opt.Ecrivain.EcrireLot(ctx, t.opt.Maintenant().UTC(), es, rs); err != nil {
		return err
	}
	if t.opt.Surveillance != nil {
		for _, r := range rs {
			t.opt.Surveillance.Compter(DimensionRegion, strconv.Itoa(int(r.Region)))
		}
	}
	clear(t.empreintes)
	clear(t.reponses)
	t.empreintes, t.reponses = t.empreintes[:0], t.reponses[:0]
	clear(t.presentes)
	if t.annuler != nil {
		t.annuler()
		t.annuler = nil
	}
	t.generation++
	return nil
}

// melanger applique le mélange de Fisher-Yates avec crypto/rand.
func melanger(n int, echanger func(i, j int)) error {
	for i := n - 1; i > 0; i-- {
		j, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return err
		}
		echanger(i, int(j.Int64()))
	}
	return nil
}
