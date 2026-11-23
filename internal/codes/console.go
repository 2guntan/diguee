package codes

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Console est la fausse implémentation de l'Envoyeur, pour le développement
// seulement : au lieu d'envoyer le code, elle l'écrit sur sa sortie (la
// console du serveur, hors du journal structuré) et garde le dernier code
// pour la page de développement. C'est la seule exception à la règle « aucune
// donnée dans les journaux » ; le numéro, lui, n'est jamais écrit.
//
// Elle ne peut pas exister en production : NouvelleConsole refuse, et
// l'intégration qui la choisit est déclarée « fake » dans le registre
// (paquet garde).
type Console struct {
	canal  string
	sortie io.Writer

	mu      sync.Mutex
	dernier string
}

// ErrConsoleEnProduction : la console des codes est interdite en production.
var ErrConsoleEnProduction = errors.New("l'affichage des codes dans la console est interdit en production")

// NouvelleConsole prépare la console des codes du canal (« WhatsApp »,
// « SMS »). production vaut vrai avec DIGUEE_ENV=production : elle est
// alors refusée.
func NouvelleConsole(canal string, sortie io.Writer, production bool) (*Console, error) {
	if production {
		return nil, ErrConsoleEnProduction
	}
	if sortie == nil {
		sortie = io.Discard
	}
	return &Console{canal: canal, sortie: sortie}, nil
}

// Envoyer écrit le code, sans le numéro.
func (c *Console) Envoyer(ctx context.Context, _, code string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.dernier = code
	c.mu.Unlock()
	_, err := fmt.Fprintf(c.sortie, "[développement, interdit en production] code %s envoyé : %s\n", c.canal, code)
	return err
}

// Dernier renvoie le dernier code « envoyé » (page de développement, tests).
func (c *Console) Dernier() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dernier
}
