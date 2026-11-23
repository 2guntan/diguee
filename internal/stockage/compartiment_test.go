package stockage_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2guntan/diguee/internal/stockage"
	"github.com/2guntan/diguee/internal/stockage/fauxs3"
)

// essayerCompartiment exerce les quatre opérations d'un Compartiment.
func essayerCompartiment(t *testing.T, c stockage.Compartiment) {
	t.Helper()
	ctx := context.Background()
	for i := range 7 {
		contenu := []byte(fmt.Sprintf("sauvegarde %d", i))
		if err := c.Envoyer(ctx, fmt.Sprintf("sauvegardes/s-%02d", i), bytes.NewReader(contenu), int64(len(contenu)), sha256.Sum256(contenu)); err != nil {
			t.Fatal(err)
		}
	}
	autre := []byte("autre")
	if err := c.Envoyer(ctx, "ailleurs/x", bytes.NewReader(autre), 5, sha256.Sum256(autre)); err != nil {
		t.Fatal(err)
	}
	// Remplacement sous la même clé.
	nouveau := []byte("sauvegarde 3, remplacée")
	if err := c.Envoyer(ctx, "sauvegardes/s-03", bytes.NewReader(nouveau), int64(len(nouveau)), sha256.Sum256(nouveau)); err != nil {
		t.Fatal(err)
	}
	// Contenu qui ne correspond pas à son SHA-256 : refusé.
	if err := c.Envoyer(ctx, "sauvegardes/faux", bytes.NewReader(autre), 5, sha256.Sum256([]byte("x"))); err == nil {
		t.Error("SHA-256 faux accepté")
	}
	objets, err := c.Lister(ctx, "sauvegardes/")
	if err != nil {
		t.Fatal(err)
	}
	if len(objets) != 7 || objets[0].Cle != "sauvegardes/s-00" || objets[6].Cle != "sauvegardes/s-06" || objets[3].Taille != int64(len(nouveau)) || objets[0].Modifie.IsZero() {
		t.Fatalf("liste %+v", objets)
	}
	var buf bytes.Buffer
	if err := c.Recuperer(ctx, "sauvegardes/s-03", &buf); err != nil || buf.String() != string(nouveau) {
		t.Fatalf("récupération %q %v", buf.String(), err)
	}
	if err := c.Recuperer(ctx, "sauvegardes/absente", &buf); !errors.Is(err, stockage.ErrIntrouvable) {
		t.Errorf("absente : %v", err)
	}
	if err := c.Supprimer(ctx, "sauvegardes/s-03"); err != nil {
		t.Fatal(err)
	}
	if err := c.Supprimer(ctx, "sauvegardes/s-03"); err != nil {
		t.Errorf("deuxième suppression : %v", err)
	}
	objets, _ = c.Lister(ctx, "sauvegardes/")
	if len(objets) != 6 {
		t.Errorf("après suppression : %+v", objets)
	}
	tous, _ := c.Lister(ctx, "")
	if len(tous) != 7 {
		t.Errorf("sans préfixe : %+v", tous)
	}
	for _, cle := range []string{"", "/abs", "a/../b", "a//b", "a b", "../x", "é"} {
		if err := c.Supprimer(ctx, cle); err == nil {
			t.Errorf("clé %q acceptée", cle)
		}
	}
}

func TestCompartimentS3(t *testing.T) {
	f := fauxs3.Nouveau(t)
	f.Modifier(func(s *fauxs3.Serveur) { s.ParPage = 2 }) // liste en plusieurs pages
	s := f.Client()
	essayerCompartiment(t, s)
	ctx := context.Background()
	if v, err := s.Versionnage(ctx); err != nil || v != "" {
		t.Errorf("versionnage %q %v", v, err)
	}
	f.Modifier(func(s *fauxs3.Serveur) { s.Versionnage = "Enabled" })
	if v, err := s.Versionnage(ctx); err != nil || v != "Enabled" {
		t.Errorf("versionnage %q %v", v, err)
	}
	// Panne : l'erreur ne garde que le statut et le code, jamais le message.
	f.Modifier(func(s *fauxs3.Serveur) { s.Panne = true })
	if _, err := s.Lister(ctx, "sauvegardes/"); err == nil || !strings.Contains(err.Error(), "500") || strings.Contains(err.Error(), fauxs3.CleSecrete) {
		t.Errorf("panne : %v", err)
	}
	f.Modifier(func(s *fauxs3.Serveur) { s.Panne = false })
	mauvais := *s
	mauvais.CleSecrete = "une-autre-cle-secrete"
	if err := mauvais.Supprimer(ctx, "sauvegardes/s-00"); err == nil || !strings.Contains(err.Error(), "SignatureDoesNotMatch") {
		t.Errorf("mauvaise clé : %v", err)
	}
}

func TestCompartimentDossier(t *testing.T) {
	d := &stockage.Dossier{Chemin: t.TempDir()}
	essayerCompartiment(t, d)
	// Aucun fichier temporaire ne reste.
	_ = filepath.WalkDir(d.Chemin, func(p string, e os.DirEntry, _ error) error {
		if strings.HasPrefix(e.Name(), ".depot-") {
			t.Errorf("fichier temporaire %s", p)
		}
		return nil
	})
	vide := &stockage.Dossier{Chemin: filepath.Join(t.TempDir(), "absent")}
	if objets, err := vide.Lister(context.Background(), ""); err != nil || len(objets) != 0 {
		t.Errorf("dossier absent : %v %v", objets, err)
	}
}
