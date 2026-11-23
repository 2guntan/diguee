// Package journaltest fournit un magasin du journal en mémoire, pour les tests
// qui n'ont pas besoin de PostgreSQL. Il n'est jamais utilisé par le serveur.
package journaltest

import (
	"context"
	"sync"

	"github.com/2guntan/diguee/internal/journal"
)

// Memoire garde les entrées en mémoire.
type Memoire struct {
	mu      sync.Mutex
	entrees []journal.Entree
}

type vue struct{ m *Memoire }

func (v vue) Derniere(context.Context) (*journal.Entree, error) {
	if len(v.m.entrees) == 0 {
		return nil, nil
	}
	e := v.m.entrees[len(v.m.entrees)-1]
	return &e, nil
}

func (v vue) DerniereDuType(_ context.Context, t journal.Type) (*journal.Entree, error) {
	for i := len(v.m.entrees) - 1; i >= 0; i-- {
		if v.m.entrees[i].Type == t {
			e := v.m.entrees[i]
			return &e, nil
		}
	}
	return nil, nil
}

// AjouterAuJournal implémente journal.Magasin.
func (m *Memoire) AjouterAuJournal(ctx context.Context, preparer func(context.Context, journal.Vue) (*journal.Entree, error)) (*journal.Entree, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, err := preparer(ctx, vue{m})
	if err != nil || e == nil {
		return nil, err
	}
	m.entrees = append(m.entrees, *e)
	return e, nil
}

// CompterJournal implémente journal.Magasin.
func (m *Memoire) CompterJournal(context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return int64(len(m.entrees)), nil
}

// LireJournal implémente journal.Magasin.
func (m *Memoire) LireJournal(_ context.Context, premier, dernier int64) ([]journal.Entree, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var es []journal.Entree
	for _, e := range m.entrees {
		if e.Numero >= premier && e.Numero <= dernier {
			es = append(es, e)
		}
	}
	return es, nil
}
