package whatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// PointAccesGraph est l'adresse de l'API Graph de Meta.
const PointAccesGraph = "https://graph.facebook.com"

// tailleMaxReponse borne la lecture d'une réponse de l'API Graph.
const tailleMaxReponse = 64 << 10

// OptionsEnvoyeurMeta règle l'envoi des codes par l'API Cloud.
type OptionsEnvoyeurMeta struct {
	PointAcces string        // PointAccesGraph par défaut (un faux serveur dans les tests)
	VersionAPI string        // DIGUEE_META_VERSION_API, par exemple v23.0
	IDNumero   string        // DIGUEE_META_ID_NUMERO : numéro de Diguee qui envoie
	JetonAcces string        // DIGUEE_META_JETON_ACCES ; secret
	Modele     string        // DIGUEE_META_MODELE_CODE : modèle d'authentification approuvé
	Langue     string        // DIGUEE_META_LANGUE_MODELE
	Delai      time.Duration // DIGUEE_META_DELAI
	Client     *http.Client  // un client avec Delai par défaut
}

// EnvoyeurMeta envoie le code de secours (spec §5.2) par un modèle de message
// d'authentification de l'API Cloud de Meta, avec un bouton « Copier le
// code ». Il implémente codes.Envoyeur. Ses erreurs ne citent ni le numéro,
// ni le code, ni le jeton d'accès, ni le corps de la réponse : seulement le
// statut HTTP et le code d'erreur numérique de Meta.
type EnvoyeurMeta struct {
	opt OptionsEnvoyeurMeta
	url string
}

// NouvelEnvoyeurMeta prépare l'envoyeur.
func NouvelEnvoyeurMeta(opt OptionsEnvoyeurMeta) (*EnvoyeurMeta, error) {
	if opt.VersionAPI == "" || opt.IDNumero == "" || opt.JetonAcces == "" || opt.Modele == "" || opt.Langue == "" {
		return nil, errors.New("envoi des codes WhatsApp : options incomplètes")
	}
	if opt.PointAcces == "" {
		opt.PointAcces = PointAccesGraph
	}
	if opt.Delai <= 0 {
		opt.Delai = 10 * time.Second
	}
	if opt.Client == nil {
		opt.Client = &http.Client{Timeout: opt.Delai}
	}
	u := strings.TrimRight(opt.PointAcces, "/") + "/" + opt.VersionAPI + "/" + opt.IDNumero + "/messages"
	return &EnvoyeurMeta{opt: opt, url: u}, nil
}

// String ne montre jamais le jeton d'accès.
func (e *EnvoyeurMeta) String() string {
	return fmt.Sprintf("envoi WhatsApp(modèle %s, %s)", e.opt.Modele, e.opt.Langue)
}

type parametre struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type composant struct {
	Type       string      `json:"type"`
	SubType    string      `json:"sub_type,omitempty"`
	Index      string      `json:"index,omitempty"`
	Parameters []parametre `json:"parameters"`
}

type messageModele struct {
	MessagingProduct string `json:"messaging_product"`
	RecipientType    string `json:"recipient_type"`
	To               string `json:"to"`
	Type             string `json:"type"`
	Template         struct {
		Name     string `json:"name"`
		Language struct {
			Code string `json:"code"`
		} `json:"language"`
		Components []composant `json:"components"`
	} `json:"template"`
}

// CorpsCode fabrique le corps de la requête d'envoi : un modèle
// d'authentification, le code dans le corps du message et dans le bouton
// « Copier le code » (que Meta range parmi les boutons d'URL, d'indice 0).
// numero est normalisé (« +221… ») ; Meta attend l'indicatif sans « + ».
func CorpsCode(modele, langue, numero, code string) ([]byte, error) {
	var m messageModele
	m.MessagingProduct, m.RecipientType, m.Type = "whatsapp", "individual", "template"
	m.To = strings.TrimPrefix(numero, "+")
	m.Template.Name = modele
	m.Template.Language.Code = langue
	m.Template.Components = []composant{
		{Type: "body", Parameters: []parametre{{Type: "text", Text: code}}},
		{Type: "button", SubType: "url", Index: "0", Parameters: []parametre{{Type: "text", Text: code}}},
	}
	return json.Marshal(m)
}

// ErreurEnvoi décrit un refus de l'API Graph, sans aucune donnée personnelle.
type ErreurEnvoi struct {
	Statut int // statut HTTP
	Code   int // error.code de Meta, 0 s'il est absent
}

func (e *ErreurEnvoi) Error() string {
	return fmt.Sprintf("API Graph : statut %d, code d'erreur Meta %d", e.Statut, e.Code)
}

// Envoyer envoie le code au numéro.
func (e *EnvoyeurMeta) Envoyer(ctx context.Context, numero, code string) error {
	corps, err := CorpsCode(e.opt.Modele, e.opt.Langue, numero, code)
	if err != nil {
		return errors.New("API Graph : corps de la requête impossible à fabriquer")
	}
	ctx, annuler := context.WithTimeout(ctx, e.opt.Delai)
	defer annuler()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(corps))
	if err != nil {
		return errors.New("API Graph : requête impossible à préparer")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.opt.JetonAcces)
	rep, err := e.opt.Client.Do(req)
	if err != nil {
		// L'erreur de net/http cite l'URL (sans le jeton, qui est dans un
		// en-tête), jamais le corps ; on n'en garde que la nature.
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			return errors.New("API Graph : délai d'attente dépassé")
		case errors.Is(err, context.Canceled):
			return context.Canceled
		}
		return errors.New("API Graph : connexion impossible")
	}
	defer rep.Body.Close()
	brut, _ := io.ReadAll(io.LimitReader(rep.Body, tailleMaxReponse))
	if rep.StatusCode != http.StatusOK {
		var r struct {
			Error struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(brut, &r)
		return &ErreurEnvoi{Statut: rep.StatusCode, Code: r.Error.Code}
	}
	var r struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if json.Unmarshal(brut, &r) != nil || len(r.Messages) == 0 || r.Messages[0].ID == "" {
		return errors.New("API Graph : réponse inattendue")
	}
	return nil
}
