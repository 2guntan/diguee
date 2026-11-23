package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/2guntan/diguee/internal/observatoire"
	"github.com/2guntan/diguee/internal/stockage"
)

// VueObservatoire est ce que reçoivent les pages publiques de l'observatoire.
type VueObservatoire struct {
	Liste   *observatoire.Liste
	Filtre  observatoire.Filtre
	Mandat  string // clé du mandat filtré, "" pour tous
	Valeurs []observatoire.Valeur

	Page, Pages          int
	Precedente, Suivante string // liens de pagination, "" s'il n'y en a pas

	Fiche *observatoire.Engagement
}

// observatoireListe sert /observatoire : la liste des engagements publics,
// filtrable par élu, mandat et statut (le dernier publié), paginée.
func (s *Serveur) observatoireListe(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v := &VueObservatoire{Valeurs: observatoire.Valeurs}
	if id, err := strconv.ParseInt(q.Get("elu"), 10, 64); err == nil && id > 0 {
		v.Filtre.Elu = id
	}
	if m, ok := observatoire.LireMandat(q.Get("mandat")); ok {
		v.Filtre.Mandat, v.Mandat = &m, m.Cle()
	}
	if val := observatoire.Valeur(q.Get("statut")); val.Valide() {
		v.Filtre.Valeur = val
	}
	v.Filtre.Page = 1
	if p, err := strconv.Atoi(q.Get("page")); err == nil && p > 1 && p < 1<<20 {
		v.Filtre.Page = p
	}
	l, err := s.opt.Observatoire.ListePublique(r.Context(), v.Filtre)
	if err != nil {
		s.opt.Journal.Error("observatoire : liste publique", "erreur", err)
		s.rendre(w, r, http.StatusServiceUnavailable, "indisponible", s.donnees())
		return
	}
	v.Liste, v.Page = l, v.Filtre.Page
	v.Pages = max(1, (l.Total+observatoire.ParPage-1)/observatoire.ParPage)
	lien := func(page int) string {
		u := url.Values{}
		if v.Filtre.Elu > 0 {
			u.Set("elu", strconv.FormatInt(v.Filtre.Elu, 10))
		}
		if v.Mandat != "" {
			u.Set("mandat", v.Mandat)
		}
		if v.Filtre.Valeur != "" {
			u.Set("statut", string(v.Filtre.Valeur))
		}
		if page > 1 {
			u.Set("page", strconv.Itoa(page))
		}
		if len(u) == 0 {
			return "/observatoire"
		}
		return "/observatoire?" + u.Encode()
	}
	if v.Page > 1 {
		v.Precedente = lien(v.Page - 1)
	}
	if v.Page < v.Pages {
		v.Suivante = lien(v.Page + 1)
	}
	d := s.donnees()
	d.Observatoire = v
	s.rendre(w, r, http.StatusOK, "observatoire", d)
}

// observatoireFiche sert la fiche publique d'un engagement.
func (s *Serveur) observatoireFiche(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.introuvable(w, r)
		return
	}
	e, err := s.opt.Observatoire.FichePublique(r.Context(), id)
	if errors.Is(err, observatoire.ErrIntrouvable) {
		s.introuvable(w, r)
		return
	}
	if err != nil {
		s.opt.Journal.Error("observatoire : fiche publique", "erreur", err)
		s.rendre(w, r, http.StatusServiceUnavailable, "indisponible", s.donnees())
		return
	}
	d := s.donnees()
	d.Observatoire = &VueObservatoire{Fiche: e}
	s.rendre(w, r, http.StatusOK, "engagement", d)
}

// observatoireArchive sert la copie archivée d'une source publique, sous son
// SHA-256 (/observatoire/archives/<sha256>.<extension>).
func (s *Serveur) observatoireArchive(w http.ResponseWriter, r *http.Request) {
	ServirArchive(w, r, s.opt.Observatoire, true, s.opt.Journal.Error, func() { s.introuvable(w, r) })
}

// ServirArchive sert une copie archivée, nommée « <sha256>.<extension> » dans
// le chemin ({fichier}), avec des en-têtes sûrs : type fixé par Diguee
// d'après la signature du fichier (jamais celui du navigateur qui l'a
// téléversé), téléchargement (Content-Disposition: attachment), nosniff, et
// une CSP qui interdit toute exécution. Le contenu est vérifié contre son
// SHA-256 avant d'être servi. Utilisé aussi par l'espace équipe (publique
// faux : toute archive connue).
func ServirArchive(w http.ResponseWriter, r *http.Request, o *observatoire.Service, publique bool,
	erreur func(string, ...any), introuvable func()) {
	nom, ext, _ := strings.Cut(r.PathValue("fichier"), ".")
	e, err := stockage.LireEmpreinte(nom)
	if err != nil {
		introuvable()
		return
	}
	typ, contenu, err := o.Archive(r.Context(), e, publique)
	switch {
	case errors.Is(err, observatoire.ErrIntrouvable):
		introuvable()
		return
	case err != nil:
		// Archive absente ou altérée dans le stockage : jamais servie.
		erreur("observatoire : copie archivée illisible", "sha256", e.String(), "erreur", err)
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	attendue := observatoire.Source{TypeArchive: typ}.Extension()
	if ext != attendue {
		http.Redirect(w, r, strings.TrimSuffix(r.URL.Path, "."+ext)+"."+attendue, http.StatusMovedPermanently)
		return
	}
	h := w.Header()
	h.Set("Content-Type", typ)
	h.Set("Content-Disposition", `attachment; filename="diguee-`+e.String()+"."+attendue+`"`)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Content-Length", strconv.Itoa(len(contenu)))
	if publique {
		// Le contenu ne change jamais sous ce nom.
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-store")
	}
	if r.Method != http.MethodHead {
		_, _ = w.Write(contenu)
	}
}

// extrait coupe une citation longue pour la liste, au mot près.
func extrait(s string) string {
	const max = 280
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)[:max]
	if i := strings.LastIndexAny(string(r), " \n"); i > max/2 {
		return string(r)[:i] + "…"
	}
	return string(r) + "…"
}
