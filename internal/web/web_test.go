package web

import (
	"context"
	"errors"
	"html"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/i18n"
	"github.com/2guntan/diguee/internal/publication"
)

// Question clé, formulation exacte de la spec §3 (Q3).
const questionCle = "Êtes-vous favorable à une loi qui permettrait, pour les mandats futurs, " +
	"de révoquer un président qui n'a pas respecté ses engagements de façon grave et mesurée\u00a0?"

// compteurFixe imite le magasin des publications : une dernière publication
// au compteur n (aucune si n vaut -1), avec ou sans résultats détaillés.
type compteurFixe struct {
	n         int
	resultats bool
	err       error
}

func (c compteurFixe) DernierResume(context.Context) (*publication.Resume, error) {
	if c.err != nil || c.n < 0 {
		return nil, c.err
	}
	return &publication.Resume{Jour: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), Reponses: c.n, ResultatsPublies: c.resultats}, nil
}

func (c compteurFixe) LirePublication(context.Context, time.Time) (*publication.Publication, error) {
	return nil, c.err
}

func (c compteurFixe) Resumes(context.Context) ([]publication.Resume, error) { return nil, c.err }

func serveur(t *testing.T, env map[string]string, c Publications) *Serveur {
	t.Helper()
	env["DIGUEE_POSTGRES_URL"] = "postgres://localhost/diguee"
	cfg, err := config.Charger(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	cs, err := i18n.Charger()
	if err != nil {
		t.Fatal(err)
	}
	s, err := Nouveau(Options{
		Config: cfg, Catalogues: cs, Publications: c,
		Version: "v0.0.0-test", SHA256: strings.Repeat("ab", 32),
		Journal:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Maintenant: func() time.Time { return time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func get(t *testing.T, s http.Handler, chemin string) (*http.Response, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, chemin, nil))
	res := rec.Result()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func texte(corps string) string {
	return html.UnescapeString(corps)
}

func TestAccueil(t *testing.T) {
	s := serveur(t, map[string]string{}, compteurFixe{n: 980})
	res, corps := get(t, s, "/")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("statut %d", res.StatusCode)
	}
	c := texte(corps)
	for _, attendu := range []string{
		questionCle,
		"Votre réponse est anonyme et définitive.",
		"980 réponses reçues.",
		"Chiffre de la publication du 1 décembre 2026, arrondi à la dizaine inférieure.",
		"à partir de 1\u202f000 réponses",
		`href="/methodologie"`, `href="/confidentialite"`,
		"Version v0.0.0-test", strings.Repeat("ab", 32),
		"Déclaration à la CDP nº\u00a0À COMPLÉTER",
		"La consultation ouvre bientôt",
	} {
		if !strings.Contains(c, attendu) {
			t.Errorf("l'accueil ne contient pas %q", attendu)
		}
	}
}

func TestAccueilPhases(t *testing.T) {
	cas := map[string]string{
		"2027-01-15": "La consultation ouvre le 15 janvier 2027",
		"2026-11-01": "Le parcours de réponse arrive bientôt",
		"2026-01-01": "La consultation est close",
	}
	for ouverture, attendu := range cas {
		s := serveur(t, map[string]string{"DIGUEE_OUVERTURE": ouverture}, compteurFixe{})
		_, corps := get(t, s, "/")
		if !strings.Contains(texte(corps), attendu) {
			t.Errorf("ouverture %s : %q absent", ouverture, attendu)
		}
	}
}

func TestCompteurSingulierEtSeuilAtteint(t *testing.T) {
	_, corps := get(t, serveur(t, map[string]string{}, compteurFixe{n: 0}), "/")
	if !strings.Contains(texte(corps), "0 réponse reçue.") {
		t.Error("compteur à 0 mal écrit")
	}
	_, corps = get(t, serveur(t, map[string]string{"DIGUEE_SEUIL_PUBLICATION": "10"}, compteurFixe{n: 10, resultats: true}), "/")
	if !strings.Contains(texte(corps), "recalculés et publiés chaque jour") {
		t.Error("le seuil atteint devrait être signalé")
	}
}

func TestCompteurIndisponible(t *testing.T) {
	res, corps := get(t, serveur(t, map[string]string{}, compteurFixe{err: errors.New("panne")}), "/")
	if res.StatusCode != http.StatusServiceUnavailable || !strings.Contains(texte(corps), "momentanément indisponible") {
		t.Errorf("statut %d", res.StatusCode)
	}
}

func TestPagesAffichentLaConfiguration(t *testing.T) {
	env := map[string]string{
		"DIGUEE_STRUCTURE_NOM":      "Structure Exemple SARL",
		"DIGUEE_STRUCTURE_ADRESSE":  "Rue 10, Dakar",
		"DIGUEE_CONTACT":            "droits@exemple.sn",
		"DIGUEE_NUMERO_CDP":         "CDP-2027-042",
		"DIGUEE_OUVERTURE":          "2027-01-15",
		"DIGUEE_SEUIL_PUBLICATION":  "1500",
		"DIGUEE_SEUIL_CASE":         "35",
		"DIGUEE_HEURE_PUBLICATION":  "07:30Z",
		"DIGUEE_PREFIXES_AUTORISES": "70,77",
		"DIGUEE_TAMPON_TAILLE":      "25",
		"DIGUEE_PREAVIS_JOURS":      "21",
	}
	s := serveur(t, env, compteurFixe{})

	_, corps := get(t, s, "/methodologie")
	c := texte(corps)
	for _, attendu := range []string{
		questionCle, "15 janvier 2027", "15 juillet 2027", "1\u202f500 réponses", "moins de 35 réponses",
		"07:30 GMT", "70, 77", "lots mélangés de 25 envois", "21 jours", "10 minutes",
	} {
		if !strings.Contains(c, attendu) {
			t.Errorf("méthodologie : %q absent", attendu)
		}
	}

	_, corps = get(t, s, "/confidentialite")
	c = texte(corps)
	for _, attendu := range []string{
		"Structure Exemple SARL", "Rue 10, Dakar", "droits@exemple.sn", "CDP-2027-042",
		"15 juillet 2027", "1\u202f500", ">35<",
	} {
		if !strings.Contains(c, attendu) {
			t.Errorf("confidentialité : %q absent", attendu)
		}
	}
}

func TestValeursProvisoiresAffichees(t *testing.T) {
	_, corps := get(t, serveur(t, map[string]string{}, compteurFixe{}), "/confidentialite")
	if n := strings.Count(texte(corps), config.ACompleter); n < 5 {
		t.Errorf("%d mentions « À COMPLÉTER », au moins 5 attendues", n)
	}
}

func TestPagesLegeresSansScript(t *testing.T) {
	s := serveur(t, map[string]string{}, compteurFixe{n: 42})
	_, css := get(t, s, "/statique/diguee.css")
	_, logo := get(t, s, "/statique/logo.svg")
	for _, chemin := range []string{"/", "/methodologie", "/confidentialite", "/introuvable"} {
		res, corps := get(t, s, chemin)
		// Poids d'une page hors polices : HTML, feuille de style et icône.
		if poids := len(corps) + len(css) + len(logo); poids >= 100*1024 {
			t.Errorf("%s pèse %d octets", chemin, poids)
		}
		bas := strings.ToLower(corps)
		for _, interdit := range []string{"<script", "javascript:", " onload=", " onclick=", "https://", "http://"} {
			if strings.Contains(bas, interdit) {
				t.Errorf("%s contient %q", chemin, interdit)
			}
		}
		if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || strings.Contains(csp, "script-src") {
			t.Errorf("%s : CSP %q", chemin, csp)
		}
		if strings.Contains(corps, "⟦") {
			t.Errorf("%s utilise une clé absente du catalogue", chemin)
		}
	}
}

func TestPageIntrouvable(t *testing.T) {
	res, corps := get(t, serveur(t, map[string]string{}, compteurFixe{}), "/nexistepas")
	if res.StatusCode != http.StatusNotFound || !strings.Contains(corps, "Page introuvable") {
		t.Errorf("statut %d", res.StatusCode)
	}
}

func TestStatiques(t *testing.T) {
	s := serveur(t, map[string]string{}, compteurFixe{})
	for _, chemin := range []string{"/statique/diguee.css", "/statique/polices/inter-latin-400-normal.woff2"} {
		if res, _ := get(t, s, chemin); res.StatusCode != http.StatusOK {
			t.Errorf("%s : statut %d", chemin, res.StatusCode)
		}
	}
	// La feuille de style ne charge rien depuis l'extérieur.
	_, css := get(t, s, "/statique/diguee.css")
	if strings.Contains(css, "http") || strings.Contains(css, "@import") {
		t.Error("la feuille de style ne doit charger que des ressources locales")
	}
}

func TestSante(t *testing.T) {
	if res, _ := get(t, serveur(t, map[string]string{}, compteurFixe{}), "/sante"); res.StatusCode != http.StatusOK {
		t.Errorf("statut %d", res.StatusCode)
	}
	if res, _ := get(t, serveur(t, map[string]string{}, compteurFixe{err: errors.New("x")}), "/sante"); res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("statut %d", res.StatusCode)
	}
}

var (
	reAction      = regexp.MustCompile(`(?s)\{\{.*?\}\}`)
	reCommentaire = regexp.MustCompile(`(?s)<!--.*?-->`)
	reAttribut    = regexp.MustCompile(`(?i)\b(aria-label|title|alt|placeholder|content|value)="([^"]*)"`)
	reBalise      = regexp.MustCompile(`(?s)<[^>]*>`)
	reLettre      = regexp.MustCompile(`\p{L}`)
)

// Aucun texte visible n'est écrit en dur dans les gabarits : une fois retirés
// les actions {{…}} et les balises, il ne doit rester aucune lettre, et les
// attributs lus par les personnes passent eux aussi par le catalogue.
func TestAucunTexteEnDurDansLesGabarits(t *testing.T) {
	err := fs.WalkDir(fichiersGabarits, "gabarits", func(chemin string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := fichiersGabarits.ReadFile(chemin)
		src := reAction.ReplaceAllString(reCommentaire.ReplaceAllString(string(b), ""), "")
		// Seules exceptions : une couleur (#13233A) et le réglage viewport.
		for _, m := range reAttribut.FindAllStringSubmatch(src, -1) {
			if v := m[2]; reLettre.MatchString(v) && !strings.HasPrefix(v, "#") && !strings.Contains(v, "=") {
				t.Errorf("%s : attribut %s écrit en dur : %q", chemin, m[1], m[2])
			}
		}
		reste := reBalise.ReplaceAllString(src, "")
		reste = strings.ReplaceAll(reste, "&nbsp;", " ")
		if reLettre.MatchString(reste) {
			t.Errorf("%s contient du texte en dur : %q", chemin, strings.Join(strings.Fields(reste), " "))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
