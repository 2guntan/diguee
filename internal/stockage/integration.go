package stockage

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/integration"
)

// Construire choisit l'implémentation d'après DIGUEE_STOCKAGE et la déclare
// dans le registre des intégrations (« stockage ») : le mode fake y est
// déclaré comme tel, donc refusé en production par la garde.
func Construire(c *config.Config, r *integration.Registre) (Stockage, error) {
	s := c.Stockage
	var st Stockage
	switch s.Mode {
	case integration.ModeFake:
		chemin := s.Dossier
		if chemin == "" {
			chemin = filepath.Join(os.TempDir(), "diguee-archives")
		}
		d, err := NouveauDossier(chemin)
		if err != nil {
			return nil, err
		}
		st = d
	case "ovh":
		st = &S3{
			PointAcces: s.PointAcces, Region: s.Region, Compartiment: s.Compartiment,
			CleAcces: s.CleAcces, CleSecrete: s.CleSecrete,
			Client: &http.Client{Timeout: s.Delai}, TailleMax: c.ArchiveTailleMax,
		}
	default:
		return nil, fmt.Errorf("DIGUEE_STOCKAGE : mode %q non pris en charge", s.Mode)
	}
	if err := r.Declarer(integration.Declaration{Nom: "stockage", Mode: s.Mode, Variable: "DIGUEE_STOCKAGE"}); err != nil {
		return nil, err
	}
	return st, nil
}
