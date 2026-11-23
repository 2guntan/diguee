package i18n

import (
	"testing"
	"time"
)

func charger(t *testing.T) *Catalogues {
	t.Helper()
	cs, err := Charger()
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func TestSeulLeFrancaisEstDisponible(t *testing.T) {
	cs := charger(t)
	if got := cs.Disponibles(); len(got) != 1 || got[0] != "fr" {
		t.Errorf("langues disponibles : %v", got)
	}
	// Le wolof et le pulaar sont prévus mais incomplets : on retombe sur le français.
	for _, code := range []string{"wo", "ff", "en", ""} {
		if c := cs.Choisir(code); c.Code != "fr" {
			t.Errorf("Choisir(%q) = %s", code, c.Code)
		}
	}
}

func TestReplisEtCleInconnue(t *testing.T) {
	cs := charger(t)
	wo := cs.parCode["wo"]
	if got := wo.T("langue.nom"); got != "Wolof" {
		t.Errorf("wo langue.nom = %q", got)
	}
	if got := wo.T("nav.methodologie"); got != "Méthodologie" {
		t.Errorf("repli sur le français attendu : %q", got)
	}
	if got := cs.Choisir("fr").T("cle.absente"); got != "⟦cle.absente⟧" {
		t.Errorf("clé absente : %q", got)
	}
}

func TestMiseEnForme(t *testing.T) {
	fr := charger(t).Choisir("fr")
	cas := [][2]string{
		{fr.Nombre(0), "0"},
		{fr.Nombre(1000), "1\u202f000"},
		{fr.Nombre(1234567), "1\u202f234\u202f567"},
		{fr.Pluriel("accueil.compteur", 0), "0 réponse reçue."},
		{fr.Pluriel("accueil.compteur", 1500), "1\u202f500 réponses reçues."},
		{fr.Date(time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC)), "15 janvier 2027"},
		{fr.Heure(6 * time.Hour), "06:00 GMT"},
		{fr.Duree(10 * time.Minute), "10 minutes"},
		{fr.Duree(time.Hour), "1 heure"},
		{fr.Duree(90 * time.Minute), "90 minutes"},
	}
	for _, c := range cas {
		if got, attendu := c[0], c[1]; got != attendu {
			t.Errorf("%q, attendu %q", got, attendu)
		}
	}
}
