package questionnaire

import (
	"net/url"
	"slices"
	"strings"
	"testing"
)

// complet renvoie un formulaire valide.
func complet() url.Values {
	return url.Values{
		"q1": {"2"}, "q2": {"1"}, "q3": {"1"}, "q4": {"3"}, "q5": {"0"}, "q6": {"2"},
		"q7": {"1", "3"}, "themes": {"5", "1", "11"},
		"region": {"15"}, "tranche_age": {"2"}, "sexe": {"1"}, "inscrit": {"3"},
		ChampMajeur: {"1"},
	}
}

func TestLireComplet(t *testing.T) {
	q := V1()
	r, errs := q.Lire(complet())
	if errs != nil {
		t.Fatal(errs)
	}
	attendu := Reponse{Version: 1, Q1: 2, Q2: 1, Q3: 1, Q4: 3, Q5: 0, Q6: 2,
		Q7: []int16{1, 3}, Themes: []int16{1, 5, 11}, Region: 15, TrancheAge: 2, Sexe: 1, Inscrit: 3}
	if r.Version != 1 || r.Q1 != 2 || r.Q4 != 3 || r.Region != 15 || r.Inscrit != 3 ||
		!slices.Equal(r.Q7, attendu.Q7) || !slices.Equal(r.Themes, attendu.Themes) {
		t.Errorf("réponse %+v, attendu %+v", r, attendu)
	}
	// Aller-retour par le formulaire (récapitulatif).
	r2, errs := q.Lire(q.Valeurs(r))
	if errs != nil || !slices.Equal(r2.Themes, r.Themes) || r2.Q5 != r.Q5 || r2.Sexe != r.Sexe {
		t.Errorf("aller-retour : %+v, %v", r2, errs)
	}
}

func TestJeNeSaisPasEnChoixMultiple(t *testing.T) {
	q := V1()
	f := complet()
	f["q7"] = []string{"0"}
	f["themes"] = []string{"0"}
	r, errs := q.Lire(f)
	if errs != nil {
		t.Fatal(errs)
	}
	if r.Q7 == nil || len(r.Q7) != 0 || r.Themes == nil || len(r.Themes) != 0 {
		t.Errorf("« Je ne sais pas » doit donner des tableaux vides : %+v", r)
	}
	v := q.Valeurs(r)
	if v.Get("q7") != "0" || v.Get("themes") != "0" {
		t.Errorf("valeurs : %v", v)
	}
}

func TestErreurs(t *testing.T) {
	q := V1()
	cas := []struct {
		modif func(url.Values)
		champ string
		cle   string
	}{
		{func(f url.Values) { delete(f, "q3") }, "q3", ErrObligatoire},
		{func(f url.Values) { f["q1"] = []string{"9"} }, "q1", ErrInvalide},
		{func(f url.Values) { f["q1"] = []string{"1", "2"} }, "q1", ErrInvalide},
		{func(f url.Values) { f["q2"] = []string{"x"} }, "q2", ErrInvalide},
		{func(f url.Values) { f["themes"] = []string{"1", "2"} }, "themes", ErrThemes},
		{func(f url.Values) { f["themes"] = []string{"1", "2", "3", "4"} }, "themes", ErrThemes},
		{func(f url.Values) { f["themes"] = []string{"1", "2", "0"} }, "themes", ErrExclusif},
		{func(f url.Values) { f["themes"] = []string{"1", "2", "12"} }, "themes", ErrInvalide},
		{func(f url.Values) { f["q7"] = []string{"0", "1"} }, "q7", ErrExclusif},
		{func(f url.Values) { delete(f, "q7") }, "q7", ErrObligatoire},
		{func(f url.Values) { f["region"] = []string{"16"} }, "region", ErrInvalide},
		{func(f url.Values) { delete(f, "sexe") }, "sexe", ErrObligatoire},
		{func(f url.Values) { delete(f, ChampMajeur) }, ChampMajeur, ErrMajeur},
		{func(f url.Values) { f[ChampMajeur] = []string{"0"} }, ChampMajeur, ErrMajeur},
	}
	for _, c := range cas {
		f := complet()
		c.modif(f)
		_, errs := q.Lire(f)
		if errs[c.champ] != c.cle {
			t.Errorf("%s : erreurs %v, attendu %s", c.champ, errs, c.cle)
		}
	}
}

func TestDefinitionV1(t *testing.T) {
	q := V1()
	nb := map[string]int{"q1": 5, "q2": 4, "q3": 5, "q4": 4, "q5": 5, "q6": 4, "q7": 5,
		"themes": 12, "region": 16, "tranche_age": 6, "sexe": 3, "inscrit": 4}
	for nom, n := range nb {
		x := q.Question(nom)
		if x == nil || len(x.Choix) != n {
			t.Errorf("%s : %d choix attendus, %+v", nom, n, x)
		}
		codes := map[int16]bool{}
		for _, c := range x.Choix {
			if codes[c.Code] {
				t.Errorf("%s : code %d en double", nom, c.Code)
			}
			codes[c.Code] = true
		}
	}
	if len(q.Questions) != len(nb) {
		t.Errorf("%d questions", len(q.Questions))
	}
	// Q1, Q2 et Q5 sont provisoires (spec §15.1), et seulement elles.
	ps := q.Provisoires()
	if len(ps) != 3 || !strings.Contains(ps[0], "q1") || !strings.Contains(ps[1], "q2") || !strings.Contains(ps[2], "q5") {
		t.Errorf("provisoires : %v", ps)
	}
}
