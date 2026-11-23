// Package sms choisit l'envoyeur des codes par SMS, le parcours de dernier
// recours (spec §5.3, ADR 0004, ticket #10), d'après DIGUEE_SMS.
//
// Le fournisseur n'est pas encore choisi : ce doit être un fournisseur qui a
// un accord direct avec les opérateurs sénégalais (ADR 0004). Seul le mode
// « fake » existe : codes.Console écrit le code dans la console du serveur
// (jamais le numéro) et le montre sur la page de développement /dev/sms. Il
// est déclaré dans le registre des intégrations, donc refusé en production.
//
// # Brancher le fournisseur
//
// Quand il sera connu, son adaptateur s'ajoute ici, sans toucher au reste :
//
//  1. un type qui implémente codes.Envoyeur (Envoyer(ctx, numero, code)),
//     sur le modèle de whatsapp.EnvoyeurMeta : un appel HTTPS au
//     fournisseur, avec le délai de ctx ; ni le numéro, ni le code, ni la
//     clé d'accès dans les journaux ou dans ses erreurs ; texte du SMS court,
//     sans lien (par exemple « Diguee : votre code est 123456. Il expire dans
//     10 minutes. Ne le communiquez à personne. ») ;
//  2. un mode de plus dans config.Charger (l.choix("DIGUEE_SMS", "fake",
//     "fake", "<nom>")) et ses réglages (DIGUEE_SMS_<NOM>_…, secrets lus
//     avec l.secret) ;
//  3. un cas de plus dans Construire, qui le déclare dans le registre sous
//     son nom (tout autre mode que « fake » est permis en production) ;
//  4. un test contre un faux serveur HTTP (httptest), comme
//     internal/whatsapp/envoi_test.go.
//
// Les limites du parcours (DIGUEE_SMS_PAR_EMPREINTE_24H, DIGUEE_SMS_PAR_HEURE,
// coupe-circuit…) ne dépendent pas du fournisseur ; son plafond de dépenses
// journalier se règle chez lui, à part (docs/À faire par le porteur.md §1.6).
package sms

import (
	"fmt"
	"io"
	"os"

	"github.com/2guntan/diguee/internal/codes"
	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/integration"
)

// Integration est l'envoi des codes par SMS.
type Integration struct {
	Mode string // fake ; le mode du fournisseur reste à ajouter
	// Envoyeur envoie les codes : codes.Console en mode « fake » (nil avec
	// DIGUEE_ENV=production : la console ne peut pas y exister).
	Envoyeur codes.Envoyeur
}

// Fausse indique le mode de développement.
func (i *Integration) Fausse() bool { return i.Mode == integration.ModeFake }

// Construire choisit l'envoyeur d'après DIGUEE_SMS et le déclare dans le
// registre des intégrations. sortie reçoit les codes du mode « fake »
// (os.Stderr si nil).
func Construire(c *config.Config, r *integration.Registre, sortie io.Writer) (*Integration, error) {
	if sortie == nil {
		sortie = os.Stderr
	}
	var i *Integration
	switch c.SMS {
	case integration.ModeFake:
		i = &Integration{Mode: c.SMS}
		if c.Env != config.Production {
			console, err := codes.NouvelleConsole("SMS", sortie, false)
			if err != nil {
				return nil, err
			}
			i.Envoyeur = console
		}
	// case "<fournisseur>" : voir la documentation du paquet.
	default:
		return nil, fmt.Errorf("DIGUEE_SMS : mode %q non pris en charge (fournisseur à brancher, voir internal/sms)", c.SMS)
	}
	if err := r.Declarer(integration.Declaration{Nom: "sms", Mode: c.SMS, Variable: "DIGUEE_SMS"}); err != nil {
		return nil, err
	}
	return i, nil
}
