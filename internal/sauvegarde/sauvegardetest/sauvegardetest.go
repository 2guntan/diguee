// Package sauvegardetest aide les tests des sauvegardes (ticket #15) : une
// paire de clés age tirée pour le test, et la présence des programmes.
package sauvegardetest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Programmes exige age, age-keygen, pg_dump et pg_restore : le test est
// sauté s'il en manque un, sauf en intégration continue (CI non vide), où
// il échoue.
func Programmes(t testing.TB, noms ...string) {
	t.Helper()
	if len(noms) == 0 {
		noms = []string{"age", "age-keygen", "pg_dump", "pg_restore"}
	}
	for _, n := range noms {
		if _, err := exec.LookPath(n); err != nil {
			if os.Getenv("CI") != "" {
				t.Fatalf("%s absent (obligatoire en intégration continue)", n)
			}
			t.Skipf("%s absent : test sauté", n)
		}
	}
}

// Cles tire une paire de clés age de test avec age-keygen et renvoie le
// fichier de la clé privée (identité) et la clé publique (age1…).
func Cles(t testing.TB) (identite, destinataire string) {
	t.Helper()
	Programmes(t, "age-keygen")
	identite = filepath.Join(t.TempDir(), "cle-de-test.txt")
	if out, err := exec.Command("age-keygen", "-o", identite).CombinedOutput(); err != nil {
		t.Fatalf("age-keygen : %v\n%s", err, out)
	}
	out, err := exec.Command("age-keygen", "-y", identite).Output()
	if err != nil {
		t.Fatalf("age-keygen -y : %v", err)
	}
	return identite, strings.TrimSpace(string(out))
}
