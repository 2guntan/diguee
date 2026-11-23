package observatoire

import (
	"testing"
	"time"
)

func TestTypeDeParSignature(t *testing.T) {
	for _, c := range []struct {
		contenu, typ string
	}{
		{"%PDF-1.7", "application/pdf"}, {"\x89PNG\r\n\x1a\n", "image/png"}, {"\xff\xd8\xff\xe0", "image/jpeg"},
		{"<svg onload=alert(1)>", ""}, {"<html>", ""}, {"GIF89a", ""}, {"", ""},
	} {
		if typ, ok := TypeDe([]byte(c.contenu)); typ != c.typ || ok != (c.typ != "") {
			t.Errorf("%q : %q %v", c.contenu, typ, ok)
		}
	}
}

func TestSeulsTenuEtEnCoursSontPubliablesSansPreavis(t *testing.T) {
	for _, v := range Valeurs {
		if v.PubliableSansPreavis() != (v == Tenu || v == EnCours) || !v.Valide() {
			t.Errorf("%s", v)
		}
	}
	if Valeur("annule").Valide() {
		t.Error("valeur inconnue acceptée")
	}
}

func TestURLEtMandat(t *testing.T) {
	for u, ok := range map[string]bool{"https://exemple.sn/a?b=c": true, "http://exemple.sn": true,
		"javascript:alert(1)": false, "https://a b": false, "https://user:mdp@exemple.sn": false, "/relatif": false, "ftp://x": false} {
		if URLValide(u) != ok {
			t.Errorf("%q", u)
		}
	}
	m := Mandat{Debut: time.Date(2022, 1, 23, 0, 0, 0, 0, time.UTC), Fin: time.Date(2027, 1, 23, 0, 0, 0, 0, time.UTC)}
	if lu, ok := LireMandat(m.Cle()); !ok || lu != m {
		t.Errorf("%s", m.Cle())
	}
	for _, s := range []string{"", "2027-01-23_2022-01-23", "2022-01-23-2027-01-23", "x"} {
		if _, ok := LireMandat(s); ok {
			t.Errorf("%q accepté", s)
		}
	}
}

func TestEtapes(t *testing.T) {
	s := Statut{}
	etapes := []Etape{Brouillon}
	s.Proposition = &Acte{}
	etapes = append(etapes, s.Etape())
	s.Validation = &Acte{}
	etapes = append(etapes, s.Etape())
	s.Publication = &Publication{}
	etapes = append(etapes, s.Etape())
	if etapes[1] != Propose || etapes[2] != Valide || etapes[3] != Publie {
		t.Errorf("%v", etapes)
	}
	if (Statut{Proposition: &Acte{}, Rejet: &Rejet{}}).Etape() != Rejete {
		t.Error("rejet")
	}
}
