package whatsapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// CheminWebhook est l'adresse du webhook à déclarer chez Meta.
const CheminWebhook = "/webhook/whatsapp"

// tailleMaxCorps borne le corps d'une notification. Meta regroupe au plus
// quelques messages par notification.
const tailleMaxCorps = 256 << 10

// delaiTraitement borne le traitement d'une notification (calcul de
// l'empreinte compris) : Meta attend une réponse rapide et réessaie sinon.
const delaiTraitement = 10 * time.Second

// OptionsWebhook regroupe les réglages du webhook.
type OptionsWebhook struct {
	Integration  *Integration
	Verificateur *Verificateur
	Journal      *slog.Logger // slog.Default() par défaut
}

// Webhook reçoit les notifications de l'API Cloud de Meta.
type Webhook struct {
	opt OptionsWebhook
}

// NouveauWebhook prépare le gestionnaire du webhook.
func NouveauWebhook(opt OptionsWebhook) (*Webhook, error) {
	if opt.Integration == nil || opt.Verificateur == nil || len(opt.Integration.SecretApp) == 0 || opt.Integration.JetonWebhook == "" {
		return nil, errors.New("webhook WhatsApp : options incomplètes")
	}
	if opt.Journal == nil {
		opt.Journal = slog.Default()
	}
	return &Webhook{opt: opt}, nil
}

// Signature renvoie la valeur de l'en-tête X-Hub-Signature-256 pour un corps :
// « sha256= » suivi du HMAC-SHA-256 hexadécimal du corps brut, calculé avec
// le secret de l'application.
func Signature(corps, secret []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write(corps)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// signatureValide compare la signature reçue à temps constant.
func signatureValide(entete string, corps, secret []byte) bool {
	recu, ok := strings.CutPrefix(entete, "sha256=")
	if !ok {
		return false
	}
	brut, err := hex.DecodeString(recu)
	if err != nil {
		return false
	}
	m := hmac.New(sha256.New, secret)
	m.Write(corps)
	return hmac.Equal(brut, m.Sum(nil))
}

// motifDefi limite le défi de la poignée de main, renvoyé tel quel.
var motifDefi = regexp.MustCompile(`^[0-9A-Za-z_.-]{1,128}$`)

func (h *Webhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		h.abonnement(w, r)
	case http.MethodPost:
		h.notification(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}

// abonnement répond à la poignée de main de Meta : hub.mode=subscribe,
// hub.verify_token égal au jeton de vérification, hub.challenge renvoyé.
func (h *Webhook) abonnement(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	defi := q.Get("hub.challenge")
	jeton := q.Get("hub.verify_token")
	if q.Get("hub.mode") != "subscribe" || !motifDefi.MatchString(defi) ||
		subtle.ConstantTimeCompare([]byte(jeton), []byte(h.opt.Integration.JetonWebhook)) != 1 {
		h.opt.Journal.Warn("webhook WhatsApp : poignée de main refusée")
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, defi)
}

// notification est le format des notifications de l'API Cloud (champ
// « messages ») ; seuls les champs utiles sont lus.
type notification struct {
	Object string `json:"object"`
	Entry  []struct {
		Changes []struct {
			Field string `json:"field"`
			Value struct {
				Metadata struct {
					PhoneNumberID string `json:"phone_number_id"`
				} `json:"metadata"`
				Messages []struct {
					From string `json:"from"`
					Type string `json:"type"`
					Text struct {
						Body string `json:"body"`
					} `json:"text"`
				} `json:"messages"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

func (h *Webhook) notification(w http.ResponseWriter, r *http.Request) {
	corps, err := io.ReadAll(http.MaxBytesReader(w, r.Body, tailleMaxCorps))
	if err != nil {
		http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
		return
	}
	// Spec T6 : sans signature valide, rien n'est lu.
	if !signatureValide(r.Header.Get("X-Hub-Signature-256"), corps, h.opt.Integration.SecretApp) {
		h.opt.Journal.Warn("webhook WhatsApp : signature invalide, notification rejetée")
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	var n notification
	if err := json.Unmarshal(corps, &n); err != nil {
		// Le corps n'est jamais journalisé, ni l'erreur d'analyse qui peut le citer.
		h.opt.Journal.Warn("webhook WhatsApp : notification signée mais illisible")
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	ctx, annuler := context.WithTimeout(r.Context(), delaiTraitement)
	defer annuler()
	echec := false
	if n.Object == "whatsapp_business_account" {
		for _, e := range n.Entry {
			for _, c := range e.Changes {
				if c.Field != "messages" || c.Value.Metadata.PhoneNumberID != h.opt.Integration.IDNumero {
					continue
				}
				// Les statuts (envoyé, lu…) et les autres types de messages
				// sont ignorés.
				for _, m := range c.Value.Messages {
					if m.Type != "text" || m.From == "" {
						continue
					}
					if err := h.opt.Verificateur.Recevoir(ctx, m.From, m.Text.Body); err != nil {
						h.opt.Journal.Error("webhook WhatsApp : vérification impossible, Meta réessaiera", "erreur", err)
						echec = true
					}
				}
			}
		}
	}
	if echec {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}
