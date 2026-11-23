// Package codes vérifie un numéro par un Code envoyé à ce numéro, en secours
// de la vérification inversée (spec §5.2 et §5.3, ADR 0004 et 0009) : par
// WhatsApp (ticket #9), puis par SMS (ticket #10), derrière une même
// interface Envoyeur.
//
// Déroulé, pour une session :
//
//  1. Demander : le numéro saisi est normalisé (+221, préfixes autorisés),
//     son empreinte est calculée et la limite par empreinte appliquée (la
//     même instance que la vérification inversée). Un code à 6 chiffres est
//     tiré (crypto/rand) et envoyé ; dès l'envoi fini, le numéro n'est plus
//     référencé nulle part. La session ne garde que l'empreinte et un HMAC
//     du code, jamais le code en clair.
//  2. Valider : un code juste, saisi avant DIGUEE_CODE_DUREE, et dans la
//     limite de DIGUEE_CODE_ESSAIS essais, vérifie la session, sauf si
//     l'empreinte a déjà participé. Après le dernier essai faux, ou après
//     l'expiration, le code est invalidé et doit être redemandé.
//
// La détection « déjà participé » n'a lieu qu'après un code juste : avant,
// elle révélerait à n'importe qui si un numéro saisi a participé.
//
// Ni le numéro ni le code ne sont journalisés.
package codes

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/2guntan/diguee/internal/empreinte"
)

// Chiffres est la longueur d'un code.
const Chiffres = 6

// Envoyeur envoie un code à un numéro. numero est normalisé (« +221 » suivi
// de 9 chiffres), code compte Chiffres chiffres. Une implémentation ne
// journalise ni le numéro ni le code, et ses erreurs ne les citent pas (ni
// aucun secret d'accès). Elle respecte l'échéance de ctx.
//
// Implémentations : whatsapp.EnvoyeurMeta (modèle d'authentification de
// l'API Cloud), Console (développement) ; le ticket #10 y ajoutera le
// fournisseur SMS.
type Envoyeur interface {
	Envoyer(ctx context.Context, numero, code string) error
}

// Statut est l'issue d'une demande ou d'une saisie de code.
type Statut int

const (
	Aucun         Statut = iota // pas de code pour cette session
	Envoye                      // code envoyé, en attente de saisie
	NumeroRefuse                // numéro mal formé ou hors +221
	PrefixeRefuse               // préfixe absent de DIGUEE_PREFIXES_AUTORISES
	Limitee                     // trop de vérifications ou de codes pour ce numéro
	Faux                        // code faux, il reste des essais
	Epuise                      // code faux au dernier essai : invalidé
	Expire                      // code expiré : invalidé
	Verifiee                    // code juste, session vérifiée
	DejaParticipe               // code juste, mais l'empreinte a déjà participé
	SessionPerdue               // code juste, mais la session a disparu
)

// Doublons dit si une empreinte a déjà participé (tampon.Tampon : base ou tampon).
type Doublons interface {
	DejaParticipe(ctx context.Context, e empreinte.Empreinte) (bool, error)
}

// Sessions marque une session comme vérifiée (session.Magasin).
type Sessions interface {
	Verifier(id string, e empreinte.Empreinte) bool
}

// Limiteur borne les vérifications et codes par empreinte
// (antifraude.LimiteEmpreinte). nil : pas de limite.
type Limiteur interface {
	Autoriser(e empreinte.Empreinte) bool
}

// Surveillance reçoit les métriques agrégées (antifraude.Surveillance).
type Surveillance interface {
	Compter(dimension, valeur string)
}

// DimensionPrefixe est la dimension des métriques par préfixe
// (antifraude.DimensionPrefixe).
const DimensionPrefixe = "prefixe"

// Options regroupe les dépendances du vérificateur.
type Options struct {
	Canal        string // « WhatsApp » ou « SMS » : seulement pour les messages d'erreur
	Envoyeur     Envoyeur
	Empreinteur  empreinte.Empreinteur
	Doublons     Doublons
	Sessions     Sessions
	Limiteur     Limiteur         // facultatif ; partagé avec la vérification inversée
	Surveillance Surveillance     // facultatif : métriques par préfixe
	Prefixes     []string         // DIGUEE_PREFIXES_AUTORISES
	Duree        time.Duration    // DIGUEE_CODE_DUREE
	Essais       int              // DIGUEE_CODE_ESSAIS
	Maintenant   func() time.Time // time.Now par défaut
	Journal      *slog.Logger     // slog.Default() par défaut
}

// entree est le code d'une session : jamais le code lui-même, seulement son
// HMAC (clé aléatoire tirée au démarrage, jamais écrite, et identifiant de
// la session), avec l'empreinte du numéro, l'échéance et les essais restants.
type entree struct {
	hmac      [32]byte
	empreinte empreinte.Empreinte
	expire    time.Time
	restants  int
}

// Verificateur tire, envoie et vérifie les codes. Tout est en mémoire : un
// redémarrage invalide les codes en cours.
type Verificateur struct {
	opt Options
	cle [32]byte

	mu         sync.Mutex
	parSession map[string]*entree

	m Metriques
}

// Metriques compte les événements, sans aucune donnée individuelle.
type Metriques struct {
	Envoyes        atomic.Int64
	EchecsEnvoi    atomic.Int64
	NumerosRefuses atomic.Int64
	Limitees       atomic.Int64
	Faux           atomic.Int64
	Epuises        atomic.Int64
	Expires        atomic.Int64
	Verifications  atomic.Int64
	DejaParticipe  atomic.Int64
}

// Nouveau prépare un vérificateur vide.
func Nouveau(opt Options) (*Verificateur, error) {
	if opt.Envoyeur == nil || opt.Empreinteur == nil || opt.Doublons == nil || opt.Sessions == nil ||
		opt.Duree <= 0 || opt.Essais <= 0 {
		return nil, errors.New("codes : options incomplètes")
	}
	if opt.Maintenant == nil {
		opt.Maintenant = time.Now
	}
	if opt.Journal == nil {
		opt.Journal = slog.Default()
	}
	v := &Verificateur{opt: opt, parSession: map[string]*entree{}}
	if _, err := rand.Read(v.cle[:]); err != nil {
		return nil, err
	}
	return v, nil
}

// Metriques renvoie les compteurs.
func (v *Verificateur) Metriques() *Metriques { return &v.m }

// Duree renvoie la validité d'un code.
func (v *Verificateur) Duree() time.Duration { return v.opt.Duree }

// empreinteCode lie le code à la session.
func (v *Verificateur) empreinteCode(session, code string) [32]byte {
	m := hmac.New(sha256.New, v.cle[:])
	m.Write([]byte(session))
	m.Write([]byte{0})
	m.Write([]byte(code))
	var h [32]byte
	copy(h[:], m.Sum(nil))
	return h
}

// tirer renvoie un code de Chiffres chiffres, uniforme (crypto/rand).
func tirer() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// ErrEnvoi signale que le code n'a pas pu partir. La session n'a pas de code.
var ErrEnvoi = errors.New("envoi du code impossible")

// Demander traite le numéro saisi pour la session : contrôle, empreinte,
// limite par empreinte, puis tirage et envoi du code. Un code précédent de
// la session est révoqué. Il renvoie Envoye, NumeroRefuse, PrefixeRefuse ou
// Limitee ; une erreur si l'empreinte ne peut pas être calculée ou si
// l'envoi échoue (ErrEnvoi), sans citer le numéro.
//
// Le numéro n'est plus référencé au retour : seuls restent l'empreinte et
// le HMAC du code.
func (v *Verificateur) Demander(ctx context.Context, session, saisie string) (Statut, error) {
	v.Oublier(session)
	numero, err := empreinte.Normaliser(saisie, v.opt.Prefixes)
	switch {
	case errors.Is(err, empreinte.ErrPrefixe):
		v.m.NumerosRefuses.Add(1)
		return PrefixeRefuse, nil
	case err != nil:
		v.m.NumerosRefuses.Add(1)
		return NumeroRefuse, nil
	}
	emp, err := v.opt.Empreinteur.Calculer(ctx, numero)
	if err != nil {
		return 0, fmt.Errorf("calcul de l'empreinte : %w", err)
	}
	if v.opt.Surveillance != nil {
		// « +221 » puis le préfixe autorisé : une valeur d'une liste fermée.
		v.opt.Surveillance.Compter(DimensionPrefixe, numero[4:6])
	}
	if v.opt.Limiteur != nil && !v.opt.Limiteur.Autoriser(emp) {
		v.m.Limitees.Add(1)
		return Limitee, nil
	}
	code, err := tirer()
	if err != nil {
		return 0, err
	}
	if err := v.opt.Envoyeur.Envoyer(ctx, numero, code); err != nil {
		v.m.EchecsEnvoi.Add(1)
		// L'erreur de l'envoyeur ne cite ni le numéro ni le code.
		v.opt.Journal.Error("envoi du code de vérification", "canal", v.opt.Canal, "erreur", err)
		return 0, ErrEnvoi
	}
	e := &entree{hmac: v.empreinteCode(session, code), empreinte: emp,
		expire: v.opt.Maintenant().Add(v.opt.Duree), restants: v.opt.Essais}
	v.mu.Lock()
	v.purger()
	v.parSession[session] = e
	v.mu.Unlock()
	v.m.Envoyes.Add(1)
	return Envoye, nil
}

// purger retire les codes expirés. v.mu est tenu.
func (v *Verificateur) purger() {
	maintenant := v.opt.Maintenant()
	for s, e := range v.parSession {
		if !maintenant.Before(e.expire) {
			delete(v.parSession, s)
		}
	}
}

// Etat dit si la session a un code valable, et combien d'essais il lui reste.
func (v *Verificateur) Etat(session string) (Statut, int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	e, ok := v.parSession[session]
	switch {
	case !ok:
		return Aucun, 0
	case !v.opt.Maintenant().Before(e.expire):
		return Expire, 0
	}
	return Envoye, e.restants
}

// Oublier retire le code de la session (nouvelle demande, autre parcours,
// session détruite).
func (v *Verificateur) Oublier(session string) {
	v.mu.Lock()
	delete(v.parSession, session)
	v.mu.Unlock()
}

// normaliserSaisie retire les espaces qu'une personne peut taper.
func normaliserSaisie(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '.', ' ', ' ':
			return -1
		}
		return r
	}, s)
}

// Valider compare la saisie au code de la session, à temps constant. Il
// renvoie Faux (avec les essais restants), Epuise, Expire ou Aucun (le code
// doit être redemandé), ou, pour un code juste, Verifiee, DejaParticipe ou
// SessionPerdue ; le code est alors consommé. Une erreur (base
// indisponible) laisse le code et ses essais tels quels.
func (v *Verificateur) Valider(ctx context.Context, session, saisie string) (Statut, int, error) {
	h := v.empreinteCode(session, normaliserSaisie(saisie))
	v.mu.Lock()
	e, ok := v.parSession[session]
	switch {
	case !ok:
		v.mu.Unlock()
		return Aucun, 0, nil
	case !v.opt.Maintenant().Before(e.expire):
		delete(v.parSession, session)
		v.mu.Unlock()
		v.m.Expires.Add(1)
		return Expire, 0, nil
	}
	// Toute saisie compte pour un essai, même mal formée.
	if !hmac.Equal(h[:], e.hmac[:]) {
		e.restants--
		restants := e.restants
		if restants <= 0 {
			delete(v.parSession, session)
		}
		v.mu.Unlock()
		if restants <= 0 {
			v.m.Epuises.Add(1)
			return Epuise, 0, nil
		}
		v.m.Faux.Add(1)
		return Faux, restants, nil
	}
	// Code juste : il est retiré tout de suite, pour qu'une seconde requête
	// simultanée ne puisse pas s'en servir.
	delete(v.parSession, session)
	v.mu.Unlock()

	deja, err := v.opt.Doublons.DejaParticipe(ctx, e.empreinte)
	if err != nil {
		v.mu.Lock()
		if _, pris := v.parSession[session]; !pris {
			v.parSession[session] = e
		}
		v.mu.Unlock()
		return 0, 0, err
	}
	if deja {
		v.m.DejaParticipe.Add(1)
		return DejaParticipe, 0, nil
	}
	if !v.opt.Sessions.Verifier(session, e.empreinte) {
		return SessionPerdue, 0, nil
	}
	v.m.Verifications.Add(1)
	return Verifiee, 0, nil
}
