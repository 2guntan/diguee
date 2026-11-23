// Package i18n charge les catalogues de textes visibles, un fichier JSON par
// langue dans catalogues/. Le français (fr) est la référence et est livré ;
// le wolof (wo) et le pulaar (ff) sont prévus. Une langue n'est proposée que
// lorsque son catalogue traduit toutes les clés du français.
//
// La langue choisie n'est jamais enregistrée dans une réponse (spec §3.2).
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Reference est la langue dont le catalogue fait foi.
const Reference = "fr"

// Prevues est la liste des langues du projet, dans l'ordre d'affichage.
var Prevues = []string{"fr", "wo", "ff"}

//go:embed catalogues/*.json
var fichiers embed.FS

// Catalogue donne les textes d'une langue.
type Catalogue struct {
	Code   string
	textes map[string]string
	repli  *Catalogue // le français, pour les clés non traduites
}

// Catalogues regroupe les langues du projet.
type Catalogues struct {
	parCode map[string]*Catalogue
}

// Charger lit les catalogues embarqués.
func Charger() (*Catalogues, error) {
	cs := &Catalogues{parCode: map[string]*Catalogue{}}
	for _, code := range Prevues {
		b, err := fichiers.ReadFile("catalogues/" + code + ".json")
		if err != nil {
			return nil, err
		}
		var textes map[string]string
		if err := json.Unmarshal(b, &textes); err != nil {
			return nil, fmt.Errorf("catalogue %s : %w", code, err)
		}
		cs.parCode[code] = &Catalogue{Code: code, textes: textes}
	}
	ref := cs.parCode[Reference]
	for code, c := range cs.parCode {
		if code == Reference {
			continue
		}
		for cle := range c.textes {
			if _, ok := ref.textes[cle]; !ok {
				return nil, fmt.Errorf("catalogue %s : clé %q absente du catalogue %s", code, cle, Reference)
			}
		}
		c.repli = ref
	}
	return cs, nil
}

// Complet indique si le catalogue traduit toutes les clés de la référence.
func (cs *Catalogues) Complet(code string) bool {
	c, ok := cs.parCode[code]
	if !ok {
		return false
	}
	for cle := range cs.parCode[Reference].textes {
		if _, ok := c.textes[cle]; !ok {
			return false
		}
	}
	return true
}

// Disponibles renvoie les langues dont le catalogue est complet.
func (cs *Catalogues) Disponibles() []string {
	var ds []string
	for _, code := range Prevues {
		if cs.Complet(code) {
			ds = append(ds, code)
		}
	}
	return ds
}

// Choisir renvoie le catalogue demandé s'il est disponible, sinon la référence.
func (cs *Catalogues) Choisir(code string) *Catalogue {
	if slices.Contains(cs.Disponibles(), code) {
		return cs.parCode[code]
	}
	return cs.parCode[Reference]
}

// Cles renvoie les clés du catalogue de référence, triées.
func (cs *Catalogues) Cles() []string {
	var ks []string
	for k := range cs.parCode[Reference].textes {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}

// Existe indique si la clé est définie dans le catalogue ou son repli.
func (c *Catalogue) Existe(cle string) bool {
	if _, ok := c.textes[cle]; ok {
		return true
	}
	return c.repli != nil && c.repli.Existe(cle)
}

// T renvoie le texte de la clé. Une clé inconnue donne « ⟦clé⟧ », que les tests
// des pages détectent.
func (c *Catalogue) T(cle string) string {
	if s, ok := c.textes[cle]; ok {
		return s
	}
	if c.repli != nil {
		return c.repli.T(cle)
	}
	return "⟦" + cle + "⟧"
}

// F renvoie le texte de la clé mis en forme avec fmt (verbes %[1]s, %[2]s…, pour
// que chaque langue puisse ordonner les valeurs à sa guise).
func (c *Catalogue) F(cle string, args ...any) string {
	return fmt.Sprintf(c.T(cle), args...)
}

// Pluriel choisit la clé « cle.un » ou « cle.plusieurs » selon n, puis y insère
// n mis en forme. En français, 0 et 1 prennent le singulier.
func (c *Catalogue) Pluriel(cle string, n int64) string {
	suffixe := ".plusieurs"
	if n < 2 {
		suffixe = ".un"
	}
	return c.F(cle+suffixe, c.Nombre(n))
}

// Nombre écrit un entier avec le séparateur de milliers de la langue.
func (c *Catalogue) Nombre(n int64) string {
	s := strconv.FormatInt(n, 10)
	signe := ""
	if strings.HasPrefix(s, "-") {
		signe, s = "-", s[1:]
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteString(c.T("format.separateur_milliers"))
		}
		b.WriteRune(r)
	}
	return signe + b.String()
}

// Date écrit un jour en toutes lettres, par exemple « 15 janvier 2027 ».
func (c *Catalogue) Date(t time.Time) string {
	mois := c.T("mois." + strconv.Itoa(int(t.Month())))
	return c.F("format.date", t.Day(), mois, t.Year())
}

// Heure écrit une heure de la journée donnée comme durée depuis minuit.
func (c *Catalogue) Heure(d time.Duration) string {
	return c.F("format.heure", int(d.Hours()), int(d.Minutes())%60)
}

// Duree écrit une durée en minutes ou en heures.
func (c *Catalogue) Duree(d time.Duration) string {
	if d%time.Hour == 0 {
		return c.Pluriel("format.heures", int64(d/time.Hour))
	}
	return c.Pluriel("format.minutes", int64(d/time.Minute))
}
