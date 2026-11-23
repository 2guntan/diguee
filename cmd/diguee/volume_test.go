package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2guntan/diguee/internal/empreinte"
	"github.com/2guntan/diguee/internal/empreinte/fauxkms"
)

// La clé du volume chiffré : créée, chiffrée par le (faux) KMS d'OVHcloud,
// jamais écrite en clair, puis déchiffrée vers un tube (ticket #7).
func TestCleVolume(t *testing.T) {
	ctx := context.Background()
	kms := fauxkms.DemarrerOVH(t)
	env := kms.EnvVolume()
	getenv := func(k string) string { return env[k] }
	fichier := filepath.Join(t.TempDir(), "volume.cle.jwe")

	var messages bytes.Buffer
	if err := cleVolume(ctx, []string{"creer", "-sortie", fichier}, getenv, &bytes.Buffer{}, &messages); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(fichier)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("fichier %v, %v", st, err)
	}
	chiffre, _ := os.ReadFile(fichier)
	if n := strings.Count(strings.TrimSpace(string(chiffre)), "."); n != 4 {
		t.Errorf("texte chiffré qui n'a pas la forme d'un JWE compact : %q", chiffre)
	}

	var cle1, cle2 bytes.Buffer
	if err := cleVolume(ctx, []string{"dechiffrer", "-entree", fichier}, getenv, &cle1, &messages); err != nil {
		t.Fatal(err)
	}
	if err := cleVolume(ctx, []string{"dechiffrer", "-entree", fichier}, getenv, &cle2, &messages); err != nil {
		t.Fatal(err)
	}
	if cle1.Len() != empreinte.TailleCleVolume || !bytes.Equal(cle1.Bytes(), cle2.Bytes()) {
		t.Fatalf("clés déchiffrées : %d octets, identiques %v", cle1.Len(), bytes.Equal(cle1.Bytes(), cle2.Bytes()))
	}
	// La clé en clair n'apparaît ni dans le fichier ni dans les messages.
	for _, forme := range []string{string(cle1.Bytes()), base64.StdEncoding.EncodeToString(cle1.Bytes()),
		base64.RawURLEncoding.EncodeToString(cle1.Bytes())} {
		if strings.Contains(string(chiffre), forme) || strings.Contains(messages.String(), forme) {
			t.Fatal("clé en clair écrite")
		}
	}
	if d := kms.Defauts(); len(d) != 0 {
		t.Errorf("requêtes non conformes à l'API : %v", d)
	}

	// Jamais de remplacement d'un texte chiffré existant.
	if err := cleVolume(ctx, []string{"creer", "-sortie", fichier}, getenv, &bytes.Buffer{}, &messages); err == nil || !strings.Contains(err.Error(), "existe déjà") {
		t.Errorf("remplacement accepté : %v", err)
	}
	if relu, _ := os.ReadFile(fichier); !bytes.Equal(relu, chiffre) {
		t.Error("fichier modifié")
	}

	// Un texte chiffré altéré est refusé, sans rien écrire.
	altere := filepath.Join(t.TempDir(), "altere.jwe")
	parts := strings.Split(strings.TrimSpace(string(chiffre)), ".")
	parts[3] = base64.RawURLEncoding.EncodeToString([]byte("autre chose que la clé du volume, 64 octets ou presque......"))
	os.WriteFile(altere, []byte(strings.Join(parts, ".")), 0o600)
	var rien bytes.Buffer
	if err := cleVolume(ctx, []string{"dechiffrer", "-entree", altere, "-essais", "1"}, getenv, &rien, &messages); err == nil || rien.Len() != 0 {
		t.Errorf("texte altéré : %v, %d octets écrits", err, rien.Len())
	}

	// KMS injoignable : nouveaux essais, puis erreur.
	kms.Regler(fauxkms.Reglage{Statut: 503, Corps: `{"error_code":503,"errors":["indisponible"]}`})
	avant := kms.Requetes()
	err = cleVolume(ctx, []string{"dechiffrer", "-entree", fichier, "-essais", "3", "-pause", "1ms"}, getenv, &rien, &messages)
	if err == nil || kms.Requetes()-avant != 3 || rien.Len() != 0 {
		t.Errorf("KMS indisponible : %v, %d requêtes", err, kms.Requetes()-avant)
	}
	kms.Regler(fauxkms.Reglage{})

	// Configuration incomplète ou certificat absent.
	delete(env, "DIGUEE_OVH_KMS_VOLUME_CLE_ID")
	if err := cleVolume(ctx, []string{"dechiffrer", "-entree", fichier}, getenv, &rien, &messages); err == nil || !strings.Contains(err.Error(), "DIGUEE_OVH_KMS_VOLUME_CLE_ID") {
		t.Errorf("configuration incomplète : %v", err)
	}
	env["DIGUEE_OVH_KMS_VOLUME_CLE_ID"] = kms.CleVolumeID
	env["DIGUEE_OVH_KMS_VOLUME_CLE_CLIENT"] = filepath.Join(t.TempDir(), "absente.key")
	if err := cleVolume(ctx, []string{"dechiffrer", "-entree", fichier}, getenv, &rien, &messages); err == nil {
		t.Error("certificat absent accepté")
	}
	if err := cleVolume(ctx, []string{"inconnue"}, getenv, &rien, &messages); err == nil {
		t.Error("sous-commande inconnue acceptée")
	}
}

// Sans certificat client, le KMS refuse (mTLS).
func TestCleVolumeExigeLeCertificatClient(t *testing.T) {
	kms := fauxkms.DemarrerOVH(t)
	tlsCfg, err := empreinte.ChargerCertificat(kms.Certificat, kms.CleClient, kms.AC)
	if err != nil {
		t.Fatal(err)
	}
	tlsCfg.Certificates = nil
	c, err := empreinte.NouvelleCleVolume(empreinte.OptionsCleVolume{PointAcces: kms.URL, KMSID: kms.KMSID, CleID: kms.CleVolumeID, TLS: tlsCfg})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Chiffrer(context.Background(), make([]byte, 64)); err == nil {
		t.Fatal("chiffrement sans certificat client accepté")
	}
}
