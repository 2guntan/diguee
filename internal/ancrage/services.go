package ancrage

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Calendrier est un calendrier OpenTimestamps.
type Calendrier interface {
	// Soumettre envoie un condensat et renvoie l'engagement partiel du
	// calendrier : un horodatage qui s'applique au condensat et se termine par
	// une attestation « en attente ».
	Soumettre(ctx context.Context, condensat []byte) (*Horodatage, error)
	// MettreANiveau demande l'horodatage complet d'un engagement (le message
	// d'une attestation en attente). trouve vaut false tant que le calendrier
	// ne l'a pas encore inscrit dans Bitcoin.
	MettreANiveau(ctx context.Context, engagement []byte) (h *Horodatage, trouve bool, err error)
	// Adresse est l'adresse du calendrier, pour les journaux.
	Adresse() string
}

// Archiveur dépose une page publique sur une archive du web.
type Archiveur interface {
	// Archiver dépose la page et renvoie le lien de la copie archivée.
	Archiver(ctx context.Context, page string) (lien string, err error)
}

// TypeOTS est le type MIME des preuves et des réponses des calendriers.
const TypeOTS = "application/vnd.opentimestamps.v1"

const agentUtilisateur = "diguee-ancrage (+https://github.com/2guntan/diguee)"

// maxReponseCalendrier borne la réponse d'un calendrier, comme le client de
// référence (10 000 octets).
const maxReponseCalendrier = 10000

// CalendrierHTTP est un calendrier public, joint en HTTPS.
type CalendrierHTTP struct {
	Base   string // https://a.pool.opentimestamps.org, sans « / » final
	Client *http.Client
}

// Adresse implémente Calendrier.
func (c *CalendrierHTTP) Adresse() string { return c.Base }

// Soumettre implémente Calendrier : POST du condensat brut sur /digest.
func (c *CalendrierHTTP) Soumettre(ctx context.Context, condensat []byte) (*Horodatage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/digest", bytes.NewReader(condensat))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	corps, statut, err := c.appeler(req)
	if err != nil {
		return nil, err
	}
	if statut != http.StatusOK {
		return nil, fmt.Errorf("calendrier %s : statut %d", c.Base, statut)
	}
	h, err := LireHorodatage(corps, condensat)
	if err != nil {
		return nil, fmt.Errorf("calendrier %s : réponse invalide : %w", c.Base, err)
	}
	if len(h.Feuilles()) == 0 {
		return nil, fmt.Errorf("calendrier %s : réponse sans attestation", c.Base)
	}
	return h, nil
}

// MettreANiveau implémente Calendrier : GET /timestamp/<engagement en hexadécimal>.
func (c *CalendrierHTTP) MettreANiveau(ctx context.Context, engagement []byte) (*Horodatage, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/timestamp/"+hex.EncodeToString(engagement), nil)
	if err != nil {
		return nil, false, err
	}
	corps, statut, err := c.appeler(req)
	if err != nil {
		return nil, false, err
	}
	switch statut {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, false, nil // pas encore inscrit dans Bitcoin
	default:
		return nil, false, fmt.Errorf("calendrier %s : statut %d", c.Base, statut)
	}
	h, err := LireHorodatage(corps, engagement)
	if err != nil {
		return nil, false, fmt.Errorf("calendrier %s : réponse invalide : %w", c.Base, err)
	}
	return h, true, nil
}

func (c *CalendrierHTTP) appeler(req *http.Request) ([]byte, int, error) {
	req.Header.Set("Accept", TypeOTS)
	req.Header.Set("User-Agent", agentUtilisateur)
	cl := c.Client
	if cl == nil {
		cl = http.DefaultClient
	}
	rep, err := cl.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("calendrier %s injoignable : %w", c.Base, err)
	}
	defer rep.Body.Close()
	corps, err := io.ReadAll(io.LimitReader(rep.Body, maxReponseCalendrier+1))
	if err != nil {
		return nil, 0, fmt.Errorf("calendrier %s : lecture de la réponse : %w", c.Base, err)
	}
	if len(corps) > maxReponseCalendrier {
		return nil, 0, fmt.Errorf("calendrier %s : réponse trop longue", c.Base)
	}
	return corps, rep.StatusCode, nil
}

// ArchiveHTTP dépose les pages sur la Wayback Machine d'archive.org avec
// « Save Page Now » (GET /save/<adresse>), sans compte.
//
// La réponse prend plusieurs formes selon la charge d'archive.org : une
// redirection ou un en-tête Content-Location vers /web/<horodatage>/<adresse>,
// une page qui cite ce lien, ou une page d'attente qui donne un identifiant
// de tâche (spn2-…) à interroger sur /save/status/<tâche>. Les trois sont
// prises en charge.
type ArchiveHTTP struct {
	Base    string // https://web.archive.org, sans « / » final
	Client  *http.Client
	Attente time.Duration // entre deux interrogations d'une tâche ; 5 s par défaut
	Essais  int           // interrogations d'une tâche ; 12 par défaut
}

var (
	reLienArchive = regexp.MustCompile(`/web/([0-9]{14})/`)
	reTache       = regexp.MustCompile(`spn2-[0-9a-f]{16,64}`)
)

const maxReponseArchive = 1 << 20

// Archiver implémente Archiveur.
func (a *ArchiveHTTP) Archiver(ctx context.Context, page string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.Base+"/save/"+page, nil)
	if err != nil {
		return "", err
	}
	corps, rep, err := a.appeler(req)
	if err != nil {
		return "", err
	}
	for _, en := range []string{"Content-Location", "Location"} {
		if v := rep.Header.Get(en); v != "" {
			if m := reLienArchive.FindStringSubmatch(v); m != nil {
				return a.lien(m[1], page), nil
			}
		}
	}
	if rep.StatusCode != http.StatusOK {
		return "", fmt.Errorf("archive.org : statut %d", rep.StatusCode)
	}
	// Le lien cité dans la page ne compte que s'il désigne bien cette page.
	for _, m := range reLienArchive.FindAllSubmatchIndex(corps, -1) {
		if bytes.HasPrefix(corps[m[1]:], []byte(page)) {
			return a.lien(string(corps[m[2]:m[3]]), page), nil
		}
	}
	tache := reTache.Find(corps)
	if tache == nil {
		return "", errors.New("archive.org : réponse sans lien d'archive ni tâche")
	}
	return a.suivre(ctx, string(tache), page)
}

// suivre interroge une tâche « Save Page Now » jusqu'à son issue.
func (a *ArchiveHTTP) suivre(ctx context.Context, tache, page string) (string, error) {
	attente, essais := a.Attente, a.Essais
	if attente <= 0 {
		attente = 5 * time.Second
	}
	if essais <= 0 {
		essais = 12
	}
	for range essais {
		t := time.NewTimer(attente)
		select {
		case <-ctx.Done():
			t.Stop()
			return "", ctx.Err()
		case <-t.C:
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.Base+"/save/status/"+tache, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Accept", "application/json")
		corps, rep, err := a.appeler(req)
		if err != nil {
			return "", err
		}
		if rep.StatusCode != http.StatusOK {
			return "", fmt.Errorf("archive.org : suivi de la tâche : statut %d", rep.StatusCode)
		}
		var etat struct {
			Status    string `json:"status"`
			Timestamp string `json:"timestamp"`
			Message   string `json:"message"`
		}
		if err := json.Unmarshal(corps, &etat); err != nil {
			return "", fmt.Errorf("archive.org : suivi de la tâche : %w", err)
		}
		switch etat.Status {
		case "success":
			if len(etat.Timestamp) != 14 || strings.Trim(etat.Timestamp, "0123456789") != "" {
				return "", errors.New("archive.org : tâche réussie sans horodatage")
			}
			return a.lien(etat.Timestamp, page), nil
		case "pending":
		default:
			return "", fmt.Errorf("archive.org : tâche en échec (%.200s)", etat.Message)
		}
	}
	return "", errors.New("archive.org : tâche toujours en cours")
}

func (a *ArchiveHTTP) lien(horodatage, page string) string {
	return a.Base + "/web/" + horodatage + "/" + page
}

func (a *ArchiveHTTP) appeler(req *http.Request) ([]byte, *http.Response, error) {
	req.Header.Set("User-Agent", agentUtilisateur)
	cl := http.Client{Timeout: 0}
	if a.Client != nil {
		cl = *a.Client
	}
	// Une redirection porte le lien de l'archive : elle n'est pas suivie.
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	rep, err := cl.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // sans l'adresse complète, déjà connue
		}
		return nil, nil, fmt.Errorf("archive.org injoignable : %w", err)
	}
	defer rep.Body.Close()
	corps, err := io.ReadAll(io.LimitReader(rep.Body, maxReponseArchive))
	if err != nil {
		return nil, nil, fmt.Errorf("archive.org : lecture de la réponse : %w", err)
	}
	return corps, rep, nil
}
