package publication

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/questionnaire"
)

// Resume est ce qu'il faut savoir d'une publication sans lire son
// instantané : le compteur de l'accueil, par exemple.
type Resume struct {
	Jour             time.Time // jour de publication (minuit UTC)
	Reponses         int       // compteur arrondi à la dizaine inférieure
	ResultatsPublies bool
}

// Publication est un instantané figé, tel qu'il est enregistré : jamais
// modifié ensuite (la base refuse UPDATE, DELETE et TRUNCATE).
type Publication struct {
	Resume
	JSON      []byte            // l'instantané, servi octet pour octet
	HTML      []byte            // le même, mis en page (fragment HTML, en français)
	Empreinte journal.Empreinte // SHA-256 de JSON, inscrit au journal public
}

// Reponses parcourt les réponses enregistrées.
type Reponses interface {
	// Parcourir appelle f pour chaque réponse de la version donnée du
	// questionnaire dont le jour est strictement antérieur à avant.
	Parcourir(ctx context.Context, avant time.Time, version int16, f func(questionnaire.Reponse) error) error
}

// Magasin conserve les publications. Implémenté par base.Base.
type Magasin interface {
	// EnregistrerPublication prend un verrou exclusif, partagé par tous les
	// processus (serveur et commande « diguee publier »). Si une publication
	// existe déjà pour ce jour, il la renvoie sans rien changer (nouvelle =
	// false). Sinon, dans une seule transaction : produire lit les réponses
	// et renvoie la publication, qui est enregistrée, puis journaliser
	// inscrit son entrée au journal public (j écrit dans la même
	// transaction) : tout est écrit, ou rien.
	EnregistrerPublication(ctx context.Context, jour time.Time,
		produire func(context.Context, Reponses) (*Publication, error),
		journaliser func(context.Context, journal.Magasin, *Publication) error) (p *Publication, nouvelle bool, err error)
	// DernierResume renvoie le résumé de la publication la plus récente, ou nil.
	DernierResume(ctx context.Context) (*Resume, error)
	// LirePublication renvoie la publication du jour, ou nil.
	LirePublication(ctx context.Context, jour time.Time) (*Publication, error)
	// Resumes renvoie toutes les publications, les plus récentes d'abord.
	Resumes(ctx context.Context) ([]Resume, error)
}

// Rendu met un instantané en page (fragment HTML) ; fourni par le paquet web.
type Rendu func(*Instantane) ([]byte, error)

// ContenuJournal est le contenu de l'entrée « publication quotidienne » du
// journal public : les totaux et l'empreinte de l'instantané, rien d'autre.
type ContenuJournal struct {
	Jour             string `json:"jour"`
	Reponses         int    `json:"reponses"`
	ResultatsPublies bool   `json:"resultats_publies"`
	SeuilPublication int    `json:"seuil_publication"`
	SeuilCase        int    `json:"seuil_case"`
	Tableaux         int    `json:"tableaux"`
	SHA256Instantane string `json:"sha256_instantane"`
}

// Publieur produit les publications.
type Publieur struct {
	Magasin          Magasin
	Questionnaire    *questionnaire.Questionnaire
	SeuilPublication int
	SeuilCase        int
	Population       *Population // nil : non fournie
	Rendu            Rendu
	Maintenant       func() time.Time // horodatage de l'entrée du journal ; time.Now par défaut
}

// Resultat décrit l'issue d'une publication.
type Resultat struct {
	Publication *Publication
	Nouvelle    bool // false : la publication du jour existait déjà, inchangée
}

// Publier produit la publication du jour donné, sur les réponses des jours
// antérieurs, si elle n'existe pas encore. Idempotente : une seule
// publication par jour, même après un redémarrage ou deux appels simultanés
// (le verrou du magasin), et une publication existante n'est jamais refaite.
//
// La clôture (ticket #16) appellera Publier avec le lendemain du dernier jour
// d'écriture, après avoir écrit le dernier tampon, pour que la dernière
// publication porte sur toutes les réponses.
func (p *Publieur) Publier(ctx context.Context, jour time.Time) (Resultat, error) {
	if p.Magasin == nil || p.Questionnaire == nil || p.Rendu == nil || p.SeuilCase <= 0 || p.SeuilPublication <= 0 {
		return Resultat{}, errors.New("publication : publieur incomplet")
	}
	maintenant := p.Maintenant
	if maintenant == nil {
		maintenant = time.Now
	}
	jour = Jour(jour)
	params := Parametres{Jour: jour, SeuilPublication: p.SeuilPublication, SeuilCase: p.SeuilCase, Population: p.Population}
	var inst *Instantane
	pub, nouvelle, err := p.Magasin.EnregistrerPublication(ctx, jour,
		func(ctx context.Context, rs Reponses) (*Publication, error) {
			a := NouvelAgregateur(p.Questionnaire)
			err := rs.Parcourir(ctx, jour, p.Questionnaire.Version, func(r questionnaire.Reponse) error {
				a.Ajouter(r)
				return nil
			})
			if err != nil {
				return nil, fmt.Errorf("lecture des réponses : %w", err)
			}
			inst = a.Instantane(params)
			return Produire(inst, p.Rendu)
		},
		func(ctx context.Context, m journal.Magasin, pub *Publication) error {
			_, err := journal.Nouveau(m, maintenant).Inscrire(ctx, journal.PublicationQuotidienne, ContenuJournal{
				Jour: inst.Jour, Reponses: inst.Reponses, ResultatsPublies: inst.ResultatsPublies,
				SeuilPublication: inst.SeuilPublication, SeuilCase: inst.SeuilCase,
				Tableaux: len(inst.Tableaux), SHA256Instantane: pub.Empreinte.String(),
			})
			return err
		})
	if err != nil {
		return Resultat{}, fmt.Errorf("publication du %s : %w", jour.Format(time.DateOnly), err)
	}
	return Resultat{Publication: pub, Nouvelle: nouvelle}, nil
}

// Produire encode l'instantané en JSON, le met en page et calcule son empreinte.
func Produire(inst *Instantane, rendu Rendu) (*Publication, error) {
	js, err := json.Marshal(inst)
	if err != nil {
		return nil, err
	}
	html, err := rendu(inst)
	if err != nil {
		return nil, fmt.Errorf("mise en page : %w", err)
	}
	jour, err := time.ParseInLocation(time.DateOnly, inst.Jour, time.UTC)
	if err != nil {
		return nil, err
	}
	return &Publication{
		Resume: Resume{Jour: jour, Reponses: inst.Reponses, ResultatsPublies: inst.ResultatsPublies},
		JSON:   js, HTML: html, Empreinte: sha256.Sum256(js),
	}, nil
}

// LireInstantane décode l'instantané d'une publication.
func (p *Publication) LireInstantane() (*Instantane, error) {
	var inst Instantane
	if err := json.Unmarshal(p.JSON, &inst); err != nil {
		return nil, err
	}
	return &inst, nil
}

// Planificateur lance la publication chaque jour à heure fixe, dans le
// serveur, et rattrape au démarrage une publication manquée.
type Planificateur struct {
	Publieur   *Publieur
	Heure      time.Duration // DIGUEE_HEURE_PUBLICATION, depuis minuit UTC
	Reessai    time.Duration // délai avant un nouvel essai après un échec ; 10 minutes par défaut
	Maintenant func() time.Time
	Journal    *slog.Logger
}

// Executer publie le jour dû à l'instant présent, s'il ne l'est pas encore :
// c'est aussi le rattrapage au démarrage. Seule la dernière publication due
// est rattrapée : une publication plus ancienne manquée ne l'est pas (son
// entrée au journal serait datée d'un autre jour que le sien).
func (pl *Planificateur) Executer(ctx context.Context) (Resultat, error) {
	jour := JourDu(pl.maintenant(), pl.Heure)
	r, err := pl.Publieur.Publier(ctx, jour)
	if err != nil {
		pl.journal().Error("publication quotidienne", "jour", jour.Format(time.DateOnly), "erreur", err)
		return r, err
	}
	if r.Nouvelle {
		pl.journal().Info("publication quotidienne", "jour", jour.Format(time.DateOnly),
			"reponses", r.Publication.Reponses, "resultats_publies", r.Publication.ResultatsPublies,
			"sha256_instantane", r.Publication.Empreinte.String())
	}
	return r, nil
}

// Lancer publie aussitôt le jour dû (rattrapage), puis chaque jour à l'heure
// fixée, jusqu'à l'annulation de ctx. Après un échec, il réessaie au bout de
// Reessai.
func (pl *Planificateur) Lancer(ctx context.Context) {
	for {
		attente := time.Until(Prochaine(pl.maintenant(), pl.Heure))
		if _, err := pl.Executer(ctx); err != nil && ctx.Err() == nil {
			attente = min(attente, pl.reessai())
		}
		if attente < time.Second {
			attente = time.Second
		}
		t := time.NewTimer(attente)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

func (pl *Planificateur) maintenant() time.Time {
	if pl.Maintenant != nil {
		return pl.Maintenant()
	}
	return time.Now()
}

func (pl *Planificateur) reessai() time.Duration {
	if pl.Reessai > 0 {
		return pl.Reessai
	}
	return 10 * time.Minute
}

func (pl *Planificateur) journal() *slog.Logger {
	if pl.Journal != nil {
		return pl.Journal
	}
	return slog.Default()
}
