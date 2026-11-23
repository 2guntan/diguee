// Package garde décide si le serveur peut démarrer : en production, aucune
// valeur ne doit valoir « À COMPLÉTER » et aucune intégration ne doit être en
// mode « fake », et rien d'autre ne doit rester provisoire (Source).
package garde

import (
	"fmt"
	"strings"

	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/integration"
)

// Source décrit ce qui, hors de la configuration, peut rester provisoire : par
// exemple les choix du questionnaire qui restent à valider.
type Source interface {
	Provisoires() []string
}

// Manques liste ce qui empêche une mise en production, un élément par ligne.
func Manques(c *config.Config, r *integration.Registre, autres ...Source) []string {
	var ms []string
	for _, nom := range c.Provisoires() {
		ms = append(ms, fmt.Sprintf("%s vaut « %s »", nom, config.ACompleter))
	}
	// Le TLS s'arrête sur notre serveur (ADR 0007) : jamais de HTTP simple
	// en production.
	if c.TLS.Mode != "acme" {
		ms = append(ms, fmt.Sprintf("DIGUEE_TLS vaut « %s » : HTTPS (acme) obligatoire", c.TLS.Mode))
	}
	for _, d := range r.Fausses() {
		ms = append(ms, fmt.Sprintf("l'intégration %s est en mode %s (réglage : %s)", d.Nom, d.Mode, d.Variable))
	}
	for _, a := range autres {
		ms = append(ms, a.Provisoires()...)
	}
	return ms
}

// Verifier renvoie une erreur qui liste tous les manques si l'environnement est
// la production. En développement, elle ne renvoie jamais d'erreur.
func Verifier(c *config.Config, r *integration.Registre, autres ...Source) error {
	if c.Env != config.Production {
		return nil
	}
	ms := Manques(c, r, autres...)
	if len(ms) == 0 {
		return nil
	}
	return fmt.Errorf("démarrage refusé en production :\n- %s", strings.Join(ms, "\n- "))
}
