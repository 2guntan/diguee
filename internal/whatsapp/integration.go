package whatsapp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/2guntan/diguee/internal/codes"
	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/integration"
)

// Integration réunit les réglages de l'API Cloud de Meta : le webhook de la
// vérification inversée et l'envoi du code de secours (spec §5.2). En mode
// « fake », ce sont des valeurs de développement publiques : la simulation
// (Simuler) signe avec elles, les codes sont écrits dans la console
// (codes.Console), et le mode est refusé en production.
type Integration struct {
	Mode         string // fake ou meta
	IDNumero     string // phone_number_id attendu dans les notifications
	SecretApp    []byte // vérifie X-Hub-Signature-256 ; secret
	JetonWebhook string // hub.verify_token de la poignée de main ; secret
	// Envoyeur envoie le code de secours : EnvoyeurMeta en mode « meta »,
	// codes.Console en mode « fake » (nil si DIGUEE_ENV=production : la
	// console ne peut pas exister en production).
	Envoyeur codes.Envoyeur
}

// Valeurs du mode « fake ». Elles sont publiques et ne protègent rien.
const (
	idNumeroDeveloppement     = "100000000000000"
	secretDeveloppement       = "diguee : secret d'application de developpement, interdit en production"
	jetonWebhookDeveloppement = "diguee-jeton-webhook-developpement"
)

// Fausse indique le mode de développement.
func (i *Integration) Fausse() bool { return i.Mode == integration.ModeFake }

// String ne montre jamais les secrets.
func (i *Integration) String() string {
	return fmt.Sprintf("whatsapp(%s, numéro %s)", i.Mode, i.IDNumero)
}

// Construire choisit l'intégration d'après DIGUEE_WHATSAPP et la déclare dans
// le registre des intégrations.
func Construire(c *config.Config, r *integration.Registre) (*Integration, error) {
	var i *Integration
	switch c.WhatsApp {
	case integration.ModeFake:
		i = &Integration{Mode: c.WhatsApp, IDNumero: idNumeroDeveloppement,
			SecretApp: []byte(secretDeveloppement), JetonWebhook: jetonWebhookDeveloppement}
		if c.Env != config.Production {
			console, err := codes.NouvelleConsole("WhatsApp", os.Stderr, false)
			if err != nil {
				return nil, err
			}
			i.Envoyeur = console
		}
	case "meta":
		i = &Integration{Mode: c.WhatsApp, IDNumero: c.Meta.IDNumero,
			SecretApp: []byte(c.Meta.SecretApp), JetonWebhook: c.Meta.JetonWebhook}
		e, err := NouvelEnvoyeurMeta(OptionsEnvoyeurMeta{
			VersionAPI: c.Meta.VersionAPI, IDNumero: c.Meta.IDNumero, JetonAcces: c.Meta.JetonAcces,
			Modele: c.Meta.ModeleCode, Langue: c.Meta.LangueModele, Delai: c.Meta.Delai,
		})
		if err != nil {
			return nil, err
		}
		i.Envoyeur = e
	default:
		return nil, fmt.Errorf("DIGUEE_WHATSAPP : mode %q non pris en charge", c.WhatsApp)
	}
	if err := r.Declarer(integration.Declaration{Nom: "whatsapp", Mode: c.WhatsApp, Variable: "DIGUEE_WHATSAPP"}); err != nil {
		return nil, err
	}
	return i, nil
}

// Notification fabrique le corps d'une notification de l'API Cloud, au format
// de Meta, pour un message texte reçu d'un expéditeur (« 221771234567 »).
func Notification(idNumero, expediteur, texte string) []byte {
	type obj = map[string]any
	n := obj{
		"object": "whatsapp_business_account",
		"entry": []obj{{
			"id": "0",
			"changes": []obj{{
				"field": "messages",
				"value": obj{
					"messaging_product": "whatsapp",
					"metadata":          obj{"display_phone_number": "0", "phone_number_id": idNumero},
					"contacts":          []obj{{"profile": obj{"name": "-"}, "wa_id": expediteur}},
					"messages": []obj{{
						"from": expediteur, "id": "wamid.simulation", "timestamp": "0",
						"type": "text", "text": obj{"body": texte},
					}},
				},
			}},
		}},
	}
	b, _ := json.Marshal(n)
	return b
}

// Simuler joue le rôle de Meta : il fabrique la notification d'un message
// envoyé par expediteur, la signe avec le secret de l'intégration et la
// remet au gestionnaire h (le vrai webhook). Il renvoie le statut HTTP. Il
// sert à la page de développement et aux tests ; il est refusé hors du mode
// « fake ».
func (i *Integration) Simuler(h http.Handler, expediteur, texte string) (int, error) {
	if !i.Fausse() {
		return 0, fmt.Errorf("la simulation n'existe qu'en mode %s", integration.ModeFake)
	}
	corps := Notification(i.IDNumero, expediteur, texte)
	req, err := http.NewRequest(http.MethodPost, CheminWebhook, bytes.NewReader(corps))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hub-Signature-256", Signature(corps, i.SecretApp))
	rep := &reponseSimulee{entetes: http.Header{}, statut: http.StatusOK}
	h.ServeHTTP(rep, req)
	return rep.statut, nil
}

// reponseSimulee recueille le statut de la réponse du webhook et jette le reste.
type reponseSimulee struct {
	entetes http.Header
	statut  int
	ecrit   bool
}

func (r *reponseSimulee) Header() http.Header { return r.entetes }

func (r *reponseSimulee) Write(b []byte) (int, error) {
	r.ecrit = true
	return len(b), nil
}

func (r *reponseSimulee) WriteHeader(statut int) {
	if !r.ecrit {
		r.statut, r.ecrit = statut, true
	}
}
