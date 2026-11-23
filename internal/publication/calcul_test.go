package publication

import (
	"encoding/json"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/questionnaire"
)

var jourTest = time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC)

func params(seuilPublication int) Parametres {
	return Parametres{Jour: jourTest, SeuilPublication: seuilPublication, SeuilCase: 30}
}

// reponse renvoie une réponse complète de la version 1.
func reponse(region, q3 int16) questionnaire.Reponse {
	return questionnaire.Reponse{Version: 1, Q1: 1, Q2: 1, Q3: q3, Q4: 1, Q5: 1, Q6: 1,
		Q7: []int16{1}, Themes: []int16{1, 2, 3}, Region: region, TrancheAge: 1, Sexe: 1, Inscrit: 1}
}

// Aleatoires tire n réponses au hasard (graine fixe), chaque champ selon les
// choix du questionnaire v1.
func Aleatoires(n int, graine uint64) []questionnaire.Reponse {
	g := rand.New(rand.NewPCG(graine, graine))
	q := questionnaire.V1()
	un := func(nom string) int16 {
		cs := q.Question(nom).Choix
		return cs[g.IntN(len(cs))].Code
	}
	var rs []questionnaire.Reponse
	for range n {
		r := questionnaire.Reponse{Version: 1, Q1: un("q1"), Q2: un("q2"), Q3: un("q3"), Q4: un("q4"),
			Q5: un("q5"), Q6: un("q6"), Region: un("region"), TrancheAge: un("tranche_age"), Sexe: un("sexe"), Inscrit: un("inscrit")}
		r.Q7 = []int16{}
		for c := int16(1); c <= 4; c++ {
			if g.IntN(2) == 0 {
				r.Q7 = append(r.Q7, c)
			}
		}
		r.Themes = []int16{}
		if g.IntN(10) > 0 {
			r.Themes = []int16{int16(1 + g.IntN(3)), int16(4 + g.IntN(4)), int16(8 + g.IntN(4))}
		}
		rs = append(rs, r)
	}
	return rs
}

func calculer(rs []questionnaire.Reponse, p Parametres) *Instantane {
	a := NouvelAgregateur(questionnaire.V1())
	for _, r := range rs {
		a.Ajouter(r)
	}
	return a.Instantane(p)
}

func tableau(t *testing.T, inst *Instantane, colonnes, lignes string) Tableau {
	t.Helper()
	for _, x := range inst.Tableaux {
		if x.Colonnes == colonnes && x.Lignes == lignes {
			return x
		}
	}
	t.Fatalf("tableau %s × %s absent", colonnes, lignes)
	return Tableau{}
}

func rangee(t *testing.T, tb Tableau, cat string) Rangee {
	t.Helper()
	for _, r := range tb.Rangees {
		if r.Categorie == cat {
			return r
		}
	}
	t.Fatalf("rangée %s absente de %s × %s", cat, tb.Colonnes, tb.Lignes)
	return Rangee{}
}

func caseDe(t *testing.T, tb Tableau, r Rangee, option string) Case {
	t.Helper()
	k := slices.Index(tb.Options, option)
	if k < 0 {
		t.Fatalf("option %s absente de %v", option, tb.Options)
	}
	return r.Cases[k]
}

func TestArrondis(t *testing.T) {
	for n, attendu := range map[int]int{0: 0, 9: 0, 10: 10, 999: 990, 1000: 1000, 1234: 1230, 1239: 1230} {
		if got := Arrondir(n); got != attendu {
			t.Errorf("Arrondir(%d) = %d, attendu %d", n, got, attendu)
		}
	}
	for _, c := range [][3]int{{1, 3, 33}, {2, 3, 67}, {1, 8, 13}, {1, 200, 1}, {1, 201, 0}, {29, 250, 12}, {0, 10, 0}, {10, 10, 100}} {
		if got := Pourcentage(c[0], c[1]); got != c[2] {
			t.Errorf("Pourcentage(%d, %d) = %d, attendu %d", c[0], c[1], got, c[2])
		}
	}
}

func TestJourDeLaPublication(t *testing.T) {
	h := 6 * time.Hour
	cas := []struct {
		maintenant, jour, prochaine string
	}{
		{"2026-12-02T06:00:00Z", "2026-12-02", "2026-12-03T06:00:00Z"},
		{"2026-12-02T05:59:59Z", "2026-12-01", "2026-12-02T06:00:00Z"},
		{"2026-12-02T23:59:00Z", "2026-12-02", "2026-12-03T06:00:00Z"},
		{"2026-12-03T00:30:00Z", "2026-12-02", "2026-12-03T06:00:00Z"},
	}
	for _, c := range cas {
		m, _ := time.Parse(time.RFC3339, c.maintenant)
		if got := JourDu(m, h).Format(time.DateOnly); got != c.jour {
			t.Errorf("JourDu(%s) = %s, attendu %s", c.maintenant, got, c.jour)
		}
		if got := Prochaine(m, h).Format(time.RFC3339); got != c.prochaine {
			t.Errorf("Prochaine(%s) = %s, attendu %s", c.maintenant, got, c.prochaine)
		}
	}
}

// Sous le Seuil de publication, rien d'autre que le compteur arrondi.
func TestSousLeSeuilLeCompteurSeul(t *testing.T) {
	rs := Aleatoires(999, 1)
	inst := calculer(rs, params(1000))
	if inst.ResultatsPublies || inst.Reponses != 990 || len(inst.Tableaux) != 0 || len(inst.Composition) != 0 ||
		inst.AutresRegions != nil || inst.Ponderation != "" {
		t.Fatalf("999 réponses : %+v", inst)
	}
	b, _ := json.Marshal(inst)
	if strings.Contains(string(b), "rangees") || strings.Contains(string(b), "options") {
		t.Errorf("aucune répartition attendue : %s", b)
	}
	inst = calculer(append(rs, Aleatoires(1, 2)...), params(1000))
	if !inst.ResultatsPublies || inst.Reponses != 1000 || len(inst.Tableaux) == 0 || inst.Ponderation != "aucune" {
		t.Fatalf("1 000 réponses : résultats attendus, %+v", inst.Reponses)
	}
}

// T10 (spec §14) : une case de 29 réponses est masquée, avec sa case
// complémentaire (la plus petite des autres) ; pourcentages et effectifs
// arrondis.
func TestT10MasquageComplementaireEtArrondis(t *testing.T) {
	var rs []questionnaire.Reponse
	// Dakar (1) : Q3 = 100 × « Très favorable », 50, 40, 29 × « Très
	// opposé », 33 × « Je ne sais pas » : 252 réponses.
	for q3, n := range map[int16]int{1: 100, 2: 50, 3: 40, 4: 29, 0: 33} {
		for range n {
			rs = append(rs, reponse(1, q3))
		}
	}
	// Thiès (13) : deux cases sous le seuil (10 et 5), pas de masquage
	// complémentaire ; 200 + 60 + 45 + 10 + 5 = 320.
	for q3, n := range map[int16]int{1: 200, 2: 60, 3: 45, 4: 10, 0: 5} {
		for range n {
			rs = append(rs, reponse(13, q3))
		}
	}
	// Kédougou (6) : 12 réponses, regroupée dans « Autres régions ».
	for range 12 {
		rs = append(rs, reponse(6, 2))
	}
	// Assez de réponses ailleurs pour franchir le seuil (diaspora).
	for i := range 500 {
		rs = append(rs, reponse(15, int16(i%5)))
	}
	inst := calculer(rs, params(1000))
	if !inst.ResultatsPublies || inst.Reponses != 1080 { // 1 084 réponses
		t.Fatalf("compteur %d", inst.Reponses)
	}
	tb := tableau(t, inst, "q3", "region")
	dakar := rangee(t, tb, "1")
	if dakar.Effectif == nil || *dakar.Effectif != 250 {
		t.Errorf("effectif de Dakar : %v, 250 attendu (252 arrondi à la dizaine inférieure)", dakar.Effectif)
	}
	if c := caseDe(t, tb, dakar, "4"); c.Masque != MasqueSeuil || c.Pourcentage != nil {
		t.Errorf("case de 29 réponses : %+v", c)
	}
	if c := caseDe(t, tb, dakar, "0"); c.Masque != MasqueComplementaire || c.Pourcentage != nil {
		t.Errorf("case complémentaire (33, la plus petite des autres) : %+v", c)
	}
	for option, pct := range map[string]int{"1": 40, "2": 20, "3": 16} { // 100/252, 50/252, 40/252
		if c := caseDe(t, tb, dakar, option); c.Masquee() || *c.Pourcentage != pct {
			t.Errorf("Dakar, option %s : %+v, %d %% attendu", option, c, pct)
		}
	}
	thies := rangee(t, tb, "13")
	for option, masque := range map[string]string{"4": MasqueSeuil, "0": MasqueSeuil, "3": "", "2": "", "1": ""} {
		if c := caseDe(t, tb, thies, option); c.Masque != masque {
			t.Errorf("Thiès, option %s : masque %q, attendu %q", option, c.Masque, masque)
		}
	}
	if c := caseDe(t, tb, thies, "1"); *c.Pourcentage != 63 { // 200/320 = 62,5
		t.Errorf("arrondi à l'unité : %d", *c.Pourcentage)
	}
	// Régions de moins de 30 réponses : regroupées ; « Autres régions » (12
	// réponses de Kédougou) compte moins de 30 réponses, donc tout est masqué.
	if !slices.Contains(inst.AutresRegions, "6") || slices.Contains(inst.AutresRegions, "1") ||
		slices.Contains(inst.AutresRegions, "0") {
		t.Errorf("régions regroupées : %v", inst.AutresRegions)
	}
	for _, r := range tb.Rangees {
		if r.Categorie == "6" {
			t.Error("Kédougou ne doit pas avoir sa propre rangée")
		}
	}
	autres := rangee(t, tb, AutresRegions)
	if autres.Effectif != nil {
		t.Errorf("effectif d'une rangée sous le seuil : %d", *autres.Effectif)
	}
	for k, c := range autres.Cases {
		if c.Masque != MasqueSeuil {
			t.Errorf("Autres régions, case %d non masquée : %+v", k, c)
		}
	}
	// Tous les effectifs publiés sont des dizaines, tous les pourcentages des
	// entiers entre 0 et 100.
	for _, x := range append(inst.Tableaux, inst.Composition...) {
		for _, r := range x.Rangees {
			if r.Effectif != nil && *r.Effectif%10 != 0 {
				t.Errorf("%s × %s : effectif %d", x.Colonnes, x.Lignes, *r.Effectif)
			}
			for _, c := range r.Cases {
				if c.Masquee() == (c.Pourcentage != nil) || (c.Pourcentage != nil && (*c.Pourcentage < 0 || *c.Pourcentage > 100)) {
					t.Errorf("%s × %s : case %+v", x.Colonnes, x.Lignes, c)
				}
			}
		}
	}
}

// Le masquage complémentaire vaut pour toutes les rangées de tous les
// tableaux : jamais une seule case masquée dans une rangée.
func TestJamaisUneSeuleCaseMasquee(t *testing.T) {
	inst := calculer(Aleatoires(3000, 7), params(1000))
	seules := 0
	for _, x := range append(inst.Tableaux, inst.Composition...) {
		for _, r := range x.Rangees {
			n := 0
			for _, c := range r.Cases {
				if c.Masquee() {
					n++
				}
			}
			if n == 1 {
				seules++
				t.Errorf("%s × %s, rangée %s : une seule case masquée", x.Colonnes, x.Lignes, r.Categorie)
			}
		}
	}
}

// Q7 et les thèmes : part des répondants qui ont choisi chaque option, mêmes
// seuils ; une option choisie par presque tous est masquée aussi.
func TestChoixMultiples(t *testing.T) {
	var rs []questionnaire.Reponse
	for i := range 1000 {
		r := reponse(1, 1)
		r.Q7 = []int16{1, 2} // tous : Président et Ministres
		if i < 300 {
			r.Q7 = []int16{1, 2, 3} // 300 : aussi Députés
		}
		if i >= 990 {
			r.Q7 = []int16{} // 10 : Je ne sais pas
		}
		r.Themes = []int16{1, 2, 3}
		if i%2 == 0 {
			r.Themes = []int16{4, 5, 6}
		}
		rs = append(rs, r)
	}
	inst := calculer(rs, params(1000))
	tb := tableau(t, inst, "q7", "")
	if !tb.ChoixMultiples {
		t.Error("Q7 est à choix multiples")
	}
	e := tb.Rangees[0]
	// Président et Ministres : 990 sur 1 000, mais seulement 10 ne l'ont pas
	// choisi : masqué. Je ne sais pas : 10, masqué. Députés : 30 %.
	for option, attendu := range map[string]string{"1": MasqueSeuil, "2": MasqueSeuil, "0": MasqueSeuil, "3": "", "4": MasqueSeuil} {
		if c := caseDe(t, tb, e, option); c.Masque != attendu {
			t.Errorf("Q7, option %s : %+v, masque %q attendu", option, c, attendu)
		}
	}
	if c := caseDe(t, tb, e, "3"); *c.Pourcentage != 30 {
		t.Errorf("Députés : %d %%", *c.Pourcentage)
	}
	th := tableau(t, inst, "themes", "")
	somme := 0
	for _, c := range th.Rangees[0].Cases {
		if c.Pourcentage != nil {
			somme += *c.Pourcentage
		}
	}
	if !th.ChoixMultiples || somme != 300 { // 6 thèmes à 50 %
		t.Errorf("thèmes : somme %d, 300 attendu (3 choix par répondant) ; %+v", somme, th.Rangees[0].Cases)
	}
}

// Aucune combinaison de trois critères, ni de deux questions : chaque
// tableau a une dimension en colonnes et au plus un critère du profil en
// rangées.
func TestAucuneCombinaisonDeTroisCriteres(t *testing.T) {
	q := questionnaire.V1()
	inst := calculer(Aleatoires(2000, 3), params(1000))
	vus := map[[2]string]bool{}
	for _, x := range append(inst.Tableaux, inst.Composition...) {
		col := q.Question(x.Colonnes)
		if col == nil {
			t.Fatalf("colonnes inconnues : %q", x.Colonnes)
		}
		if x.Lignes != "" {
			lig := q.Question(x.Lignes)
			if lig == nil || lig.Partie != questionnaire.PartieProfil || x.Lignes == x.Colonnes {
				t.Errorf("rangées %q : seul un critère du profil, distinct des colonnes, est admis", x.Lignes)
			}
		}
		for _, r := range x.Rangees {
			if (x.Lignes == "") != (r.Categorie == Ensemble) {
				t.Errorf("%s × %s : rangée %q", x.Colonnes, x.Lignes, r.Categorie)
			}
		}
		k := [2]string{x.Colonnes, x.Lignes}
		if vus[k] && x.Lignes != "" {
			t.Errorf("tableau %v en double", k)
		}
		vus[k] = true
	}
	// 8 questions seules, 8 × 4 croisements, 6 paires de critères, 4 compositions.
	if len(inst.Tableaux) != 8+8*4+6 || len(inst.Composition) != 4 {
		t.Errorf("%d tableaux, %d compositions", len(inst.Tableaux), len(inst.Composition))
	}
	// Le JSON publié ne contient que des entiers (forme canonique du journal).
	if _, err := journal.CanoniqueDe(inst); err != nil {
		t.Errorf("instantané non canonisable : %v", err)
	}
}

// Une réponse d'une autre version du questionnaire n'est pas comptée.
func TestAutreVersionIgnoree(t *testing.T) {
	r := reponse(1, 1)
	r.Version = 2
	if inst := calculer([]questionnaire.Reponse{r, reponse(1, 1)}, params(1000)); inst.Reponses != 0 {
		t.Errorf("compteur %d", inst.Reponses)
	}
	a := NouvelAgregateur(questionnaire.V1())
	a.Ajouter(r)
	if a.Total() != 0 {
		t.Error("version 2 comptée")
	}
}

func TestPopulation(t *testing.T) {
	ok := `{"source": "ANSD, test", "region": {"1": 400, "2": 100, "3": 100, "4": 100, "5": 100, "6": 100, "7": 100,
		"8": 100, "9": 100, "10": 100, "11": 100, "12": 100, "13": 100, "14": 100},
		"tranche_age": {"1": 30, "2": 30, "3": 20, "4": 15, "5": 5}, "sexe": {"1": 51, "2": 49}}`
	p, err := LirePopulation([]byte(ok))
	if err != nil {
		t.Fatal(err)
	}
	for _, mauvais := range []string{
		`{"source": "À COMPLÉTER", "region": {}, "tranche_age": {}, "sexe": {}}`,
		strings.Replace(ok, `"14": 100`, `"15": 100`, 1),
		strings.Replace(ok, `"1": 51`, `"1": 0`, 1),
		strings.Replace(ok, `"source"`, `"autre": 1, "source"`, 1),
	} {
		if _, err := LirePopulation([]byte(mauvais)); err == nil {
			t.Errorf("fichier accepté : %s", mauvais)
		}
	}
	pr := params(1000)
	pr.Population = p
	inst := calculer(Aleatoires(1500, 4), pr)
	if inst.SourcePopulation != "ANSD, test" {
		t.Errorf("source %q", inst.SourcePopulation)
	}
	for _, x := range inst.Composition {
		switch x.Colonnes {
		case "sexe":
			if len(x.Population) != 3 || *x.Population[0] != 51 || *x.Population[1] != 49 || x.Population[2] != nil {
				t.Errorf("population par sexe : %v", x.Population)
			}
		case "region":
			if k := slices.Index(x.Options, "1"); k < 0 || *x.Population[k] != 24 { // 400 / 1 700
				t.Errorf("Dakar : %v", x.Population)
			}
			if k := slices.Index(x.Options, "15"); k >= 0 && x.Population[k] != nil {
				t.Error("la diaspora n'a pas de population de référence")
			}
		case "inscrit":
			if x.Population != nil {
				t.Error("l'inscription n'est pas comparée")
			}
		}
	}
	if inst := calculer(Aleatoires(1500, 4), params(1000)); inst.SourcePopulation != "" || inst.Composition[0].Population != nil {
		t.Error("sans population, rien n'est comparé")
	}
}
