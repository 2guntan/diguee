package publication

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/2guntan/diguee/internal/config"
)

// Population est la composition de la population de référence (ANSD), à
// laquelle la publication compare celle de l'échantillon (spec §8, règle 6).
// Elle est lue dans le fichier DIGUEE_POPULATION_ANSD ; tant qu'elle vaut
// « À COMPLÉTER », la page des résultats l'affiche comme non disponible et la
// garde refuse la production.
type Population struct {
	Source    string                     // citation de la source, publiée avec les résultats
	Effectifs map[string]map[int16]int64 // critère → code de la catégorie → effectif
}

// CategoriesPopulation donne, pour chaque critère attendu dans le fichier,
// les codes des catégories (ceux du questionnaire v1) : les 14 régions (sans
// la diaspora), les 5 tranches d'âge, les 2 sexes. L'inscription sur les
// listes électorales n'est pas comparée.
var CategoriesPopulation = map[string][]int16{
	"region":      {1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14},
	"tranche_age": {1, 2, 3, 4, 5},
	"sexe":        {1, 2},
}

// fichierPopulation est le format du fichier, par exemple :
//
//	{
//	  "source": "ANSD, RGPH-5 (2023), population de 18 ans et plus",
//	  "region": {"1": 2400000, "2": 1100000, …, "14": 380000},
//	  "tranche_age": {"1": 2300000, …, "5": 600000},
//	  "sexe": {"1": 4700000, "2": 4500000}
//	}
//
// Les effectifs sont des entiers positifs ; les codes sont ceux du
// questionnaire (docs/À faire par le porteur.md, §2.3).
type fichierPopulation struct {
	Source     string           `json:"source"`
	Region     map[string]int64 `json:"region"`
	TrancheAge map[string]int64 `json:"tranche_age"`
	Sexe       map[string]int64 `json:"sexe"`
}

// ChargerPopulation lit le fichier de population. Une valeur « À COMPLÉTER »
// renvoie nil, sans erreur : la population n'est pas encore fournie.
func ChargerPopulation(t config.Texte) (*Population, error) {
	if t.Provisoire() {
		return nil, nil
	}
	b, err := os.ReadFile(t.Valeur)
	if err != nil {
		return nil, fmt.Errorf("%s : %w", t.Variable, err)
	}
	p, err := LirePopulation(b)
	if err != nil {
		return nil, fmt.Errorf("%s (%s) : %w", t.Variable, t.Valeur, err)
	}
	return p, nil
}

// LirePopulation lit et contrôle le contenu d'un fichier de population.
func LirePopulation(b []byte) (*Population, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var f fichierPopulation
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("JSON invalide : %w", err)
	}
	src := strings.TrimSpace(f.Source)
	if src == "" || src == config.ACompleter {
		return nil, errors.New("« source » doit citer la source des chiffres")
	}
	p := &Population{Source: src, Effectifs: map[string]map[int16]int64{}}
	var errs []error
	fichier := map[string]map[string]int64{"region": f.Region, "tranche_age": f.TrancheAge, "sexe": f.Sexe}
	for _, nom := range []string{"region", "tranche_age", "sexe"} {
		m := fichier[nom]
		attendus := CategoriesPopulation[nom]
		e := map[int16]int64{}
		for _, k := range slices.Sorted(maps.Keys(m)) {
			v := m[k]
			c, err := strconv.ParseInt(k, 10, 16)
			if err != nil || !slices.Contains(attendus, int16(c)) {
				errs = append(errs, fmt.Errorf("« %s » : code %q inconnu", nom, k))
				continue
			}
			if v <= 0 {
				errs = append(errs, fmt.Errorf("« %s » : l'effectif du code %s doit être positif", nom, k))
			}
			e[int16(c)] = v
		}
		for _, c := range attendus {
			if _, ok := e[c]; !ok {
				errs = append(errs, fmt.Errorf("« %s » : code %d manquant", nom, c))
			}
		}
		p.Effectifs[nom] = e
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return p, nil
}
