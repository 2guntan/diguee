package observatoire

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/stockage"
)

// DelaiMinimal est le délai minimal du préavis, en jours (ADR 0008) ; la
// base refuse une échéance plus proche de l'envoi.
const DelaiMinimal = 15

// Textes donne les textes du dossier PDF (implémenté par i18n.Catalogue).
type Textes interface {
	T(cle string) string
	F(cle string, args ...any) string
	Date(t time.Time) string
}

// ReglagesPreavis règle le préavis.
type ReglagesPreavis struct {
	// Jours est le délai laissé à l'élu, en jours (DIGUEE_PREAVIS_JOURS) ;
	// DelaiMinimal au moins.
	Jours int
	// AdresseReponse est l'adresse où l'élu envoie son droit de réponse
	// (DIGUEE_ADRESSE_REPONSE), écrite dans le dossier.
	AdresseReponse string
	// Textes du dossier ; nil : les clés du catalogue elles-mêmes.
	Textes Textes
}

// ReglerPreavis règle le préavis. Un délai inférieur à DelaiMinimal est
// porté à DelaiMinimal.
func (s *Service) ReglerPreavis(r ReglagesPreavis) {
	r.Jours = max(r.Jours, DelaiMinimal)
	if r.AdresseReponse == "" {
		r.AdresseReponse = "À COMPLÉTER"
	}
	if r.Textes == nil {
		r.Textes = clesSeules{}
	}
	s.preavis = r
}

// JoursPreavis renvoie le délai du préavis, en jours.
func (s *Service) JoursPreavis() int { return s.preavis.Jours }

type clesSeules struct{}

func (clesSeules) T(cle string) string              { return cle }
func (clesSeules) F(cle string, args ...any) string { return cle + fmt.Sprint(args...) }
func (clesSeules) Date(t time.Time) string          { return t.Format(time.DateOnly) }

// Dossier renvoie le dossier PDF de l'élu pour un statut « non tenu » ou
// « partiel » validé. Une fois le préavis enregistré, c'est la copie
// conservée du dossier envoyé, vérifiée contre son SHA-256.
func (s *Service) Dossier(ctx context.Context, statut int64) ([]byte, error) {
	e, st, err := s.Statut(ctx, statut)
	switch {
	case err != nil:
		return nil, err
	case !st.Valeur.DemandePreavis() || st.Validation == nil || st.Rejet != nil:
		return nil, ErrEtape
	case st.PreavisEnvoye != nil:
		b, err := s.st.Lire(ctx, st.PreavisEnvoye.Dossier)
		switch {
		case errors.Is(err, stockage.ErrAltere):
			return nil, fmt.Errorf("%w : %w", ErrArchiveAlteree, err)
		case errors.Is(err, stockage.ErrIntrouvable):
			return nil, fmt.Errorf("%w : %w", ErrArchiveAbsente, err)
		case err != nil:
			return nil, fmt.Errorf("%w : %w", ErrStockage, err)
		}
		return b, nil
	}
	return ComposerDossier(e, st, s.preavis), nil
}

// EnregistrerPreavis enregistre l'envoi du dossier à l'élu, fait par un
// membre lui-même (la plateforme n'envoie rien) : la date d'envoi, entre la
// validation du statut et aujourd'hui, et comment il a été envoyé. Le
// dossier est conservé dans le stockage sous son SHA-256 ; l'échéance est
// fixée à la date d'envoi plus le délai du préavis.
func (s *Service) EnregistrerPreavis(ctx context.Context, acteur string, statut int64, envoyeLe time.Time, moyen string) error {
	moyen, envoyeLe = nettoyer(moyen), auJour(envoyeLe)
	e, st, err := s.Statut(ctx, statut)
	switch {
	case err != nil:
		return err
	case !st.Valeur.DemandePreavis():
		return ErrEtape
	case st.Etape() != Valide:
		return ErrEtape
	case envoyeLe.IsZero() || envoyeLe.Before(st.Validation.Le) || envoyeLe.After(s.jour()):
		return ErrDateEnvoi
	case !longueur(moyen, 1, 500):
		return ErrMoyen
	}
	dossier := ComposerDossier(e, st, s.preavis)
	emp, err := s.st.Deposer(ctx, "application/pdf", dossier)
	if err != nil {
		return fmt.Errorf("%w : %w", ErrStockage, err)
	}
	return s.m.EnregistrerPreavis(ctx, statut, PreavisEnvoye{Acte: s.acte(acteur), EnvoyeLe: envoyeLe,
		Echeance: envoyeLe.AddDate(0, 0, s.preavis.Jours), Moyen: moyen, Dossier: emp})
}

// SaisieReponse est la saisie d'un droit de réponse.
type SaisieReponse struct {
	Texte       string
	Canal       Canal
	CanalDetail string
	RecuLe      time.Time
}

// SaisirReponse enregistre un droit de réponse de l'élu à un statut qui a
// fait l'objet d'un préavis, recopié tel quel, avec son canal de réception.
// Il doit ensuite être vérifié par un autre membre.
func (s *Service) SaisirReponse(ctx context.Context, acteur string, statut int64, in SaisieReponse) (int64, error) {
	in.Texte, in.CanalDetail, in.RecuLe = nettoyer(in.Texte), nettoyer(in.CanalDetail), auJour(in.RecuLe)
	_, st, err := s.Statut(ctx, statut)
	switch {
	case err != nil:
		return 0, err
	case st.PreavisEnvoye == nil:
		return 0, ErrSansPreavis
	case !longueur(in.Texte, 1, 10000):
		return 0, ErrTexteReponse
	case !in.Canal.Valide():
		return 0, ErrCanal
	case !longueur(in.CanalDetail, 1, 500):
		return 0, ErrCanalDetail
	case in.RecuLe.IsZero() || in.RecuLe.Before(st.PreavisEnvoye.EnvoyeLe) || in.RecuLe.After(s.jour()):
		return 0, ErrDateReception
	}
	return s.m.SaisirReponse(ctx, DroitReponse{StatutID: statut, Texte: in.Texte, Canal: in.Canal,
		CanalDetail: in.CanalDetail, RecuLe: in.RecuLe, Saisi: s.acte(acteur)})
}

// Reponse renvoie un droit de réponse, son statut et son engagement (équipe).
func (s *Service) Reponse(ctx context.Context, id int64) (*Engagement, *Statut, *DroitReponse, error) {
	eid, err := s.m.EngagementDeLaReponse(ctx, id)
	if err != nil {
		return nil, nil, nil, err
	}
	e, err := s.m.Engagement(ctx, eid)
	if err != nil {
		return nil, nil, nil, err
	}
	for i := range e.Reponses {
		if r := &e.Reponses[i]; r.ID == id {
			for j := range e.Statuts {
				if e.Statuts[j].ID == r.StatutID {
					return e, &e.Statuts[j], r, nil
				}
			}
		}
	}
	return nil, nil, nil, ErrIntrouvable
}

// VerifierReponse : un autre membre que celui qui l'a saisi vérifie le
// droit de réponse (texte identique à l'original, canal vérifiable). Si le
// statut est déjà publié, le droit de réponse l'est aussitôt et inscrit au
// journal public ; sinon il le sera avec le statut. Renvoie le numéro de
// l'entrée du journal, ou 0.
func (s *Service) VerifierReponse(ctx context.Context, acteur string, id int64) (int64, error) {
	_, _, r, err := s.Reponse(ctx, id)
	switch {
	case err != nil:
		return 0, err
	case r.Saisi.Par == acteur:
		return 0, ErrQuatreYeux
	case r.Etape() != ReponseSaisie:
		return 0, ErrEtape
	}
	return s.m.VerifierReponse(ctx, id, s.acte(acteur), s.journaliserReponse)
}

// RejeterReponse : un autre membre que celui qui l'a saisi refuse le droit
// de réponse (canal non vérifiable, par exemple), avec un motif. Il n'est
// pas publié.
func (s *Service) RejeterReponse(ctx context.Context, acteur string, id int64, motif string) error {
	motif = nettoyer(motif)
	if !longueur(motif, 1, 2000) {
		return ErrMotif
	}
	_, _, r, err := s.Reponse(ctx, id)
	switch {
	case err != nil:
		return err
	case r.Saisi.Par == acteur:
		return ErrQuatreYeux
	case r.Etape() != ReponseSaisie:
		return ErrEtape
	}
	return s.m.RejeterReponse(ctx, id, s.acte(acteur), motif)
}

// journaliserReponse inscrit l'entrée « droit de réponse publié ».
func (s *Service) journaliserReponse(ctx context.Context, jm journal.Magasin, e *Engagement, st *Statut, r *DroitReponse) (int64, error) {
	if r.Etape() != ReponseVerifiee || st.Publication == nil {
		return 0, ErrEtape
	}
	c, err := ContenuJournalReponse(e, st, r)
	if err != nil {
		return 0, err
	}
	ent, err := journal.Nouveau(jm, s.maintenant).Inscrire(ctx, journal.DroitDeReponse, c)
	if err != nil {
		return 0, fmt.Errorf("%w : %w", ErrJournalRefuse, err)
	}
	return ent.Numero, nil
}

// ContenuReponse est le contenu de l'entrée « droit de réponse publié » du
// journal public : le texte n'y figure pas, seulement son SHA-256.
type ContenuReponse struct {
	Engagement   int64  `json:"engagement"`
	Statut       int64  `json:"statut"`
	DroitReponse int64  `json:"droit_reponse"`
	Elu          string `json:"elu"`
	Canal        Canal  `json:"canal"`
	RecuLe       string `json:"recu_le"` // AAAA-MM-JJ
	SaisiPar     string `json:"saisi_par"`
	VerifiePar   string `json:"verifie_par"`
	Texte        string `json:"texte_sha256"`
}

// TexteReponse est ce que résume ContenuReponse.Texte : le SHA-256 du JSON
// canonique (RFC 8785) de ces champs, tels qu'affichés sur la fiche.
type TexteReponse struct {
	Texte  string `json:"texte"`
	Canal  Canal  `json:"canal"`
	RecuLe string `json:"recu_le"` // AAAA-MM-JJ
}

// EmpreinteReponse calcule le SHA-256 du texte d'un droit de réponse.
func EmpreinteReponse(r *DroitReponse) (string, error) {
	c, err := journal.CanoniqueDe(TexteReponse{Texte: r.Texte, Canal: r.Canal, RecuLe: r.RecuLe.Format(time.DateOnly)})
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(c)
	return hex.EncodeToString(h[:]), nil
}

// ContenuJournalReponse prépare l'entrée du journal d'un droit de réponse.
func ContenuJournalReponse(e *Engagement, st *Statut, r *DroitReponse) (ContenuReponse, error) {
	texte, err := EmpreinteReponse(r)
	if err != nil {
		return ContenuReponse{}, err
	}
	c := ContenuReponse{Engagement: e.ID, Statut: st.ID, DroitReponse: r.ID, Elu: e.Elu.Nom, Canal: r.Canal,
		RecuLe: r.RecuLe.Format(time.DateOnly), SaisiPar: r.Saisi.ParNom, Texte: texte}
	if r.Verification != nil {
		c.VerifiePar = r.Verification.ParNom
	}
	return c, nil
}

// auJour ramène une date au jour (UTC) ; la date zéro reste zéro.
func auJour(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
