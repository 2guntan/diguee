package equipe

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/2guntan/diguee/internal/totp"
)

// Limites des tentatives de connexion échouées, comptées en mémoire.
const (
	// EssaisParCompte échecs au plus par identifiant (existant ou non) dans
	// FenetreCompte : au-delà, cet identifiant est bloqué.
	EssaisParCompte = 5
	FenetreCompte   = 15 * time.Minute
	// EssaisParSource échecs au plus par adresse (remplacée par un HMAC à sel
	// quotidien) dans FenetreSource, tous identifiants confondus.
	EssaisParSource = 20
	FenetreSource   = time.Hour
)

// Emetteur est le nom affiché par les applications d'authentification.
const Emetteur = "Diguee"

// Acces est ce qui est montré une seule fois au membre, à la création de son
// compte ou à la réinitialisation de son accès. Rien de tout cela n'est
// conservé en clair.
type Acces struct {
	Identifiant string
	MotDePasse  string
	Secret      string // secret TOTP en base32, pour une saisie manuelle
	URI         string // URI otpauth://, pour un QR code
}

// Service applique les règles des comptes : qui peut faire quoi, connexion,
// anti-rejeu des codes et limitation des échecs.
type Service struct {
	magasin    Magasin
	chiffreur  *Chiffreur
	sessions   *Sessions
	maintenant func() time.Time

	parCompte *Limiteur
	parSource *Limiteur
	sources   selSource

	mu         sync.Mutex
	dernierPas map[string]int64 // dernier pas TOTP accepté, par membre
	// plancher est le pas courant au démarrage : les dernierPas d'avant un
	// redémarrage sont perdus, donc aucun code de ce pas ou d'un pas antérieur
	// n'est accepté. Après un redémarrage, il faut attendre le code suivant.
	plancher int64
}

// NouveauService assemble le service. maintenant vaut time.Now si nil.
func NouveauService(m Magasin, c *Chiffreur, s *Sessions, maintenant func() time.Time) *Service {
	if maintenant == nil {
		maintenant = time.Now
	}
	return &Service{
		magasin: m, chiffreur: c, sessions: s, maintenant: maintenant,
		parCompte:  NouveauLimiteur(EssaisParCompte, FenetreCompte),
		parSource:  NouveauLimiteur(EssaisParSource, FenetreSource),
		dernierPas: map[string]int64{},
		plancher:   totp.Pas(maintenant()),
	}
}

var reIdentifiant = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,39}$`)

// NormaliserIdentifiant met l'identifiant en minuscules, sans espaces autour.
func NormaliserIdentifiant(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func validerNom(nom string) (string, error) {
	nom = strings.Join(strings.Fields(nom), " ")
	if nom == "" || utf8.RuneCountInString(nom) > 100 || strings.ContainsFunc(nom, unicode.IsControl) {
		return "", ErrNom
	}
	return nom, nil
}

// NormaliserRoles dédoublonne et ordonne les rôles ; ErrRoles si la liste est
// vide ou contient un rôle inconnu.
func NormaliserRoles(roles []Role) ([]Role, error) {
	var rs []Role
	for _, r := range Roles {
		if slices.Contains(roles, r) {
			rs = append(rs, r)
		}
	}
	for _, r := range roles {
		if !r.Valide() {
			return nil, ErrRoles
		}
	}
	if len(rs) == 0 {
		return nil, ErrRoles
	}
	return rs, nil
}

func uuidV4() (string, error) {
	var u [16]byte
	if _, err := rand.Read(u[:]); err != nil {
		return "", err
	}
	u[6] = u[6]&0x0f | 0x40
	u[8] = u[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16]), nil
}

func (s *Service) jour() time.Time {
	t := s.maintenant().UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// nouvelAcces tire un mot de passe et un secret TOTP pour le membre.
func (s *Service) nouvelAcces(id, identifiant string) (Acces, string, []byte, error) {
	mdp, err := NouveauMotDePasse()
	if err != nil {
		return Acces{}, "", nil, err
	}
	empreinteMdp, err := EmpreinteMotDePasse(mdp)
	if err != nil {
		return Acces{}, "", nil, err
	}
	secret, err := totp.NouveauSecret()
	if err != nil {
		return Acces{}, "", nil, err
	}
	chiffre, err := s.chiffreur.Chiffrer(id, secret)
	if err != nil {
		return Acces{}, "", nil, err
	}
	return Acces{
		Identifiant: identifiant, MotDePasse: mdp,
		Secret: totp.Base32(secret), URI: totp.URI(Emetteur, identifiant, secret),
	}, empreinteMdp, chiffre, nil
}

// Creer crée un compte actif. acteur est l'administrateur qui agit ; nil
// désigne la commande « diguee creer-membre », lancée sur le serveur.
func (s *Service) Creer(ctx context.Context, acteur *Membre, identifiant, nom string, roles []Role) (Membre, Acces, error) {
	a := Action{Type: ActionCreationCommande}
	if acteur != nil {
		if !acteur.Actif || !acteur.A(Administrateur) {
			return Membre{}, Acces{}, ErrNonAdministrateur
		}
		a = Action{Type: ActionCreation, Acteur: acteur.ID}
	}
	identifiant = NormaliserIdentifiant(identifiant)
	if !reIdentifiant.MatchString(identifiant) {
		return Membre{}, Acces{}, ErrIdentifiant
	}
	nom, err := validerNom(nom)
	if err != nil {
		return Membre{}, Acces{}, err
	}
	if roles, err = NormaliserRoles(roles); err != nil {
		return Membre{}, Acces{}, err
	}
	id, err := uuidV4()
	if err != nil {
		return Membre{}, Acces{}, err
	}
	acces, empreinteMdp, chiffre, err := s.nouvelAcces(id, identifiant)
	if err != nil {
		return Membre{}, Acces{}, err
	}
	m := Membre{ID: id, Identifiant: identifiant, Nom: nom, Roles: roles, Actif: true}
	a.Jour, a.Cible, a.Roles = s.jour(), id, roles
	if err := s.magasin.CreerMembre(ctx, Compte{Membre: m, MotDePasse: empreinteMdp, SecretChiffre: chiffre}, a); err != nil {
		return Membre{}, Acces{}, err
	}
	return m, acces, nil
}

func exigerAdministrateur(acteur Membre) error {
	if !acteur.Actif || !acteur.A(Administrateur) {
		return ErrNonAdministrateur
	}
	return nil
}

// Desactiver désactive un compte, définitivement, et ferme ses sessions.
func (s *Service) Desactiver(ctx context.Context, acteur Membre, id string) error {
	if err := exigerAdministrateur(acteur); err != nil {
		return err
	}
	if id == acteur.ID {
		return ErrSoiMeme
	}
	if err := s.magasin.DesactiverMembre(ctx, id, Action{Jour: s.jour(), Type: ActionDesactivation, Acteur: acteur.ID, Cible: id}); err != nil {
		return err
	}
	s.sessions.FermerMembre(id)
	return nil
}

// ChangerRoles remplace les rôles d'un compte actif. Ils sont relus à chaque
// requête : le changement vaut aussi pour les sessions ouvertes.
func (s *Service) ChangerRoles(ctx context.Context, acteur Membre, id string, roles []Role) error {
	if err := exigerAdministrateur(acteur); err != nil {
		return err
	}
	roles, err := NormaliserRoles(roles)
	if err != nil {
		return err
	}
	return s.magasin.ChangerRoles(ctx, id, roles, Action{Jour: s.jour(), Type: ActionRoles, Acteur: acteur.ID, Cible: id, Roles: roles})
}

// Reinitialiser tire un nouveau mot de passe et un nouveau secret TOTP pour
// un compte actif (téléphone perdu, par exemple) et ferme ses sessions.
func (s *Service) Reinitialiser(ctx context.Context, acteur Membre, id string) (Acces, error) {
	if err := exigerAdministrateur(acteur); err != nil {
		return Acces{}, err
	}
	m, err := s.magasin.MembreParID(ctx, id)
	if err != nil {
		return Acces{}, err
	}
	if !m.Actif {
		return Acces{}, ErrInactif
	}
	acces, empreinteMdp, chiffre, err := s.nouvelAcces(m.ID, m.Identifiant)
	if err != nil {
		return Acces{}, err
	}
	if err := s.magasin.RemplacerAcces(ctx, m.ID, empreinteMdp, chiffre,
		Action{Jour: s.jour(), Type: ActionReinitialiser, Acteur: acteur.ID, Cible: m.ID}); err != nil {
		return Acces{}, err
	}
	s.sessions.FermerMembre(m.ID)
	return acces, nil
}

// consommer vérifie le code et, s'il est bon, retient son pas : le même code
// (ou un plus ancien) ne sera plus accepté pour ce membre.
func (s *Service) consommer(id string, secret []byte, code string, t time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := totp.Verifier(secret, code, t, max(s.plancher, s.dernierPas[id]))
	if ok {
		s.dernierPas[id] = p
	}
	return ok
}

// hote retire le port de l'adresse de la requête.
func hote(adresse string) string {
	if h, _, err := net.SplitHostPort(adresse); err == nil {
		return h
	}
	return adresse
}

// Connecter vérifie l'identifiant, le mot de passe et le code TOTP, tous trois
// obligatoires. Elle renvoie ErrConnexion sans dire ce qui a échoué, ou
// ErrTropDEssais si les échecs récents bloquent l'identifiant ou l'adresse.
// adresse (r.RemoteAddr) ne sert qu'à la limitation, sous forme de HMAC.
func (s *Service) Connecter(ctx context.Context, identifiant, motDePasse, code, adresse string) (Membre, error) {
	t := s.maintenant()
	identifiant = NormaliserIdentifiant(identifiant)
	cleCompte := "compte:" + identifiant
	cleSource := "source:" + s.sources.cle(hote(adresse), t)
	if s.parCompte.Bloque(cleCompte, t) || s.parSource.Bloque(cleSource, t) {
		return Membre{}, ErrTropDEssais
	}
	echec := func() (Membre, error) {
		s.parCompte.Echec(cleCompte, t)
		s.parSource.Echec(cleSource, t)
		return Membre{}, ErrConnexion
	}
	if !reIdentifiant.MatchString(identifiant) {
		VerifierMotDePasse(empreinteLeurre(), motDePasse)
		return echec()
	}
	c, err := s.magasin.CompteParIdentifiant(ctx, identifiant)
	if errors.Is(err, ErrInconnu) {
		VerifierMotDePasse(empreinteLeurre(), motDePasse)
		return echec()
	}
	if err != nil {
		return Membre{}, err
	}
	mdpBon := VerifierMotDePasse(c.MotDePasse, motDePasse)
	secret, err := s.chiffreur.Dechiffrer(c.ID, c.SecretChiffre)
	if err != nil {
		return Membre{}, err
	}
	codeBon := s.consommer(c.ID, secret, code, t)
	if !mdpBon || !codeBon || !c.Actif {
		return echec()
	}
	// La connexion n'est ouverte que si elle a pu être tracée.
	if err := s.magasin.Auditer(ctx, Action{Jour: s.jour(), Type: ActionConnexion, Acteur: c.ID, Cible: c.ID}); err != nil {
		return Membre{}, err
	}
	s.parCompte.Effacer(cleCompte)
	return c.Membre, nil
}
