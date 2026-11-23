package equipe

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/integration"
)

// Chiffreur chiffre les secrets TOTP avec AES-256-GCM. Le texte chiffré est
// lié au membre (son ID sert de donnée authentifiée) : un secret recopié sur
// un autre compte ne se déchiffre pas.
type Chiffreur struct {
	aead cipher.AEAD
}

// cleDeveloppement est la clé utilisée sans DIGUEE_CLE_TOTP. Elle est
// publique : elle ne protège rien, d'où l'interdiction en production.
var cleDeveloppement = []byte("diguee : cle TOTP dev, interdite")

// NouveauChiffreur renvoie un chiffreur pour une clé de 32 octets.
func NouveauChiffreur(cle []byte) (*Chiffreur, error) {
	if len(cle) != 32 {
		return nil, errors.New("la clé de chiffrement des secrets TOTP doit faire 32 octets")
	}
	bloc, err := aes.NewCipher(cle)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(bloc)
	if err != nil {
		return nil, err
	}
	return &Chiffreur{aead: aead}, nil
}

// ConstruireChiffreur choisit la clé d'après DIGUEE_CLE_TOTP et déclare le
// chiffrement dans le registre des intégrations : sans clé, le mode est
// « fake » (clé de développement), refusé en production par le paquet garde.
func ConstruireChiffreur(c *config.Config, r *integration.Registre) (*Chiffreur, error) {
	cle, mode := c.CleTOTP, "aes-gcm"
	if cle == nil {
		cle, mode = cleDeveloppement, integration.ModeFake
	}
	ch, err := NouveauChiffreur(cle)
	if err != nil {
		return nil, err
	}
	if err := r.Declarer(integration.Declaration{
		Nom: "chiffrement_totp", Mode: mode, Variable: "DIGUEE_CLE_TOTP",
	}); err != nil {
		return nil, err
	}
	return ch, nil
}

// Chiffrer renvoie nonce || texte chiffré du secret, lié à l'ID du membre.
func (c *Chiffreur) Chiffrer(membreID string, secret []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize(), c.aead.NonceSize()+len(secret)+c.aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return c.aead.Seal(nonce, nonce, secret, []byte(membreID)), nil
}

// Dechiffrer inverse Chiffrer.
func (c *Chiffreur) Dechiffrer(membreID string, chiffre []byte) ([]byte, error) {
	n := c.aead.NonceSize()
	if len(chiffre) < n+c.aead.Overhead() {
		return nil, errors.New("secret TOTP chiffré trop court")
	}
	s, err := c.aead.Open(nil, chiffre[:n], chiffre[n:], []byte(membreID))
	if err != nil {
		return nil, fmt.Errorf("déchiffrement du secret TOTP : %w", err)
	}
	return s, nil
}
