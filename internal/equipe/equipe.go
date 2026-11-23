// Package equipe gère l'espace équipe, servi sous /equipe (spec §9, ADR 0008) :
// comptes nominatifs, rôles cumulables (rédacteur, relecteur, administrateur),
// connexion par mot de passe et TOTP (jamais par SMS), sessions de 8 heures,
// protection CSRF des formulaires, et audit interne des actions sur les
// comptes.
//
// Les autres pages de l'espace (observatoire, ticket #13) s'y ajoutent avec
// Espace.Gerer, qui vérifie la session, le rôle et le jeton CSRF ; le membre
// connecté se lit dans le contexte de la requête avec MembreDe.
//
// Ce paquet ne journalise jamais d'adresse IP, d'identifiant de session, de
// mot de passe, de code ni de secret.
package equipe

import (
	"context"
	"errors"
	"slices"
	"time"
)

// Role est un rôle de l'espace équipe. Un membre peut en cumuler plusieurs.
type Role string

const (
	// Redacteur saisit un engagement ou propose un nouveau statut.
	Redacteur Role = "redacteur"
	// Relecteur valide la saisie d'un autre membre (ADR 0008).
	Relecteur Role = "relecteur"
	// Administrateur crée, désactive et change les rôles des comptes.
	Administrateur Role = "administrateur"
)

// Roles est la liste de tous les rôles, dans l'ordre d'affichage.
var Roles = []Role{Redacteur, Relecteur, Administrateur}

// Valide indique si r est un rôle connu.
func (r Role) Valide() bool { return slices.Contains(Roles, r) }

// Membre est un compte nominatif, sans ses secrets.
type Membre struct {
	ID          string // UUID
	Identifiant string // nom de connexion, par exemple « awa.diop »
	Nom         string // nom complet
	Roles       []Role
	Actif       bool
}

// A indique si le membre a le rôle r.
func (m Membre) A(r Role) bool { return slices.Contains(m.Roles, r) }

// AUnDe indique si le membre a au moins un des rôles donnés ; sans rôle
// demandé, tout membre convient.
func (m Membre) AUnDe(roles ...Role) bool {
	if len(roles) == 0 {
		return true
	}
	for _, r := range roles {
		if m.A(r) {
			return true
		}
	}
	return false
}

// Compte est un membre avec ses secrets, tels qu'ils sont stockés.
type Compte struct {
	Membre
	MotDePasse    string // empreinte PBKDF2 (voir motdepasse.go)
	SecretChiffre []byte // secret TOTP chiffré par le Chiffreur
}

// TypeAction est le type d'une ligne de l'audit interne.
type TypeAction string

const (
	ActionCreation         TypeAction = "creation"
	ActionDesactivation    TypeAction = "desactivation"
	ActionRoles            TypeAction = "changement_roles"
	ActionReinitialiser    TypeAction = "reinitialisation_acces"
	ActionConnexion        TypeAction = "connexion"
	ActionCreationCommande TypeAction = "creation_commande"
	// ActionReactivationSMS : un administrateur a réactivé le parcours SMS
	// suspendu par le coupe-circuit (ticket #10). Sans cible.
	ActionReactivationSMS TypeAction = "reactivation_sms"
)

// Action est une ligne de l'audit interne des comptes. Elle ne porte que le
// jour, ni l'heure ni l'adresse IP.
type Action struct {
	Numero int64
	Jour   time.Time
	Type   TypeAction
	Acteur string // ID du membre qui agit ; vide pour la commande diguee creer-membre
	Cible  string // ID du membre concerné ; vide pour une action sur la plateforme (réactivation du SMS)
	Roles  []Role // rôles après l'action (création, changement de rôles)

	// Remplis à la lecture.
	ActeurIdentifiant string
	CibleIdentifiant  string
}

// Magasin stocke les comptes et l'audit (implémenté par base.Base). Chaque
// écriture et sa ligne d'audit sont faites dans la même transaction.
type Magasin interface {
	CreerMembre(ctx context.Context, c Compte, a Action) error
	CompteParIdentifiant(ctx context.Context, identifiant string) (Compte, error)
	MembreParID(ctx context.Context, id string) (Membre, error)
	ListerMembres(ctx context.Context) ([]Membre, error)
	// DesactiverMembre et ChangerRoles renvoient ErrDernierAdministrateur
	// s'il ne restait plus aucun administrateur actif.
	DesactiverMembre(ctx context.Context, id string, a Action) error
	ChangerRoles(ctx context.Context, id string, roles []Role, a Action) error
	RemplacerAcces(ctx context.Context, id, motDePasse string, secretChiffre []byte, a Action) error
	Auditer(ctx context.Context, a Action) error
	ListerAudit(ctx context.Context, limite int) ([]Action, error)
}

// Erreurs du magasin et du service.
var (
	ErrInconnu               = errors.New("membre inconnu")
	ErrInactif               = errors.New("compte désactivé")
	ErrIdentifiantPris       = errors.New("identifiant déjà utilisé")
	ErrDernierAdministrateur = errors.New("il doit rester au moins un administrateur actif")
	ErrSoiMeme               = errors.New("un administrateur ne peut pas désactiver son propre compte")
	ErrNonAdministrateur     = errors.New("action réservée aux administrateurs")
	ErrIdentifiant           = errors.New("identifiant invalide : 2 à 40 caractères parmi a-z, 0-9, « . », « _ » et « - », en commençant par une lettre ou un chiffre")
	ErrNom                   = errors.New("nom invalide : 1 à 100 caractères")
	ErrRoles                 = errors.New("au moins un rôle valide est demandé")
	// ErrConnexion est la seule erreur montrée à la connexion : elle ne dit
	// pas ce qui a échoué (identifiant, mot de passe, code ou compte désactivé).
	ErrConnexion = errors.New("connexion refusée")
	// ErrTropDEssais signale que les tentatives sont temporairement bloquées.
	ErrTropDEssais = errors.New("trop de tentatives de connexion")
)
