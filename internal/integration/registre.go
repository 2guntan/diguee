// Package integration tient le registre des services externes (KMS, WhatsApp,
// SMS, stockage d'objets, ancrage). Chaque intégration y déclare le mode
// choisi par la configuration au moment où elle est construite ; le paquet
// garde s'en sert pour refuser, en production, toute implémentation « fake ».
package integration

import (
	"fmt"
	"slices"
	"strings"
	"sync"
)

// ModeFake est le nom du mode des fausses implémentations.
const ModeFake = "fake"

// Declaration décrit une intégration et le mode actif.
type Declaration struct {
	Nom      string // par exemple « empreinteur »
	Mode     string // par exemple « fake » ou « ovh »
	Variable string // variable d'environnement qui choisit le mode
}

// Fausse indique une fausse implémentation, interdite en production.
func (d Declaration) Fausse() bool { return d.Mode == ModeFake }

// Registre recense les intégrations actives.
type Registre struct {
	mu   sync.Mutex
	decl []Declaration
}

// Declarer inscrit une intégration. Un même nom ne peut être déclaré qu'une fois.
func (r *Registre) Declarer(d Declaration) error {
	if d.Nom == "" || d.Mode == "" || d.Variable == "" {
		return fmt.Errorf("déclaration incomplète : %+v", d)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.decl {
		if e.Nom == d.Nom {
			return fmt.Errorf("intégration %q déjà déclarée", d.Nom)
		}
	}
	r.decl = append(r.decl, d)
	return nil
}

// Declarations renvoie les intégrations triées par nom.
func (r *Registre) Declarations() []Declaration {
	r.mu.Lock()
	defer r.mu.Unlock()
	ds := slices.Clone(r.decl)
	slices.SortFunc(ds, func(a, b Declaration) int { return strings.Compare(a.Nom, b.Nom) })
	return ds
}

// Fausses renvoie les intégrations en mode « fake », triées par nom.
func (r *Registre) Fausses() []Declaration {
	var fs []Declaration
	for _, d := range r.Declarations() {
		if d.Fausse() {
			fs = append(fs, d)
		}
	}
	return fs
}
