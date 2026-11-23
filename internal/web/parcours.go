package web

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/questionnaire"
	"github.com/2guntan/diguee/internal/session"
	"github.com/2guntan/diguee/internal/tampon"
)

// Parcours de réponse (spec §4) : vérification inversée par WhatsApp
// (verification.go), questionnaire, récapitulatif, envoi, merci. Tout fonctionne sans
// JavaScript : les réponses en cours ne vivent que dans le formulaire du
// navigateur, et le récapitulatif les porte dans des champs cachés. Rien n'est
// écrit en base avant la confirmation ; à l'envoi, la participation et la
// réponse partent dans le tampon (ADR 0003) et la session est détruite.

// tailleMaxFormulaire borne le corps d'un formulaire.
const tailleMaxFormulaire = 64 << 10

// VueChoix est un choix tel que l'affiche le formulaire.
type VueChoix struct {
	ID    string
	Code  int16
	Cle   string
	Coche bool
}

// VueQuestion est une question telle que l'affiche le formulaire.
type VueQuestion struct {
	Numero int // 1 à 8 pour les questions, 0 pour le profil
	Nom    string
	Cle    string
	Aide   string
	Saisie string // « radio » ou « checkbox »
	Requis bool   // attribut required (boutons radio seulement)
	Choix  []VueChoix
	Erreur string
}

// Coches renvoie les choix cochés, pour le récapitulatif.
func (q VueQuestion) Coches() []VueChoix {
	var cs []VueChoix
	for _, c := range q.Choix {
		if c.Coche {
			cs = append(cs, c)
		}
	}
	return cs
}

// Cache est un champ caché du récapitulatif.
type Cache struct {
	Nom    string
	Valeur string
}

// parcoursActif indique si le parcours de réponse est servi : il faut la
// vérification inversée, un empreinteur, un tampon et des sessions.
func (s *Serveur) parcoursActif() bool {
	o := s.opt
	return o.WhatsApp != nil && o.Verificateur != nil && o.Empreinteur != nil && o.Tampon != nil && o.Sessions != nil
}

// ouvert enveloppe les pages du parcours : hors de la période d'ouverture,
// ou si le parcours n'est pas servi, on revient à l'accueil.
func (s *Serveur) ouvert(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.parcoursActif() || s.opt.Config.Phase(s.opt.Maintenant()) != config.PhaseOuverte {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, tailleMaxFormulaire)
			if err := r.ParseForm(); err != nil {
				http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
				return
			}
		}
		h(w, r)
	}
}

// avecSession exige une session vérifiée.
func (s *Serveur) avecSession(h func(http.ResponseWriter, *http.Request, session.Etat)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		etat, ok := s.opt.Sessions.Lire(r)
		if !ok || !etat.Verifiee {
			if r.Method == http.MethodGet {
				http.Redirect(w, r, "/verifier", http.StatusSeeOther)
				return
			}
			s.rendre(w, r, http.StatusUnauthorized, "session_expiree", s.donnees())
			return
		}
		h(w, r, etat)
	}
}

// questionnairePage affiche le questionnaire, vide (GET) ou prérempli avec
// le formulaire reçu (POST, bouton « Modifier » du récapitulatif).
func (s *Serveur) questionnairePage(w http.ResponseWriter, r *http.Request, _ session.Etat) {
	var form url.Values
	if r.Method == http.MethodPost {
		form = r.PostForm
	}
	s.rendre(w, r, http.StatusOK, "questionnaire", s.vueQuestionnaire(form, nil))
}

// recapitulatif valide le questionnaire et affiche le récapitulatif, sans
// rien enregistrer.
func (s *Serveur) recapitulatif(w http.ResponseWriter, r *http.Request, _ session.Etat) {
	rep, errs := s.opt.Questionnaire.Lire(r.PostForm)
	if errs != nil {
		s.rendre(w, r, http.StatusUnprocessableEntity, "questionnaire", s.vueQuestionnaire(r.PostForm, errs))
		return
	}
	valeurs := s.opt.Questionnaire.Valeurs(rep)
	d := s.vueQuestionnaire(valeurs, nil)
	for _, q := range append(slices.Clone(d.Questions), d.Profil...) {
		for _, c := range q.Coches() {
			d.Caches = append(d.Caches, Cache{Nom: q.Nom, Valeur: strconv.Itoa(int(c.Code))})
		}
	}
	d.Caches = append(d.Caches, Cache{Nom: questionnaire.ChampMajeur, Valeur: "1"})
	s.rendre(w, r, http.StatusOK, "recapitulatif", d)
}

// envoyer place la participation et la réponse dans le tampon, puis détruit
// la session.
func (s *Serveur) envoyer(w http.ResponseWriter, r *http.Request, etat session.Etat) {
	rep, errs := s.opt.Questionnaire.Lire(r.PostForm)
	if errs != nil {
		s.rendre(w, r, http.StatusUnprocessableEntity, "questionnaire", s.vueQuestionnaire(r.PostForm, errs))
		return
	}
	err := s.opt.Tampon.Ajouter(r.Context(), etat.Empreinte, rep)
	switch {
	case errors.Is(err, tampon.ErrDejaParticipe):
		s.detruireSession(w, r)
		s.rendre(w, r, http.StatusConflict, "deja_participe", s.donnees())
	case err != nil:
		s.opt.Journal.Error("dépôt dans le tampon", "erreur", err)
		s.rendre(w, r, http.StatusServiceUnavailable, "indisponible", s.donnees())
	default:
		s.detruireSession(w, r)
		http.Redirect(w, r, "/merci", http.StatusSeeOther)
	}
}

// vueQuestionnaire prépare l'affichage du questionnaire avec les valeurs du
// formulaire et les erreurs éventuelles.
func (s *Serveur) vueQuestionnaire(form url.Values, errs questionnaire.Erreurs) Donnees {
	d := s.donnees()
	q := s.opt.Questionnaire
	numero := 0
	for _, x := range q.Questions {
		v := VueQuestion{Nom: x.Nom, Cle: x.Cle, Aide: x.Aide, Saisie: "checkbox", Erreur: errs[x.Nom]}
		if x.Type == questionnaire.Unique {
			v.Saisie, v.Requis = "radio", true
		}
		for _, c := range x.Choix {
			code := strconv.Itoa(int(c.Code))
			v.Choix = append(v.Choix, VueChoix{
				ID: x.Nom + "-" + code, Code: c.Code, Cle: c.Cle,
				Coche: slices.Contains(form[x.Nom], code),
			})
		}
		if x.Partie == questionnaire.PartieProfil {
			d.Profil = append(d.Profil, v)
		} else {
			numero++
			v.Numero = numero
			d.Questions = append(d.Questions, v)
		}
	}
	d.CleMajeur = q.CleMajeur
	d.MajeurCoche = form.Get(questionnaire.ChampMajeur) == "1"
	d.MajeurErreur = errs[questionnaire.ChampMajeur]
	d.Erreurs = len(errs) > 0
	return d
}
