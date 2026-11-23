package whatsapp

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/2guntan/diguee/internal/empreinte"
)

// Statut est l'état de la vérification d'une session.
type Statut int

const (
	Aucun         Statut = iota // pas de jeton pour cette session
	Attente                     // jeton affiché, message pas encore reçu
	Verifiee                    // message reçu, empreinte calculée, session vérifiée
	DejaParticipe               // l'empreinte est déjà en base ou dans le tampon
	NumeroRefuse                // expéditeur hors +221 ou numéro mal formé
	PrefixeRefuse               // préfixe absent de DIGUEE_PREFIXES_AUTORISES
	Limitee                     // trop de vérifications pour ce numéro (ticket #8)
	Expiree                     // jeton expiré avant réception, ou session disparue
	enCours                     // message reçu, empreinte en cours de calcul
)

// Doublons dit si une empreinte a déjà participé (base ou tampon).
type Doublons interface {
	DejaParticipe(ctx context.Context, e empreinte.Empreinte) (bool, error)
}

// Sessions marque une session comme vérifiée (session.Magasin).
type Sessions interface {
	Verifier(id string, e empreinte.Empreinte) bool
}

// Limiteur borne le nombre de vérifications par empreinte (spec §7 :
// DIGUEE_VERIF_PAR_EMPREINTE_24H) : antifraude.LimiteEmpreinte. Autoriser
// compte la tentative et dit si elle est permise. nil : pas de limite.
type Limiteur interface {
	Autoriser(e empreinte.Empreinte) bool
}

// Surveillance reçoit les métriques agrégées (antifraude.Surveillance) : ici,
// le préfixe de chaque numéro accepté, sans rien d'autre. nil : aucune.
type Surveillance interface {
	Compter(dimension, valeur string)
}

// DimensionPrefixe est la dimension des métriques par préfixe
// (antifraude.DimensionPrefixe).
const DimensionPrefixe = "prefixe"

// OptionsVerificateur regroupe les dépendances du vérificateur.
type OptionsVerificateur struct {
	Empreinteur  empreinte.Empreinteur
	Doublons     Doublons
	Sessions     Sessions
	Limiteur     Limiteur         // facultatif : limite par empreinte
	Surveillance Surveillance     // facultatif : métriques par préfixe
	Prefixes     []string         // DIGUEE_PREFIXES_AUTORISES
	Duree        time.Duration    // DIGUEE_JETON_DUREE
	Maintenant   func() time.Time // time.Now par défaut
	Journal      *slog.Logger     // slog.Default() par défaut
}

// Jeton est un jeton affiché à une session.
type Jeton struct {
	Texte  string    // « DIGUEE 4821-KX »
	Expire time.Time // fin de validité
}

type entree struct {
	jeton   Jeton
	session string
	statut  Statut
	// expediteur reconnaît le premier expéditeur sans garder son numéro :
	// HMAC-SHA-256 de l'identifiant WhatsApp avec une clé aléatoire tirée au
	// démarrage et jamais écrite. Il sert seulement à distinguer une
	// nouvelle livraison du même message (Meta réessaie) d'un second
	// expéditeur.
	expediteur [32]byte
	utilise    bool
}

// conservation est la durée pendant laquelle un jeton reste connu après son
// expiration : sa session peut encore afficher son issue (déjà participé,
// numéro refusé) et un second expéditeur est encore repéré.
const conservation = 30 * time.Minute

// Verificateur crée les jetons et traite les messages reçus. Tout est en
// mémoire : un redémarrage invalide les jetons en cours.
type Verificateur struct {
	opt OptionsVerificateur
	cle [32]byte

	mu         sync.Mutex
	parTexte   map[string]*entree
	parSession map[string]*entree

	m Metriques
}

// Metriques compte les événements de la vérification inversée, sans aucune
// donnée individuelle.
type Metriques struct {
	JetonsCrees      atomic.Int64
	Verifications    atomic.Int64
	DejaParticipe    atomic.Int64
	NumerosRefuses   atomic.Int64 // hors +221, mal formé ou préfixe non autorisé
	JetonsInconnus   atomic.Int64 // jeton inconnu, expiré, ou message sans jeton
	SecondExpediteur atomic.Int64 // jeton déjà utilisé reçu d'un autre expéditeur
	Limitees         atomic.Int64 // refusées par la limite par empreinte
}

// NouveauVerificateur prépare un vérificateur vide.
func NouveauVerificateur(opt OptionsVerificateur) (*Verificateur, error) {
	if opt.Empreinteur == nil || opt.Doublons == nil || opt.Sessions == nil || opt.Duree <= 0 {
		return nil, errors.New("vérification inversée : options incomplètes")
	}
	if opt.Maintenant == nil {
		opt.Maintenant = time.Now
	}
	if opt.Journal == nil {
		opt.Journal = slog.Default()
	}
	v := &Verificateur{opt: opt, parTexte: map[string]*entree{}, parSession: map[string]*entree{}}
	if _, err := rand.Read(v.cle[:]); err != nil {
		return nil, err
	}
	return v, nil
}

// Metriques renvoie les compteurs.
func (v *Verificateur) Metriques() *Metriques { return &v.m }

// Creer tire un nouveau jeton pour la session. Un jeton précédent de la même
// session est révoqué.
func (v *Verificateur) Creer(session string) (Jeton, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	maintenant := v.opt.Maintenant()
	for t, e := range v.parTexte { // purge
		if maintenant.After(e.jeton.Expire.Add(conservation)) {
			delete(v.parTexte, t)
			if v.parSession[e.session] == e {
				delete(v.parSession, e.session)
			}
		}
	}
	if ancien, ok := v.parSession[session]; ok && ancien.statut == Attente {
		delete(v.parTexte, ancien.jeton.Texte)
	}
	for range 100 {
		texte, err := nouveauTexteJeton()
		if err != nil {
			return Jeton{}, err
		}
		if _, pris := v.parTexte[texte]; pris {
			continue
		}
		e := &entree{jeton: Jeton{Texte: texte, Expire: maintenant.Add(v.opt.Duree)}, session: session, statut: Attente}
		v.parTexte[texte] = e
		v.parSession[session] = e
		v.m.JetonsCrees.Add(1)
		return e.jeton, nil
	}
	return Jeton{}, errors.New("vérification inversée : aucun jeton libre")
}

// Etat renvoie le dernier jeton de la session et son statut.
func (v *Verificateur) Etat(session string) (Jeton, Statut) {
	v.mu.Lock()
	defer v.mu.Unlock()
	e, ok := v.parSession[session]
	if !ok {
		return Jeton{}, Aucun
	}
	switch {
	case e.statut == enCours:
		return e.jeton, Attente
	case e.statut == Attente && !v.opt.Maintenant().Before(e.jeton.Expire):
		return e.jeton, Expiree
	}
	return e.jeton, e.statut
}

// Oublier retire le jeton de la session (envoi terminé ou session détruite).
func (v *Verificateur) Oublier(session string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if e, ok := v.parSession[session]; ok {
		delete(v.parSession, session)
		if e.statut == Attente {
			delete(v.parTexte, e.jeton.Texte)
		}
	}
}

// Recevoir traite un message reçu : expediteur est l'identifiant WhatsApp de
// l'expéditeur (« 221771234567 », champ from de Meta), texte le corps du
// message. Il renvoie une erreur seulement si le traitement doit être
// réessayé (KMS ou base indisponible) ; le jeton reste alors utilisable.
// Ni le numéro ni le texte ne sont journalisés.
func (v *Verificateur) Recevoir(ctx context.Context, expediteur, texte string) error {
	t, err := extraireJeton(texte)
	if err != nil {
		v.m.JetonsInconnus.Add(1)
		return nil
	}
	m := hmac.New(sha256.New, v.cle[:])
	m.Write([]byte(expediteur))
	var cle [32]byte
	copy(cle[:], m.Sum(nil))

	v.mu.Lock()
	e, ok := v.parTexte[t]
	switch {
	case !ok, !e.utilise && !v.opt.Maintenant().Before(e.jeton.Expire):
		v.mu.Unlock()
		v.m.JetonsInconnus.Add(1)
		return nil
	case e.utilise:
		second := !hmac.Equal(cle[:], e.expediteur[:])
		v.mu.Unlock()
		if second {
			// Seul le premier expéditeur compte (spec §5.1).
			v.m.SecondExpediteur.Add(1)
			v.opt.Journal.Warn("vérification inversée : jeton déjà utilisé reçu d'un autre expéditeur")
		}
		return nil
	}
	e.utilise, e.expediteur, e.statut = true, cle, enCours
	session := e.session
	v.mu.Unlock()

	statut, err := v.verifier(ctx, session, expediteur)
	v.mu.Lock()
	defer v.mu.Unlock()
	if err != nil {
		// Échec passager : le jeton redevient utilisable (Meta réessaiera).
		e.utilise, e.expediteur, e.statut = false, [32]byte{}, Attente
		return err
	}
	e.statut = statut
	return nil
}

// verifier contrôle le numéro, calcule son empreinte et marque la session.
// Le numéro n'est plus référencé au retour.
func (v *Verificateur) verifier(ctx context.Context, session, expediteur string) (Statut, error) {
	if !strings.HasPrefix(expediteur, "+") {
		expediteur = "+" + expediteur
	}
	numero, err := empreinte.Normaliser(expediteur, v.opt.Prefixes)
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
		return 0, err
	}
	if v.opt.Surveillance != nil {
		// « +221 » puis le préfixe autorisé : une valeur d'une liste fermée.
		v.opt.Surveillance.Compter(DimensionPrefixe, numero[4:6])
	}
	if v.opt.Limiteur != nil && !v.opt.Limiteur.Autoriser(emp) {
		v.m.Limitees.Add(1)
		return Limitee, nil
	}
	deja, err := v.opt.Doublons.DejaParticipe(ctx, emp)
	if err != nil {
		return 0, err
	}
	if deja {
		v.m.DejaParticipe.Add(1)
		return DejaParticipe, nil
	}
	if !v.opt.Sessions.Verifier(session, emp) {
		return Expiree, nil
	}
	v.m.Verifications.Add(1)
	return Verifiee, nil
}
