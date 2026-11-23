// Package cloture conduit la Clôture de la consultation (ticket #16, spec
// §12, ADR 0002) : la fermeture, faite par le serveur à la date de clôture,
// puis la procédure de destruction, lancée par « diguee cloturer » avec le
// rôle propriétaire.
//
// Chaque étape est inscrite au journal public dans une entrée « cloture »
// dont le champ « etape » la nomme, dans l'ordre de Etapes ; la dernière est
// le procès-verbal (entrée « proces_verbal »). Le journal public est aussi
// l'état de la procédure : relancée, elle reprend à la première étape qui n'y
// est pas inscrite, et une étape n'est inscrite que si la précédente est la
// dernière entrée « cloture » (vérifié sous le verrou du journal). Aucune
// étape ne peut donc être sautée.
package cloture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/publication"
)

// Étapes de la Clôture, dans l'ordre.
const (
	// Faite par le serveur : dernier tampon écrit, dernière publication
	// quotidienne produite.
	EtapeFermeture = "fermeture"
	// Faites par « diguee cloturer ».
	EtapeVerification           = "verification"
	EtapeSuppressionParticipant = "suppression_participations" // base.EtapeSuppressionParticipations
	EtapeLiberationEspace       = "liberation_espace"
	EtapeSuppressionSauvegardes = "suppression_sauvegardes"
	EtapeNouvelleSauvegarde     = "nouvelle_sauvegarde"
	EtapeDestructionCle         = "destruction_cle_empreinte"
	EtapeRenouvellementVolume   = "renouvellement_volume"
)

// Etapes renvoie les étapes inscrites dans des entrées « cloture », dans
// l'ordre ; le procès-verbal suit la dernière.
func Etapes() []string {
	return []string{EtapeFermeture, EtapeVerification, EtapeSuppressionParticipant, EtapeLiberationEspace,
		EtapeSuppressionSauvegardes, EtapeNouvelleSauvegarde, EtapeDestructionCle, EtapeRenouvellementVolume}
}

// etapeDe lit le champ « etape » d'une entrée « cloture ».
func etapeDe(e *journal.Entree) (string, error) {
	var c struct {
		Etape string `json:"etape"`
	}
	if err := json.Unmarshal(e.Contenu, &c); err != nil {
		return "", fmt.Errorf("entrée %d du journal : %w", e.Numero, err)
	}
	return c.Etape, nil
}

// apres renvoie la condition d'inscription d'une étape : la dernière entrée
// « cloture » est l'étape précédente (aucune pour la première).
func apres(precedente string) func(context.Context, journal.Vue) (bool, error) {
	return func(ctx context.Context, v journal.Vue) (bool, error) {
		d, err := v.DerniereDuType(ctx, journal.Cloture)
		if err != nil {
			return false, err
		}
		if d == nil {
			return precedente == "", nil
		}
		e, err := etapeDe(d)
		return err == nil && e == precedente, err
	}
}

// ContenuFermeture est le contenu de l'entrée « cloture » de la fermeture.
type ContenuFermeture struct {
	Etape               string `json:"etape"`
	DateCloture         string `json:"date_cloture"`
	DernierePublication string `json:"derniere_publication"`
	SHA256Instantane    string `json:"sha256_instantane"`
	ReponsesPubliees    int    `json:"reponses_publiees"` // compteur de la publication, arrondi
}

// Tampon est le tampon mélangé du serveur (tampon.Tampon).
type Tampon interface {
	Fermer(ctx context.Context) error
}

// Publieur produit une publication quotidienne (publication.Publieur).
type Publieur interface {
	Publier(ctx context.Context, jour time.Time) (publication.Resultat, error)
}

// Reponses donne le jour de la réponse la plus récente (base.Base).
type Reponses interface {
	DernierJourReponse(ctx context.Context) (time.Time, bool, error)
}

// Fermeture ferme la consultation dans le serveur, à la date de clôture :
// le tampon est écrit une dernière fois et n'accepte plus rien, puis, à
// l'heure de publication qui suit le dernier jour d'écriture, la dernière
// publication quotidienne est produite (elle porte donc sur toutes les
// réponses), et la fermeture est inscrite au journal public. Les pages du
// parcours, elles, affichent « Consultation close » dès la date de clôture
// (paquet web), et plus aucun jeton ni code n'est délivré.
//
// La publication attend l'heure habituelle (DIGUEE_HEURE_PUBLICATION) du
// lendemain du dernier lot, au lieu d'être produite aussitôt : une
// publication par jour, toujours à la même heure, et jamais d'entrée du
// journal écrite à un instant lié à un envoi (paquet journal).
type Fermeture struct {
	Cloture    time.Time     // instant de la clôture (DIGUEE_CLOTURE, minuit UTC)
	Heure      time.Duration // DIGUEE_HEURE_PUBLICATION
	Tampon     Tampon
	Publieur   Publieur
	Reponses   Reponses
	Journal    *journal.Journal
	Maintenant func() time.Time // time.Now par défaut
	Reessai    time.Duration    // après un échec ; 10 minutes par défaut
	Log        *slog.Logger
}

// Bilan de Executer.
type BilanFermeture struct {
	Faite    bool            // la fermeture est inscrite au journal (maintenant ou avant)
	Prochain time.Time       // si non faite : quand réessayer
	Entree   *journal.Entree // l'entrée de la fermeture, si elle vient d'être inscrite
}

func (f *Fermeture) maintenant() time.Time {
	if f.Maintenant != nil {
		return f.Maintenant().UTC()
	}
	return time.Now().UTC()
}

func (f *Fermeture) log() *slog.Logger {
	if f.Log != nil {
		return f.Log
	}
	return slog.Default()
}

// dejaFaite cherche l'entrée de la fermeture dans le journal.
func dejaFaite(ctx context.Context, j *journal.Journal) (bool, error) {
	es, err := j.Toutes(ctx)
	if err != nil {
		return false, err
	}
	for i := range es {
		if es[i].Type == journal.Cloture {
			return true, nil
		}
	}
	return false, nil
}

// Executer fait ce qui est dû à l'instant présent : rien avant la clôture ;
// ensuite, écrire et fermer le tampon, puis, à l'heure due, publier et
// inscrire la fermeture. Idempotente.
func (f *Fermeture) Executer(ctx context.Context) (BilanFermeture, error) {
	if f.Tampon == nil || f.Publieur == nil || f.Reponses == nil || f.Journal == nil || f.Cloture.IsZero() {
		return BilanFermeture{}, errors.New("clôture : fermeture incomplète")
	}
	maintenant := f.maintenant()
	if maintenant.Before(f.Cloture) {
		return BilanFermeture{Prochain: f.Cloture}, nil
	}
	if faite, err := dejaFaite(ctx, f.Journal); err != nil || faite {
		return BilanFermeture{Faite: faite}, err
	}
	// Le dernier tampon : écrit, puis plus aucun envoi accepté.
	if err := f.Tampon.Fermer(ctx); err != nil {
		return BilanFermeture{}, fmt.Errorf("clôture : écriture du dernier tampon : %w", err)
	}
	jourCloture := publication.Jour(f.Cloture)
	jourPub := jourCloture
	dernier, ok, err := f.Reponses.DernierJourReponse(ctx)
	if err != nil {
		return BilanFermeture{}, fmt.Errorf("clôture : lecture des réponses : %w", err)
	}
	if ok && !dernier.Before(jourPub) {
		jourPub = dernier.AddDate(0, 0, 1)
	}
	if due := jourPub.Add(f.Heure); maintenant.Before(due) {
		return BilanFermeture{Prochain: due}, nil
	}
	// Serveur arrêté pendant l'heure due : la publication du jour courant
	// porte elle aussi sur toutes les réponses (une publication plus
	// ancienne n'est jamais rattrapée, voir publication.Planificateur).
	if courant := publication.JourDu(maintenant, f.Heure); courant.After(jourPub) {
		jourPub = courant
	}
	r, err := f.Publieur.Publier(ctx, jourPub)
	if err != nil {
		return BilanFermeture{}, fmt.Errorf("clôture : dernière publication : %w", err)
	}
	c := ContenuFermeture{
		Etape: EtapeFermeture, DateCloture: jourCloture.Format(time.DateOnly),
		DernierePublication: r.Publication.Jour.Format(time.DateOnly),
		SHA256Instantane:    r.Publication.Empreinte.String(), ReponsesPubliees: r.Publication.Reponses,
	}
	e, err := f.Journal.InscrireSi(ctx, journal.Cloture, c, apres(""))
	if err != nil {
		return BilanFermeture{}, fmt.Errorf("clôture : inscription de la fermeture : %w", err)
	}
	if e != nil {
		f.log().Info("clôture : fermeture inscrite au journal public", "entree", e.Numero,
			"derniere_publication", c.DernierePublication, "sha256_instantane", c.SHA256Instantane)
	}
	return BilanFermeture{Faite: true, Entree: e}, nil
}

// Lancer attend la clôture, puis exécute la fermeture jusqu'à ce qu'elle
// soit inscrite, ou jusqu'à l'annulation de ctx. Après un échec, elle
// réessaie au bout de Reessai.
func (f *Fermeture) Lancer(ctx context.Context) {
	for {
		b, err := f.Executer(ctx)
		if err == nil && b.Faite {
			return
		}
		attente := time.Until(b.Prochain)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			f.log().Error("clôture : fermeture en échec, nouvel essai", "erreur", err)
			attente = f.Reessai
			if attente <= 0 {
				attente = 10 * time.Minute
			}
		}
		// Les longues attentes sont découpées : l'horloge du système peut
		// être réglée entre-temps.
		attente = max(time.Second, min(attente, time.Hour))
		t := time.NewTimer(attente)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}
