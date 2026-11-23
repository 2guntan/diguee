// Package pdftest vérifie, dans les tests, les PDF écrits par le paquet pdf.
package pdftest

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Verifier contrôle la structure d'un PDF : en-tête, fin de fichier, table
// des références croisées (chaque position pointe sur son objet) et trailer.
func Verifier(t testing.TB, b []byte) {
	t.Helper()
	if !bytes.HasPrefix(b, []byte("%PDF-1.4\n")) || !bytes.HasSuffix(b, []byte("%%EOF\n")) {
		t.Fatal("en-tête ou fin de fichier absents")
	}
	m := regexp.MustCompile(`startxref\n(\d+)\n%%EOF\n$`).FindSubmatch(b)
	if m == nil {
		t.Fatal("startxref absent")
	}
	xref, _ := strconv.Atoi(string(m[1]))
	if !bytes.HasPrefix(b[xref:], []byte("xref\n0 ")) {
		t.Fatalf("startxref (%d) ne pointe pas sur la table", xref)
	}
	lignes := strings.Split(string(b[xref:]), "\n")
	var n int
	if _, err := fmtSscan(lignes[1], &n); err != nil || n < 2 {
		t.Fatalf("en-tête de la table : %q", lignes[1])
	}
	if lignes[2] != "0000000000 65535 f " {
		t.Fatalf("première entrée : %q", lignes[2])
	}
	for i := 1; i < n; i++ {
		l := lignes[2+i]
		if len(l)+1 != 20 || !strings.HasSuffix(l, " 00000 n ") {
			t.Fatalf("entrée %d mal formée : %q", i, l)
		}
		pos, _ := strconv.Atoi(l[:10])
		if !bytes.HasPrefix(b[pos:], []byte(strconv.Itoa(i)+" 0 obj\n")) {
			t.Fatalf("l'entrée %d (%d) ne pointe pas sur son objet", i, pos)
		}
	}
	tr := regexp.MustCompile(`trailer\n<< /Size (\d+) /Root 1 0 R /Info (\d+) 0 R /ID \[<[0-9a-f]{32}> <[0-9a-f]{32}>\] >>`).FindSubmatch(b)
	if tr == nil || string(tr[1]) != strconv.Itoa(n) {
		t.Fatalf("trailer : %q", tr)
	}
	// Chaque flux a la longueur annoncée.
	for _, f := range regexp.MustCompile(`(?s)<< /Length (\d+) >>\nstream\n`).FindAllSubmatchIndex(b, -1) {
		longueur, _ := strconv.Atoi(string(b[f[2]:f[3]]))
		if !bytes.HasPrefix(b[f[1]+longueur:], []byte("\nendstream")) {
			t.Fatalf("flux à %d : longueur %d inexacte", f[0], longueur)
		}
	}
}

func fmtSscan(s string, n *int) (int, error) {
	champs := strings.Fields(s)
	if len(champs) != 2 || champs[0] != "0" {
		return 0, strconv.ErrSyntax
	}
	v, err := strconv.Atoi(champs[1])
	*n = v
	return 1, err
}

// TexteDe extrait le texte d'un PDF avec pdftotext, s'il est installé.
func TexteDe(t testing.TB, b []byte) (string, bool) {
	t.Helper()
	outil, err := exec.LookPath("pdftotext")
	if err != nil {
		return "", false
	}
	f := filepath.Join(t.TempDir(), "d.pdf")
	if err := os.WriteFile(f, b, 0o600); err != nil {
		t.Fatal(err)
	}
	sortie, err := exec.Command(outil, "-enc", "UTF-8", f, "-").Output()
	if err != nil {
		t.Fatalf("pdftotext : %v", err)
	}
	return string(sortie), true
}
