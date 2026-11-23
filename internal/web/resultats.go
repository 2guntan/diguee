package web

import (
	"bytes"
	"context"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/2guntan/diguee/internal/i18n"
	"github.com/2guntan/diguee/internal/publication"
	"github.com/2guntan/diguee/internal/questionnaire"
)

// Publications donne les publications quotidiennes (spec §8), enregistrées
// par le paquet publication. Implémenté par base.Base.
type Publications interface {
	// DernierResume renvoie le résumé de la dernière publication, ou nil.
	DernierResume(ctx context.Context) (*publication.Resume, error)
	// LirePublication renvoie la publication du jour, ou nil.
	LirePublication(ctx context.Context, jour time.Time) (*publication.Publication, error)
	// Resumes renvoie toutes les publications, les plus récentes d'abord.
	Resumes(ctx context.Context) ([]publication.Resume, error)
}

// VueResultats est ce qu'affichent les pages /resultats.
type VueResultats struct {
	Aucune    bool          // aucune publication pour l'instant
	Jour      time.Time     // jour de la publication affichée
	Derniere  bool          // c'est la dernière publication
	Fragment  template.HTML // l'instantané mis en page, figé à la publication
	LienJSON  string
	Empreinte string // SHA-256 du JSON, inscrit au journal public

	Publications []VuePublication // page des publications passées
}

// VuePublication est une ligne de la liste des publications.
type VuePublication struct {
	Jour      time.Time
	Lien      string
	Reponses  int
	Resultats bool
}

func cheminPublication(j time.Time) string { return "/resultats/" + j.Format(time.DateOnly) }

func (s *Serveur) resultatsDerniers(w http.ResponseWriter, r *http.Request) {
	d := s.donnees()
	res, err := s.opt.Publications.DernierResume(r.Context())
	if err != nil {
		s.indisponible(w, r, "lecture de la dernière publication", err)
		return
	}
	if res == nil {
		d.Resultats = &VueResultats{Aucune: true}
		s.rendre(w, r, http.StatusOK, "resultats", d)
		return
	}
	s.resultatsDu(w, r, res.Jour, true)
}

// resultatsJour sert /resultats/AAAA-MM-JJ (page) et /resultats/AAAA-MM-JJ.json.
func (s *Serveur) resultatsJour(w http.ResponseWriter, r *http.Request) {
	v := r.PathValue("jour")
	enJSON := strings.HasSuffix(v, ".json")
	j, err := time.ParseInLocation(time.DateOnly, strings.TrimSuffix(v, ".json"), time.UTC)
	if err != nil {
		s.introuvable(w, r)
		return
	}
	if enJSON {
		s.exportJSON(w, r, j)
		return
	}
	res, err := s.opt.Publications.DernierResume(r.Context())
	if err != nil {
		s.indisponible(w, r, "lecture de la dernière publication", err)
		return
	}
	s.resultatsDu(w, r, j, res != nil && res.Jour.Equal(j))
}

func (s *Serveur) resultatsDu(w http.ResponseWriter, r *http.Request, j time.Time, derniere bool) {
	d := s.donnees()
	p, err := s.opt.Publications.LirePublication(r.Context(), j)
	if err != nil {
		s.indisponible(w, r, "lecture d'une publication", err)
		return
	}
	if p == nil {
		s.introuvable(w, r)
		return
	}
	d.Resultats = &VueResultats{
		Jour: p.Jour, Derniere: derniere,
		// Fragment produit par RendreInstantane à la publication, enregistré
		// tel quel et jamais modifié.
		Fragment: template.HTML(p.HTML),
		LienJSON: cheminPublication(p.Jour) + ".json", Empreinte: p.Empreinte.String(),
	}
	s.rendre(w, r, http.StatusOK, "resultats", d)
}

func (s *Serveur) resultatsJSON(w http.ResponseWriter, r *http.Request) {
	res, err := s.opt.Publications.DernierResume(r.Context())
	if err != nil {
		s.indisponible(w, r, "lecture de la dernière publication", err)
		return
	}
	if res == nil {
		s.introuvable(w, r)
		return
	}
	s.exportJSON(w, r, res.Jour)
}

// exportJSON sert l'instantané octet pour octet : son SHA-256 est celui
// inscrit au journal public.
func (s *Serveur) exportJSON(w http.ResponseWriter, r *http.Request, j time.Time) {
	p, err := s.opt.Publications.LirePublication(r.Context(), j)
	if err != nil {
		s.opt.Journal.Error("export d'une publication", "erreur", err)
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	if p == nil {
		s.introuvable(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Content-Disposition", `inline; filename="resultats-`+p.Jour.Format(time.DateOnly)+`.json"`)
	_, _ = w.Write(p.JSON)
}

func (s *Serveur) resultatsPublications(w http.ResponseWriter, r *http.Request) {
	d := s.donnees()
	rs, err := s.opt.Publications.Resumes(r.Context())
	if err != nil {
		s.indisponible(w, r, "lecture des publications", err)
		return
	}
	v := &VueResultats{Aucune: len(rs) == 0}
	for _, x := range rs {
		v.Publications = append(v.Publications, VuePublication{Jour: x.Jour, Lien: cheminPublication(x.Jour),
			Reponses: x.Reponses, Resultats: x.ResultatsPublies})
	}
	d.Resultats = v
	s.rendre(w, r, http.StatusOK, "resultats_publications", d)
}

func (s *Serveur) indisponible(w http.ResponseWriter, r *http.Request, quoi string, err error) {
	s.opt.Journal.Error(quoi, "erreur", err)
	s.rendre(w, r, http.StatusServiceUnavailable, "indisponible", s.donnees())
}

// VueInstantane est l'instantané tel qu'il est mis en page.
type VueInstantane struct {
	Jour, DernierJour time.Time
	Reponses          int
	Resultats         bool
	SeuilPublication  int
	SeuilCase         int
	AutresRegions     []string // noms des régions regroupées
	SourcePopulation  string
	Composition       []VueComposition
	Questions         []VueQuestionResultats
	Profil            []VueTableau // critères croisés deux à deux
}

// VueComposition est la composition de l'échantillon pour un critère.
type VueComposition struct {
	Titre  string
	Lignes []VueLigneComposition
}

// VueLigneComposition est une catégorie de la composition.
type VueLigneComposition struct {
	Libelle, Echantillon, Population string
}

// VueQuestionResultats regroupe les tableaux d'une question.
type VueQuestionResultats struct {
	Libelle     string
	Ensemble    VueTableau
	Croisements []VueTableau
}

// VueTableau est un tableau prêt à afficher.
type VueTableau struct {
	Titre    string
	EnTete   string // libellé de la colonne des rangées
	Multiple bool
	Colonnes []string
	Rangees  []VueRangee
}

// VueRangee est une rangée prête à afficher.
type VueRangee struct {
	Libelle  string
	Cases    []string
	Effectif string
}

// RendreInstantane renvoie la mise en page des instantanés, en français (la
// langue de référence), utilisée au moment de la publication : le fragment
// HTML est enregistré avec le JSON et ne change plus ensuite.
func RendreInstantane(cs *i18n.Catalogues, q *questionnaire.Questionnaire) (publication.Rendu, error) {
	c := cs.Choisir(i18n.Reference)
	g, err := template.New("instantane").Funcs(fonctions(c)).ParseFS(fichiersGabarits, "gabarits/instantane.html")
	if err != nil {
		return nil, err
	}
	return func(inst *publication.Instantane) ([]byte, error) {
		v, err := vueInstantane(c, q, inst)
		if err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		if err := g.ExecuteTemplate(&buf, "instantane", v); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}, nil
}

func vueInstantane(c *i18n.Catalogue, q *questionnaire.Questionnaire, inst *publication.Instantane) (*VueInstantane, error) {
	jour, err := time.ParseInLocation(time.DateOnly, inst.Jour, time.UTC)
	if err != nil {
		return nil, err
	}
	v := &VueInstantane{
		Jour: jour, DernierJour: jour.AddDate(0, 0, -1), Reponses: inst.Reponses, Resultats: inst.ResultatsPublies,
		SeuilPublication: inst.SeuilPublication, SeuilCase: inst.SeuilCase, SourcePopulation: inst.SourcePopulation,
	}
	for _, code := range inst.AutresRegions {
		v.AutresRegions = append(v.AutresRegions, libelleOption(c, q, "region", code))
	}
	masque := c.F("resultats.masque.seuil", c.Nombre(int64(inst.SeuilCase)))
	for _, t := range inst.Composition {
		vc := VueComposition{Titre: c.T("resultats.critere." + t.Colonnes)}
		r := t.Rangees[0]
		for k, o := range t.Options {
			l := VueLigneComposition{Libelle: libelleOption(c, q, t.Colonnes, o), Echantillon: texteCase(c, r.Cases[k], masque)}
			switch {
			case inst.SourcePopulation == "":
				l.Population = c.T("resultats.population.non_disponible")
			case t.Population == nil || k >= len(t.Population) || t.Population[k] == nil:
				l.Population = c.T("resultats.population.sans_objet")
			default:
				l.Population = c.F("resultats.pourcentage", c.Nombre(int64(*t.Population[k])))
			}
			vc.Lignes = append(vc.Lignes, l)
		}
		v.Composition = append(v.Composition, vc)
	}
	for _, t := range inst.Tableaux {
		vt := vueTableau(c, q, t, masque)
		if q.Question(t.Colonnes).Partie == questionnaire.PartieProfil {
			vt.Titre = c.F("resultats.titre.profil", c.T("resultats.critere."+t.Colonnes), c.T("resultats.selon."+t.Lignes))
			v.Profil = append(v.Profil, vt)
			continue
		}
		if t.Lignes == "" {
			v.Questions = append(v.Questions, VueQuestionResultats{Libelle: c.T(q.Question(t.Colonnes).Cle), Ensemble: vt})
			continue
		}
		vt.Titre = c.F("resultats.titre.question", c.T("resultats.selon."+t.Lignes))
		dq := &v.Questions[len(v.Questions)-1]
		dq.Croisements = append(dq.Croisements, vt)
	}
	return v, nil
}

func vueTableau(c *i18n.Catalogue, q *questionnaire.Questionnaire, t publication.Tableau, masque string) VueTableau {
	vt := VueTableau{Multiple: t.ChoixMultiples, EnTete: c.T("resultats.ensemble.entete")}
	if t.Lignes != "" {
		vt.EnTete = c.T("resultats.critere." + t.Lignes)
	}
	for _, o := range t.Options {
		vt.Colonnes = append(vt.Colonnes, libelleOption(c, q, t.Colonnes, o))
	}
	for _, r := range t.Rangees {
		vr := VueRangee{Effectif: masque}
		if r.Categorie == publication.Ensemble {
			vr.Libelle = c.T("resultats.ensemble")
		} else {
			vr.Libelle = libelleOption(c, q, t.Lignes, r.Categorie)
		}
		if r.Effectif != nil {
			vr.Effectif = c.Nombre(int64(*r.Effectif))
		}
		for _, x := range r.Cases {
			vr.Cases = append(vr.Cases, texteCase(c, x, masque))
		}
		vt.Rangees = append(vt.Rangees, vr)
	}
	return vt
}

func texteCase(c *i18n.Catalogue, x publication.Case, masque string) string {
	switch {
	case x.Masque == publication.MasqueComplementaire:
		return c.T("resultats.masque.complementaire")
	case x.Masquee() || x.Pourcentage == nil:
		return masque
	default:
		return c.F("resultats.pourcentage", c.Nombre(int64(*x.Pourcentage)))
	}
}

// libelleOption renvoie le libellé d'une catégorie d'une question ou d'un
// critère.
func libelleOption(c *i18n.Catalogue, q *questionnaire.Questionnaire, nom, code string) string {
	if code == publication.AutresRegions {
		return c.T("resultats.autres_regions")
	}
	if x := q.Question(nom); x != nil {
		if n, err := strconv.ParseInt(code, 10, 16); err == nil {
			for _, ch := range x.Choix {
				if ch.Code == int16(n) {
					return c.T(ch.Cle)
				}
			}
		}
	}
	return code
}
