package observatoire

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/stockage"
)

// Erreur est une erreur de saisie ou de circuit ; sa valeur est la clé du
// message dans le catalogue.
type Erreur string

func (e Erreur) Error() string { return "observatoire : " + string(e) }

// Erreurs du service (et de la base, qui revérifie les mêmes règles).
const (
	ErrIntrouvable    Erreur = "obs.erreur.introuvable"
	ErrNom            Erreur = "obs.erreur.nom"
	ErrFonction       Erreur = "obs.erreur.fonction"
	ErrContact        Erreur = "obs.erreur.contact"
	ErrMandat         Erreur = "obs.erreur.mandat"
	ErrDate           Erreur = "obs.erreur.date"
	ErrURL            Erreur = "obs.erreur.url"
	ErrTitre          Erreur = "obs.erreur.titre"
	ErrFichierVide    Erreur = "obs.erreur.fichier_vide"
	ErrFichierTaille  Erreur = "obs.erreur.fichier_taille"
	ErrFichierType    Erreur = "obs.erreur.fichier_type"
	ErrCitation       Erreur = "obs.erreur.citation"
	ErrContexte       Erreur = "obs.erreur.contexte"
	ErrEcheance       Erreur = "obs.erreur.echeance"
	ErrSansSource     Erreur = "obs.erreur.sans_source"
	ErrSourceInconnue Erreur = "obs.erreur.source_inconnue"
	ErrEluInconnu     Erreur = "obs.erreur.elu_inconnu"
	ErrValeur         Erreur = "obs.erreur.valeur"
	ErrExplication    Erreur = "obs.erreur.explication"
	ErrMotif          Erreur = "obs.erreur.motif"
	ErrSansPreuve     Erreur = "obs.erreur.sans_preuve"
	ErrNonValide      Erreur = "obs.erreur.engagement_non_valide"
	ErrDejaValide     Erreur = "obs.erreur.deja_valide"
	ErrEtape          Erreur = "obs.erreur.etape"
	ErrPasLeRedacteur Erreur = "obs.erreur.pas_le_redacteur"
	ErrQuatreYeux     Erreur = "obs.erreur.quatre_yeux"
	ErrPreavis        Erreur = "obs.erreur.preavis"
	ErrPreavisEnCours Erreur = "obs.erreur.preavis_en_cours"
	ErrSansPreavis    Erreur = "obs.erreur.sans_preavis"
	ErrDateEnvoi      Erreur = "obs.erreur.date_envoi"
	ErrMoyen          Erreur = "obs.erreur.moyen"
	ErrReponseAttente Erreur = "obs.erreur.reponse_en_attente"
	ErrTexteReponse   Erreur = "obs.erreur.texte_reponse"
	ErrCanal          Erreur = "obs.erreur.canal"
	ErrCanalDetail    Erreur = "obs.erreur.canal_detail"
	ErrDateReception  Erreur = "obs.erreur.date_reception"
	ErrJournalRefuse  Erreur = "obs.erreur.journal"
	ErrStockage       Erreur = "obs.erreur.stockage"
	ErrArchiveAlteree Erreur = "obs.erreur.archive_alteree"
	ErrArchiveAbsente Erreur = "obs.erreur.archive_absente"
)

// Types de fichiers archivés acceptés, reconnus à leur signature (jamais au
// nom ni au type annoncés par le navigateur), et leur extension.
var extensions = map[string]string{"application/pdf": "pdf", "image/png": "png", "image/jpeg": "jpg"}

// TypeDe reconnaît un PDF, une image PNG ou JPEG à ses premiers octets.
func TypeDe(b []byte) (string, bool) {
	switch {
	case bytes.HasPrefix(b, []byte("%PDF-")):
		return "application/pdf", true
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png", true
	case bytes.HasPrefix(b, []byte("\xff\xd8\xff")):
		return "image/jpeg", true
	}
	return "", false
}

// Service applique les règles de l'observatoire.
type Service struct {
	m          Magasin
	st         stockage.Stockage
	tailleMax  int64
	maintenant func() time.Time
	preavis    ReglagesPreavis
}

// NouveauService renvoie le service. tailleMax borne une copie archivée
// (DIGUEE_ARCHIVE_TAILLE_MO) ; maintenant vaut time.Now si nil. Le préavis
// suit les réglages par défaut (DelaiMinimal jours, adresse de réponse « À
// COMPLÉTER », textes de repli) tant que ReglerPreavis n'est pas appelé.
func NouveauService(m Magasin, st stockage.Stockage, tailleMax int64, maintenant func() time.Time) *Service {
	if maintenant == nil {
		maintenant = time.Now
	}
	s := &Service{m: m, st: st, tailleMax: tailleMax, maintenant: maintenant}
	s.ReglerPreavis(ReglagesPreavis{})
	return s
}

// Aujourdhui renvoie le jour courant (UTC), d'après l'horloge du service.
func (s *Service) Aujourdhui() time.Time { return s.jour() }

// TailleMax renvoie la taille maximale d'une copie archivée, en octets.
func (s *Service) TailleMax() int64 { return s.tailleMax }

// jour renvoie le jour courant (UTC) : aucune date de l'observatoire n'est
// plus fine que le jour.
func (s *Service) jour() time.Time {
	t := s.maintenant().UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func (s *Service) acte(acteur string) Acte { return Acte{Par: acteur, Le: s.jour()} }

func longueur(v string, min, max int) bool {
	n := utf8.RuneCountInString(v)
	return utf8.ValidString(v) && n >= min && n <= max
}

// nettoyer retire les espaces autour d'un texte et normalise ses fins de
// ligne ; le texte lui-même est gardé tel quel (citation mot pour mot).
func nettoyer(v string) string {
	return strings.TrimSpace(strings.ReplaceAll(v, "\r\n", "\n"))
}

// SaisieElu est la saisie d'un élu.
type SaisieElu struct {
	Nom, Fonction, ContactOfficiel string
	Mandat                         Mandat
}

// SaisirElu enregistre un élu.
func (s *Service) SaisirElu(ctx context.Context, acteur string, e SaisieElu) (int64, error) {
	e.Nom, e.Fonction, e.ContactOfficiel = nettoyer(e.Nom), nettoyer(e.Fonction), nettoyer(e.ContactOfficiel)
	switch {
	case !longueur(e.Nom, 1, 200):
		return 0, ErrNom
	case !longueur(e.Fonction, 1, 200):
		return 0, ErrFonction
	case !longueur(e.ContactOfficiel, 0, 500):
		return 0, ErrContact
	case e.Mandat.Debut.IsZero() || !e.Mandat.Fin.After(e.Mandat.Debut):
		return 0, ErrMandat
	}
	return s.m.AjouterElu(ctx, Elu{Nom: e.Nom, Fonction: e.Fonction, ContactOfficiel: e.ContactOfficiel,
		Mandat: e.Mandat, Saisi: s.acte(acteur)})
}

// Elus renvoie tous les élus.
func (s *Service) Elus(ctx context.Context) ([]Elu, error) { return s.m.Elus(ctx) }

// Sources renvoie toutes les sources.
func (s *Service) Sources(ctx context.Context) ([]Source, error) { return s.m.Sources(ctx) }

// SaisieSource est la saisie d'une source : son lien d'origine et la copie
// archivée téléversée par le rédacteur.
type SaisieSource struct {
	URL, Titre string
	Date       time.Time
	Fichier    []byte
}

// URLValide accepte une adresse http:// ou https:// absolue, sans
// identifiants ni espace.
func URLValide(v string) bool {
	if len(v) > 2048 || strings.ContainsAny(v, " \t\r\n") {
		return false
	}
	u, err := url.Parse(v)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil
}

// SaisirSource vérifie le fichier (taille, type reconnu à sa signature), le
// dépose dans le stockage sous son SHA-256, puis enregistre la source.
func (s *Service) SaisirSource(ctx context.Context, acteur string, in SaisieSource) (Source, error) {
	in.URL, in.Titre = strings.TrimSpace(in.URL), nettoyer(in.Titre)
	switch {
	case !URLValide(in.URL):
		return Source{}, ErrURL
	case !longueur(in.Titre, 1, 300):
		return Source{}, ErrTitre
	case in.Date.IsZero():
		return Source{}, ErrDate
	case len(in.Fichier) == 0:
		return Source{}, ErrFichierVide
	case int64(len(in.Fichier)) > s.tailleMax:
		return Source{}, ErrFichierTaille
	}
	typ, ok := TypeDe(in.Fichier)
	if !ok {
		return Source{}, ErrFichierType
	}
	e, err := s.st.Deposer(ctx, typ, in.Fichier)
	if err != nil {
		return Source{}, fmt.Errorf("%w : %w", ErrStockage, err)
	}
	if relu, err := s.st.Lire(ctx, e); err != nil || !bytes.Equal(relu, in.Fichier) {
		return Source{}, fmt.Errorf("%w : relecture après dépôt : %v", ErrStockage, err)
	}
	src := Source{URL: in.URL, Titre: in.Titre, Date: in.Date, Archive: e, TypeArchive: typ,
		Taille: int64(len(in.Fichier)), Ajoutee: s.acte(acteur)}
	id, err := s.m.AjouterSource(ctx, src)
	src.ID = id
	return src, err
}

// SaisieEngagement est la saisie d'un engagement.
type SaisieEngagement struct {
	Elu                       int64
	Citation, Contexte        string
	DateDeclaration, Echeance time.Time
	Sources                   []int64
}

// SaisirEngagement enregistre un engagement et ses premières sources (une
// au moins).
func (s *Service) SaisirEngagement(ctx context.Context, acteur string, in SaisieEngagement) (int64, error) {
	in.Citation, in.Contexte = nettoyer(in.Citation), nettoyer(in.Contexte)
	switch {
	case in.Elu <= 0:
		return 0, ErrEluInconnu
	case !longueur(in.Citation, 1, 5000):
		return 0, ErrCitation
	case !longueur(in.Contexte, 0, 5000):
		return 0, ErrContexte
	case in.DateDeclaration.IsZero() || in.Echeance.IsZero():
		return 0, ErrDate
	case in.Echeance.Before(in.DateDeclaration):
		return 0, ErrEcheance
	case len(in.Sources) == 0:
		return 0, ErrSansSource
	}
	slices.Sort(in.Sources)
	in.Sources = slices.Compact(in.Sources)
	return s.m.AjouterEngagement(ctx, Engagement{Elu: Elu{ID: in.Elu}, Citation: in.Citation, Contexte: in.Contexte,
		DateDeclaration: in.DateDeclaration, Echeance: in.Echeance, Cree: s.acte(acteur)}, in.Sources)
}

// Engagements renvoie tous les engagements (équipe).
func (s *Service) Engagements(ctx context.Context) ([]Engagement, error) { return s.m.Engagements(ctx) }

// Engagement renvoie un engagement complet (équipe).
func (s *Service) Engagement(ctx context.Context, id int64) (*Engagement, error) {
	return s.m.Engagement(ctx, id)
}

// LierSource ajoute une source à un engagement qui n'est pas encore validé.
func (s *Service) LierSource(ctx context.Context, acteur string, engagement, source int64) error {
	e, err := s.m.Engagement(ctx, engagement)
	if err != nil {
		return err
	}
	if e.Validation != nil {
		return ErrDejaValide
	}
	return s.m.LierSource(ctx, engagement, source)
}

// ValiderEngagement : un relecteur, distinct du rédacteur, valide la saisie.
func (s *Service) ValiderEngagement(ctx context.Context, acteur string, id int64) error {
	e, err := s.m.Engagement(ctx, id)
	switch {
	case err != nil:
		return err
	case e.Validation != nil:
		return ErrDejaValide
	case e.Cree.Par == acteur:
		return ErrQuatreYeux
	case len(e.Sources) == 0:
		return ErrSansSource
	}
	return s.m.ValiderEngagement(ctx, id, s.acte(acteur))
}

// Statut renvoie un statut et son engagement (équipe).
func (s *Service) Statut(ctx context.Context, id int64) (*Engagement, *Statut, error) {
	eid, err := s.m.EngagementDuStatut(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	e, err := s.m.Engagement(ctx, eid)
	if err != nil {
		return nil, nil, err
	}
	for i := range e.Statuts {
		if e.Statuts[i].ID == id {
			return e, &e.Statuts[i], nil
		}
	}
	return nil, nil, ErrIntrouvable
}

// RedigerStatut enregistre le brouillon d'un statut, avec ses premières
// preuves, pour un engagement validé.
func (s *Service) RedigerStatut(ctx context.Context, acteur string, engagement int64, v Valeur, explication string, preuves []int64) (int64, error) {
	explication = nettoyer(explication)
	switch {
	case !v.Valide():
		return 0, ErrValeur
	case !longueur(explication, 1, 5000):
		return 0, ErrExplication
	}
	e, err := s.m.Engagement(ctx, engagement)
	if err != nil {
		return 0, err
	}
	if e.Validation == nil {
		return 0, ErrNonValide
	}
	slices.Sort(preuves)
	preuves = slices.Compact(preuves)
	return s.m.RedigerStatut(ctx, Statut{EngagementID: engagement, Valeur: v, Explication: explication,
		Redige: s.acte(acteur)}, preuves)
}

// AjouterPreuve ajoute une preuve au brouillon de son rédacteur.
func (s *Service) AjouterPreuve(ctx context.Context, acteur string, statut, source int64) error {
	_, st, err := s.Statut(ctx, statut)
	switch {
	case err != nil:
		return err
	case st.Redige.Par != acteur:
		return ErrPasLeRedacteur
	case st.Etape() != Brouillon:
		return ErrEtape
	}
	return s.m.AjouterPreuve(ctx, statut, source)
}

// Proposer : le rédacteur propose son brouillon, qui a au moins une preuve.
func (s *Service) Proposer(ctx context.Context, acteur string, statut int64) error {
	_, st, err := s.Statut(ctx, statut)
	switch {
	case err != nil:
		return err
	case st.Redige.Par != acteur:
		return ErrPasLeRedacteur
	case st.Etape() != Brouillon:
		return ErrEtape
	case len(st.Preuves) == 0:
		return ErrSansPreuve
	}
	return s.m.Proposer(ctx, statut, s.acte(acteur))
}

// Valider : un relecteur, forcément une autre personne que le rédacteur,
// valide un statut proposé (T7).
func (s *Service) Valider(ctx context.Context, acteur string, statut int64) error {
	_, st, err := s.Statut(ctx, statut)
	switch {
	case err != nil:
		return err
	case st.Redige.Par == acteur:
		return ErrQuatreYeux
	case st.Etape() != Propose:
		return ErrEtape
	}
	return s.m.Valider(ctx, statut, s.acte(acteur))
}

// Rejeter : un relecteur distinct du rédacteur renvoie la proposition.
func (s *Service) Rejeter(ctx context.Context, acteur string, statut int64, motif string) error {
	motif = nettoyer(motif)
	if !longueur(motif, 1, 2000) {
		return ErrMotif
	}
	_, st, err := s.Statut(ctx, statut)
	switch {
	case err != nil:
		return err
	case st.Redige.Par == acteur:
		return ErrQuatreYeux
	case st.Etape() != Propose:
		return ErrEtape
	}
	return s.m.Rejeter(ctx, statut, s.acte(acteur), motif)
}

// Publier publie un statut validé et l'inscrit au journal public, dans la
// même transaction. « Non tenu » et « partiel » ne sont publiables qu'avec
// un préavis enregistré (ErrSansPreavis) et échu (ErrPreavisEnCours) ; aucun
// statut n'est publié tant qu'un droit de réponse attend sa vérification
// (ErrReponseAttente). Les droits de réponse vérifiés sont publiés avec le
// statut. Renvoie le numéro de l'entrée du journal du statut.
func (s *Service) Publier(ctx context.Context, acteur string, statut int64) (int64, error) {
	e, st, err := s.Statut(ctx, statut)
	if err != nil {
		return 0, err
	}
	if err := s.publiable(e, st); err != nil {
		return 0, err
	}
	a := s.acte(acteur)
	return s.m.Publier(ctx, statut, a, func(ctx context.Context, jm journal.Magasin, e *Engagement, st *Statut) (int64, error) {
		if err := s.publiable(e, st); err != nil {
			return 0, err
		}
		c, err := ContenuJournal(e, st)
		if err != nil {
			return 0, err
		}
		ent, err := journal.Nouveau(jm, s.maintenant).Inscrire(ctx, journal.StatutPublie, c)
		if err != nil {
			return 0, fmt.Errorf("%w : %w", ErrJournalRefuse, err)
		}
		return ent.Numero, nil
	}, s.journaliserReponse)
}

// publiable vérifie qu'un statut peut être publié aujourd'hui.
func (s *Service) publiable(e *Engagement, st *Statut) error {
	switch {
	case st.Validation == nil || (st.Etape() != Valide && st.Etape() != Preavis):
		return ErrEtape
	case st.Valeur.DemandePreavis() && st.PreavisEnvoye == nil:
		return ErrSansPreavis
	case st.Valeur.DemandePreavis() && s.jour().Before(st.PreavisEnvoye.Echeance):
		return ErrPreavisEnCours
	case e.ReponseEnAttente(st.ID):
		return ErrReponseAttente
	}
	return nil
}

// ContenuStatut est le contenu de l'entrée « statut publié » du journal
// public. Les textes n'y figurent pas, seulement leur SHA-256 (Texte) ; les
// copies archivées y figurent par leur SHA-256, que chacun peut recalculer
// sur les fichiers servis.
type ContenuStatut struct {
	Engagement int64    `json:"engagement"`
	Statut     int64    `json:"statut"`
	Valeur     Valeur   `json:"valeur"`
	Elu        string   `json:"elu"`
	RedigePar  string   `json:"redige_par"`
	ValidePar  string   `json:"valide_par"`
	Sources    []string `json:"sources_sha256"`
	Preuves    []string `json:"preuves_sha256"`
	Texte      string   `json:"texte_sha256"`
	// Préavis (non tenu, partiel) : date d'envoi du dossier à l'élu
	// (AAAA-MM-JJ), son échéance, et le SHA-256 du dossier envoyé.
	PreavisEnvoyeLe string `json:"preavis_envoye_le,omitempty"`
	PreavisEcheance string `json:"preavis_echeance,omitempty"`
	Dossier         string `json:"dossier_sha256,omitempty"`
}

// Texte est ce que résume ContenuStatut.Texte : le SHA-256 du JSON canonique
// (RFC 8785) de ces champs, tels qu'affichés sur la fiche.
type Texte struct {
	Citation        string `json:"citation"`
	DateDeclaration string `json:"date_declaration"` // AAAA-MM-JJ
	Echeance        string `json:"echeance"`         // AAAA-MM-JJ
	Valeur          Valeur `json:"valeur"`
	Explication     string `json:"explication"`
}

// EmpreinteTexte calcule le SHA-256 du texte d'un statut publié.
func EmpreinteTexte(e *Engagement, st *Statut) (string, error) {
	c, err := journal.CanoniqueDe(Texte{Citation: e.Citation, DateDeclaration: e.DateDeclaration.Format(time.DateOnly),
		Echeance: e.Echeance.Format(time.DateOnly), Valeur: st.Valeur, Explication: st.Explication})
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(c)
	return hex.EncodeToString(h[:]), nil
}

// ContenuJournal prépare l'entrée du journal public d'un statut publié.
func ContenuJournal(e *Engagement, st *Statut) (ContenuStatut, error) {
	texte, err := EmpreinteTexte(e, st)
	if err != nil {
		return ContenuStatut{}, err
	}
	c := ContenuStatut{Engagement: e.ID, Statut: st.ID, Valeur: st.Valeur, Elu: e.Elu.Nom,
		RedigePar: st.Redige.ParNom, ValidePar: st.Validation.ParNom, Texte: texte,
		Sources: []string{}, Preuves: []string{}}
	for _, src := range e.Sources {
		c.Sources = append(c.Sources, src.Archive.String())
	}
	for _, p := range st.Preuves {
		c.Preuves = append(c.Preuves, p.Archive.String())
	}
	if p := st.PreavisEnvoye; p != nil {
		c.PreavisEnvoyeLe, c.PreavisEcheance = p.EnvoyeLe.Format(time.DateOnly), p.Echeance.Format(time.DateOnly)
		c.Dossier = p.Dossier.String()
	}
	return c, nil
}

// ListePublique renvoie une page de la liste publique.
func (s *Service) ListePublique(ctx context.Context, f Filtre) (*Liste, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	return s.m.ListePublique(ctx, f)
}

// FichePublique renvoie la fiche publique d'un engagement : ses sources et
// ses statuts publiés, dans l'ordre de publication. ErrIntrouvable s'il
// n'est pas public.
func (s *Service) FichePublique(ctx context.Context, id int64) (*Engagement, error) {
	return s.m.FichePublique(ctx, id)
}

// Archive renvoie le type et le contenu d'une copie archivée, vérifiée
// contre son SHA-256. Publique : seulement une archive d'un engagement public
// ou d'un statut publié ; sinon toute archive connue (équipe, pour relire).
func (s *Service) Archive(ctx context.Context, e stockage.Empreinte, publique bool) (string, []byte, error) {
	typ, err := s.m.TypeArchive(ctx, e, publique)
	if err != nil {
		return "", nil, err
	}
	b, err := s.st.Lire(ctx, e)
	switch {
	case errors.Is(err, stockage.ErrAltere):
		return "", nil, fmt.Errorf("%w : %w", ErrArchiveAlteree, err)
	case errors.Is(err, stockage.ErrIntrouvable):
		return "", nil, fmt.Errorf("%w : %w", ErrArchiveAbsente, err)
	case err != nil:
		return "", nil, fmt.Errorf("%w : %w", ErrStockage, err)
	}
	return typ, b, nil
}
