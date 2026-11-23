package equipe

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/2guntan/diguee/internal/observatoire"
)

// cheminSources reçoit le téléversement des copies archivées : seule route
// de l'espace dont le corps peut dépasser 64 Ko.
const cheminSources = "/equipe/observatoire/sources"

// VueObs est ce que reçoivent les pages /equipe/observatoire.
type VueObs struct {
	Valeurs     []observatoire.Valeur
	TailleMax   int64
	Elus        []observatoire.Elu
	Sources     []observatoire.Source
	Engagements []observatoire.Engagement
	Engagement  *observatoire.Engagement
	Statut      *observatoire.Statut
	// Tableau de bord.
	AValider   []observatoire.Engagement // engagements saisis, pas encore validés
	Brouillons []LigneStatut             // brouillons du membre connecté
	Proposes   []LigneStatut             // statuts proposés, à valider ou rejeter
	Valides    []LigneStatut             // statuts validés, à publier
	// Saisie à conserver après une erreur.
	Saisie map[string]string
}

// LigneStatut est un statut avec son engagement, pour le tableau de bord.
type LigneStatut struct {
	Engagement observatoire.Engagement
	Statut     observatoire.Statut
}

// Rôles des pages de l'observatoire : la consultation est ouverte aux
// rédacteurs et aux relecteurs ; chaque action demande son rôle.
var lecteursObs = []Role{Redacteur, Relecteur}

func (e *Espace) monterObservatoire() {
	e.Gerer("GET /equipe/observatoire", e.obsTableau, lecteursObs...)
	e.Gerer("GET /equipe/observatoire/elus", e.obsElus, lecteursObs...)
	e.Gerer("POST /equipe/observatoire/elus", e.obsCreerElu, Redacteur)
	e.Gerer("GET "+cheminSources, e.obsSources, lecteursObs...)
	e.Gerer("POST "+cheminSources, e.obsCreerSource, Redacteur)
	e.Gerer("GET /equipe/observatoire/archives/{fichier}", e.obsArchive, lecteursObs...)
	e.Gerer("GET /equipe/observatoire/engagements", e.obsNouvelEngagement, lecteursObs...)
	e.Gerer("POST /equipe/observatoire/engagements", e.obsCreerEngagement, Redacteur)
	e.Gerer("GET /equipe/observatoire/engagements/{id}", e.obsEngagement, lecteursObs...)
	e.Gerer("POST /equipe/observatoire/engagements/{id}/sources", e.obsLierSource, Redacteur)
	e.Gerer("POST /equipe/observatoire/engagements/{id}/valider", e.obsValiderEngagement, Relecteur)
	e.Gerer("POST /equipe/observatoire/engagements/{id}/statuts", e.obsRedigerStatut, Redacteur)
	e.Gerer("GET /equipe/observatoire/statuts/{id}", e.obsStatut, lecteursObs...)
	e.Gerer("POST /equipe/observatoire/statuts/{id}/preuves", e.obsAjouterPreuve, Redacteur)
	e.Gerer("POST /equipe/observatoire/statuts/{id}/proposer", e.obsProposer, Redacteur)
	e.Gerer("POST /equipe/observatoire/statuts/{id}/valider", e.obsValider, Relecteur)
	e.Gerer("POST /equipe/observatoire/statuts/{id}/rejeter", e.obsRejeter, Relecteur)
	e.Gerer("POST /equipe/observatoire/statuts/{id}/publier", e.obsPublier, Redacteur, Relecteur)
}

func (e *Espace) vueObs() *VueObs {
	return &VueObs{Valeurs: observatoire.Valeurs, TailleMax: e.opt.Observatoire.TailleMax(), Saisie: map[string]string{}}
}

// echecObs affiche l'erreur de saisie (clé du catalogue) sur la page
// donnée, ou une erreur interne.
func (e *Espace) echecObs(w http.ResponseWriter, r *http.Request, quoi string, err error,
	page func(http.ResponseWriter, *http.Request, int, Donnees)) {
	var oe observatoire.Erreur
	if !errors.As(err, &oe) || oe == observatoire.ErrStockage || oe == observatoire.ErrJournalRefuse {
		e.erreurInterne(w, "observatoire : "+quoi, err)
		return
	}
	if oe == observatoire.ErrIntrouvable {
		e.rendre(w, http.StatusNotFound, "introuvable", donnees(r))
		return
	}
	d := donnees(r)
	d.Erreur = string(oe)
	page(w, r, http.StatusBadRequest, d)
}

func idDuChemin(r *http.Request) int64 {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

func idsDuFormulaire(r *http.Request, champ string) []int64 {
	var ids []int64
	for _, v := range r.PostForm[champ] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

// dateDuFormulaire lit une date AAAA-MM-JJ ; zéro si absente ou invalide.
func dateDuFormulaire(r *http.Request, champ string) time.Time {
	t, err := time.Parse(time.DateOnly, r.PostFormValue(champ))
	if err != nil {
		return time.Time{}
	}
	return t
}

// saisie garde les champs envoyés, pour les remettre dans le formulaire.
func saisie(r *http.Request, d *Donnees, champs ...string) {
	if d.Obs == nil {
		d.Obs = &VueObs{Saisie: map[string]string{}}
	}
	for _, c := range champs {
		d.Obs.Saisie[c] = r.PostFormValue(c)
	}
}

// Tableau de bord : ce qui attend une action.
func (e *Espace) obsTableau(w http.ResponseWriter, r *http.Request) {
	es, err := e.opt.Observatoire.Engagements(r.Context())
	if err != nil {
		e.erreurInterne(w, "observatoire : engagements", err)
		return
	}
	d := donnees(r)
	v := e.vueObs()
	v.Engagements = es
	for _, eng := range es {
		if eng.Validation == nil {
			v.AValider = append(v.AValider, eng)
		}
		for _, st := range eng.Statuts {
			l := LigneStatut{Engagement: eng, Statut: st}
			switch st.Etape() {
			case observatoire.Brouillon:
				if st.Redige.Par == d.Membre.ID {
					v.Brouillons = append(v.Brouillons, l)
				}
			case observatoire.Propose:
				v.Proposes = append(v.Proposes, l)
			case observatoire.Valide:
				v.Valides = append(v.Valides, l)
			}
		}
	}
	d.Obs = v
	d.Message = map[string]string{"publie": "equipe.obs.message.publie"}[r.URL.Query().Get("fait")]
	e.rendre(w, http.StatusOK, "obs_tableau", d)
}

// Élus.
func (e *Espace) pageElus(w http.ResponseWriter, r *http.Request, statut int, d Donnees) {
	elus, err := e.opt.Observatoire.Elus(r.Context())
	if err != nil {
		e.erreurInterne(w, "observatoire : élus", err)
		return
	}
	if d.Obs == nil {
		d.Obs = e.vueObs()
	}
	d.Obs.Elus = elus
	e.rendre(w, statut, "obs_elus", d)
}

func (e *Espace) obsElus(w http.ResponseWriter, r *http.Request) {
	d := donnees(r)
	d.Obs = e.vueObs()
	if r.URL.Query().Get("fait") == "elu" {
		d.Message = "equipe.obs.message.elu"
	}
	e.pageElus(w, r, http.StatusOK, d)
}

func (e *Espace) obsCreerElu(w http.ResponseWriter, r *http.Request) {
	m, _ := MembreDe(r.Context())
	_, err := e.opt.Observatoire.SaisirElu(r.Context(), m.ID, observatoire.SaisieElu{
		Nom: r.PostFormValue("nom"), Fonction: r.PostFormValue("fonction"), ContactOfficiel: r.PostFormValue("contact"),
		Mandat: observatoire.Mandat{Debut: dateDuFormulaire(r, "mandat_debut"), Fin: dateDuFormulaire(r, "mandat_fin")},
	})
	if err != nil {
		e.echecObs(w, r, "saisie d'un élu", err, func(w http.ResponseWriter, r *http.Request, s int, d Donnees) {
			d.Obs = e.vueObs()
			saisie(r, &d, "nom", "fonction", "contact", "mandat_debut", "mandat_fin")
			e.pageElus(w, r, s, d)
		})
		return
	}
	e.opt.Journal.Info("observatoire : élu saisi")
	http.Redirect(w, r, "/equipe/observatoire/elus?fait=elu", http.StatusSeeOther)
}

// Sources.
func (e *Espace) pageSources(w http.ResponseWriter, r *http.Request, statut int, d Donnees) {
	ss, err := e.opt.Observatoire.Sources(r.Context())
	if err != nil {
		e.erreurInterne(w, "observatoire : sources", err)
		return
	}
	if d.Obs == nil {
		d.Obs = e.vueObs()
	}
	d.Obs.Sources = ss
	e.rendre(w, statut, "obs_sources", d)
}

func (e *Espace) obsSources(w http.ResponseWriter, r *http.Request) {
	d := donnees(r)
	d.Obs = e.vueObs()
	if r.URL.Query().Get("fait") == "source" {
		d.Message = "equipe.obs.message.source"
	}
	e.pageSources(w, r, http.StatusOK, d)
}

func (e *Espace) obsCreerSource(w http.ResponseWriter, r *http.Request) {
	m, _ := MembreDe(r.Context())
	var contenu []byte
	if f, _, err := r.FormFile("fichier"); err == nil {
		contenu, err = io.ReadAll(io.LimitReader(f, e.opt.Observatoire.TailleMax()+1))
		f.Close()
		if err != nil {
			e.erreurInterne(w, "observatoire : lecture du fichier", err)
			return
		}
	}
	_, err := e.opt.Observatoire.SaisirSource(r.Context(), m.ID, observatoire.SaisieSource{
		URL: r.PostFormValue("url"), Titre: r.PostFormValue("titre"), Date: dateDuFormulaire(r, "date"), Fichier: contenu,
	})
	if err != nil {
		e.echecObs(w, r, "saisie d'une source", err, func(w http.ResponseWriter, r *http.Request, s int, d Donnees) {
			d.Obs = e.vueObs()
			saisie(r, &d, "url", "titre", "date")
			e.pageSources(w, r, s, d)
		})
		return
	}
	e.opt.Journal.Info("observatoire : source archivée")
	http.Redirect(w, r, cheminSources+"?fait=source", http.StatusSeeOther)
}

func (e *Espace) obsArchive(w http.ResponseWriter, r *http.Request) {
	e.opt.ServirArchive(w, r, func() { e.rendre(w, http.StatusNotFound, "introuvable", donnees(r)) })
}

// Engagements.
func (e *Espace) pageNouvelEngagement(w http.ResponseWriter, r *http.Request, statut int, d Donnees) {
	elus, err := e.opt.Observatoire.Elus(r.Context())
	if err != nil {
		e.erreurInterne(w, "observatoire : élus", err)
		return
	}
	ss, err := e.opt.Observatoire.Sources(r.Context())
	if err != nil {
		e.erreurInterne(w, "observatoire : sources", err)
		return
	}
	if d.Obs == nil {
		d.Obs = e.vueObs()
	}
	d.Obs.Elus, d.Obs.Sources = elus, ss
	e.rendre(w, statut, "obs_nouvel_engagement", d)
}

func (e *Espace) obsNouvelEngagement(w http.ResponseWriter, r *http.Request) {
	d := donnees(r)
	d.Obs = e.vueObs()
	e.pageNouvelEngagement(w, r, http.StatusOK, d)
}

func (e *Espace) obsCreerEngagement(w http.ResponseWriter, r *http.Request) {
	m, _ := MembreDe(r.Context())
	elu, _ := strconv.ParseInt(r.PostFormValue("elu"), 10, 64)
	id, err := e.opt.Observatoire.SaisirEngagement(r.Context(), m.ID, observatoire.SaisieEngagement{
		Elu: elu, Citation: r.PostFormValue("citation"), Contexte: r.PostFormValue("contexte"),
		DateDeclaration: dateDuFormulaire(r, "date_declaration"), Echeance: dateDuFormulaire(r, "echeance"),
		Sources: idsDuFormulaire(r, "sources"),
	})
	if err != nil {
		e.echecObs(w, r, "saisie d'un engagement", err, func(w http.ResponseWriter, r *http.Request, s int, d Donnees) {
			d.Obs = e.vueObs()
			saisie(r, &d, "elu", "citation", "contexte", "date_declaration", "echeance")
			e.pageNouvelEngagement(w, r, s, d)
		})
		return
	}
	e.opt.Journal.Info("observatoire : engagement saisi")
	http.Redirect(w, r, "/equipe/observatoire/engagements/"+strconv.FormatInt(id, 10)+"?fait=engagement", http.StatusSeeOther)
}

func (e *Espace) pageEngagement(w http.ResponseWriter, r *http.Request, statut int, d Donnees) {
	eng, err := e.opt.Observatoire.Engagement(r.Context(), idDuChemin(r))
	if errors.Is(err, observatoire.ErrIntrouvable) {
		e.rendre(w, http.StatusNotFound, "introuvable", donnees(r))
		return
	}
	if err != nil {
		e.erreurInterne(w, "observatoire : engagement", err)
		return
	}
	ss, err := e.opt.Observatoire.Sources(r.Context())
	if err != nil {
		e.erreurInterne(w, "observatoire : sources", err)
		return
	}
	if d.Obs == nil {
		d.Obs = e.vueObs()
	}
	d.Obs.Engagement, d.Obs.Sources = eng, ss
	e.rendre(w, statut, "obs_engagement", d)
}

var messagesObs = map[string]string{
	"engagement": "equipe.obs.message.engagement", "source": "equipe.obs.message.source_liee",
	"valide": "equipe.obs.message.valide", "statut": "equipe.obs.message.statut", "preuve": "equipe.obs.message.preuve",
	"propose": "equipe.obs.message.propose", "rejete": "equipe.obs.message.rejete",
}

func (e *Espace) obsEngagement(w http.ResponseWriter, r *http.Request) {
	d := donnees(r)
	d.Obs = e.vueObs()
	d.Message = messagesObs[r.URL.Query().Get("fait")]
	e.pageEngagement(w, r, http.StatusOK, d)
}

// actionEngagement exécute une action sur l'engagement du chemin, puis
// renvoie vers sa page.
func (e *Espace) actionEngagement(quoi, fait string, f func(ctx context.Context, membre string, id int64, r *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m, _ := MembreDe(r.Context())
		id := idDuChemin(r)
		if err := f(r.Context(), m.ID, id, r); err != nil {
			e.echecObs(w, r, quoi, err, e.pageEngagement)
			return
		}
		e.opt.Journal.Info("observatoire : " + quoi)
		http.Redirect(w, r, "/equipe/observatoire/engagements/"+strconv.FormatInt(id, 10)+"?fait="+fait, http.StatusSeeOther)
	}
}

func (e *Espace) obsLierSource(w http.ResponseWriter, r *http.Request) {
	e.actionEngagement("source ajoutée à un engagement", "source", func(ctx context.Context, m string, id int64, r *http.Request) error {
		src, _ := strconv.ParseInt(r.PostFormValue("source"), 10, 64)
		return e.opt.Observatoire.LierSource(ctx, m, id, src)
	})(w, r)
}

func (e *Espace) obsValiderEngagement(w http.ResponseWriter, r *http.Request) {
	e.actionEngagement("engagement validé", "valide", func(ctx context.Context, m string, id int64, _ *http.Request) error {
		return e.opt.Observatoire.ValiderEngagement(ctx, m, id)
	})(w, r)
}

func (e *Espace) obsRedigerStatut(w http.ResponseWriter, r *http.Request) {
	m, _ := MembreDe(r.Context())
	id := idDuChemin(r)
	st, err := e.opt.Observatoire.RedigerStatut(r.Context(), m.ID, id, observatoire.Valeur(r.PostFormValue("valeur")),
		r.PostFormValue("explication"), idsDuFormulaire(r, "preuves"))
	if err != nil {
		e.echecObs(w, r, "rédaction d'un statut", err, func(w http.ResponseWriter, r *http.Request, s int, d Donnees) {
			d.Obs = e.vueObs()
			saisie(r, &d, "valeur", "explication")
			e.pageEngagement(w, r, s, d)
		})
		return
	}
	e.opt.Journal.Info("observatoire : statut rédigé")
	http.Redirect(w, r, "/equipe/observatoire/statuts/"+strconv.FormatInt(st, 10)+"?fait=statut", http.StatusSeeOther)
}

// Statuts.
func (e *Espace) pageStatut(w http.ResponseWriter, r *http.Request, statut int, d Donnees) {
	eng, st, err := e.opt.Observatoire.Statut(r.Context(), idDuChemin(r))
	if errors.Is(err, observatoire.ErrIntrouvable) {
		e.rendre(w, http.StatusNotFound, "introuvable", donnees(r))
		return
	}
	if err != nil {
		e.erreurInterne(w, "observatoire : statut", err)
		return
	}
	ss, err := e.opt.Observatoire.Sources(r.Context())
	if err != nil {
		e.erreurInterne(w, "observatoire : sources", err)
		return
	}
	if d.Obs == nil {
		d.Obs = e.vueObs()
	}
	d.Obs.Engagement, d.Obs.Statut, d.Obs.Sources = eng, st, ss
	e.rendre(w, statut, "obs_statut", d)
}

func (e *Espace) obsStatut(w http.ResponseWriter, r *http.Request) {
	d := donnees(r)
	d.Obs = e.vueObs()
	d.Message = messagesObs[r.URL.Query().Get("fait")]
	e.pageStatut(w, r, http.StatusOK, d)
}

// actionStatut exécute une étape du circuit sur le statut du chemin.
func (e *Espace) actionStatut(quoi, fait string, f func(ctx context.Context, membre string, id int64, r *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m, _ := MembreDe(r.Context())
		id := idDuChemin(r)
		if err := f(r.Context(), m.ID, id, r); err != nil {
			e.echecObs(w, r, quoi, err, e.pageStatut)
			return
		}
		e.opt.Journal.Info("observatoire : " + quoi)
		http.Redirect(w, r, "/equipe/observatoire/statuts/"+strconv.FormatInt(id, 10)+"?fait="+fait, http.StatusSeeOther)
	}
}

func (e *Espace) obsAjouterPreuve(w http.ResponseWriter, r *http.Request) {
	e.actionStatut("preuve ajoutée", "preuve", func(ctx context.Context, m string, id int64, r *http.Request) error {
		src, _ := strconv.ParseInt(r.PostFormValue("source"), 10, 64)
		return e.opt.Observatoire.AjouterPreuve(ctx, m, id, src)
	})(w, r)
}

func (e *Espace) obsProposer(w http.ResponseWriter, r *http.Request) {
	e.actionStatut("statut proposé", "propose", func(ctx context.Context, m string, id int64, _ *http.Request) error {
		return e.opt.Observatoire.Proposer(ctx, m, id)
	})(w, r)
}

func (e *Espace) obsValider(w http.ResponseWriter, r *http.Request) {
	e.actionStatut("statut validé", "valide", func(ctx context.Context, m string, id int64, _ *http.Request) error {
		return e.opt.Observatoire.Valider(ctx, m, id)
	})(w, r)
}

func (e *Espace) obsRejeter(w http.ResponseWriter, r *http.Request) {
	e.actionStatut("statut rejeté", "rejete", func(ctx context.Context, m string, id int64, r *http.Request) error {
		return e.opt.Observatoire.Rejeter(ctx, m, id, r.PostFormValue("motif"))
	})(w, r)
}

func (e *Espace) obsPublier(w http.ResponseWriter, r *http.Request) {
	m, _ := MembreDe(r.Context())
	numero, err := e.opt.Observatoire.Publier(r.Context(), m.ID, idDuChemin(r))
	if err != nil {
		e.echecObs(w, r, "publication d'un statut", err, e.pageStatut)
		return
	}
	e.opt.Journal.Info("observatoire : statut publié et inscrit au journal public", "entree", numero)
	http.Redirect(w, r, "/equipe/observatoire?fait=publie", http.StatusSeeOther)
}
