package ancrage

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/integration"
)

// Integration réunit les services d'ancrage choisis par DIGUEE_ANCRAGE.
type Integration struct {
	Mode           string // fake ou reel
	Calendriers    []Calendrier
	MinCalendriers int
	Archiveur      Archiveur
	// Ouvrir renvoie le calendrier d'une attestation en attente, pour la mise
	// à niveau, s'il est admis.
	Ouvrir func(uri string) (Calendrier, bool)
}

// Fausse indique le mode de développement.
func (i *Integration) Fausse() bool { return i.Mode == integration.ModeFake }

// Suffixes des calendriers publics admis pour la mise à niveau, comme la
// liste blanche du client de référence : l'URI d'une attestation vient de la
// réponse d'un calendrier, et n'est suivie que si elle désigne un calendrier
// connu.
var suffixesAdmis = []string{".calendar.opentimestamps.org", ".calendar.eternitywall.com", ".calendar.catallaxy.com"}

// Admis indique si l'URI désigne un calendrier à interroger pour la mise à
// niveau : en https, un calendrier configuré ou un calendrier public connu.
func Admis(uri string, configures []string) bool {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	for _, c := range configures {
		if cu, err := url.Parse(c); err == nil && strings.EqualFold(cu.Host, u.Host) {
			return true
		}
	}
	h := strings.ToLower(u.Hostname())
	return slices.ContainsFunc(suffixesAdmis, func(s string) bool { return strings.HasSuffix(h, s) })
}

// Construire choisit les services d'après DIGUEE_ANCRAGE et les déclare dans
// le registre des intégrations : le mode « fake » y est déclaré comme tel,
// donc refusé en production. Le mode « reel » ne demande aucun compte.
func Construire(c *config.Config, r *integration.Registre) (*Integration, error) {
	a := c.Ancrage
	var i *Integration
	switch a.Mode {
	case integration.ModeFake:
		i = &Integration{Mode: a.Mode, MinCalendriers: a.MinCalendriers, Archiveur: FausseArchive{}}
		for k := range a.Calendriers {
			i.Calendriers = append(i.Calendriers, FauxCalendrier{Nom: fmt.Sprintf("calendrier-%c", 'a'+rune(k%26))})
		}
		i.Ouvrir = func(string) (Calendrier, bool) { return nil, false }
	case "reel":
		client := &http.Client{Timeout: a.Delai}
		i = &Integration{Mode: a.Mode, MinCalendriers: a.MinCalendriers,
			Archiveur: &ArchiveHTTP{Base: a.Archive, Client: client}}
		for _, u := range a.Calendriers {
			i.Calendriers = append(i.Calendriers, &CalendrierHTTP{Base: u, Client: client})
		}
		configures := slices.Clone(a.Calendriers)
		i.Ouvrir = func(uri string) (Calendrier, bool) {
			if !Admis(uri, configures) {
				return nil, false
			}
			return &CalendrierHTTP{Base: strings.TrimRight(uri, "/"), Client: client}, true
		}
	default:
		return nil, fmt.Errorf("DIGUEE_ANCRAGE : mode %q non pris en charge", a.Mode)
	}
	if err := r.Declarer(integration.Declaration{Nom: "ancrage", Mode: a.Mode, Variable: "DIGUEE_ANCRAGE"}); err != nil {
		return nil, err
	}
	return i, nil
}

// FauxCalendrier imite un calendrier sans réseau : il renvoie un engagement
// de la même forme que les vrais (nonce ajouté, SHA-256, attestation en
// attente), vers une adresse en .invalid qui n'existe pas. Il ne se met
// jamais à niveau.
type FauxCalendrier struct{ Nom string }

// Adresse implémente Calendrier.
func (f FauxCalendrier) Adresse() string { return "https://" + f.Nom + ".diguee.invalid" }

// Soumettre implémente Calendrier.
func (f FauxCalendrier) Soumettre(_ context.Context, condensat []byte) (*Horodatage, error) {
	h := &Horodatage{Message: slices.Clone(condensat)}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	s, err := h.Ajouter(Op{Tag: OpAppend, Arg: nonce})
	if err == nil {
		s, err = s.Ajouter(Op{Tag: OpSHA256})
	}
	if err != nil {
		return nil, err
	}
	s.Attester(EnAttente(f.Adresse()))
	return h, nil
}

// MettreANiveau implémente Calendrier : jamais inscrit.
func (f FauxCalendrier) MettreANiveau(context.Context, []byte) (*Horodatage, bool, error) {
	return nil, false, nil
}

// FausseArchive imite archive.org sans réseau : le lien renvoyé, en .invalid,
// ne mène nulle part.
type FausseArchive struct{}

// Archiver implémente Archiveur.
func (FausseArchive) Archiver(_ context.Context, page string) (string, error) {
	return "https://archive.diguee.invalid/web/" + time.Now().UTC().Format("20060102150405") + "/" + page, nil
}
