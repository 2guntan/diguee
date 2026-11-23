package stockage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Dossier est la fausse implémentation : les objets sont des fichiers d'un
// dossier local, nommés par leur empreinte. Développement seulement.
type Dossier struct {
	Chemin string
}

// NouveauDossier crée le dossier s'il n'existe pas.
func NouveauDossier(chemin string) (*Dossier, error) {
	if chemin == "" {
		return nil, errors.New("stockage : dossier vide")
	}
	if err := os.MkdirAll(filepath.Join(chemin, "archives"), 0o700); err != nil {
		return nil, fmt.Errorf("stockage : %w", err)
	}
	return &Dossier{Chemin: chemin}, nil
}

func (d *Dossier) fichier(e Empreinte) string {
	return filepath.Join(d.Chemin, filepath.FromSlash(Cle(e)))
}

// Deposer implémente Stockage : écriture dans un fichier temporaire, puis
// renommage, pour qu'un objet ne soit jamais lu à moitié écrit.
func (d *Dossier) Deposer(_ context.Context, _ string, contenu []byte) (Empreinte, error) {
	e := Calculer(contenu)
	cible := d.fichier(e)
	if _, err := os.Stat(cible); err == nil {
		return e, nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(cible), ".depot-*")
	if err != nil {
		return e, fmt.Errorf("stockage : %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(contenu); err != nil {
		tmp.Close()
		return e, fmt.Errorf("stockage : %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return e, fmt.Errorf("stockage : %w", err)
	}
	if err := tmp.Close(); err != nil {
		return e, fmt.Errorf("stockage : %w", err)
	}
	if err := os.Rename(tmp.Name(), cible); err != nil {
		return e, fmt.Errorf("stockage : %w", err)
	}
	return e, nil
}

// Lire implémente Stockage.
func (d *Dossier) Lire(_ context.Context, e Empreinte) ([]byte, error) {
	b, err := os.ReadFile(d.fichier(e))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrIntrouvable
	}
	if err != nil {
		return nil, fmt.Errorf("stockage : %w", err)
	}
	return verifier(e, b)
}
