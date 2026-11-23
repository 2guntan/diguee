package equipe

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/2guntan/diguee/internal/ancrage"
	"github.com/2guntan/diguee/internal/antifraude"
	"github.com/2guntan/diguee/internal/i18n"
	"github.com/2guntan/diguee/internal/observatoire"
)

//go:embed gabarits/*.html
var fichiersGabarits embed.FS

var pagesEquipe = []string{"connexion", "accueil", "membres", "acces", "audit", "interdit", "introuvable", "sms", "ancrage",
	"obs_tableau", "obs_elus", "obs_sources", "obs_nouvel_engagement", "obs_engagement", "obs_statut"}

// nomCookieConnexion porte le jeton CSRF du formulaire de connexion, avant
// toute session (protection contre la connexion forcée).
const nomCookieConnexion = "diguee_equipe_connexion"

// LimiteAudit est le nombre de lignes d'audit affichées.
const LimiteAudit = 200

// OptionsEspace regroupe les dépendances de l'espace équipe.
type OptionsEspace struct {
	Service    *Service
	Sessions   *Sessions
	Magasin    Magasin
	Catalogues *i18n.Catalogues
	Journal    *slog.Logger
	// CoupeCircuitSMS sert /equipe/sms (administrateurs) s'il est fourni :
	// état du coupe-circuit du parcours SMS et réactivation manuelle.
	CoupeCircuitSMS CoupeCircuit
	// Ancrage sert /equipe/ancrage (administrateurs) s'il est fourni : état
	// de l'ancrage extérieur du journal public et texte hebdomadaire prêt à
	// publier sur X (ticket #12). La plateforme ne publie jamais elle-même.
	Ancrage TableauAncrage
	// Observatoire sert /equipe/observatoire (rédacteurs et relecteurs)
	// s'il est fourni : saisie des élus, des sources et des engagements,
	// circuit des statuts (ticket #13).
	Observatoire *observatoire.Service
	// ServirArchive sert une copie archivée (web.ServirArchive), pour que les
	// relecteurs relisent les fichiers avant validation.
	ServirArchive func(w http.ResponseWriter, r *http.Request, introuvable func())
}

// TableauAncrage donne l'état des ancrages. Implémenté par ancrage.Ancreur.
type TableauAncrage interface {
	Tableau(ctx context.Context) (*ancrage.Tableau, error)
}

// Espace sert /equipe. Il est séparé des pages publiques : ses propres
// routes, gabarits, cookies (limités à /equipe) et contrôles d'accès.
type Espace struct {
	opt      OptionsEspace
	gabarits map[string]*template.Template
	mux      *http.ServeMux
}

type cleContexte int

const (
	cleMembre cleContexte = iota
	cleCSRF
)

// MembreDe renvoie le membre connecté, dans un gestionnaire enregistré avec
// Espace.Gerer. Ses rôles ont été relus en base pour cette requête.
func MembreDe(ctx context.Context) (Membre, bool) {
	m, ok := ctx.Value(cleMembre).(Membre)
	return m, ok
}

// JetonCSRFDe renvoie le jeton CSRF de la session, à placer dans le champ
// caché « csrf » de chaque formulaire POST de l'espace.
func JetonCSRFDe(ctx context.Context) string {
	s, _ := ctx.Value(cleCSRF).(string)
	return s
}

// NouvelEspace prépare les gabarits et les routes de l'espace équipe.
func NouvelEspace(o OptionsEspace) (*Espace, error) {
	if o.Journal == nil {
		o.Journal = slog.Default()
	}
	e := &Espace{opt: o, gabarits: map[string]*template.Template{}, mux: http.NewServeMux()}
	ref := o.Catalogues.Choisir(i18n.Reference)
	for _, p := range pagesEquipe {
		fichiers := []string{"gabarits/base.html", "gabarits/" + p + ".html"}
		if strings.HasPrefix(p, "obs_") {
			fichiers = append(fichiers, "gabarits/obs_commun.html")
		}
		g, err := template.New(p).Funcs(fonctions(ref)).ParseFS(fichiersGabarits, fichiers...)
		if err != nil {
			return nil, fmt.Errorf("gabarit équipe %s : %w", p, err)
		}
		e.gabarits[p] = g
	}
	e.mux.HandleFunc("GET /equipe", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/equipe/", http.StatusSeeOther)
	})
	e.mux.HandleFunc("GET /equipe/connexion", e.connexionPage)
	e.mux.HandleFunc("POST /equipe/connexion", e.connexion)
	e.Gerer("POST /equipe/deconnexion", e.deconnexion)
	e.Gerer("GET /equipe/{$}", e.accueil)
	e.Gerer("GET /equipe/membres", e.membres, Administrateur)
	e.Gerer("POST /equipe/membres", e.creer, Administrateur)
	e.Gerer("POST /equipe/membres/{id}/roles", e.roles, Administrateur)
	e.Gerer("POST /equipe/membres/{id}/desactiver", e.desactiver, Administrateur)
	e.Gerer("POST /equipe/membres/{id}/reinitialiser", e.reinitialiser, Administrateur)
	e.Gerer("GET /equipe/audit", e.audit, Administrateur)
	if o.CoupeCircuitSMS != nil {
		e.Gerer("GET /equipe/sms", e.pageSMS, Administrateur)
		e.Gerer("POST /equipe/sms/reactiver", e.reactiverSMS, Administrateur)
	}
	if o.Ancrage != nil {
		e.Gerer("GET /equipe/ancrage", e.pageAncrage, Administrateur)
	}
	if o.Observatoire != nil {
		e.monterObservatoire()
	}
	e.mux.HandleFunc("/equipe/", func(w http.ResponseWriter, r *http.Request) {
		e.rendre(w, http.StatusNotFound, "introuvable", Donnees{})
	})
	return e, nil
}

// ServeHTTP sert l'espace équipe.
func (e *Espace) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	limite := int64(64 << 10)
	if e.opt.Observatoire != nil && r.Method == http.MethodPost && r.URL.Path == cheminSources {
		// Téléversement d'une copie archivée : la taille maximale du
		// fichier, plus les autres champs.
		limite = e.opt.Observatoire.TailleMax() + 64<<10
	}
	r.Body = http.MaxBytesReader(w, r.Body, limite)
	e.mux.ServeHTTP(w, r)
}

// Gerer enregistre un gestionnaire de l'espace (motif de http.ServeMux, par
// exemple « GET /equipe/engagements »). Il n'est appelé que si :
//   - la requête porte une session ouverte de moins de 8 heures ;
//   - le compte est toujours actif (relu en base à chaque requête) ;
//   - le membre a au moins un des rôles donnés (sans rôle : tout membre) ;
//   - pour toute méthode autre que GET et HEAD, la requête vient du site et
//     porte le jeton CSRF de la session dans le champ « csrf ».
//
// Sinon : redirection vers la connexion, ou 403.
func (e *Espace) Gerer(motif string, h http.HandlerFunc, roles ...Role) {
	e.mux.HandleFunc(motif, func(w http.ResponseWriter, r *http.Request) {
		id, csrf, ok := e.opt.Sessions.Lire(r)
		if !ok {
			http.Redirect(w, r, "/equipe/connexion", http.StatusSeeOther)
			return
		}
		m, err := e.opt.Magasin.MembreParID(r.Context(), id)
		if errors.Is(err, ErrInconnu) || (err == nil && !m.Actif) {
			e.opt.Sessions.FermerMembre(id)
			e.opt.Sessions.Fermer(w, r)
			http.Redirect(w, r, "/equipe/connexion", http.StatusSeeOther)
			return
		}
		if err != nil {
			e.erreurInterne(w, "lecture du membre", err)
			return
		}
		d := Donnees{Membre: &m, CSRF: csrf}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if err := lireFormulaire(r); err != nil {
				var trop *http.MaxBytesError
				if errors.As(err, &trop) {
					d.Erreur = "equipe.erreur.trop_gros"
					e.rendre(w, http.StatusRequestEntityTooLarge, "interdit", d)
					return
				}
			}
			if !origineSure(r) || !egal(r.PostFormValue("csrf"), csrf) {
				d.Erreur = "equipe.erreur.csrf"
				e.rendre(w, http.StatusForbidden, "interdit", d)
				return
			}
		}
		if !m.AUnDe(roles...) {
			d.Erreur = "equipe.erreur.role"
			e.rendre(w, http.StatusForbidden, "interdit", d)
			return
		}
		ctx := context.WithValue(context.WithValue(r.Context(), cleMembre, m), cleCSRF, csrf)
		h(w, r.WithContext(ctx))
	})
}

// lireFormulaire lit le corps d'un formulaire, ordinaire ou multipart (le
// téléversement d'une source), en mémoire : le corps est déjà borné par
// ServeHTTP, aucun fichier temporaire n'est écrit.
func lireFormulaire(r *http.Request) error {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		return r.ParseMultipartForm(1 << 40)
	}
	return r.ParseForm()
}

// origineSure refuse une requête envoyée depuis un autre site, d'après les
// en-têtes que posent les navigateurs (Sec-Fetch-Site, Origin).
func origineSure(r *http.Request) bool {
	if s := r.Header.Get("Sec-Fetch-Site"); s != "" && s != "same-origin" && s != "none" {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || u.Host != r.Host {
			return false
		}
	}
	return true
}

// Donnees est ce que reçoit chaque gabarit de l'espace.
type Donnees struct {
	Membre      *Membre // membre connecté
	CSRF        string
	Erreur      string // clé d'un message d'erreur
	Message     string // clé d'un message de confirmation
	Identifiant string // saisie à conserver dans le formulaire de connexion

	Roles   []Role // tous les rôles, pour les formulaires
	Membres []Membre
	Audit   []Action
	Acces   *Acces
	Cible   *Membre

	AvecSMS bool                         // le parcours SMS est servi : lien vers /equipe/sms
	SMS     *antifraude.EtatCoupeCircuit // page /equipe/sms

	AvecAncrage bool             // l'ancrage est servi : lien vers /equipe/ancrage
	Ancrage     *ancrage.Tableau // page /equipe/ancrage

	AvecObservatoire bool    // l'observatoire est servi : lien vers /equipe/observatoire
	Obs              *VueObs // pages /equipe/observatoire
}

func (e *Espace) rendre(w http.ResponseWriter, statut int, page string, d Donnees) {
	d.Roles = Roles
	d.AvecSMS = e.opt.CoupeCircuitSMS != nil
	d.AvecAncrage = e.opt.Ancrage != nil
	d.AvecObservatoire = e.opt.Observatoire != nil
	var buf bytes.Buffer
	if err := e.gabarits[page].ExecuteTemplate(&buf, "base", d); err != nil {
		e.erreurInterne(w, "rendu de page équipe", err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(statut)
	_, _ = w.Write(buf.Bytes())
}

func (e *Espace) erreurInterne(w http.ResponseWriter, quoi string, err error) {
	e.opt.Journal.Error("équipe : "+quoi, "erreur", err)
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}

func (e *Espace) connexionPage(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := e.opt.Sessions.Lire(r); ok {
		http.Redirect(w, r, "/equipe/", http.StatusSeeOther)
		return
	}
	e.formulaireConnexion(w, http.StatusOK, Donnees{})
}

// formulaireConnexion pose un jeton CSRF neuf (cookie et champ caché).
func (e *Espace) formulaireConnexion(w http.ResponseWriter, statut int, d Donnees) {
	jeton, err := aleatoire()
	if err != nil {
		e.erreurInterne(w, "jeton de connexion", err)
		return
	}
	http.SetCookie(w, cookieEquipe(nomCookieConnexion, jeton, 3600))
	d.CSRF = jeton
	e.rendre(w, statut, "connexion", d)
}

func (e *Espace) connexion(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(nomCookieConnexion)
	if !origineSure(r) || err != nil || !egal(r.PostFormValue("csrf"), c.Value) {
		e.formulaireConnexion(w, http.StatusForbidden, Donnees{Erreur: "equipe.erreur.csrf"})
		return
	}
	identifiant := r.PostFormValue("identifiant")
	m, err := e.opt.Service.Connecter(r.Context(), identifiant, r.PostFormValue("mot_de_passe"),
		r.PostFormValue("code"), r.RemoteAddr)
	switch {
	case errors.Is(err, ErrTropDEssais):
		e.opt.Journal.Warn("équipe : connexion bloquée après trop d'échecs")
		e.formulaireConnexion(w, http.StatusTooManyRequests, Donnees{Erreur: "equipe.connexion.trop", Identifiant: identifiant})
		return
	case errors.Is(err, ErrConnexion):
		e.opt.Journal.Info("équipe : connexion refusée")
		e.formulaireConnexion(w, http.StatusUnauthorized, Donnees{Erreur: "equipe.connexion.refusee", Identifiant: identifiant})
		return
	case err != nil:
		e.erreurInterne(w, "connexion", err)
		return
	}
	http.SetCookie(w, cookieEquipe(nomCookieConnexion, "", -1))
	if err := e.opt.Sessions.Ouvrir(w, r, m.ID); err != nil {
		e.erreurInterne(w, "ouverture de session", err)
		return
	}
	e.opt.Journal.Info("équipe : connexion")
	http.Redirect(w, r, "/equipe/", http.StatusSeeOther)
}

func (e *Espace) deconnexion(w http.ResponseWriter, r *http.Request) {
	e.opt.Sessions.Fermer(w, r)
	http.Redirect(w, r, "/equipe/connexion", http.StatusSeeOther)
}

// donnees prépare les données d'une page servie par Gerer.
func donnees(r *http.Request) Donnees {
	m, _ := MembreDe(r.Context())
	return Donnees{Membre: &m, CSRF: JetonCSRFDe(r.Context())}
}

func (e *Espace) accueil(w http.ResponseWriter, r *http.Request) {
	e.rendre(w, http.StatusOK, "accueil", donnees(r))
}

func (e *Espace) pageMembres(w http.ResponseWriter, r *http.Request, statut int, d Donnees) {
	ms, err := e.opt.Magasin.ListerMembres(r.Context())
	if err != nil {
		e.erreurInterne(w, "liste des membres", err)
		return
	}
	d.Membres = ms
	e.rendre(w, statut, "membres", d)
}

func (e *Espace) membres(w http.ResponseWriter, r *http.Request) {
	d := donnees(r)
	d.Message = map[string]string{
		"roles": "equipe.membres.roles_changes", "desactive": "equipe.membres.desactive",
	}[r.URL.Query().Get("fait")]
	e.pageMembres(w, r, http.StatusOK, d)
}

func rolesDuFormulaire(r *http.Request) []Role {
	var rs []Role
	for _, v := range r.PostForm["roles"] {
		rs = append(rs, Role(v))
	}
	return rs
}

// cleErreur traduit une erreur du service en clé du catalogue ; "" pour une
// erreur technique.
func cleErreur(err error) string {
	for e, cle := range map[error]string{
		ErrIdentifiant: "equipe.erreur.identifiant", ErrNom: "equipe.erreur.nom", ErrRoles: "equipe.erreur.roles",
		ErrIdentifiantPris: "equipe.erreur.identifiant_pris", ErrDernierAdministrateur: "equipe.erreur.dernier_admin",
		ErrSoiMeme: "equipe.erreur.soi_meme", ErrInconnu: "equipe.erreur.inconnu", ErrInactif: "equipe.erreur.inactif",
		ErrNonAdministrateur: "equipe.erreur.role",
	} {
		if errors.Is(err, e) {
			return cle
		}
	}
	return ""
}

// echecAdmin affiche la page des membres avec l'erreur, ou une erreur interne.
func (e *Espace) echecAdmin(w http.ResponseWriter, r *http.Request, quoi string, err error) {
	cle := cleErreur(err)
	if cle == "" {
		e.erreurInterne(w, quoi, err)
		return
	}
	d := donnees(r)
	d.Erreur = cle
	e.pageMembres(w, r, http.StatusBadRequest, d)
}

func (e *Espace) creer(w http.ResponseWriter, r *http.Request) {
	acteur, _ := MembreDe(r.Context())
	m, acces, err := e.opt.Service.Creer(r.Context(), &acteur, r.PostFormValue("identifiant"),
		r.PostFormValue("nom"), rolesDuFormulaire(r))
	if err != nil {
		e.echecAdmin(w, r, "création de compte", err)
		return
	}
	e.opt.Journal.Info("équipe : compte créé")
	d := donnees(r)
	d.Acces, d.Cible, d.Message = &acces, &m, "equipe.acces.cree"
	e.rendre(w, http.StatusOK, "acces", d)
}

func (e *Espace) roles(w http.ResponseWriter, r *http.Request) {
	acteur, _ := MembreDe(r.Context())
	if err := e.opt.Service.ChangerRoles(r.Context(), acteur, r.PathValue("id"), rolesDuFormulaire(r)); err != nil {
		e.echecAdmin(w, r, "changement de rôles", err)
		return
	}
	e.opt.Journal.Info("équipe : rôles changés")
	http.Redirect(w, r, "/equipe/membres?fait=roles", http.StatusSeeOther)
}

func (e *Espace) desactiver(w http.ResponseWriter, r *http.Request) {
	acteur, _ := MembreDe(r.Context())
	if err := e.opt.Service.Desactiver(r.Context(), acteur, r.PathValue("id")); err != nil {
		e.echecAdmin(w, r, "désactivation", err)
		return
	}
	e.opt.Journal.Info("équipe : compte désactivé")
	http.Redirect(w, r, "/equipe/membres?fait=desactive", http.StatusSeeOther)
}

func (e *Espace) reinitialiser(w http.ResponseWriter, r *http.Request) {
	acteur, _ := MembreDe(r.Context())
	id := r.PathValue("id")
	acces, err := e.opt.Service.Reinitialiser(r.Context(), acteur, id)
	if err != nil {
		e.echecAdmin(w, r, "réinitialisation", err)
		return
	}
	m, err := e.opt.Magasin.MembreParID(r.Context(), id)
	if err != nil {
		e.erreurInterne(w, "lecture du membre", err)
		return
	}
	e.opt.Journal.Info("équipe : accès réinitialisé")
	d := donnees(r)
	d.Acces, d.Cible, d.Message = &acces, &m, "equipe.acces.reinitialise"
	e.rendre(w, http.StatusOK, "acces", d)
}

func (e *Espace) audit(w http.ResponseWriter, r *http.Request) {
	as, err := e.opt.Magasin.ListerAudit(r.Context(), LimiteAudit)
	if err != nil {
		e.erreurInterne(w, "audit", err)
		return
	}
	d := donnees(r)
	d.Audit = as
	e.rendre(w, http.StatusOK, "audit", d)
}

// pageSMS montre l'état du coupe-circuit du parcours SMS.
func (e *Espace) pageSMS(w http.ResponseWriter, r *http.Request) {
	d := donnees(r)
	etat := e.opt.CoupeCircuitSMS.Etat()
	d.SMS = &etat
	d.Message = map[string]string{
		"reactive": "equipe.sms.reactive", "actif": "equipe.sms.deja_actif",
	}[r.URL.Query().Get("fait")]
	e.rendre(w, http.StatusOK, "sms", d)
}

// reactiverSMS réactive le parcours SMS suspendu, avec une ligne d'audit.
func (e *Espace) reactiverSMS(w http.ResponseWriter, r *http.Request) {
	acteur, _ := MembreDe(r.Context())
	fait, err := e.opt.Service.ReactiverSMS(r.Context(), acteur, e.opt.CoupeCircuitSMS)
	if err != nil {
		e.erreurInterne(w, "réactivation du parcours SMS", err)
		return
	}
	if !fait {
		http.Redirect(w, r, "/equipe/sms?fait=actif", http.StatusSeeOther)
		return
	}
	e.opt.Journal.Warn("équipe : parcours SMS réactivé à la main par un administrateur")
	http.Redirect(w, r, "/equipe/sms?fait=reactive", http.StatusSeeOther)
}

// pageAncrage montre l'état de l'ancrage du journal public et les textes
// hebdomadaires à publier sur X, à copier par le porteur.
func (e *Espace) pageAncrage(w http.ResponseWriter, r *http.Request) {
	t, err := e.opt.Ancrage.Tableau(r.Context())
	if err != nil {
		e.erreurInterne(w, "état de l'ancrage", err)
		return
	}
	d := donnees(r)
	d.Ancrage = t
	e.rendre(w, http.StatusOK, "ancrage", d)
}

func fonctions(c *i18n.Catalogue) template.FuncMap {
	return template.FuncMap{
		"langue": func() string { return c.Code },
		"t":      c.T,
		"tf":     c.F,
		"jour":   func(t time.Time) string { return c.Date(t) },
		"dateheure": func(t time.Time) string {
			t = t.UTC()
			return c.F("format.date_heure", c.Date(t), c.Heure(time.Duration(t.Hour())*time.Hour+time.Duration(t.Minute())*time.Minute))
		},
		"duree":  c.Duree,
		"nombre": func(n int64) string { return c.Nombre(n) },
		"entier": func(n int) string { return c.Nombre(int64(n)) },
		"role":   func(r Role) string { return c.T("equipe.role." + string(r)) },
		"action": func(a TypeAction) string { return c.T("equipe.action." + string(a)) },
		"a":      func(m Membre, r Role) bool { return m.A(r) },
		"valeur": func(v observatoire.Valeur) string { return c.T("obs.valeur." + string(v)) },
		"etape":  func(x observatoire.Etape) string { return c.T("obs.etape." + string(x)) },
		"taille": func(n int64) string { return c.F("format.ko", c.Nombre((n+1023)/1024)) },
		"mo":     func(n int64) string { return c.Nombre(n >> 20) },
	}
}
