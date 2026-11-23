package web

import (
	"errors"
	"net/http"

	"github.com/2guntan/diguee/internal/altcha"
	"github.com/2guntan/diguee/internal/codes"
)

// Code envoyé par WhatsApp, en secours de la vérification inversée (spec
// §5.2, ADR 0004 et 0009) :
//
//	GET  /verifier/code         saisie du numéro, captcha ALTCHA
//	POST /verifier/code         limite par adresse, captcha, numéro contrôlé,
//	                            empreinte, limite par empreinte, envoi du code
//	GET  /verifier/code/saisie  saisie du code
//	POST /verifier/code/saisie  vérification du code
//
// Le parcours SMS (ticket #10) reprendra ces gestionnaires avec son propre
// codes.Verificateur (Options.CodesSMS, à ajouter) et ses chemins.

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

func (s *Serveur) codePage(w http.ResponseWriter, r *http.Request) {
	if etat, ok := s.opt.Sessions.Lire(r); ok && etat.Verifiee {
		http.Redirect(w, r, "/questionnaire", http.StatusSeeOther)
		return
	}
	s.rendreCode(w, r, http.StatusOK, "")
}

// rendreCode affiche la saisie du numéro avec un nouveau défi ALTCHA.
func (s *Serveur) rendreCode(w http.ResponseWriter, r *http.Request, statut int, erreur string) {
	defi, err := s.opt.Altcha.NouveauDefi()
	if err != nil {
		s.opt.Journal.Error("création du défi ALTCHA", "erreur", err)
		s.rendre(w, r, http.StatusServiceUnavailable, "indisponible", s.donnees())
		return
	}
	d := s.donnees()
	d.Defi = &defi
	d.Erreur = erreur
	d.Script = scriptAltcha
	w.Header().Set("Content-Security-Policy", cspCaptcha)
	s.rendre(w, r, statut, "verifier_code", d)
}

// demanderCode traite le numéro saisi. Dans l'ordre : limite par adresse
// (ralentissement, jamais de blocage), captcha, puis codes.Demander
// (format et préfixe, empreinte, limite par empreinte partagée avec la
// vérification inversée, envoi). Le numéro est retiré du formulaire dès sa
// lecture et n'est plus référencé après l'envoi.
func (s *Serveur) demanderCode(w http.ResponseWriter, r *http.Request) {
	s.opt.Ralentisseur.Freiner(r)
	saisie := r.PostForm.Get(champNumero)
	r.PostForm.Del(champNumero)
	r.Form.Del(champNumero)
	if etat, ok := s.opt.Sessions.Lire(r); ok && etat.Verifiee {
		http.Redirect(w, r, "/questionnaire", http.StatusSeeOther)
		return
	}
	switch err := s.opt.Altcha.Verifier(r.PostForm.Get(altcha.Champ)); {
	case err == nil:
	case errors.Is(err, altcha.ErrAbsente):
		http.Redirect(w, r, "/verifier/sans-javascript", http.StatusSeeOther)
		return
	case errors.Is(err, altcha.ErrSaturee):
		s.opt.Journal.Error("captcha ALTCHA : mémoire des solutions pleine")
		s.rendre(w, r, http.StatusServiceUnavailable, "indisponible", s.donnees())
		return
	case errors.Is(err, altcha.ErrExpiree):
		s.rendreCode(w, r, http.StatusBadRequest, "verif.captcha.expire")
		return
	default:
		s.rendreCode(w, r, http.StatusBadRequest, "verif.captcha.invalide")
		return
	}
	id, err := s.opt.Sessions.Ouvrir(w, r)
	if err != nil {
		s.opt.Journal.Error("ouverture de session", "erreur", err)
		s.rendre(w, r, http.StatusServiceUnavailable, "indisponible", s.donnees())
		return
	}
	// Un seul parcours à la fois : le jeton éventuel de la session est révoqué.
	s.opt.Verificateur.Oublier(id)
	statut, err := s.opt.Codes.Demander(r.Context(), id, saisie)
	switch {
	case errors.Is(err, codes.ErrEnvoi):
		s.rendreCode(w, r, http.StatusServiceUnavailable, "code.erreur.envoi")
		return
	case err != nil:
		s.opt.Journal.Error("demande de code", "erreur", err)
		s.rendre(w, r, http.StatusServiceUnavailable, "indisponible", s.donnees())
		return
	case statut != codes.Envoye:
		s.rendreCode(w, r, http.StatusBadRequest, erreursCode[statut])
		return
	}
	http.Redirect(w, r, "/verifier/code/saisie", http.StatusSeeOther)
}

// saisieCode affiche la saisie du code (GET) ou la vérifie (POST).
func (s *Serveur) saisieCode(w http.ResponseWriter, r *http.Request) {
	id, ok := s.opt.Sessions.ID(r)
	if !ok {
		if r.Method == http.MethodGet {
			http.Redirect(w, r, "/verifier/code", http.StatusSeeOther)
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
	if r.Method == http.MethodGet {
		statut, restants := s.opt.Codes.Etat(id)
		if statut == codes.Aucun {
			http.Redirect(w, r, "/verifier/code", http.StatusSeeOther)
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
	statut, restants, err := s.opt.Codes.Valider(r.Context(), id, saisie)
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
