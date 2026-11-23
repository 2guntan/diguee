package sauvegarde

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Etat est le fichier d'état écrit après chaque sauvegarde (réussie ou
// non), lu par le serveur pour l'afficher aux administrateurs dans l'espace
// équipe. Il ne contient ni secret ni donnée de la base.
type Etat struct {
	DerniereTentative time.Time  `json:"derniere_tentative"`
	Reussie           bool       `json:"reussie"`
	Erreur            string     `json:"erreur,omitempty"`
	DerniereReussite  *time.Time `json:"derniere_reussite,omitempty"`
	Cle               string     `json:"cle,omitempty"` // de la dernière sauvegarde réussie
	Taille            int64      `json:"taille,omitempty"`
}

// Retard est l'âge au-delà duquel l'absence de sauvegarde réussie est une
// alerte : une par nuit, plus une marge.
const Retard = 27 * time.Hour

// EnAlerte indique s'il faut alerter : dernière tentative en échec, ou
// aucune réussite depuis Retard (minuteur arrêté, commande tuée…).
func (e *Etat) EnAlerte(maintenant time.Time) bool {
	if e == nil || !e.Reussie || e.DerniereReussite == nil {
		return true
	}
	return maintenant.Sub(*e.DerniereReussite) > Retard
}

// LireEtat lit le fichier d'état ; nil, nil s'il n'existe pas encore.
func LireEtat(chemin string) (*Etat, error) {
	b, err := os.ReadFile(chemin)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var e Etat
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, fmt.Errorf("état des sauvegardes illisible : %w", err)
	}
	return &e, nil
}

// Enregistrer met à jour le fichier d'état après une tentative : r est le
// résultat (nil si la sauvegarde n'a pas été envoyée), err l'erreur
// éventuelle. Le fichier est remplacé d'un coup (renommage), lisible par
// tous (0644) : le serveur, sous un autre utilisateur, le lit.
func Enregistrer(chemin string, maintenant time.Time, r *Resultat, err error) error {
	e, lerr := LireEtat(chemin)
	if lerr != nil || e == nil {
		e = &Etat{}
	}
	e.DerniereTentative = maintenant.UTC()
	e.Reussie = err == nil
	e.Erreur = ""
	if err != nil {
		msg := err.Error()
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		e.Erreur = msg
	}
	if r != nil {
		d := r.Date
		e.DerniereReussite = &d
		e.Cle = r.Cle
		e.Taille = r.Taille
	}
	b, jerr := json.MarshalIndent(e, "", "  ")
	if jerr != nil {
		return jerr
	}
	tmp, terr := os.CreateTemp(filepath.Dir(chemin), ".etat-*")
	if terr != nil {
		return terr
	}
	defer os.Remove(tmp.Name())
	if _, werr := tmp.Write(append(b, '\n')); werr != nil {
		tmp.Close()
		return werr
	}
	if cerr := tmp.Chmod(0o644); cerr != nil {
		tmp.Close()
		return cerr
	}
	if cerr := tmp.Close(); cerr != nil {
		return cerr
	}
	return os.Rename(tmp.Name(), chemin)
}
