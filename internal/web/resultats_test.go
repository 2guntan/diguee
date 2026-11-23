package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math/rand/v2"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/i18n"
	"github.com/2guntan/diguee/internal/publication"
	"github.com/2guntan/diguee/internal/questionnaire"
)

// publicationsMemoire garde des publications en mémoire.
type publicationsMemoire struct{ ps []*publication.Publication } // les plus récentes d'abord

func (m *publicationsMemoire) DernierResume(context.Context) (*publication.Resume, error) {
	if len(m.ps) == 0 {
		return nil, nil
	}
	r := m.ps[0].Resume
	return &r, nil
}

func (m *publicationsMemoire) LirePublication(_ context.Context, j time.Time) (*publication.Publication, error) {
	for _, p := range m.ps {
		if p.Jour.Equal(j) {
			return p, nil
		}
	}
	return nil, nil
}

func (m *publicationsMemoire) Resumes(context.Context) ([]publication.Resume, error) {
	var rs []publication.Resume
	for _, p := range m.ps {
		rs = append(rs, p.Resume)
	}
	return rs, nil
}

// aleatoires tire n réponses au hasard (graine fixe).
func aleatoires(n int, graine uint64) []questionnaire.Reponse {
	g := rand.New(rand.NewPCG(graine, graine))
	q := questionnaire.V1()
	un := func(nom string) int16 {
		cs := q.Question(nom).Choix
		return cs[g.IntN(len(cs))].Code
	}
	rs := make([]questionnaire.Reponse, n)
	for i := range rs {
		rs[i] = questionnaire.Reponse{Version: 1, Q1: un("q1"), Q2: un("q2"), Q3: un("q3"), Q4: un("q4"),
			Q5: un("q5"), Q6: un("q6"), Q7: []int16{int16(1 + g.IntN(4))}, Themes: []int16{1, int16(2 + g.IntN(5)), int16(7 + g.IntN(5))},
			Region: un("region"), TrancheAge: un("tranche_age"), Sexe: un("sexe"), Inscrit: un("inscrit")}
		if g.IntN(8) == 0 {
			rs[i].Q7, rs[i].Themes = []int16{}, []int16{}
		}
	}
	return rs
}

// produire calcule et met en page la publication du jour sur ces réponses.
func produire(t *testing.T, jour time.Time, rs []questionnaire.Reponse, pop *publication.Population) *publication.Publication {
	t.Helper()
	cs, err := i18n.Charger()
	if err != nil {
		t.Fatal(err)
	}
	rendu, err := RendreInstantane(cs, questionnaire.V1())
	if err != nil {
		t.Fatal(err)
	}
	a := publication.NouvelAgregateur(questionnaire.V1())
	for _, r := range rs {
		a.Ajouter(r)
	}
	p, err := publication.Produire(a.Instantane(publication.Parametres{Jour: jour, SeuilPublication: 1000, SeuilCase: 30, Population: pop}), rendu)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var (
	jour1 = time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	jour2 = time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC)
)

// La page des résultats, sous le seuil puis au-delà, pèse moins de 100 Ko
// sans script (T13), quelle que soit la répartition des réponses.
func TestPageResultats(t *testing.T) {
	sous := produire(t, jour1, aleatoires(999, 1), nil)
	m := &publicationsMemoire{ps: []*publication.Publication{sous}}
	s := serveur(t, map[string]string{}, m)
	_, css := get(t, s, "/statique/diguee.css")
	_, logo := get(t, s, "/statique/logo.svg")
	verifierPage := func(chemin string, corps string, res *http.Response) {
		t.Helper()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s : statut %d", chemin, res.StatusCode)
		}
		if poids := len(corps) + len(css) + len(logo); poids >= 100*1024 {
			t.Errorf("%s pèse %d octets", chemin, poids)
		}
		bas := strings.ToLower(corps)
		for _, interdit := range []string{"<script", "javascript:", " onload=", " onclick=", "⟦", "%!"} {
			if strings.Contains(bas, interdit) {
				t.Errorf("%s contient %q", chemin, interdit)
			}
		}
		if csp := res.Header.Get("Content-Security-Policy"); strings.Contains(csp, "script-src") {
			t.Errorf("%s : CSP %q", chemin, csp)
		}
	}

	res, corps := get(t, s, "/resultats")
	verifierPage("/resultats (999)", corps, res)
	c := texte(corps)
	for _, attendu := range []string{"990 réponses.", "à partir de 1 000 réponses", "Publication du 1 décembre 2026",
		"jusqu'au 30 novembre 2026 inclus", "06:00 GMT"} {
		if !strings.Contains(c, attendu) {
			t.Errorf("sous le seuil : %q absent", attendu)
		}
	}
	for _, absent := range []string{"<table", "non pondérés", "Dakar"} {
		if strings.Contains(c, absent) {
			t.Errorf("sous le seuil, la page ne doit afficher que le compteur : %q présent", absent)
		}
	}

	for _, n := range []int{1000, 3000, 60000} {
		p := produire(t, jour2, aleatoires(n, uint64(n)), nil)
		m.ps = []*publication.Publication{p, sous}
		res, corps := get(t, s, "/resultats")
		verifierPage("/resultats", corps, res)
		c := texte(corps)
		for _, attendu := range []string{"Résultats non pondérés.", "Composition de l'échantillon", "non disponible",
			"Les chiffres de population de l'ANSD ne sont pas encore disponibles.", "Très favorable", "selon la région",
			"Tranche d'âge selon la région", "Dakar", "Effectif", p.Empreinte.String(), `href="/resultats/2026-12-02.json"`} {
			if !strings.Contains(c, attendu) {
				t.Errorf("%d réponses : %q absent", n, attendu)
			}
		}
		if n == 1000 && !strings.Contains(c, "moins de 30 réponses") {
			t.Error("cases masquées attendues avec 1 000 réponses")
		}
		if strings.Contains(c, "n'est pas la dernière") {
			t.Error("la dernière publication n'est pas une archive")
		}
	}

	// L'export JSON est l'instantané enregistré, octet pour octet : son
	// SHA-256 est celui du journal.
	for _, chemin := range []string{"/resultats.json", "/resultats/2026-12-02.json"} {
		res, corps := get(t, s, chemin)
		h := sha256.Sum256([]byte(corps))
		if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") ||
			hex.EncodeToString(h[:]) != m.ps[0].Empreinte.String() {
			t.Errorf("%s : statut %d, empreinte %x", chemin, res.StatusCode, h)
		}
	}
	// Publication passée et liste des publications.
	res, corps = get(t, s, "/resultats/2026-12-01")
	verifierPage("/resultats/2026-12-01", corps, res)
	if !strings.Contains(texte(corps), "Vous consultez la publication du 1 décembre 2026, qui n'est pas la dernière.") {
		t.Error("une publication passée doit le dire")
	}
	res, corps = get(t, s, "/resultats/publications")
	verifierPage("/resultats/publications", corps, res)
	for _, attendu := range []string{`href="/resultats/2026-12-01"`, `href="/resultats/2026-12-02.json"`, "Publication du 2 décembre 2026"} {
		if !strings.Contains(corps, attendu) {
			t.Errorf("liste des publications : %q absent", attendu)
		}
	}
	for _, chemin := range []string{"/resultats/2026-11-30", "/resultats/2026-11-30.json", "/resultats/hier", "/resultats/2026-13-01"} {
		if res, _ := get(t, s, chemin); res.StatusCode != http.StatusNotFound {
			t.Errorf("%s : statut %d", chemin, res.StatusCode)
		}
	}
}

// Avec les chiffres de l'ANSD, la composition les compare à l'échantillon.
func TestResultatsAvecPopulation(t *testing.T) {
	pop, err := publication.LirePopulation([]byte(`{"source": "ANSD, RGPH-5 (exemple de test)",
		"region": {"1": 400, "2": 100, "3": 100, "4": 100, "5": 100, "6": 100, "7": 100, "8": 100, "9": 100,
		"10": 100, "11": 100, "12": 100, "13": 100, "14": 100},
		"tranche_age": {"1": 30, "2": 30, "3": 20, "4": 15, "5": 5}, "sexe": {"1": 51, "2": 49}}`))
	if err != nil {
		t.Fatal(err)
	}
	m := &publicationsMemoire{ps: []*publication.Publication{produire(t, jour2, aleatoires(5000, 9), pop)}}
	_, corps := get(t, serveur(t, map[string]string{}, m), "/resultats")
	c := texte(corps)
	for _, attendu := range []string{"Population de référence : ANSD, RGPH-5 (exemple de test).", "<td>24 %<tr>", "<td>51 %<tr>", "sans objet"} {
		if !strings.Contains(c, attendu) {
			t.Errorf("%q absent", attendu)
		}
	}
	if strings.Contains(c, "non disponible") {
		t.Error("la population est disponible")
	}
}

func TestResultatsSansPublication(t *testing.T) {
	s := serveur(t, map[string]string{}, &publicationsMemoire{})
	res, corps := get(t, s, "/resultats")
	if res.StatusCode != http.StatusOK || !strings.Contains(texte(corps), "Aucune publication pour l'instant.") {
		t.Errorf("statut %d", res.StatusCode)
	}
	if res, _ := get(t, s, "/resultats.json"); res.StatusCode != http.StatusNotFound {
		t.Errorf("/resultats.json : %d", res.StatusCode)
	}
	// L'accueil ne compte pas en direct : sans publication, 0.
	_, corps = get(t, s, "/")
	if c := texte(corps); !strings.Contains(c, "0 réponse reçue.") || strings.Contains(c, "Chiffre de la publication") {
		t.Error("compteur de l'accueil sans publication")
	}
}

// L'accueil affiche le compteur de la dernière publication et, au-delà du
// seuil, mène aux résultats.
func TestAccueilCompteurDeLaDernierePublication(t *testing.T) {
	m := &publicationsMemoire{ps: []*publication.Publication{produire(t, jour2, aleatoires(1234, 5), nil), produire(t, jour1, aleatoires(999, 1), nil)}}
	_, corps := get(t, serveur(t, map[string]string{}, m), "/")
	c := texte(corps)
	for _, attendu := range []string{"1 230 réponses reçues.", "Chiffre de la publication du 2 décembre 2026", `href="/resultats">Voir les résultats`} {
		if !strings.Contains(c, attendu) {
			t.Errorf("accueil : %q absent", attendu)
		}
	}
}

// Le fragment enregistré ne contient ni heure, ni texte hors catalogue.
func TestFragmentDateAuJour(t *testing.T) {
	p := produire(t, jour2, aleatoires(2000, 3), nil)
	h := string(p.HTML)
	if strings.Contains(h, "GMT") || strings.Contains(h, "⟦") || strings.Contains(h, "%!") {
		t.Errorf("fragment : heure ou clé absente")
	}
	if !strings.Contains(h, "<details>") {
		t.Error("les croisements sont repliés dans des <details>")
	}
}
