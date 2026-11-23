// Package https sert la plateforme en HTTPS, avec un certificat Let's
// Encrypt obtenu par ACME (spec §11, ADR 0007, ticket #7) : le TLS s'arrête
// sur notre serveur, sans mandataire devant.
//
//   - :443 sert le site ; le certificat est obtenu et renouvelé par
//     golang.org/x/crypto/acme/autocert (défi TLS-ALPN-01 sur ce port, ou
//     HTTP-01 sur :80), gardé dans un dossier (DIGUEE_ACME_CACHE), pour le
//     seul nom d'hôte de DIGUEE_URL_PUBLIQUE ;
//   - :80 ne sert que le défi HTTP-01 et la redirection vers HTTPS ;
//   - chaque réponse HTTPS porte l'en-tête HSTS.
//
// Rien n'est journalisé qui dépende du client : ni adresse, ni nom d'hôte
// demandé (une connexion par l'adresse IP ou pour un autre nom est refusée
// sans bruit), ni erreur de poignée de main (http.Server.ErrorLog les cite
// avec l'adresse du client : elles sont jetées).
package https

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// ValeurHSTS est l'en-tête Strict-Transport-Security : un an, sans
// includeSubDomains ni preload (la consultation n'a pas de sous-domaine, et
// la préinscription concernerait diguee.sn tout entier, chez PlanetHoster).
const ValeurHSTS = "max-age=31536000"

// AnnuaireLetsEncrypt est l'annuaire ACME de production de Let's Encrypt.
const AnnuaireLetsEncrypt = "https://acme-v02.api.letsencrypt.org/directory"

// Options configure Construire.
type Options struct {
	URLPublique  string // https://consultation.diguee.sn : son nom d'hôte est le seul accepté
	Cache        string // dossier des certificats et de la clé du compte ACME
	Annuaire     string // annuaire ACME ; AnnuaireLetsEncrypt par défaut
	Courriel     string // contact du compte ACME, facultatif
	AdresseHTTPS string // « :443 » par défaut
	AdresseHTTP  string // « :80 » par défaut
	Journal      *slog.Logger
	// ClientACME remplace le client HTTP des appels à l'annuaire (tests).
	ClientACME *http.Client
}

// Serveurs sont les deux serveurs du mode ACME.
type Serveurs struct {
	HTTPS        *http.Server // le site, en TLS
	HTTP         *http.Server // défi HTTP-01 et redirection
	Gestionnaire *autocert.Manager
	hote         string
}

// Hote renvoie le seul nom d'hôte servi.
func (s *Serveurs) Hote() string { return s.hote }

// HoteDe renvoie le nom d'hôte (sans port, en minuscules) d'une adresse
// https://.
func HoteDe(urlPublique string) (string, error) {
	u, err := url.Parse(urlPublique)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return "", fmt.Errorf("adresse publique https:// attendue : %q", urlPublique)
	}
	h := strings.ToLower(u.Hostname())
	if net.ParseIP(h) != nil {
		return "", fmt.Errorf("Let's Encrypt ne délivre pas de certificat pour une adresse IP : %q", urlPublique)
	}
	return h, nil
}

// Construire prépare les deux serveurs ; site est le gestionnaire du site
// (web.Serveur), qui reçoit HSTS en plus. Rien n'écoute avant Lancer.
func Construire(o Options, site http.Handler) (*Serveurs, error) {
	hote, err := HoteDe(o.URLPublique)
	if err != nil {
		return nil, err
	}
	if o.Cache == "" {
		return nil, errors.New("dossier du cache ACME obligatoire")
	}
	if o.Annuaire == "" {
		o.Annuaire = AnnuaireLetsEncrypt
	}
	if o.AdresseHTTPS == "" {
		o.AdresseHTTPS = ":443"
	}
	if o.AdresseHTTP == "" {
		o.AdresseHTTP = ":80"
	}
	if o.Journal == nil {
		o.Journal = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	g := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(o.Cache),
		HostPolicy: Politique(hote),
		Client:     &acme.Client{DirectoryURL: o.Annuaire, HTTPClient: o.ClientACME},
		Email:      o.Courriel,
	}
	tlsCfg := g.TLSConfig()
	tlsCfg.MinVersion = tls.VersionTLS12
	tlsCfg.GetCertificate = certificat(g, hote, o.Journal)

	silence := log.New(io.Discard, "", 0)
	return &Serveurs{
		Gestionnaire: g, hote: hote,
		HTTPS: &http.Server{
			Addr: o.AdresseHTTPS, Handler: HSTS(site), TLSConfig: tlsCfg,
			ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
			WriteTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second,
			ErrorLog: silence,
		},
		HTTP: &http.Server{
			Addr: o.AdresseHTTP, Handler: g.HTTPHandler(Redirection(o.URLPublique)),
			ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
			WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second,
			ErrorLog: silence, MaxHeaderBytes: 8 << 10,
		},
	}, nil
}

// Lancer ouvre les deux ports et sert jusqu'à l'erreur du premier qui
// s'arrête. Les écoutes sont ouvertes avant de rendre la main : une adresse
// occupée est signalée tout de suite.
func (s *Serveurs) Lancer() (<-chan error, error) {
	lHTTPS, err := net.Listen("tcp", s.HTTPS.Addr)
	if err != nil {
		return nil, fmt.Errorf("écoute HTTPS : %w", err)
	}
	lHTTP, err := net.Listen("tcp", s.HTTP.Addr)
	if err != nil {
		lHTTPS.Close()
		return nil, fmt.Errorf("écoute HTTP : %w", err)
	}
	erreurs := make(chan error, 2)
	go func() { erreurs <- s.HTTPS.ServeTLS(lHTTPS, "", "") }()
	go func() { erreurs <- s.HTTP.Serve(lHTTP) }()
	return erreurs, nil
}

// Arreter arrête les deux serveurs proprement.
func (s *Serveurs) Arreter(ctx context.Context) error {
	return errors.Join(s.HTTPS.Shutdown(ctx), s.HTTP.Shutdown(ctx))
}

// Politique n'accepte que le nom d'hôte donné (sans tenir compte d'un port
// ni de la casse), pour les certificats comme pour le défi HTTP-01.
func Politique(hote string) autocert.HostPolicy {
	return func(_ context.Context, h string) error {
		if nom, _, err := net.SplitHostPort(h); err == nil {
			h = nom
		}
		if !strings.EqualFold(strings.TrimSuffix(h, "."), hote) {
			return errors.New("nom d'hôte non servi")
		}
		return nil
	}
}

// certificat filtre le nom demandé avant autocert, et journalise les échecs
// d'obtention d'un certificat (au plus une fois par minute), sans rien du
// client.
func certificat(g *autocert.Manager, hote string, j *slog.Logger) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	var mu sync.Mutex
	var dernier time.Time
	return func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		if !strings.EqualFold(strings.TrimSuffix(hello.ServerName, "."), hote) {
			return nil, errors.New("nom d'hôte non servi")
		}
		c, err := g.GetCertificate(hello)
		if err != nil {
			mu.Lock()
			if time.Since(dernier) >= time.Minute {
				dernier = time.Now()
				// L'erreur d'autocert cite l'annuaire et notre nom d'hôte,
				// jamais le client.
				j.Warn("certificat TLS indisponible", "hote", hote, "erreur", err.Error())
			}
			mu.Unlock()
		}
		return c, err
	}
}

// Redirection renvoie vers HTTPS, toujours sur le nom de l'adresse publique
// (jamais sur l'en-tête Host reçu) : GET et HEAD reçoivent un 301 ; les
// autres méthodes un 400, sans lire le corps.
func Redirection(urlPublique string) http.Handler {
	base := strings.TrimRight(urlPublique, "/")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Connection", "close")
			http.Error(w, "HTTPS obligatoire : "+base, http.StatusBadRequest)
			return
		}
		cible := base + "/"
		if u := r.URL.RequestURI(); strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "//") {
			cible = base + u
		}
		http.Redirect(w, r, cible, http.StatusMovedPermanently)
	})
}

// HSTS ajoute Strict-Transport-Security aux réponses servies en TLS.
func HSTS(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			w.Header().Set("Strict-Transport-Security", ValeurHSTS)
		}
		h.ServeHTTP(w, r)
	})
}
