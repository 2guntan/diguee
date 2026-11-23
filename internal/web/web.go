// Package web sert les pages publiques, générées côté serveur avec
// html/template, sans JavaScript. Tous les textes visibles viennent des
// catalogues de langue (paquet i18n).
package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/2guntan/diguee/internal/altcha"
	"github.com/2guntan/diguee/internal/antifraude"
	"github.com/2guntan/diguee/internal/codes"
	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/empreinte"
	"github.com/2guntan/diguee/internal/i18n"
	"github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/observatoire"
	"github.com/2guntan/diguee/internal/questionnaire"
	"github.com/2guntan/diguee/internal/session"
	"github.com/2guntan/diguee/internal/sms"
	"github.com/2guntan/diguee/internal/tampon"
	"github.com/2guntan/diguee/internal/whatsapp"
)

//go:embed gabarits/*.html
var fichiersGabarits embed.FS

//go:embed statique
var fichiersStatiques embed.FS

// Pages servies, chacune avec son gabarit.
var pages = []string{"accueil", "methodologie", "confidentialite", "introuvable", "indisponible",
	"verifier", "verifier_whatsapp", "dev_whatsapp", "questionnaire", "recapitulatif", "merci", "deja_participe", "session_expiree", "journal", "resultats", "resultats_publications",
	"sans_javascript", "verifier_sms_suspendu", "verifier_code", "verifier_code_saisie", "dev_sms",
	"observatoire", "engagement", "close"}

// Politique de sécurité : aucun script, aucune ressource tierce.
const csp = "default-src 'none'; style-src 'self'; font-src 'self'; img-src 'self'; " +
	"form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// cspAvecScript est la politique de la seule page qui charge un script : la
// page du jeton de la vérification inversée, qui interroge le serveur toutes
// les 3 secondes (spec §5.1). Le script est servi depuis le domaine.
const cspAvecScript = "default-src 'none'; script-src 'self'; connect-src 'self'; style-src 'self'; " +
	"font-src 'self'; img-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// cspCaptcha est la politique de la page qui calcule le captcha ALTCHA
// (spec §4, étape 2) : le script, servi depuis le domaine, n'a besoin
// d'aucune connexion, le défi étant écrit dans la page.
const cspCaptcha = "default-src 'none'; script-src 'self'; style-src 'self'; " +
	"font-src 'self'; img-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// Scripts servis depuis le domaine, chacun autorisé par la CSP de sa seule page.
const (
	scriptVerifier = "/statique/verifier.js" // page du jeton : interrogation toutes les 3 s
	scriptAltcha   = "/statique/altcha.js"   // page de vérification : preuve de travail
)

// marqueACompleter signale dans les pages une valeur encore provisoire
// (« À COMPLÉTER »). Elle ne se voit qu'en développement : en production, la
// garde refuse de démarrer s'il en reste une.
const marqueACompleter = `<mark class="a-completer">` + config.ACompleter + `</mark>`

// Options regroupe les dépendances du serveur web.
type Options struct {
	Config     *config.Config
	Catalogues *i18n.Catalogues
	// Publications donne les publications quotidiennes : le compteur de
	// l'accueil est celui de la dernière (jamais un comptage en direct), et
	// /resultats les sert. Obligatoire.
	Publications Publications
	Version      string
	SHA256       string
	Journal      *slog.Logger
	Maintenant   func() time.Time // time.Now par défaut

	// JournalPublic sert /journal et /journal.json s'il est fourni.
	JournalPublic *journal.Journal
	// Ancrages donne les ancrages extérieurs du journal (ticket #12) : les
	// derniers sont affichés sur /journal, les preuves OpenTimestamps servies
	// sous /journal/ancrages/AAAA-MM-JJ.ots et la liste complète sous
	// /journal/ancrages.json. Facultatif.
	Ancrages Ancrages

	// Observatoire sert les pages publiques de l'observatoire (ticket #13)
	// s'il est fourni : /observatoire (liste filtrable),
	// /observatoire/engagements/{id} (fiche) et
	// /observatoire/archives/<sha256>.<extension> (copies archivées).
	Observatoire *observatoire.Service

	// Equipe sert l'espace équipe sous /equipe s'il est fourni (paquet
	// equipe) : routes, cookies et contrôles d'accès séparés des pages
	// publiques, mêmes en-têtes de sécurité et même journal d'accès.
	Equipe http.Handler

	// Parcours de réponse : servi seulement si tous ces champs sont remplis.
	Empreinteur   empreinte.Empreinteur
	Tampon        *tampon.Tampon
	Sessions      *session.Magasin
	Questionnaire *questionnaire.Questionnaire // questionnaire.Courant() par défaut
	// Vérification inversée par WhatsApp (spec §5.1) : l'intégration
	// (webhook servi sous /webhook/whatsapp) et le vérificateur des jetons.
	// En mode « fake », la page /dev/whatsapp simule l'envoi du message ;
	// elle n'existe jamais en production.
	WhatsApp     *whatsapp.Integration
	Verificateur *whatsapp.Verificateur
	// Anti-fraude avant la vérification (spec §7, ADR 0007) : le captcha
	// ALTCHA, obligatoire pour servir le parcours, et la limite par adresse
	// (construite d'après DIGUEE_IP_PAR_HEURE si nil).
	Altcha       *altcha.Altcha
	Ralentisseur *antifraude.Ralentisseur
	// Codes : code envoyé par WhatsApp, en secours de la vérification
	// inversée (spec §5.2), servi sous /verifier/code s'il est fourni. Il
	// envoie par WhatsApp.Envoyeur et partage la limite par empreinte de
	// la vérification inversée.
	Codes *codes.Verificateur
	// Code par SMS, en dernier recours (spec §5.3, ticket #10), servi sous
	// /verifier/sms si SMS et CodesSMS sont fournis. CodesSMS porte les
	// limites du SMS (limite par empreinte propre au canal, en plus de la
	// limite partagée, essais) et son coupe-circuit. La limite par adresse
	// du SMS est construite d'après DIGUEE_SMS_IP_PAR_HEURE si
	// RalentisseurSMS est nil ; le quota des demandes sans captcha (sans
	// JavaScript), d'après DIGUEE_SMS_SANS_JS_PAR_HEURE et
	// DIGUEE_SMS_SANS_JS_IP_PAR_HEURE. En mode « fake », la page /dev/sms
	// montre le dernier code ; elle n'existe jamais en production.
	SMS             *sms.Integration
	CodesSMS        *codes.Verificateur
	RalentisseurSMS *antifraude.Ralentisseur
}

// Serveur sert les pages publiques.
type Serveur struct {
	opt      Options
	gabarits map[string]*template.Template
	mux      *http.ServeMux
	webhook  http.Handler

	codeWhatsApp *canalCode // nil si le code par WhatsApp n'est pas servi
	sms          *canalCode // nil si le code par SMS n'est pas servi
}

// Nouveau prépare les gabarits et les routes.
func Nouveau(opt Options) (*Serveur, error) {
	if opt.Maintenant == nil {
		opt.Maintenant = time.Now
	}
	if opt.Journal == nil {
		opt.Journal = slog.Default()
	}
	if opt.Questionnaire == nil {
		opt.Questionnaire = questionnaire.Courant()
	}
	if opt.Ralentisseur == nil {
		opt.Ralentisseur = antifraude.NouveauRalentisseur(antifraude.OptionsRalentisseur{
			ParHeure: opt.Config.IPParHeure, Maintenant: opt.Maintenant, Journal: opt.Journal,
		})
	}
	if opt.WhatsApp != nil && opt.WhatsApp.Fausse() && opt.Config.Env == config.Production {
		return nil, fmt.Errorf("la simulation WhatsApp (DIGUEE_WHATSAPP=fake) est interdite en production")
	}
	if opt.SMS != nil && opt.SMS.Fausse() && opt.Config.Env == config.Production {
		return nil, fmt.Errorf("la console des codes SMS (DIGUEE_SMS=fake) est interdite en production")
	}
	if opt.Publications == nil {
		return nil, fmt.Errorf("web : Publications est obligatoire")
	}
	s := &Serveur{opt: opt, gabarits: map[string]*template.Template{}, mux: http.NewServeMux()}
	if opt.Codes != nil {
		s.codeWhatsApp = &canalCode{codes: opt.Codes, ralentisseur: opt.Ralentisseur, vue: VueCanal{
			Chemin: "/verifier/code", Saisie: "/verifier/code/saisie",
			Titre: "code.titre", Principe: "code.principe", Meta: "code.meta",
			Consigne: "code.saisie.consigne", Libelle: "code.saisie.libelle", ErreurEnvoi: "code.erreur.envoi",
		}}
		if opt.WhatsApp != nil && opt.WhatsApp.Fausse() {
			s.codeWhatsApp.vue.Dev = "/dev/whatsapp"
		}
	}
	if opt.SMS != nil && opt.CodesSMS != nil {
		if opt.RalentisseurSMS == nil {
			opt.RalentisseurSMS = antifraude.NouveauRalentisseur(antifraude.OptionsRalentisseur{
				ParHeure: opt.Config.SMSIPParHeure, Maintenant: opt.Maintenant, Journal: opt.Journal,
			})
			s.opt.RalentisseurSMS = opt.RalentisseurSMS
		}
		s.sms = &canalCode{codes: opt.CodesSMS, ralentisseur: opt.RalentisseurSMS,
			sansJS: nouveauQuotaSansJS(opt.Config.SMSSansJSIPParHeure, opt.Config.SMSSansJSParHeure, opt.Maintenant, opt.Journal),
			vue: VueCanal{SMS: true,
				Chemin: "/verifier/sms", Saisie: "/verifier/sms/saisie",
				Titre: "sms.titre", Principe: "sms.principe", Meta: "sms.meta",
				Consigne: "sms.saisie.consigne", Libelle: "sms.saisie.libelle", ErreurEnvoi: "sms.erreur.envoi",
			}}
		if c, ok := opt.SMS.Envoyeur.(*codes.Console); ok {
			s.sms.console = c
			s.sms.vue.Dev = "/dev/sms"
		}
	}
	for _, p := range pages {
		// Les fonctions sont remplacées à chaque requête par celles de la langue
		// choisie ; celles-ci ne servent qu'à l'analyse.
		g, err := template.New(p).Funcs(fonctions(opt.Catalogues.Choisir(i18n.Reference))).
			ParseFS(fichiersGabarits, "gabarits/base.html", "gabarits/"+p+".html")
		if err != nil {
			return nil, fmt.Errorf("gabarit %s : %w", p, err)
		}
		s.gabarits[p] = g
	}

	statique, err := fs.Sub(fichiersStatiques, "statique")
	if err != nil {
		return nil, err
	}
	s.mux.Handle("GET /statique/", http.StripPrefix("/statique/", cacheStatique(http.FileServerFS(statique))))
	s.mux.HandleFunc("GET /{$}", s.accueil)
	s.mux.HandleFunc("GET /methodologie", s.page("methodologie"))
	s.mux.HandleFunc("GET /confidentialite", s.page("confidentialite"))
	s.mux.HandleFunc("GET /sante", s.sante)
	s.mux.HandleFunc("GET /resultats", s.resultatsDerniers)
	s.mux.HandleFunc("GET /resultats.json", s.resultatsJSON)
	s.mux.HandleFunc("GET /resultats/publications", s.resultatsPublications)
	s.mux.HandleFunc("GET /resultats/{jour}", s.resultatsJour)
	if opt.JournalPublic != nil {
		s.mux.HandleFunc("GET /journal", s.journalPage)
		s.mux.HandleFunc("GET /journal.json", s.journalExport)
	}
	if opt.Ancrages != nil {
		s.mux.HandleFunc("GET /journal/ancrages/{fichier}", s.preuveOTS)
		s.mux.HandleFunc("GET /journal/ancrages.json", s.ancragesJSON)
	}
	if opt.Observatoire != nil {
		s.mux.HandleFunc("GET /observatoire", s.observatoireListe)
		s.mux.HandleFunc("GET /observatoire/engagements/{id}", s.observatoireFiche)
		s.mux.HandleFunc("GET /observatoire/archives/{fichier}", s.observatoireArchive)
	}
	if opt.Equipe != nil {
		s.mux.Handle("/equipe", opt.Equipe)
		s.mux.Handle("/equipe/", opt.Equipe)
	}
	if opt.WhatsApp != nil && opt.Verificateur != nil {
		wh, err := whatsapp.NouveauWebhook(whatsapp.OptionsWebhook{
			Integration: opt.WhatsApp, Verificateur: opt.Verificateur, Journal: opt.Journal,
		})
		if err != nil {
			return nil, err
		}
		s.webhook = wh
		// Servi même hors de la période d'ouverture : Meta désactive un
		// webhook qui échoue.
		s.mux.Handle(whatsapp.CheminWebhook, wh)
		if opt.WhatsApp.Fausse() {
			s.mux.HandleFunc("GET /dev/whatsapp", s.devWhatsApp)
			s.mux.HandleFunc("POST /dev/whatsapp", s.devWhatsApp)
		}
	}
	s.mux.HandleFunc("GET /verifier", s.ouvert(s.verifierPage))
	s.mux.HandleFunc("POST /verifier", s.ouvert(s.creerJeton))
	s.mux.HandleFunc("GET /verifier/whatsapp", s.ouvert(s.pageJeton))
	s.mux.HandleFunc("POST /verifier/whatsapp", s.ouvert(s.pageJeton))
	s.mux.HandleFunc("GET /verifier/etat", s.ouvert(s.etatJeton))
	for _, c := range []*canalCode{s.codeWhatsApp, s.sms} {
		if c == nil {
			continue
		}
		s.mux.HandleFunc("GET "+c.vue.Chemin, s.ouvert(s.codePage(c)))
		s.mux.HandleFunc("POST "+c.vue.Chemin, s.ouvert(s.demanderCode(c)))
		s.mux.HandleFunc("GET "+c.vue.Saisie, s.ouvert(s.saisieCode(c)))
		s.mux.HandleFunc("POST "+c.vue.Saisie, s.ouvert(s.saisieCode(c)))
	}
	if s.sms != nil && s.sms.console != nil {
		s.mux.HandleFunc("GET /dev/sms", s.devSMS)
	}
	s.mux.HandleFunc("GET /verifier/sans-javascript", s.ouvert(s.page("sans_javascript")))
	s.mux.HandleFunc("GET /questionnaire", s.ouvert(s.avecSession(s.questionnairePage)))
	s.mux.HandleFunc("POST /questionnaire", s.ouvert(s.avecSession(s.questionnairePage)))
	s.mux.HandleFunc("POST /questionnaire/recapitulatif", s.ouvert(s.avecSession(s.recapitulatif)))
	s.mux.HandleFunc("POST /envoyer", s.ouvert(s.avecSession(s.envoyer)))
	s.mux.HandleFunc("GET /merci", s.page("merci"))
	s.mux.HandleFunc("/", s.introuvable)
	return s, nil
}

// ServeHTTP applique les en-têtes de sécurité et le journal d'accès.
func (s *Serveur) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	debut := time.Now()
	h := w.Header()
	h.Set("Content-Security-Policy", csp)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	ew := &ecrivainStatut{ResponseWriter: w, statut: http.StatusOK}
	s.mux.ServeHTTP(ew, r)
	// Ni adresse IP, ni paramètres de requête, ni agent utilisateur, ni corps.
	s.opt.Journal.Info("requête", "methode", r.Method, "chemin", r.URL.Path,
		"statut", ew.statut, "duree_ms", time.Since(debut).Milliseconds())
}

// Donnees est ce que reçoit chaque gabarit.
type Donnees struct {
	Config   *config.Config
	Phase    config.Phase
	Version  string
	SHA256   string
	Parcours bool // le parcours de réponse est servi

	// Accueil : le compteur de la dernière publication quotidienne.
	Compteur     int64
	SeuilAtteint bool      // la dernière publication contient les résultats détaillés
	JourCompteur time.Time // jour de cette publication ; zéro s'il n'y en a pas encore

	Resultats *VueResultats // pages /resultats

	// Pages du parcours.
	Erreur       string // clé d'un message d'erreur
	Erreurs      bool   // le questionnaire envoyé contient des erreurs
	Questions    []VueQuestion
	Profil       []VueQuestion
	CleMajeur    string
	MajeurCoche  bool
	MajeurErreur string
	Caches       []Cache // champs cachés du récapitulatif

	// Vérification inversée.
	Jeton      string        // « DIGUEE 4821-KX »
	LienWaMe   string        // https://wa.me/<numéro>?text=<jeton>
	QR         template.HTML // le même lien en QR code (SVG)
	Script     string        // script chargé par la page (scriptVerifier ou scriptAltcha)
	Defi       *altcha.Defi  // page de vérification : défi du captcha
	Simulation bool          // DIGUEE_WHATSAPP=fake : lien vers la page de simulation
	StatutDev  int           // page de simulation : statut renvoyé par le webhook

	// Code de secours.
	CodeSecours bool          // le parcours du code par WhatsApp est servi
	SMSSecours  bool          // le parcours du code par SMS est servi
	Canal       *VueCanal     // pages du code : le canal (WhatsApp ou SMS)
	Essais      int           // essais restants pour le code en cours
	DureeCode   time.Duration // validité d'un code
	CodeDev     string        // page de simulation : dernier code écrit dans la console

	Journal *VueJournal // page /journal

	AvecObservatoire bool             // l'observatoire est servi : lien dans la navigation
	Observatoire     *VueObservatoire // pages /observatoire
}

func (s *Serveur) donnees() Donnees {
	return Donnees{
		Config:      s.opt.Config,
		Phase:       s.opt.Config.Phase(s.opt.Maintenant()),
		Version:     s.opt.Version,
		SHA256:      s.opt.SHA256,
		Parcours:    s.parcoursActif(),
		Simulation:  s.opt.WhatsApp != nil && s.opt.WhatsApp.Fausse(),
		CodeSecours: s.codeWhatsApp != nil,
		SMSSecours:  s.sms != nil,
		DureeCode:   s.opt.Config.CodeDuree,

		AvecObservatoire: s.opt.Observatoire != nil,
	}
}

// langue choisit le catalogue d'après le paramètre « langue », s'il désigne une
// langue disponible ; sinon le français.
func (s *Serveur) langue(r *http.Request) *i18n.Catalogue {
	return s.opt.Catalogues.Choisir(r.URL.Query().Get("langue"))
}

func (s *Serveur) rendre(w http.ResponseWriter, r *http.Request, statut int, page string, d Donnees) {
	g, err := s.gabarits[page].Clone()
	if err == nil {
		var buf bytes.Buffer
		err = g.Funcs(fonctions(s.langue(r))).ExecuteTemplate(&buf, "base", d)
		if err == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(statut)
			_, _ = w.Write(buf.Bytes())
			return
		}
	}
	s.opt.Journal.Error("rendu de page", "page", page, "erreur", err)
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}

func (s *Serveur) accueil(w http.ResponseWriter, r *http.Request) {
	d := s.donnees()
	res, err := s.opt.Publications.DernierResume(r.Context())
	if err != nil {
		s.opt.Journal.Error("lecture du compteur", "erreur", err)
		s.rendre(w, r, http.StatusServiceUnavailable, "indisponible", d)
		return
	}
	if res != nil {
		d.Compteur, d.SeuilAtteint, d.JourCompteur = int64(res.Reponses), res.ResultatsPublies, res.Jour
	}
	s.rendre(w, r, http.StatusOK, "accueil", d)
}

func (s *Serveur) page(nom string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.rendre(w, r, http.StatusOK, nom, s.donnees())
	}
}

func (s *Serveur) introuvable(w http.ResponseWriter, r *http.Request) {
	s.rendre(w, r, http.StatusNotFound, "introuvable", s.donnees())
}

// sante répond 200 si la base répond ; utilisé par systemd et la supervision.
func (s *Serveur) sante(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if _, err := s.opt.Publications.DernierResume(r.Context()); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("base indisponible\n"))
		return
	}
	_, _ = w.Write([]byte("ok\n"))
}

func cacheStatique(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "polices/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		h.ServeHTTP(w, r)
	})
}

// fonctions donne aux gabarits l'accès au catalogue de la langue.
func fonctions(c *i18n.Catalogue) template.FuncMap {
	return template.FuncMap{
		"langue": func() string { return c.Code },
		// signaler échappe le texte et y signale les valeurs provisoires.
		"signaler": func(s string) template.HTML {
			return template.HTML(strings.ReplaceAll(template.HTMLEscapeString(s), config.ACompleter, marqueACompleter))
		},
		"t":       c.T,
		"tf":      c.F,
		"pluriel": func(cle string, n any) string { return c.Pluriel(cle, entier(n)) },
		"nombre":  func(n any) string { return c.Nombre(entier(n)) },
		"duree":   c.Duree,
		"date":    c.Date,
		"heure":   c.Heure,
		"dateheure": func(t time.Time) string {
			t = t.UTC()
			return c.F("format.date_heure", c.Date(t), c.Heure(time.Duration(t.Hour())*time.Hour+time.Duration(t.Minute())*time.Minute))
		},
		"jour": func(d config.Date) string {
			if d.Provisoire() {
				return config.ACompleter
			}
			return c.Date(d.Jour)
		},
		"valeur":     func(v observatoire.Valeur) string { return c.T("obs.valeur." + string(v)) },
		"canal":      func(x observatoire.Canal) string { return c.T("obs.canal." + string(x)) },
		"datecourte": c.DateCourte,
		"extrait":    extrait,
		"taille":     func(n int64) string { return Taille(c, n) },
		"liste":      func(xs []string) string { return strings.Join(xs, c.T("format.liste.separateur")) },
		"multiplicateur": func(f float64) string {
			return c.F("format.multiplicateur", strconv.FormatFloat(f, 'f', -1, 64))
		},
	}
}

// Taille écrit une taille de fichier en kilo-octets (arrondis au-dessus).
func Taille(c *i18n.Catalogue, n int64) string {
	return c.F("format.ko", c.Nombre((n+1023)/1024))
}

func entier(n any) int64 {
	switch v := n.(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case int32:
		return int64(v)
	default:
		panic(fmt.Sprintf("entier attendu, reçu %T", n))
	}
}

type ecrivainStatut struct {
	http.ResponseWriter
	statut int
}

func (e *ecrivainStatut) WriteHeader(statut int) {
	e.statut = statut
	e.ResponseWriter.WriteHeader(statut)
}
