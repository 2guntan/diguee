// Package stockage conserve les copies archivées des sources de
// l'observatoire (spec §6.4, ADR 0008) : des fichiers PDF ou des images,
// rangés sous leur SHA-256.
//
// Deux implémentations, choisies par DIGUEE_STOCKAGE (intégration
// « stockage » du registre) :
//   - fake : un dossier local (Dossier), pour le développement ; refusé en
//     production par la garde ;
//   - ovh : l'Object Storage d'OVHcloud par son API S3 (S3), signée en AWS
//     Signature Version 4, écrite avec la bibliothèque standard.
//
// Un objet est rangé sous la clé « archives/<sha256 en hexadécimal> » : son
// nom est son empreinte, il n'est jamais remplacé par un autre contenu, et
// sa lecture vérifie l'empreinte (ErrAltere sinon). La liste des objets à
// sauvegarder (ticket #15) est celle des empreintes de la table source.
package stockage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

// Empreinte est le SHA-256 d'un objet, qui lui sert de nom.
type Empreinte [sha256.Size]byte

// String écrit l'empreinte en hexadécimal minuscule.
func (e Empreinte) String() string { return hex.EncodeToString(e[:]) }

// LireEmpreinte lit une empreinte écrite en hexadécimal minuscule.
func LireEmpreinte(s string) (Empreinte, error) {
	var e Empreinte
	if len(s) != 2*len(e) {
		return e, fmt.Errorf("empreinte : 64 caractères hexadécimaux attendus")
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return e, fmt.Errorf("empreinte : hexadécimal minuscule attendu")
		}
	}
	_, err := hex.Decode(e[:], []byte(s))
	return e, err
}

// Calculer renvoie l'empreinte d'un contenu.
func Calculer(contenu []byte) Empreinte { return sha256.Sum256(contenu) }

// Cle est la clé (le chemin) d'un objet dans le stockage.
func Cle(e Empreinte) string { return "archives/" + e.String() }

// Erreurs de lecture.
var (
	ErrIntrouvable = errors.New("stockage : objet introuvable")
	// ErrAltere signale un objet dont le contenu ne correspond plus à son
	// empreinte : il n'est jamais servi.
	ErrAltere = errors.New("stockage : le contenu ne correspond pas à son SHA-256")
)

// Stockage conserve des objets sous leur SHA-256.
type Stockage interface {
	// Deposer range le contenu sous son SHA-256 et renvoie celui-ci. Déposer
	// deux fois le même contenu est sans effet. typeContenu (par exemple
	// application/pdf) est indicatif : le serveur le fixe lui-même en
	// servant le fichier.
	Deposer(ctx context.Context, typeContenu string, contenu []byte) (Empreinte, error)
	// Lire renvoie le contenu rangé sous l'empreinte, après avoir vérifié
	// qu'il lui correspond : ErrIntrouvable s'il n'existe pas, ErrAltere
	// s'il a changé.
	Lire(ctx context.Context, e Empreinte) ([]byte, error)
}

// verifier contrôle qu'un contenu lu correspond à son empreinte.
func verifier(e Empreinte, contenu []byte) ([]byte, error) {
	if Calculer(contenu) != e {
		return nil, fmt.Errorf("%w (%s)", ErrAltere, e)
	}
	return contenu, nil
}
