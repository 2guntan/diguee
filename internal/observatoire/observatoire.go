// Package observatoire tient l'observatoire des engagements (spec §6.4 et
// §9, ADR 0008) : élus, engagements cités mot pour mot, sources archivées
// sous leur SHA-256, et statuts.
//
// Tout est en ajout seul, jusque dans la base (migration 0007). Le circuit
// d'un statut est une suite d'étapes ajoutées, jamais une ligne modifiée :
//
//	brouillon (rédacteur, avec ses preuves)
//	  → proposé (par son rédacteur)
//	    → validé (par un relecteur distinct du rédacteur) → publié (journal public)
//	    → rejeté (par un relecteur distinct, avec un motif ; fin du circuit)
//
// Un engagement est lui aussi validé par un relecteur distinct de son
// rédacteur avant qu'un statut puisse être rédigé ; il n'apparaît sur les
// pages publiques qu'avec son premier statut publié. Une erreur se corrige
// en publiant un nouveau statut qui l'explique.
//
// La règle des quatre yeux est vérifiée ici (Service) et imposée par la base
// (contraintes CHECK).
//
// « Non tenu » et « partiel » demandent en plus un préavis (ticket #14,
// migration 0008) : la plateforme prépare le dossier de l'élu en PDF, un
// membre l'envoie lui-même par le canal officiel de l'élu (la plateforme
// n'envoie jamais rien) et enregistre la date d'envoi ; le statut n'est
// publiable qu'à partir de l'échéance (DIGUEE_PREAVIS_JOURS jours plus
// tard, 15 au moins), ce que la base impose aussi :
//
//	validé (non tenu, partiel) → préavis envoyé (J) → J+15 → publié
//	                                └ droit de réponse saisi → vérifié (par un autre membre) → publié
//	                                                         └ rejeté (par un autre membre, avec un motif)
//
// Un droit de réponse vérifié est publié tel quel avec le statut, ou dès sa
// vérification si le statut est déjà publié ; chaque publication est
// inscrite au journal public dans la même transaction.
package observatoire

import (
	"context"
	"time"

	"github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/stockage"
)

// Valeur est la valeur d'un statut.
type Valeur string

const (
	EnCours Valeur = "en_cours"
	Tenu    Valeur = "tenu"
	Partiel Valeur = "partiel"
	NonTenu Valeur = "non_tenu"
)

// Valeurs est la liste des valeurs, dans l'ordre d'affichage.
var Valeurs = []Valeur{EnCours, Tenu, Partiel, NonTenu}

// Valide indique une valeur connue.
func (v Valeur) Valide() bool {
	for _, x := range Valeurs {
		if v == x {
			return true
		}
	}
	return false
}

// PubliableSansPreavis indique les valeurs publiables dès leur validation :
// « tenu » et « en cours ». « Non tenu » et « partiel » demandent un préavis
// de DIGUEE_PREAVIS_JOURS jours à l'élu (ticket #14).
func (v Valeur) PubliableSansPreavis() bool { return v == Tenu || v == EnCours }

// DemandePreavis indique les valeurs qui demandent un préavis à l'élu.
func (v Valeur) DemandePreavis() bool { return v.Valide() && !v.PubliableSansPreavis() }

// Etape est l'étape atteinte par un statut.
type Etape string

const (
	Brouillon Etape = "brouillon"
	Propose   Etape = "propose"
	Valide    Etape = "valide"
	Rejete    Etape = "rejete"
	Preavis   Etape = "preavis" // préavis envoyé à l'élu (non tenu, partiel)
	Publie    Etape = "publie"
)

// Acte est une étape franchie : par qui (ID du membre, et son nom à la
// lecture), et quel jour.
type Acte struct {
	Par    string
	ParNom string
	Le     time.Time
}

// Mandat est la période d'un mandat d'élu.
type Mandat struct {
	Debut, Fin time.Time
}

// Cle identifie le mandat dans un filtre : « AAAA-MM-JJ_AAAA-MM-JJ ».
func (m Mandat) Cle() string {
	return m.Debut.Format(time.DateOnly) + "_" + m.Fin.Format(time.DateOnly)
}

// LireMandat lit une clé de mandat.
func LireMandat(s string) (Mandat, bool) {
	if len(s) != 21 || s[10] != '_' {
		return Mandat{}, false
	}
	d, err1 := time.Parse(time.DateOnly, s[:10])
	f, err2 := time.Parse(time.DateOnly, s[11:])
	return Mandat{Debut: d, Fin: f}, err1 == nil && err2 == nil && f.After(d)
}

// Elu est une personne élue, à une fonction, pour un mandat.
type Elu struct {
	ID              int64
	Nom             string
	Fonction        string
	Mandat          Mandat
	ContactOfficiel string // canal officiel du préavis (ticket #14) ; jamais publié
	Saisi           Acte
}

// Source est un document d'origine : son lien et la copie archivée par
// Diguee, rangée dans le stockage sous son SHA-256.
type Source struct {
	ID          int64
	URL         string
	Titre       string
	Date        time.Time
	Archive     stockage.Empreinte
	TypeArchive string // application/pdf, image/png ou image/jpeg
	Taille      int64
	Ajoutee     Acte
}

// Extension renvoie l'extension du fichier archivé.
func (s Source) Extension() string { return extensions[s.TypeArchive] }

// Engagement est une promesse publique d'un élu, citée mot pour mot.
type Engagement struct {
	ID              int64
	Elu             Elu
	Citation        string
	Contexte        string
	DateDeclaration time.Time
	Echeance        time.Time
	Cree            Acte
	Validation      *Acte // nil tant qu'un relecteur ne l'a pas validé
	Sources         []Source
	// Statuts : tous pour l'équipe (par identifiant) ; les seuls statuts
	// publiés, dans l'ordre de publication, sur les pages publiques.
	Statuts []Statut
	// Reponses : les droits de réponse de l'élu ; tous pour l'équipe (par
	// identifiant), les seuls publiés, dans l'ordre de publication, sur les
	// pages publiques.
	Reponses []DroitReponse
}

// ReponsesAu renvoie les droits de réponse à un statut.
func (e Engagement) ReponsesAu(statut int64) []DroitReponse {
	var rs []DroitReponse
	for _, r := range e.Reponses {
		if r.StatutID == statut {
			rs = append(rs, r)
		}
	}
	return rs
}

// ReponseEnAttente indique qu'un droit de réponse au statut attend sa
// vérification : le statut n'est pas publié avant.
func (e Engagement) ReponseEnAttente(statut int64) bool {
	for _, r := range e.ReponsesAu(statut) {
		if r.Etape() == ReponseSaisie {
			return true
		}
	}
	return false
}

// Dernier renvoie le dernier statut publié, ou nil.
func (e Engagement) Dernier() *Statut {
	for i := len(e.Statuts) - 1; i >= 0; i-- {
		if e.Statuts[i].Publication != nil {
			return &e.Statuts[i]
		}
	}
	return nil
}

// Rejet est le refus d'une proposition par un relecteur.
type Rejet struct {
	Acte
	Motif string
}

// Publication est la publication d'un statut, inscrite au journal public.
type Publication struct {
	Acte
	JournalNumero int64
}

// Statut est l'état d'un engagement à une date donnée, avec ses preuves.
type Statut struct {
	ID            int64
	EngagementID  int64
	Valeur        Valeur
	Explication   string
	Redige        Acte
	Preuves       []Source
	Proposition   *Acte
	Validation    *Acte
	Rejet         *Rejet
	PreavisEnvoye *PreavisEnvoye // nil sans préavis
	Publication   *Publication
}

// PreavisEnvoye est l'envoi du dossier à l'élu, enregistré par un membre
// (Acte) : la date d'envoi, l'échéance à partir de laquelle le statut est
// publiable, comment il a été envoyé et le SHA-256 du dossier PDF envoyé.
type PreavisEnvoye struct {
	Acte
	EnvoyeLe time.Time
	Echeance time.Time
	Moyen    string // jamais publié
	Dossier  stockage.Empreinte
}

// Etape renvoie la dernière étape franchie.
func (s Statut) Etape() Etape {
	switch {
	case s.Publication != nil:
		return Publie
	case s.Rejet != nil:
		return Rejete
	case s.PreavisEnvoye != nil:
		return Preavis
	case s.Validation != nil:
		return Valide
	case s.Proposition != nil:
		return Propose
	default:
		return Brouillon
	}
}

// Canal est le canal vérifiable par lequel un droit de réponse est reçu.
type Canal string

const (
	AdresseOfficielle Canal = "adresse_officielle" // courriel ou adresse officielle de l'élu
	CourrierSigne     Canal = "courrier_signe"     // courrier signé
	CompteOfficiel    Canal = "compte_officiel"    // compte officiel de l'élu sur un réseau
)

// Canaux est la liste des canaux acceptés.
var Canaux = []Canal{AdresseOfficielle, CourrierSigne, CompteOfficiel}

// Valide indique un canal accepté.
func (c Canal) Valide() bool {
	for _, x := range Canaux {
		if c == x {
			return true
		}
	}
	return false
}

// EtapeReponse est l'étape atteinte par un droit de réponse.
type EtapeReponse string

const (
	ReponseSaisie   EtapeReponse = "saisie"
	ReponseVerifiee EtapeReponse = "verifiee"
	ReponseRejetee  EtapeReponse = "rejetee"
	ReponsePubliee  EtapeReponse = "publiee"
)

// DroitReponse est le texte de l'élu, reçu par un canal vérifiable,
// recopié tel quel par le membre qui l'a reçu (Saisi), vérifié par un
// autre membre, puis publié sans commentaire.
type DroitReponse struct {
	ID           int64
	StatutID     int64
	Texte        string
	Canal        Canal
	CanalDetail  string // référence du canal (adresse, compte, courrier) ; jamais publiée
	RecuLe       time.Time
	Saisi        Acte
	Verification *Acte
	Rejet        *Rejet
	Publication  *Publication
}

// Etape renvoie la dernière étape franchie.
func (r DroitReponse) Etape() EtapeReponse {
	switch {
	case r.Publication != nil:
		return ReponsePubliee
	case r.Rejet != nil:
		return ReponseRejetee
	case r.Verification != nil:
		return ReponseVerifiee
	default:
		return ReponseSaisie
	}
}

// Filtre restreint la liste publique des engagements.
type Filtre struct {
	Elu    int64   // 0 : tous
	Mandat *Mandat // nil : tous
	Valeur Valeur  // "" : tous ; sinon la valeur du dernier statut publié
	Page   int     // à partir de 1
}

// ParPage est le nombre d'engagements par page publique.
const ParPage = 30

// Liste est une page de la liste publique.
type Liste struct {
	Engagements []Engagement // chacun avec son seul dernier statut publié
	Total       int
	Elus        []Elu    // élus qui ont au moins un engagement public
	Mandats     []Mandat // mandats de ces élus
}

// Journaliser inscrit au journal public, dans la transaction de la
// publication, l'entrée du statut, et renvoie son numéro.
type Journaliser func(ctx context.Context, m journal.Magasin, e *Engagement, s *Statut) (int64, error)

// JournaliserReponse inscrit au journal public, dans la transaction de sa
// publication, l'entrée d'un droit de réponse, et renvoie son numéro.
type JournaliserReponse func(ctx context.Context, m journal.Magasin, e *Engagement, s *Statut, r *DroitReponse) (int64, error)

// Magasin conserve l'observatoire (implémenté par base.Base). Chaque
// écriture est une insertion ; la base vérifie à nouveau toutes les règles.
type Magasin interface {
	AjouterElu(ctx context.Context, e Elu) (int64, error)
	Elus(ctx context.Context) ([]Elu, error)
	AjouterSource(ctx context.Context, s Source) (int64, error)
	Sources(ctx context.Context) ([]Source, error)
	AjouterEngagement(ctx context.Context, e Engagement, sources []int64) (int64, error)
	LierSource(ctx context.Context, engagement, source int64) error
	ValiderEngagement(ctx context.Context, engagement int64, a Acte) error
	// Engagements renvoie tous les engagements, sans sources ni preuves,
	// avec leurs statuts (pour l'équipe).
	Engagements(ctx context.Context) ([]Engagement, error)
	// Engagement renvoie un engagement complet : sources, statuts dans
	// l'ordre de leurs identifiants, preuves et étapes. ErrIntrouvable.
	Engagement(ctx context.Context, id int64) (*Engagement, error)
	// EngagementDuStatut renvoie l'identifiant de l'engagement d'un statut.
	EngagementDuStatut(ctx context.Context, statut int64) (int64, error)
	RedigerStatut(ctx context.Context, s Statut, preuves []int64) (int64, error)
	AjouterPreuve(ctx context.Context, statut, source int64) error
	Proposer(ctx context.Context, statut int64, a Acte) error
	Valider(ctx context.Context, statut int64, a Acte) error
	Rejeter(ctx context.Context, statut int64, a Acte, motif string) error
	// Publier inscrit la publication et l'entrée du journal public dans la
	// même transaction, sous verrou, après avoir relu l'engagement ; puis,
	// dans la même transaction, chaque droit de réponse vérifié au statut,
	// avec son entrée (jr).
	Publier(ctx context.Context, statut int64, a Acte, j Journaliser, jr JournaliserReponse) (int64, error)
	// EnregistrerPreavis inscrit l'envoi du dossier d'un statut validé.
	EnregistrerPreavis(ctx context.Context, statut int64, p PreavisEnvoye) error
	// SaisirReponse enregistre un droit de réponse à un statut préavisé.
	SaisirReponse(ctx context.Context, r DroitReponse) (int64, error)
	// EngagementDeLaReponse renvoie l'identifiant de l'engagement d'un droit
	// de réponse.
	EngagementDeLaReponse(ctx context.Context, reponse int64) (int64, error)
	// VerifierReponse inscrit la vérification ; si le statut est déjà
	// publié, publie aussitôt le droit de réponse avec son entrée du journal
	// (jr), dans la même transaction. Renvoie le numéro de l'entrée, ou 0.
	VerifierReponse(ctx context.Context, reponse int64, a Acte, jr JournaliserReponse) (int64, error)
	RejeterReponse(ctx context.Context, reponse int64, a Acte, motif string) error

	// Pages publiques : seuls les engagements validés qui ont au moins un
	// statut publié, et seuls leurs statuts publiés.
	ListePublique(ctx context.Context, f Filtre) (*Liste, error)
	FichePublique(ctx context.Context, id int64) (*Engagement, error)
	// TypeArchive renvoie le type d'une archive connue ; publique : seulement
	// si elle est source d'un engagement public ou preuve d'un statut publié.
	TypeArchive(ctx context.Context, e stockage.Empreinte, publique bool) (string, error)
}
