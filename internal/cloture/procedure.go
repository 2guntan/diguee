package cloture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/2guntan/diguee/internal/base"
	"github.com/2guntan/diguee/internal/empreinte"
	"github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/sauvegarde"
	"github.com/2guntan/diguee/internal/stockage"
)

// DestructeurCle détruit la clé de l'empreinte (empreinte.DestructeurOVH).
type DestructeurCle interface {
	Detruire(ctx context.Context) (empreinte.BilanDestruction, error)
}

// AttestationVolume est le compte rendu du renouvellement des clés du
// volume chiffré, écrit par « volume.sh renouveler » (root), que la
// procédure ne peut pas faire elle-même : elle tourne sans privilège.
type AttestationVolume struct {
	Date              time.Time `json:"date"`                // fin du renouvellement (UTC)
	SHA256CleChiffree string    `json:"sha256_cle_chiffree"` // SHA-256 de la nouvelle clé chiffrée par le KMS (JWE)
	SHA256EnteteLUKS  string    `json:"sha256_entete_luks"`  // SHA-256 de l'en-tête LUKS après renouvellement
	Rechiffrement     bool      `json:"rechiffrement"`       // clé maîtresse du volume changée (cryptsetup reencrypt)
	EspaceLibreEfface bool      `json:"espace_libre_efface"` // espace libre du système de fichiers réécrit
}

// ErrAttente signale une procédure arrêtée sur une étape manuelle : ce n'est
// pas un échec, la relance reprend là.
var ErrAttente = errors.New("clôture en attente d'une étape manuelle")

// Procedure est la procédure de destruction (« diguee cloturer »).
type Procedure struct {
	// Base, avec le rôle propriétaire : seul à pouvoir supprimer
	// participation et libérer l'espace.
	Base    *base.Base
	Cloture time.Time // DIGUEE_CLOTURE (minuit UTC)
	// Sauvegardes est le compartiment des sauvegardes (ticket #15), et
	// Sauvegarder fait une sauvegarde de la base (sauvegarde.Faire).
	Sauvegardes stockage.Compartiment
	Sauvegarder func(ctx context.Context) (*sauvegarde.Resultat, error)
	// Cle détruit la clé de l'empreinte ; ModeCle est DIGUEE_EMPREINTEUR
	// (« fake » : clé de développement écrite dans le code, rien à détruire,
	// refusé en production).
	Cle     DestructeurCle
	ModeCle string
	// Volume lit l'attestation du renouvellement des clés du volume
	// (nil, nil : pas encore faite).
	Volume func(ctx context.Context) (*AttestationVolume, error)
	// SansVolume : pas de volume chiffré (développement, simulation) ;
	// refusé en production par la commande.
	SansVolume bool
	// Controler vérifie, avant la première étape destructrice, que tout ce
	// dont les étapes suivantes ont besoin répond (compartiment, KMS,
	// programmes) : une étape ne doit pas échouer faute de configuration
	// après la suppression de la table. Facultatif.
	Controler  func(ctx context.Context) error
	Maintenant func() time.Time
	Log        *slog.Logger
	Sortie     io.Writer // compte rendu lisible ; io.Discard si nil
}

// Bilan est l'issue d'une exécution.
type Bilan struct {
	Faites       []string        // étapes faites par cette exécution
	ProcesVerbal *journal.Entree // le procès-verbal, s'il est inscrit (maintenant ou avant)
	DejaTerminee bool            // le procès-verbal existait déjà
}

// ErreurEtape est l'échec d'une étape : la procédure s'arrête là.
type ErreurEtape struct {
	Etape string
	Err   error
}

func (e *ErreurEtape) Error() string { return "étape " + e.Etape + " : " + e.Err.Error() }
func (e *ErreurEtape) Unwrap() error { return e.Err }

func (p *Procedure) maintenant() time.Time {
	if p.Maintenant != nil {
		return p.Maintenant().UTC()
	}
	return time.Now().UTC()
}

func (p *Procedure) sortie() io.Writer {
	if p.Sortie != nil {
		return p.Sortie
	}
	return io.Discard
}

func (p *Procedure) log() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}

// etat est l'état de la procédure lu dans le journal public.
type etat struct {
	entrees      []journal.Entree
	parEtape     map[string]*journal.Entree
	procesVerbal *journal.Entree
}

func lireEtat(ctx context.Context, j *journal.Journal) (*etat, error) {
	es, err := j.Toutes(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := journal.VerifierChaine(es); err != nil {
		return nil, fmt.Errorf("journal public NON VALIDE : %w", err)
	}
	e := &etat{entrees: es, parEtape: map[string]*journal.Entree{}}
	var ordre []string
	for i := range es {
		switch es[i].Type {
		case journal.Cloture:
			nom, err := etapeDe(&es[i])
			if err != nil {
				return nil, err
			}
			if _, deja := e.parEtape[nom]; deja || !slices.Contains(Etapes(), nom) {
				return nil, fmt.Errorf("journal public : étape de clôture %q inattendue (entrée %d)", nom, es[i].Numero)
			}
			e.parEtape[nom] = &es[i]
			ordre = append(ordre, nom)
		case journal.ProcesVerbal:
			e.procesVerbal = &es[i]
		}
	}
	// Les étapes inscrites sont exactement le début de la liste.
	if !slices.Equal(ordre, Etapes()[:len(ordre)]) {
		return nil, fmt.Errorf("journal public : étapes de clôture inscrites dans le désordre : %v", ordre)
	}
	if e.procesVerbal != nil && len(ordre) != len(Etapes()) {
		return nil, errors.New("journal public : procès-verbal inscrit avant la fin des étapes")
	}
	return e, nil
}

// contenu décode le contenu d'une étape inscrite.
func (e *etat) contenu(etape string, v any) error {
	en := e.parEtape[etape]
	if en == nil {
		return fmt.Errorf("étape %s absente du journal", etape)
	}
	return json.Unmarshal(en.Contenu, v)
}

// sha256Texte renvoie le SHA-256 hexadécimal de lignes jointes par « \n ».
func sha256Texte(lignes []string) string {
	s := sha256.Sum256([]byte(strings.Join(lignes, "\n")))
	return hex.EncodeToString(s[:])
}

// Executer exécute les étapes qui manquent, dans l'ordre, et s'arrête à la
// première qui échoue (ErreurEtape), ou sur une étape manuelle (ErrAttente).
func (p *Procedure) Executer(ctx context.Context) (Bilan, error) {
	if p.Base == nil || p.Cloture.IsZero() || p.Sauvegardes == nil || p.Sauvegarder == nil || (p.Volume == nil && !p.SansVolume) {
		return Bilan{}, errors.New("clôture : procédure incomplète")
	}
	if p.Cle == nil && p.ModeCle != "fake" {
		return Bilan{}, errors.New("clôture : destruction de la clé de l'empreinte non configurée")
	}
	liberer, ok, err := p.Base.VerrouCloture(ctx)
	if err != nil {
		return Bilan{}, err
	}
	if !ok {
		return Bilan{}, errors.New("clôture : une autre procédure de clôture est en cours")
	}
	defer liberer()

	j := journal.Nouveau(p.Base, p.Maintenant)
	var bilan Bilan
	// L'étape de suppression des participations n'est pas dans la table :
	// elle est inscrite dans la transaction même de la suppression.
	etapes := map[string]func(context.Context, *journal.Journal, *etat) (any, error){
		EtapeVerification:           p.verifier,
		EtapeLiberationEspace:       p.libererEspace,
		EtapeSuppressionSauvegardes: p.supprimerSauvegardes,
		EtapeNouvelleSauvegarde:     p.nouvelleSauvegarde,
		EtapeDestructionCle:         p.detruireCle,
		EtapeRenouvellementVolume:   p.renouvelerVolume,
	}
	for {
		e, err := lireEtat(ctx, j)
		if err != nil {
			return bilan, err
		}
		if e.procesVerbal != nil {
			bilan.ProcesVerbal = e.procesVerbal
			bilan.DejaTerminee = len(bilan.Faites) == 0
			return bilan, nil
		}
		if e.parEtape[EtapeFermeture] == nil {
			return bilan, &ErreurEtape{Etape: EtapeVerification, Err: errors.New(
				"la fermeture n'est pas inscrite au journal public : le serveur l'inscrit après la date de clôture, " +
					"à l'heure de la dernière publication quotidienne (le serveur doit tourner)")}
		}
		prochaine := ""
		for _, et := range Etapes() {
			if e.parEtape[et] == nil {
				prochaine = et
				break
			}
		}
		if prochaine == "" {
			pv, err := p.procesVerbal(ctx, j, e)
			if err != nil {
				return bilan, p.echec("proces_verbal", err)
			}
			bilan.ProcesVerbal = pv
			bilan.Faites = append(bilan.Faites, "proces_verbal")
			fmt.Fprintf(p.sortie(), "procès-verbal inscrit au journal public : entrée %d, empreinte %s\n", pv.Numero, pv.Empreinte)
			return bilan, nil
		}
		precedente := Etapes()[slices.Index(Etapes(), prochaine)-1]
		if prochaine == EtapeSuppressionParticipant && p.Controler != nil {
			if err := p.Controler(ctx); err != nil {
				return bilan, p.echec(prochaine, fmt.Errorf("contrôle préalable (rien n'a été supprimé) : %w", err))
			}
		}
		fmt.Fprintf(p.sortie(), "étape %s…\n", prochaine)
		var en *journal.Entree
		if prochaine == EtapeSuppressionParticipant {
			en, err = p.supprimerParticipations(ctx, precedente)
		} else {
			var contenu any
			contenu, err = etapes[prochaine](ctx, j, e)
			if err == nil {
				en, err = j.InscrireSi(ctx, journal.Cloture, contenu, apres(precedente))
				if err == nil && en == nil {
					err = errors.New("inscription refusée : l'étape précédente n'est plus la dernière inscrite (autre procédure ?)")
				}
			}
		}
		if errors.Is(err, ErrAttente) {
			p.log().Warn("clôture en attente", "etape", prochaine, "raison", err.Error())
			fmt.Fprintf(p.sortie(), "EN ATTENTE : %v\n", err)
			return bilan, err
		}
		if err != nil {
			return bilan, p.echec(prochaine, err)
		}
		bilan.Faites = append(bilan.Faites, prochaine)
		p.log().Info("clôture : étape inscrite au journal public", "etape", prochaine, "entree", en.Numero)
		fmt.Fprintf(p.sortie(), "étape %s faite : entrée %d du journal public, empreinte %s\n", prochaine, en.Numero, en.Empreinte)
	}
}

// echec journalise l'alerte et renvoie l'erreur de l'étape.
func (p *Procedure) echec(etape string, err error) error {
	ee := &ErreurEtape{Etape: etape, Err: err}
	p.log().Error("ALERTE clôture : la procédure s'arrête", "etape", etape, "erreur", err.Error())
	fmt.Fprintf(p.sortie(), "ÉCHEC : %v\nLa procédure s'est arrêtée ; aucune étape suivante n'a été faite. Corriger, puis relancer : elle reprendra à cette étape.\n", ee)
	return ee
}

// ContenuVerification est le contenu de l'étape « verification ».
type ContenuVerification struct {
	Etape               string `json:"etape"`
	Phase               string `json:"phase"`
	DateCloture         string `json:"date_cloture"`
	EntreeFermeture     int64  `json:"entree_fermeture"`
	DernierePublication string `json:"derniere_publication"`
	SHA256Instantane    string `json:"sha256_instantane"`
	DernierJourReponse  string `json:"dernier_jour_reponse,omitempty"`
	Reponses            int64  `json:"reponses"`     // lignes de reponse, conservées
	Publications        int64  `json:"publications"` // lignes de publication, conservées
	EntreesJournal      int64  `json:"entrees_journal"`
}

// compte renvoie le nombre de lignes d'une table du bilan.
func compte(ls []base.LignesTable, nom string) int64 {
	for _, l := range ls {
		if l.Nom == nom {
			return l.Lignes
		}
	}
	return -1
}

// verifier contrôle que la destruction peut commencer : consultation close,
// fermeture inscrite par le serveur (dernier tampon écrit), dernière
// publication enregistrée et portant sur toutes les réponses.
func (p *Procedure) verifier(ctx context.Context, _ *journal.Journal, e *etat) (any, error) {
	maintenant := p.maintenant()
	if maintenant.Before(p.Cloture) {
		return nil, fmt.Errorf("la consultation n'est pas close (clôture le %s)", p.Cloture.Format(time.DateOnly))
	}
	var f ContenuFermeture
	if err := e.contenu(EtapeFermeture, &f); err != nil {
		return nil, err
	}
	if f.DateCloture != p.Cloture.Format(time.DateOnly) {
		return nil, fmt.Errorf("la fermeture inscrite porte la date de clôture %s, la configuration %s", f.DateCloture, p.Cloture.Format(time.DateOnly))
	}
	jour, err := time.ParseInLocation(time.DateOnly, f.DernierePublication, time.UTC)
	if err != nil {
		return nil, fmt.Errorf("fermeture : dernière publication : %w", err)
	}
	pub, err := p.Base.LirePublication(ctx, jour)
	if err != nil {
		return nil, err
	}
	if pub == nil || pub.Empreinte.String() != f.SHA256Instantane {
		return nil, fmt.Errorf("la dernière publication (%s) est absente ou différente de celle de la fermeture", f.DernierePublication)
	}
	c := ContenuVerification{Etape: EtapeVerification, Phase: "close", DateCloture: f.DateCloture,
		EntreeFermeture: e.parEtape[EtapeFermeture].Numero, DernierePublication: f.DernierePublication,
		SHA256Instantane: f.SHA256Instantane, EntreesJournal: int64(len(e.entrees))}
	dernier, ok, err := p.Base.DernierJourReponse(ctx)
	if err != nil {
		return nil, err
	}
	if ok {
		if !dernier.Before(jour) {
			return nil, fmt.Errorf("des réponses du %s ne sont pas dans la dernière publication (%s) : le tampon a-t-il été écrit après la fermeture ?",
				dernier.Format(time.DateOnly), f.DernierePublication)
		}
		c.DernierJourReponse = dernier.Format(time.DateOnly)
	}
	ls, err := p.Base.CompterTables(ctx)
	if err != nil {
		return nil, err
	}
	c.Reponses, c.Publications = compte(ls, "reponse"), compte(ls, "publication")
	if c.Reponses < 0 || c.Publications < 0 {
		return nil, errors.New("tables reponse ou publication absentes")
	}
	if compte(ls, "participation") < 0 {
		return nil, errors.New("la table participation est absente alors que sa suppression n'est pas inscrite au journal public")
	}
	return c, nil
}

// ContenuSuppression est le contenu de l'étape « suppression_participations ».
type ContenuSuppression struct {
	Etape                    string `json:"etape"`
	ParticipationsSupprimees int64  `json:"participations_supprimees"` // -1 : table déjà absente
	Ordres                   string `json:"ordres"`
	SHA256Ordres             string `json:"sha256_ordres"`
}

// supprimerParticipations supprime la table et inscrit l'étape dans la même
// transaction.
func (p *Procedure) supprimerParticipations(ctx context.Context, precedente string) (*journal.Entree, error) {
	var en *journal.Entree
	err := p.Base.SupprimerParticipations(ctx, func(ctx context.Context, lignes int64, m journal.Magasin) error {
		c := ContenuSuppression{Etape: EtapeSuppressionParticipant, ParticipationsSupprimees: lignes,
			Ordres: strings.Join(base.OrdresSuppressionParticipations, " ; "), SHA256Ordres: sha256Texte(base.OrdresSuppressionParticipations)}
		var err error
		en, err = journal.Nouveau(m, p.Maintenant).InscrireSi(ctx, journal.Cloture, c, apres(precedente))
		if err == nil && en == nil {
			err = errors.New("inscription refusée : l'étape précédente n'est plus la dernière inscrite")
		}
		return err
	})
	return en, err
}

// ContenuLiberation est le contenu de l'étape « liberation_espace ».
type ContenuLiberation struct {
	Etape        string `json:"etape"`
	Ordres       int    `json:"ordres"`
	SHA256Ordres string `json:"sha256_ordres"`
}

func (p *Procedure) libererEspace(ctx context.Context, _ *journal.Journal, _ *etat) (any, error) {
	if existe, err := p.Base.TableExiste(ctx, "participation"); err != nil || existe {
		if err == nil {
			err = errors.New("la table participation existe encore")
		}
		return nil, err
	}
	ordres, err := p.Base.LibererEspace(ctx)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(p.sortie(), "  %s\n", strings.Join(ordres, "\n  "))
	return ContenuLiberation{Etape: EtapeLiberationEspace, Ordres: len(ordres), SHA256Ordres: sha256Texte(ordres)}, nil
}

// ContenuSuppressionSauvegardes est le contenu de l'étape « suppression_sauvegardes ».
type ContenuSuppressionSauvegardes struct {
	Etape                 string `json:"etape"`
	SauvegardesSupprimees int    `json:"sauvegardes_supprimees"` // objets supprimés sous sauvegardes/
	SHA256Cles            string `json:"sha256_cles"`            // SHA-256 de la liste triée de leurs clés
}

// supprimerToutes supprime tout ce qui est sous sauvegardes/ et renvoie les
// clés vues avant la suppression et le nombre d'objets supprimés.
func (p *Procedure) supprimerToutes(ctx context.Context) ([]string, int, error) {
	objets, err := p.Sauvegardes.Lister(ctx, sauvegarde.Prefixe)
	if err != nil {
		return nil, 0, err
	}
	var cles []string
	for _, o := range objets {
		cles = append(cles, o.Cle)
	}
	slices.Sort(cles)
	n, err := sauvegarde.SupprimerToutes(ctx, p.Sauvegardes)
	if err != nil {
		return cles, n, err
	}
	for _, k := range cles {
		fmt.Fprintf(p.sortie(), "  supprimée : %s\n", k)
	}
	return cles, n, nil
}

func (p *Procedure) supprimerSauvegardes(ctx context.Context, _ *journal.Journal, _ *etat) (any, error) {
	cles, n, err := p.supprimerToutes(ctx)
	if err != nil {
		return nil, err
	}
	return ContenuSuppressionSauvegardes{Etape: EtapeSuppressionSauvegardes, SauvegardesSupprimees: n, SHA256Cles: sha256Texte(cles)}, nil
}

// ContenuNouvelleSauvegarde est le contenu de l'étape « nouvelle_sauvegarde ».
type ContenuNouvelleSauvegarde struct {
	Etape string `json:"etape"`
	Cle   string `json:"cle"`
	// SauvegardesSupprimeesAvant compte les objets apparus depuis l'étape
	// précédente (sauvegarde de nuit, essai précédent interrompu),
	// supprimés avant la nouvelle sauvegarde.
	SauvegardesSupprimeesAvant int    `json:"sauvegardes_supprimees_avant"`
	Octets                     int64  `json:"octets"`
	SHA256                     string `json:"sha256"` // du fichier chiffré envoyé
}

// nouvelleSauvegarde fait une sauvegarde de la base sans participation.
// Tout objet apparu depuis la suppression est d'abord supprimé : une
// sauvegarde de nuit commencée avant la suppression de la table pourrait
// avoir été envoyée après. Ensuite, la nouvelle sauvegarde doit être la
// seule du compartiment.
func (p *Procedure) nouvelleSauvegarde(ctx context.Context, _ *journal.Journal, _ *etat) (any, error) {
	if existe, err := p.Base.TableExiste(ctx, "participation"); err != nil || existe {
		if err == nil {
			err = errors.New("la table participation existe encore")
		}
		return nil, err
	}
	_, avant, err := p.supprimerToutes(ctx)
	if err != nil {
		return nil, err
	}
	r, err := p.Sauvegarder(ctx)
	if err != nil {
		return nil, fmt.Errorf("sauvegarde : %w", err)
	}
	ss, autres, err := sauvegarde.Lister(ctx, p.Sauvegardes)
	if err != nil {
		return nil, err
	}
	if len(autres) > 0 || len(ss) != 1 || ss[0].Cle != r.Cle {
		return nil, fmt.Errorf("après la nouvelle sauvegarde, le compartiment devrait ne contenir qu'elle (%s) : %d sauvegarde(s), %d autre(s) objet(s)", r.Cle, len(ss), len(autres))
	}
	fmt.Fprintf(p.sortie(), "  nouvelle sauvegarde : %s (%d octets, SHA-256 %s)\n", r.Cle, r.Taille, r.SHA256)
	return ContenuNouvelleSauvegarde{Etape: EtapeNouvelleSauvegarde, Cle: r.Cle, SauvegardesSupprimeesAvant: avant,
		Octets: r.Taille, SHA256: r.SHA256}, nil
}

// ContenuDestructionCle est le contenu de l'étape « destruction_cle_empreinte ».
type ContenuDestructionCle struct {
	Etape              string `json:"etape"`
	KMS                string `json:"kms"`
	DejaDetruite       bool   `json:"deja_detruite"`
	EtatFinal          string `json:"etat_final"`
	SHA256Operations   string `json:"sha256_operations"`
	ConfirmerVraiKMS   bool   `json:"a_confirmer_sur_le_vrai_kms,omitempty"`
	CleDeDeveloppement bool   `json:"cle_de_developpement,omitempty"`
}

func (p *Procedure) detruireCle(ctx context.Context, _ *journal.Journal, _ *etat) (any, error) {
	if p.ModeCle == "fake" {
		// Développement et simulation sans faux KMS : la clé HMAC est écrite
		// dans le code ; refusé en production (garde).
		ops := []string{"clé HMAC de développement : aucun KMS"}
		return ContenuDestructionCle{Etape: EtapeDestructionCle, KMS: "fake", EtatFinal: "sans objet",
			SHA256Operations: sha256Texte(ops), CleDeDeveloppement: true}, nil
	}
	b, err := p.Cle.Detruire(ctx)
	for _, o := range b.Operations {
		fmt.Fprintf(p.sortie(), "  %s\n", o)
	}
	if err != nil {
		return nil, err
	}
	return ContenuDestructionCle{Etape: EtapeDestructionCle, KMS: p.ModeCle, DejaDetruite: b.DejaDetruite,
		EtatFinal: b.EtatFinal, SHA256Operations: sha256Texte(b.Operations), ConfirmerVraiKMS: true}, nil
}

// ContenuVolume est le contenu de l'étape « renouvellement_volume ».
type ContenuVolume struct {
	Etape             string `json:"etape"`
	DateAttestation   string `json:"date_attestation"`
	SHA256CleChiffree string `json:"sha256_cle_chiffree"`
	SHA256EnteteLUKS  string `json:"sha256_entete_luks"`
	Rechiffrement     bool   `json:"rechiffrement"`
	EspaceLibreEfface bool   `json:"espace_libre_efface"`
	SansVolume        bool   `json:"sans_volume_chiffre,omitempty"`
}

func (p *Procedure) renouvelerVolume(ctx context.Context, _ *journal.Journal, e *etat) (any, error) {
	if p.SansVolume {
		return ContenuVolume{Etape: EtapeRenouvellementVolume, DateAttestation: p.maintenant().Format(journal.FormatHorodatage), SansVolume: true}, nil
	}
	a, err := p.Volume(ctx)
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, fmt.Errorf("%w : renouvellement des clés du volume chiffré, à lancer par root (sudo /usr/local/lib/diguee/volume.sh renouveler, deploiement/README.md §12), puis relancer la clôture", ErrAttente)
	}
	// L'attestation doit suivre la suppression des données et la
	// destruction de la clé : une attestation plus ancienne est celle d'un
	// essai antérieur.
	depuis := e.parEtape[EtapeDestructionCle].Horodatage
	if a.Date.UTC().Truncate(time.Minute).Before(depuis) {
		return nil, fmt.Errorf("%w : l'attestation du volume (%s) précède la destruction de la clé (%s) : relancer volume.sh renouveler",
			ErrAttente, a.Date.UTC().Format(time.RFC3339), depuis.Format(time.RFC3339))
	}
	for nom, h := range map[string]string{"sha256_cle_chiffree": a.SHA256CleChiffree, "sha256_entete_luks": a.SHA256EnteteLUKS} {
		if len(h) != 64 || !journal.EstHex(h) {
			return nil, fmt.Errorf("attestation du volume : %s invalide", nom)
		}
	}
	return ContenuVolume{Etape: EtapeRenouvellementVolume, DateAttestation: a.Date.UTC().Format(journal.FormatHorodatage),
		SHA256CleChiffree: a.SHA256CleChiffree, SHA256EnteteLUKS: a.SHA256EnteteLUKS,
		Rechiffrement: a.Rechiffrement, EspaceLibreEfface: a.EspaceLibreEfface}, nil
}

// OperationPV décrit une étape dans le procès-verbal.
type OperationPV struct {
	Etape     string `json:"etape"`
	Entree    int64  `json:"entree"`    // numéro de l'entrée du journal public
	Empreinte string `json:"empreinte"` // SHA-256 de l'entrée (journal public)
}

// ContenuProcesVerbal est le contenu du procès-verbal.
type ContenuProcesVerbal struct {
	Objet                     string             `json:"objet"`
	Date                      string             `json:"date"`
	DateCloture               string             `json:"date_cloture"`
	Operations                []OperationPV      `json:"operations"`
	ParticipationsSupprimees  int64              `json:"participations_supprimees"`
	SauvegardesSupprimees     int                `json:"sauvegardes_supprimees"`
	NouvelleSauvegarde        string             `json:"nouvelle_sauvegarde"`
	SHA256NouvelleSauvegarde  string             `json:"sha256_nouvelle_sauvegarde"`
	CleEmpreinte              string             `json:"cle_empreinte"`
	VolumeRenouvele           string             `json:"volume_renouvele"`
	Tables                    []base.LignesTable `json:"tables"`
	Participation             string             `json:"participation"`
	Reponses                  int64              `json:"reponses"`
	Publications              int64              `json:"publications"`
	DernierePublication       string             `json:"derniere_publication"`
	SHA256DernierePublication string             `json:"sha256_derniere_publication"`
	JournalVerifie            int64              `json:"journal_verifie"` // entrées vérifiées avant le procès-verbal
}

// procesVerbal contrôle l'état final et inscrit le procès-verbal.
func (p *Procedure) procesVerbal(ctx context.Context, j *journal.Journal, e *etat) (*journal.Entree, error) {
	var v ContenuVerification
	var s ContenuSuppression
	var ss ContenuSuppressionSauvegardes
	var ns ContenuNouvelleSauvegarde
	var dc ContenuDestructionCle
	var vo ContenuVolume
	for _, x := range []struct {
		etape string
		v     any
	}{{EtapeVerification, &v}, {EtapeSuppressionParticipant, &s}, {EtapeSuppressionSauvegardes, &ss},
		{EtapeNouvelleSauvegarde, &ns}, {EtapeDestructionCle, &dc}, {EtapeRenouvellementVolume, &vo}} {
		if err := e.contenu(x.etape, x.v); err != nil {
			return nil, err
		}
	}
	ls, err := p.Base.CompterTables(ctx)
	if err != nil {
		return nil, err
	}
	if compte(ls, "participation") >= 0 {
		return nil, errors.New("la table participation existe encore")
	}
	// Les réponses et les publications sont intactes.
	if r := compte(ls, "reponse"); r != v.Reponses {
		return nil, fmt.Errorf("reponse compte %d lignes, %d à la vérification", r, v.Reponses)
	}
	if n := compte(ls, "publication"); n < v.Publications {
		return nil, fmt.Errorf("publication compte %d lignes, %d à la vérification", n, v.Publications)
	}
	jour, _ := time.ParseInLocation(time.DateOnly, v.DernierePublication, time.UTC)
	pub, err := p.Base.LirePublication(ctx, jour)
	if err != nil {
		return nil, err
	}
	if pub == nil || pub.Empreinte.String() != v.SHA256Instantane {
		return nil, errors.New("la dernière publication a changé depuis la vérification")
	}
	ss2, autres, err := sauvegarde.Lister(ctx, p.Sauvegardes)
	if err != nil {
		return nil, err
	}
	if len(autres) > 0 || len(ss2) == 0 || ss2[0].Cle != ns.Cle {
		return nil, fmt.Errorf("compartiment des sauvegardes : la plus ancienne doit être celle de la clôture (%s)", ns.Cle)
	}
	c := ContenuProcesVerbal{
		Objet: "destruction des empreintes, de la clé de l'empreinte et des sauvegardes (Clôture, ADR 0002)",
		Date:  p.maintenant().Format(time.DateOnly), DateCloture: v.DateCloture,
		ParticipationsSupprimees: s.ParticipationsSupprimees,
		SauvegardesSupprimees:    ss.SauvegardesSupprimees + ns.SauvegardesSupprimeesAvant,
		NouvelleSauvegarde:       ns.Cle, SHA256NouvelleSauvegarde: ns.SHA256,
		CleEmpreinte:    "détruite (" + dc.KMS + ", " + dc.EtatFinal + ")",
		VolumeRenouvele: vo.DateAttestation,
		Tables:          ls, Participation: "absente",
		Reponses: compte(ls, "reponse"), Publications: compte(ls, "publication"),
		DernierePublication: v.DernierePublication, SHA256DernierePublication: v.SHA256Instantane,
		JournalVerifie: int64(len(e.entrees)),
	}
	if vo.SansVolume {
		c.VolumeRenouvele = "sans volume chiffré (développement)"
	}
	if dc.CleDeDeveloppement {
		c.CleEmpreinte = "clé de développement (aucun KMS)"
	}
	for _, et := range Etapes() {
		en := e.parEtape[et]
		c.Operations = append(c.Operations, OperationPV{Etape: et, Entree: en.Numero, Empreinte: en.Empreinte.String()})
	}
	en, err := j.InscrireSi(ctx, journal.ProcesVerbal, c, func(ctx context.Context, v journal.Vue) (bool, error) {
		d, err := v.DerniereDuType(ctx, journal.ProcesVerbal)
		if err != nil || d != nil {
			return false, err
		}
		ok, err := apres(EtapeRenouvellementVolume)(ctx, v)
		return ok, err
	})
	if err != nil {
		return nil, err
	}
	if en == nil {
		return nil, errors.New("inscription du procès-verbal refusée (déjà inscrit, ou étapes incomplètes)")
	}
	// La chaîne, procès-verbal compris, est vérifiée une dernière fois.
	es, err := j.Toutes(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := journal.VerifierChaine(es); err != nil {
		return nil, fmt.Errorf("journal public NON VALIDE après le procès-verbal : %w", err)
	}
	return en, nil
}
