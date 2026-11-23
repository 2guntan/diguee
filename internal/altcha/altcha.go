// Package altcha implémente, dans le dépôt et sans service tiers (ADR 0007),
// le captcha ALTCHA : une preuve de travail calculée par le navigateur avant
// la création d'un jeton ou l'envoi d'un code (spec §4, étape 2 ; §7).
//
// Le format suit le protocole ALTCHA (version 1) :
//
//	défi     {"algorithm":"SHA-256","challenge":…,"maxnumber":…,"salt":…,"signature":…}
//	challenge = hex(SHA-256(salt + nombre secret)), nombre tiré dans [0, maxnumber]
//	salt      = 24 chiffres hexadécimaux aléatoires + "?expires=" + date Unix d'expiration
//	signature = hex(HMAC-SHA-256(clé du serveur, challenge))
//	solution  base64(JSON {"algorithm","challenge","number","salt","signature","took"}),
//	          envoyée dans le champ de formulaire « altcha »
//
// Le navigateur essaie les nombres de 0 à maxnumber jusqu'à retrouver le
// challenge ; le serveur vérifie la signature, l'expiration, la solution, et
// refuse une solution déjà utilisée (mémoire bornée).
package altcha

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/integration"
)

// Algorithme est le seul algorithme accepté.
const Algorithme = "SHA-256"

// Champ est le nom du champ de formulaire qui porte la solution.
const Champ = "altcha"

// Valeurs par défaut.
const (
	// DureeDefaut est la validité d'un défi : le temps de lire la page,
	// de calculer et d'envoyer le formulaire.
	DureeDefaut = 20 * time.Minute
	// MaxEnMemoireDefaut borne la mémoire des solutions déjà utilisées.
	MaxEnMemoireDefaut = 200_000
	// tailleMaxSolution borne la solution lue dans le formulaire.
	tailleMaxSolution = 2048
)

// Erreurs de Verifier.
var (
	ErrAbsente      = errors.New("altcha : aucune solution (JavaScript désactivé ?)")
	ErrInvalide     = errors.New("altcha : solution invalide")
	ErrExpiree      = errors.New("altcha : défi expiré")
	ErrDejaUtilisee = errors.New("altcha : solution déjà utilisée")
	ErrSaturee      = errors.New("altcha : trop de solutions en mémoire")
)

// Defi est le défi envoyé au navigateur.
type Defi struct {
	Algorithm string `json:"algorithm"`
	Challenge string `json:"challenge"`
	MaxNumber int64  `json:"maxnumber"`
	Salt      string `json:"salt"`
	Signature string `json:"signature"`
}

// Solution est la charge renvoyée par le navigateur (avant base64).
type Solution struct {
	Algorithm string `json:"algorithm"`
	Challenge string `json:"challenge"`
	Number    int64  `json:"number"`
	Salt      string `json:"salt"`
	Signature string `json:"signature"`
	Took      int64  `json:"took,omitempty"`
}

// Options règle le captcha.
type Options struct {
	Cle          []byte           // clé HMAC des défis, 32 octets au moins
	Difficulte   int64            // DIGUEE_ALTCHA_DIFFICULTE : maxnumber
	Duree        time.Duration    // DureeDefaut si nul
	MaxEnMemoire int              // MaxEnMemoireDefaut si nul
	Maintenant   func() time.Time // time.Now par défaut
}

// Altcha crée et vérifie les défis. Il est sûr pour un usage concurrent.
type Altcha struct {
	opt Options

	mu       sync.Mutex
	utilises map[string]time.Time // challenge → expiration du défi

	refusees atomic.Int64
}

// Nouveau prépare un captcha.
func Nouveau(opt Options) (*Altcha, error) {
	if len(opt.Cle) < 32 {
		return nil, errors.New("altcha : clé de 32 octets au moins attendue")
	}
	if opt.Difficulte < 1 {
		return nil, errors.New("altcha : la difficulté doit être positive")
	}
	if opt.Duree <= 0 {
		opt.Duree = DureeDefaut
	}
	if opt.MaxEnMemoire <= 0 {
		opt.MaxEnMemoire = MaxEnMemoireDefaut
	}
	if opt.Maintenant == nil {
		opt.Maintenant = time.Now
	}
	return &Altcha{opt: opt, utilises: map[string]time.Time{}}, nil
}

// cleDeveloppement est la clé des défis quand DIGUEE_CLE_ALTCHA est absente.
// Elle est publique (dans le dépôt) : l'intégration est alors déclarée
// « fake », donc refusée en production par le paquet garde.
var cleDeveloppement = []byte("diguee-developpement-cle-altcha-publique-ne-pas-utiliser")

// Construire prépare le captcha d'après la configuration et le déclare dans
// le registre des intégrations : sans DIGUEE_CLE_ALTCHA, la clé de
// développement est utilisée et le mode est « fake ».
func Construire(c *config.Config, r *integration.Registre) (*Altcha, error) {
	cle, mode := c.CleAltcha, "hmac"
	if cle == nil {
		cle, mode = cleDeveloppement, integration.ModeFake
	}
	a, err := Nouveau(Options{Cle: cle, Difficulte: int64(c.AltchaDifficulte)})
	if err != nil {
		return nil, err
	}
	if err := r.Declarer(integration.Declaration{Nom: "captcha_altcha", Mode: mode, Variable: "DIGUEE_CLE_ALTCHA"}); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *Altcha) signer(challenge string) string {
	m := hmac.New(sha256.New, a.opt.Cle)
	m.Write([]byte(challenge))
	return hex.EncodeToString(m.Sum(nil))
}

func hachage(sel string, n int64) string {
	h := sha256.Sum256([]byte(sel + strconv.FormatInt(n, 10)))
	return hex.EncodeToString(h[:])
}

// NouveauDefi tire un défi : sel aléatoire avec son expiration, nombre secret
// dans [0, Difficulte], signature.
func (a *Altcha) NouveauDefi() (Defi, error) {
	var alea [12]byte
	if _, err := rand.Read(alea[:]); err != nil {
		return Defi{}, err
	}
	n, err := rand.Int(rand.Reader, big.NewInt(a.opt.Difficulte+1))
	if err != nil {
		return Defi{}, err
	}
	expire := a.opt.Maintenant().Add(a.opt.Duree).Unix()
	sel := hex.EncodeToString(alea[:]) + "?expires=" + strconv.FormatInt(expire, 10)
	challenge := hachage(sel, n.Int64())
	return Defi{Algorithm: Algorithme, Challenge: challenge, MaxNumber: a.opt.Difficulte, Salt: sel, Signature: a.signer(challenge)}, nil
}

// expiration lit le paramètre « expires » du sel (obligatoire ici).
func expiration(sel string) (time.Time, bool) {
	_, params, ok := strings.Cut(sel, "?")
	if !ok {
		return time.Time{}, false
	}
	v, err := url.ParseQuery(params)
	if err != nil {
		return time.Time{}, false
	}
	e, err := strconv.ParseInt(v.Get("expires"), 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(e, 0), true
}

// Verifier contrôle une solution reçue dans le champ « altcha » : format,
// algorithme, signature (à temps constant), expiration, solution, puis usage
// unique. Une solution acceptée ne l'est plus jamais ensuite.
func (a *Altcha) Verifier(charge string) error {
	err := a.verifier(charge)
	if err != nil && !errors.Is(err, ErrAbsente) {
		a.refusees.Add(1)
	}
	return err
}

func (a *Altcha) verifier(charge string) error {
	charge = strings.TrimSpace(charge)
	if charge == "" {
		return ErrAbsente
	}
	if len(charge) > tailleMaxSolution {
		return ErrInvalide
	}
	brut, err := base64.StdEncoding.DecodeString(charge)
	if err != nil {
		return ErrInvalide
	}
	var s Solution
	if err := json.Unmarshal(brut, &s); err != nil {
		return ErrInvalide
	}
	if s.Algorithm != Algorithme || s.Number < 0 || s.Number > a.opt.Difficulte || len(s.Challenge) != 64 {
		return ErrInvalide
	}
	if !hmac.Equal([]byte(a.signer(s.Challenge)), []byte(strings.ToLower(s.Signature))) {
		return ErrInvalide
	}
	expire, ok := expiration(s.Salt)
	if !ok {
		return ErrInvalide
	}
	maintenant := a.opt.Maintenant()
	if !maintenant.Before(expire) {
		return ErrExpiree
	}
	if hachage(s.Salt, s.Number) != s.Challenge {
		return ErrInvalide
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, pris := a.utilises[s.Challenge]; pris {
		return ErrDejaUtilisee
	}
	if len(a.utilises) >= a.opt.MaxEnMemoire {
		for c, e := range a.utilises {
			if !maintenant.Before(e) {
				delete(a.utilises, c)
			}
		}
		if len(a.utilises) >= a.opt.MaxEnMemoire {
			// Par prudence, on refuse plutôt que d'oublier une solution
			// encore valable, qui pourrait alors être rejouée.
			return ErrSaturee
		}
	}
	a.utilises[s.Challenge] = expire
	return nil
}

// Refusees renvoie le nombre de solutions refusées (hors absence) depuis le
// démarrage.
func (a *Altcha) Refusees() int64 { return a.refusees.Load() }

// Resoudre calcule la solution d'un défi, comme le fait le navigateur
// (statique/altcha.js) ; elle sert aux tests.
func Resoudre(d Defi) (string, error) {
	if d.Algorithm != Algorithme {
		return "", fmt.Errorf("altcha : algorithme %q", d.Algorithm)
	}
	for n := int64(0); n <= d.MaxNumber; n++ {
		if hachage(d.Salt, n) == d.Challenge {
			b, err := json.Marshal(Solution{Algorithm: d.Algorithm, Challenge: d.Challenge, Number: n, Salt: d.Salt, Signature: d.Signature})
			if err != nil {
				return "", err
			}
			return base64.StdEncoding.EncodeToString(b), nil
		}
	}
	return "", errors.New("altcha : aucune solution")
}
