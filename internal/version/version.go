// Package version donne la version du programme et le SHA-256 de son
// exécutable, affichés au pied de chaque page et inscrits au journal public à
// chaque mise en production.
package version

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"runtime/debug"
)

// version est fixée au build par le Makefile :
//
//	-ldflags "-X github.com/2guntan/diguee/internal/version.version=$(git describe --tags --always --dirty)"
var version string

// Version renvoie la version fixée au build ou, à défaut, la révision git
// enregistrée par la chaîne de compilation (« +modifié » si l'arbre de travail
// n'était pas propre), ou « dev ».
func Version() string {
	if version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	var rev, modifie string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modifie = s.Value
		}
	}
	if rev == "" {
		return "dev"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if modifie == "true" {
		rev += "+modifié"
	}
	return rev
}

// SHA256Executable calcule le SHA-256 de l'exécutable en cours, tel qu'il est
// sur le disque. Deux builds reproductibles du même commit donnent la même valeur.
func SHA256Executable() (string, error) {
	chemin, err := os.Executable()
	if err != nil {
		return "", err
	}
	return SHA256Fichier(chemin)
}

// SHA256Fichier calcule le SHA-256 d'un fichier, en hexadécimal.
func SHA256Fichier(chemin string) (string, error) {
	f, err := os.Open(chemin)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
