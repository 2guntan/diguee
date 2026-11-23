package web

import (
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/2guntan/diguee/internal/altcha"
	"github.com/2guntan/diguee/internal/codes"
	"github.com/2guntan/diguee/internal/qr"
	"github.com/2guntan/diguee/internal/whatsapp"
)

// Vérification inversée par WhatsApp (spec §5.1, ADR 0009) :
//
//	GET  /verifier            explication, captcha ALTCHA, bouton « Obtenir mon message »
//	POST /verifier            limite par adresse, captcha, puis ouvre une session
//	                          non vérifiée et tire un jeton
//	GET  /verifier/sans-javascript   explication pour qui n'a pas JavaScript
//	GET  /verifier/sms        code par SMS, en dernier recours (code.go)
//	GET  /verifier/whatsapp   jeton, lien wa.me (mobile), QR code (ordinateur)
//	POST /verifier/whatsapp   bouton « J'ai envoyé le message » (sans JavaScript)
//	GET  /verifier/etat       interrogé toutes les 3 s par /statique/verifier.js
//
// Le webhook (paquet whatsapp) marque la session vérifiée ; la page le
// constate et passe au questionnaire, avec un nouvel identifiant de session.

// Clés des messages de la page du jeton, par statut.
var erreursVerification = map[whatsapp.Statut]string{
	whatsapp.NumeroRefuse:  "verif.erreur.format",
	whatsapp.PrefixeRefuse: "verif.erreur.prefixe",
	whatsapp.Limitee:       "verif.erreur.limite",
	whatsapp.Expiree:       "verif.erreur.expire",
	whatsapp.Verifiee:      "verif.erreur.expire", // session disparue entre-temps
}

func (s *Serveur) verifierPage(w http.ResponseWriter, r *http.Request) {
	if etat, ok := s.opt.Sessions.Lire(r); ok && etat.Verifiee {
		http.Redirect(w, r, "/questionnaire", http.StatusSeeOther)
		return
	}
	s.rendreVerifier(w, r, http.StatusOK, "")
}

// rendreVerifier affiche la page de vérification avec un nouveau défi ALTCHA,
// que le script /statique/altcha.js résout dans le navigateur (spec §4,
// étape 2). erreur est la clé d'un message, ou vide.
func (s *Serveur) rendreVerifier(w http.ResponseWriter, r *http.Request, statut int, erreur string) {
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
	s.rendre(w, r, statut, "verifier", d)
}

// creerJeton ouvre une session non vérifiée (ou reprend la session en cours)
// et lui attribue un nouveau jeton. Avant toute création de jeton :
//
//  1. la demande est comptée par adresse (HMAC à sel quotidien) et, au-delà
//     de DIGUEE_IP_PAR_HEURE dans l'heure, ralentie, sans jamais être
//     bloquée (spec §7) ;
//  2. la solution ALTCHA est vérifiée (signature, expiration, solution,
//     usage unique). Sans solution, la personne n'a probablement pas
//     JavaScript : elle est dirigée vers la page qui l'explique.
func (s *Serveur) creerJeton(w http.ResponseWriter, r *http.Request) {
	s.opt.Ralentisseur.Freiner(r)
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
		s.rendreVerifier(w, r, http.StatusBadRequest, "verif.captcha.expire")
		return
	default:
		s.rendreVerifier(w, r, http.StatusBadRequest, "verif.captcha.invalide")
		return
	}
	id, err := s.opt.Sessions.Ouvrir(w, r)
	if err == nil {
		// Un seul parcours à la fois : le code éventuel de la session est révoqué.
		s.oublierParcours(id)
		_, err = s.opt.Verificateur.Creer(id)
	}
	if errors.Is(err, whatsapp.ErrClose) {
		s.consultationClose(w, r)
		return
	}
	if err != nil {
		s.opt.Journal.Error("création du jeton de vérification", "erreur", err)
		s.rendre(w, r, http.StatusServiceUnavailable, "indisponible", s.donnees())
		return
	}
	http.Redirect(w, r, "/verifier/whatsapp", http.StatusSeeOther)
}

// pageJeton affiche le jeton tant que le message n'est pas reçu, puis suit
// l'issue de la vérification. En POST (« J'ai envoyé le message »), elle
// dit en plus que le message n'est pas encore arrivé.
func (s *Serveur) pageJeton(w http.ResponseWriter, r *http.Request) {
	id, ok := s.opt.Sessions.ID(r)
	if !ok {
		if r.Method == http.MethodGet {
			http.Redirect(w, r, "/verifier", http.StatusSeeOther)
			return
		}
		s.rendre(w, r, http.StatusUnauthorized, "session_expiree", s.donnees())
		return
	}
	if etat, _ := s.opt.Sessions.Lire(r); etat.Verifiee {
		// La session devient vérifiée hors de la requête du navigateur : on
		// change son identifiant avant de continuer.
		if err := s.opt.Sessions.Renouveler(w, r); err != nil {
			s.rendre(w, r, http.StatusUnauthorized, "session_expiree", s.donnees())
			return
		}
		s.opt.Verificateur.Oublier(id)
		http.Redirect(w, r, "/questionnaire", http.StatusSeeOther)
		return
	}
	jeton, statut := s.opt.Verificateur.Etat(id)
	d := s.donnees()
	switch statut {
	case whatsapp.Aucun:
		http.Redirect(w, r, "/verifier", http.StatusSeeOther)
		return
	case whatsapp.DejaParticipe:
		// Rien d'autre n'est révélé (spec §4, étape 3).
		s.detruireSession(w, r)
		s.rendre(w, r, http.StatusConflict, "deja_participe", d)
		return
	case whatsapp.Attente:
		d.Jeton = jeton.Texte
		d.LienWaMe = s.lienWaMe(jeton.Texte)
		if code, err := qr.Encoder([]byte(d.LienWaMe)); err == nil {
			// Le SVG ne contient que des nombres et le libellé échappé.
			d.QR = template.HTML(code.SVG(s.langue(r).T("verif.whatsapp.qr.libelle")))
		}
		d.Script = scriptVerifier
		w.Header().Set("Content-Security-Policy", cspAvecScript)
		if r.Method == http.MethodPost {
			d.Erreur = "verif.whatsapp.pas_encore"
		}
	default:
		d.Erreur = erreursVerification[statut]
	}
	s.rendre(w, r, http.StatusOK, "verifier_whatsapp", d)
}

// etatJeton répond à l'interrogation de la page : « attente » tant que le
// message n'est pas reçu, « fin » sinon (la page se recharge alors). Rien
// d'autre n'est révélé.
func (s *Serveur) etatJeton(w http.ResponseWriter, r *http.Request) {
	etat := "fin"
	if id, ok := s.opt.Sessions.ID(r); ok {
		if e, _ := s.opt.Sessions.Lire(r); !e.Verifiee {
			if _, statut := s.opt.Verificateur.Etat(id); statut == whatsapp.Attente {
				etat = "attente"
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]string{"etat": etat})
}

// lienWaMe renvoie le lien qui ouvre WhatsApp avec le message prérempli.
func (s *Serveur) lienWaMe(jeton string) string {
	numero, _ := s.opt.Config.NumeroWhatsAppChiffres() // vide tant qu'il est « À COMPLÉTER »
	return "https://wa.me/" + numero + "?text=" + url.PathEscape(jeton)
}

// detruireSession détruit la session et oublie son jeton ou son code.
func (s *Serveur) detruireSession(w http.ResponseWriter, r *http.Request) {
	if id, ok := s.opt.Sessions.ID(r); ok {
		s.oublierParcours(id)
	}
	s.opt.Sessions.Detruire(w, r)
}

// devWhatsApp est la page de développement qui joue le rôle du citoyen et de
// Meta : elle fabrique la notification d'un message WhatsApp, la signe avec
// le secret de développement et la remet au vrai webhook. Elle n'existe
// qu'avec DIGUEE_WHATSAPP=fake, donc jamais en production. Le numéro saisi
// n'est ni journalisé ni réaffiché.
func (s *Serveur) devWhatsApp(w http.ResponseWriter, r *http.Request) {
	d := s.donnees()
	if id, ok := s.opt.Sessions.ID(r); ok {
		if j, statut := s.opt.Verificateur.Etat(id); statut == whatsapp.Attente {
			d.Jeton = j.Texte
		}
	}
	// Mode « fake » seulement : le dernier code écrit dans la console.
	if c, ok := s.opt.WhatsApp.Envoyeur.(*codes.Console); ok {
		d.CodeDev = c.Dernier()
	}
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, tailleMaxFormulaire)
		if err := r.ParseForm(); err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		// Meta transmet l'expéditeur en chiffres, indicatif compris, sans « + ».
		expediteur := strings.Map(func(c rune) rune {
			if c >= '0' && c <= '9' {
				return c
			}
			return -1
		}, r.PostForm.Get("numero"))
		statut, err := s.opt.WhatsApp.Simuler(s, expediteur, r.PostForm.Get("texte"))
		if err != nil {
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
			return
		}
		d.StatutDev = statut
	}
	s.rendre(w, r, http.StatusOK, "dev_whatsapp", d)
}
