package web

import (
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/2guntan/diguee/internal/altcha"
	"github.com/2guntan/diguee/internal/antifraude"
	"github.com/2guntan/diguee/internal/codes"
)

// Codes de secours (spec §5.2 et §5.3, ADR 0004 et 0009), servis par les
// mêmes gestionnaires pour chaque canal :
//
//	GET  /verifier/code          code par WhatsApp : saisie du numéro, captcha ALTCHA
//	POST /verifier/code          limite par adresse, captcha, numéro contrôlé,
//	                             empreinte, limite par empreinte, envoi du code
//	GET  /verifier/code/saisie   saisie du code
//	POST /verifier/code/saisie   vérification du code
//	GET  /verifier/sms           code par SMS (« Je n'ai pas WhatsApp »)
//	POST /verifier/sms           idem, avec les limites du SMS et son coupe-circuit
//	GET  /verifier/sms/saisie
//	POST /verifier/sms/saisie
//
// Chaque canal a son propre codes.Verificateur ; tous partagent la même
// limite par empreinte que la vérification inversée.
//
// Sans JavaScript (spec §4, étape 2, et §7 ; choix à valider par le
// porteur) : le captcha ALTCHA demande JavaScript. Le canal SMS accepte donc
// une demande sans solution du captcha, mais seulement sous un quota séparé,
// bien plus strict (quotaSansJS : par adresse et pour tout le site, par
// heure), en plus de toutes ses autres limites et de son coupe-circuit. Une
// solution présente mais fausse ou expirée est refusée comme ailleurs. Le
// canal WhatsApp, lui, exige toujours le captcha.

// champNumero et champCode sont les champs des formulaires.
const (
	champNumero = "numero"
	champCode   = "code"
)

// Clés des messages des pages du code, par statut.
var erreursCode = map[codes.Statut]string{
	codes.NumeroRefuse:  "code.erreur.format",
	codes.PrefixeRefuse: "code.erreur.prefixe",
	codes.Limitee:       "verif.erreur.limite",
	codes.Epuise:        "code.erreur.epuise",
	codes.Expire:        "code.erreur.expire",
	codes.Aucun:         "code.erreur.expire",
	codes.SessionPerdue: "code.erreur.expire",
}

// VueCanal donne aux gabarits du code ce qui change d'un canal à l'autre :
// les chemins et les clés du catalogue.
type VueCanal struct {
	SMS         bool
	Chemin      string // saisie du numéro
	Saisie      string // saisie du code
	Dev         string // page de simulation (mode « fake »), sinon vide
	Titre       string
	Principe    string
	Meta        string
	Consigne    string
	Libelle     string
	ErreurEnvoi string
}

// canalCode est un canal du code de secours.
type canalCode struct {
	vue          VueCanal
	codes        *codes.Verificateur
	ralentisseur *antifraude.Ralentisseur
	console      *codes.Console // mode « fake » : dernier code, pour la page de simulation
	sansJS       *quotaSansJS   // SMS : demandes sans captcha permises sous quota ; nil sinon
}

// quotaSansJS borne les demandes de code SMS faites sans captcha : au plus
// ParAdresse par heure glissante depuis une même adresse (HMAC à sel
// quotidien, comme le ralentisseur), et au plus Global par heure glissante
// pour tout le site. Au-delà, la demande est refusée (et non ralentie) : le
// captcha n'est pas là pour freiner les robots. Une alerte, sans aucune
// donnée, part au plus une fois par heure quand le quota global est atteint.
type quotaSansJS struct {
	adresses   antifraude.Anonymiseur
	parAdresse *antifraude.Fenetre
	global     *antifraude.Fenetre
	maintenant func() time.Time
	journal    *slog.Logger

	mu            sync.Mutex
	derniereHeure int64
	refusees      atomic.Int64
}

func nouveauQuotaSansJS(parAdresse, global int, maintenant func() time.Time, journal *slog.Logger) *quotaSansJS {
	return &quotaSansJS{
		parAdresse: antifraude.NouvelleFenetre(parAdresse, parAdresse, time.Hour),
		global:     antifraude.NouvelleFenetre(global, global, time.Hour),
		maintenant: maintenant, journal: journal, derniereHeure: -1,
	}
}

// autoriser compte la demande et dit si elle est permise.
func (q *quotaSansJS) autoriser(r *http.Request) bool {
	t := q.maintenant()
	if !q.parAdresse.AjouterSiPossible(q.adresses.Cle(r.RemoteAddr, t), t) {
		q.refusees.Add(1)
		return false
	}
	if q.global.AjouterSiPossible("", t) {
		return true
	}
	q.refusees.Add(1)
	h := t.Unix() / 3600
	q.mu.Lock()
	alerter := q.derniereHeure != h
	q.derniereHeure = h
	q.mu.Unlock()
	if alerter {
		q.journal.Warn("anti-fraude : quota horaire des codes SMS sans captcha atteint ; demandes sans JavaScript refusées",
			"quota_par_heure", q.global.Max)
	}
	return false
}

func (s *Serveur) codePage(c *canalCode) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if etat, ok := s.opt.Sessions.Lire(r); ok && etat.Verifiee {
			http.Redirect(w, r, "/questionnaire", http.StatusSeeOther)
			return
		}
		if c.codes.Suspendu() {
			s.rendreSuspendu(w, r)
			return
		}
		s.rendreCode(w, r, c, http.StatusOK, "")
	}
}

// rendreSuspendu explique que le secours par SMS est momentanément
// indisponible (coupe-circuit).
func (s *Serveur) rendreSuspendu(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "3600")
	s.rendre(w, r, http.StatusServiceUnavailable, "verifier_sms_suspendu", s.donnees())
}

// rendreCode affiche la saisie du numéro avec un nouveau défi ALTCHA.
func (s *Serveur) rendreCode(w http.ResponseWriter, r *http.Request, c *canalCode, statut int, erreur string) {
	defi, err := s.opt.Altcha.NouveauDefi()
	if err != nil {
		s.opt.Journal.Error("création du défi ALTCHA", "erreur", err)
		s.rendre(w, r, http.StatusServiceUnavailable, "indisponible", s.donnees())
		return
	}
	d := s.donnees()
	d.Canal = &c.vue
	d.Defi = &defi
	d.Erreur = erreur
	d.Script = scriptAltcha
	w.Header().Set("Content-Security-Policy", cspCaptcha)
	s.rendre(w, r, statut, "verifier_code", d)
}

// demanderCode traite le numéro saisi. Dans l'ordre : limite par adresse du
// canal (ralentissement, jamais de blocage), coupe-circuit, captcha (ou,
// pour le SMS sans JavaScript, le quota sans captcha), puis codes.Demander
// (format et préfixe, empreinte, limites par empreinte, coupe-circuit,
// envoi). Le numéro est retiré du formulaire dès sa lecture et n'est plus
// référencé après l'envoi.
func (s *Serveur) demanderCode(c *canalCode) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c.ralentisseur.Freiner(r)
		saisie := r.PostForm.Get(champNumero)
		r.PostForm.Del(champNumero)
		r.Form.Del(champNumero)
		if etat, ok := s.opt.Sessions.Lire(r); ok && etat.Verifiee {
			http.Redirect(w, r, "/questionnaire", http.StatusSeeOther)
			return
		}
		if c.codes.Suspendu() {
			s.rendreSuspendu(w, r)
			return
		}
		switch err := s.opt.Altcha.Verifier(r.PostForm.Get(altcha.Champ)); {
		case err == nil:
		case errors.Is(err, altcha.ErrAbsente) && c.sansJS != nil:
			// Probablement sans JavaScript : quota séparé, très strict.
			if !c.sansJS.autoriser(r) {
				s.rendreCode(w, r, c, http.StatusTooManyRequests, "sms.erreur.sans_js")
				return
			}
		case errors.Is(err, altcha.ErrAbsente):
			http.Redirect(w, r, "/verifier/sans-javascript", http.StatusSeeOther)
			return
		case errors.Is(err, altcha.ErrSaturee):
			s.opt.Journal.Error("captcha ALTCHA : mémoire des solutions pleine")
			s.rendre(w, r, http.StatusServiceUnavailable, "indisponible", s.donnees())
			return
		case errors.Is(err, altcha.ErrExpiree):
			s.rendreCode(w, r, c, http.StatusBadRequest, "verif.captcha.expire")
			return
		default:
			s.rendreCode(w, r, c, http.StatusBadRequest, "verif.captcha.invalide")
			return
		}
		id, err := s.opt.Sessions.Ouvrir(w, r)
		if err != nil {
			s.opt.Journal.Error("ouverture de session", "erreur", err)
			s.rendre(w, r, http.StatusServiceUnavailable, "indisponible", s.donnees())
			return
		}
		// Un seul parcours à la fois : le jeton ou le code d'un autre canal
		// éventuel de la session est révoqué.
		s.oublierParcours(id)
		statut, err := c.codes.Demander(r.Context(), id, saisie)
		switch {
		case errors.Is(err, codes.ErrEnvoi):
			s.rendreCode(w, r, c, http.StatusServiceUnavailable, c.vue.ErreurEnvoi)
			return
		case err != nil:
			s.opt.Journal.Error("demande de code", "erreur", err)
			s.rendre(w, r, http.StatusServiceUnavailable, "indisponible", s.donnees())
			return
		case statut == codes.Suspendu:
			s.rendreSuspendu(w, r)
			return
		case statut != codes.Envoye:
			s.rendreCode(w, r, c, http.StatusBadRequest, erreursCode[statut])
			return
		}
		http.Redirect(w, r, c.vue.Saisie, http.StatusSeeOther)
	}
}

// saisieCode affiche la saisie du code (GET) ou la vérifie (POST). Un code
// déjà envoyé reste saisissable même si le canal a été suspendu depuis :
// aucun nouvel envoi n'en dépend.
func (s *Serveur) saisieCode(c *canalCode) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := s.opt.Sessions.ID(r)
		if !ok {
			if r.Method == http.MethodGet {
				http.Redirect(w, r, c.vue.Chemin, http.StatusSeeOther)
				return
			}
			s.rendre(w, r, http.StatusUnauthorized, "session_expiree", s.donnees())
			return
		}
		if etat, _ := s.opt.Sessions.Lire(r); etat.Verifiee {
			http.Redirect(w, r, "/questionnaire", http.StatusSeeOther)
			return
		}
		d := s.donnees()
		d.Canal = &c.vue
		if r.Method == http.MethodGet {
			statut, restants := c.codes.Etat(id)
			if statut == codes.Aucun {
				http.Redirect(w, r, c.vue.Chemin, http.StatusSeeOther)
				return
			}
			if statut == codes.Envoye {
				d.Essais = restants
			} else {
				d.Erreur = erreursCode[statut]
			}
			s.rendre(w, r, http.StatusOK, "verifier_code_saisie", d)
			return
		}
		saisie := r.PostForm.Get(champCode)
		r.PostForm.Del(champCode)
		r.Form.Del(champCode)
		statut, restants, err := c.codes.Valider(r.Context(), id, saisie)
		switch {
		case err != nil:
			s.opt.Journal.Error("vérification du code", "erreur", err)
			s.rendre(w, r, http.StatusServiceUnavailable, "indisponible", d)
			return
		case statut == codes.Verifiee:
			// La session devient vérifiée : nouvel identifiant.
			if err := s.opt.Sessions.Renouveler(w, r); err != nil {
				s.rendre(w, r, http.StatusUnauthorized, "session_expiree", d)
				return
			}
			http.Redirect(w, r, "/questionnaire", http.StatusSeeOther)
			return
		case statut == codes.DejaParticipe:
			// Rien d'autre n'est révélé (spec §4, étape 3).
			s.detruireSession(w, r)
			s.rendre(w, r, http.StatusConflict, "deja_participe", d)
			return
		case statut == codes.Faux:
			d.Essais = restants
			d.Erreur = "code.erreur.faux"
		default:
			d.Erreur = erreursCode[statut]
		}
		s.rendre(w, r, http.StatusBadRequest, "verifier_code_saisie", d)
	}
}

// devSMS est la page de développement qui montre le dernier code « envoyé »
// par SMS (console du mode « fake »). Elle n'existe qu'avec DIGUEE_SMS=fake,
// donc jamais en production. Elle ne montre jamais de numéro.
func (s *Serveur) devSMS(w http.ResponseWriter, r *http.Request) {
	d := s.donnees()
	d.Canal = &s.sms.vue
	d.CodeDev = s.sms.console.Dernier()
	s.rendre(w, r, http.StatusOK, "dev_sms", d)
}

// oublierParcours révoque le jeton et les codes de la session, sur tous les
// canaux.
func (s *Serveur) oublierParcours(id string) {
	s.opt.Verificateur.Oublier(id)
	for _, c := range []*canalCode{s.codeWhatsApp, s.sms} {
		if c != nil {
			c.codes.Oublier(id)
		}
	}
}
