package web

import (
	"bytes"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/2guntan/diguee/internal/i18n"
	"github.com/2guntan/diguee/internal/journal"
)

// EntreesParPage est le nombre d'entrées par page de /journal : la page reste
// sous 100 Ko quelle que soit la taille du journal.
const EntreesParPage = 50

// VueJournal est ce qu'affiche la page /journal.
type VueJournal struct {
	Nombre    int64  // nombre d'entrées
	Tete      string // empreinte de la dernière entrée
	Commande  string // commande de vérification
	Entrees   []VueEntree
	Page      int64 // 1 : les plus récentes
	Pages     int64
	Recentes  int64 // page des entrées plus récentes, 0 s'il n'y en a pas
	Anciennes int64 // page des entrées plus anciennes, 0 s'il n'y en a pas
}

// VueEntree est une entrée telle qu'elle est affichée.
type VueEntree struct {
	Numero        int64
	Horodatage    time.Time
	HorodatageISO string
	Type          string
	CleType       string // clé du catalogue pour le type, vide s'il n'y en a pas
	Champs        []VueChamp
	Precedente    string
	Empreinte     string
}

// VueChamp est un champ du contenu d'une entrée.
type VueChamp struct {
	Cle    string // clé du catalogue pour le nom du champ, vide s'il n'y en a pas
	Nom    string // nom du champ dans le JSON
	Valeur string
	Code   bool // valeur affichée telle quelle (JSON, empreinte)
}

func (s *Serveur) journalPage(w http.ResponseWriter, r *http.Request) {
	d := s.donnees()
	ctx := r.Context()
	n, err := s.opt.JournalPublic.Compter(ctx)
	if err != nil {
		s.opt.Journal.Error("lecture du journal public", "erreur", err)
		s.rendre(w, r, http.StatusServiceUnavailable, "indisponible", d)
		return
	}
	v := &VueJournal{Nombre: n, Commande: journal.CommandeVerification, Page: 1,
		Pages: max(1, (n+EntreesParPage-1)/EntreesParPage)}
	if p := r.URL.Query().Get("page"); p != "" {
		v.Page, err = strconv.ParseInt(p, 10, 64)
		if err != nil || v.Page < 1 || v.Page > v.Pages {
			s.introuvable(w, r)
			return
		}
	}
	if n > 0 {
		dernier := n - (v.Page-1)*EntreesParPage
		es, err := s.opt.JournalPublic.Lire(ctx, max(1, dernier-EntreesParPage+1), dernier)
		if err == nil && v.Page == 1 && len(es) > 0 {
			v.Tete = es[len(es)-1].Empreinte.String()
		} else if err == nil && v.Page > 1 {
			var tete []journal.Entree
			if tete, err = s.opt.JournalPublic.Lire(ctx, n, n); err == nil && len(tete) == 1 {
				v.Tete = tete[0].Empreinte.String()
			}
		}
		if err != nil {
			s.opt.Journal.Error("lecture du journal public", "erreur", err)
			s.rendre(w, r, http.StatusServiceUnavailable, "indisponible", d)
			return
		}
		c := s.langue(r)
		for i := len(es) - 1; i >= 0; i-- { // les plus récentes d'abord
			v.Entrees = append(v.Entrees, vueEntree(c, es[i]))
		}
	}
	if v.Page > 1 {
		v.Recentes = v.Page - 1
	}
	if v.Page < v.Pages {
		v.Anciennes = v.Page + 1
	}
	d.Journal = v
	s.rendre(w, r, http.StatusOK, "journal", d)
}

func vueEntree(c *i18n.Catalogue, e journal.Entree) VueEntree {
	v := VueEntree{
		Numero: e.Numero, Horodatage: e.Horodatage, HorodatageISO: e.Horodatage.Format(journal.FormatHorodatage),
		Type: string(e.Type), Precedente: e.EmpreintePrecedente.String(), Empreinte: e.Empreinte.String(),
	}
	if cle := "journal.type." + string(e.Type); c.Existe(cle) {
		v.CleType = cle
	}
	var valeurs map[string]json.RawMessage
	_ = json.Unmarshal(e.Contenu, &valeurs) // toujours un objet (contrôlé à l'écriture)
	champs := slices.Sorted(maps.Keys(valeurs))
	for _, k := range champs {
		ch := VueChamp{Nom: k}
		if cle := "journal.champ." + k; c.Existe(cle) {
			ch.Cle = cle
		}
		brut := valeurs[k]
		var x any
		dec := json.NewDecoder(bytes.NewReader(brut))
		dec.UseNumber()
		_ = dec.Decode(&x)
		switch y := x.(type) {
		case string:
			ch.Valeur = y
			ch.Code = len(y) == 64 && journal.EstHex(y)
		case json.Number:
			if n, err := y.Int64(); err == nil {
				ch.Valeur = c.Nombre(n)
			} else {
				ch.Valeur = y.String()
			}
		default:
			ch.Valeur, ch.Code = string(brut), true
		}
		v.Champs = append(v.Champs, ch)
	}
	return v
}

func (s *Serveur) journalExport(w http.ResponseWriter, r *http.Request) {
	es, err := s.opt.JournalPublic.Toutes(r.Context())
	var buf bytes.Buffer
	if err == nil {
		err = journal.Exporter(&buf, es)
	}
	if err != nil {
		s.opt.Journal.Error("export du journal public", "erreur", err)
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Content-Disposition", `inline; filename="journal.json"`)
	_, _ = w.Write(buf.Bytes())
}
