package version

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSHA256Fichier(t *testing.T) {
	chemin := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(chemin, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := SHA256Fichier(chemin)
	if err != nil {
		t.Fatal(err)
	}
	if got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("SHA-256 = %s", got)
	}
}

func TestVersionFixeeAuBuild(t *testing.T) {
	defer func(v string) { version = v }(version)
	version = "v1.2.3"
	if got := Version(); got != "v1.2.3" {
		t.Errorf("Version() = %q", got)
	}
	version = ""
	if Version() == "" {
		t.Error("Version() ne doit jamais être vide")
	}
}

func TestSHA256Executable(t *testing.T) {
	s, err := SHA256Executable()
	if err != nil || len(s) != 64 {
		t.Errorf("SHA256Executable() = %q, %v", s, err)
	}
}
