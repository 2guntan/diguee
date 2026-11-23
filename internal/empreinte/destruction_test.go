package empreinte

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/2guntan/diguee/internal/empreinte/fauxkms"
)

func destructeur(t *testing.T, f *fauxkms.OVH) *DestructeurOVH {
	t.Helper()
	tlsCfg, err := ChargerCertificat(f.Certificat, f.CleClient, f.AC)
	if err != nil {
		t.Fatal(err)
	}
	d, err := NouveauDestructeurOVH(OptionsDestructeur{PointAcces: f.URL, KMSID: f.KMSID, CleID: f.CleID, TLS: tlsCfg})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// Clôture (ticket #16) : la clé est désactivée puis supprimée ; ensuite,
// plus aucune empreinte ne peut être calculée. Relancée, la destruction
// constate que la clé n'existe plus.
func TestDestructionDeLaCle(t *testing.T) {
	ctx := context.Background()
	f := fauxkms.DemarrerOVH(t)
	e := construireOVH(t, f, nil)
	if _, err := e.Calculer(ctx, "+221700000000"); err != nil {
		t.Fatal(err)
	}
	d := destructeur(t, f)
	if etat, err := d.Etat(ctx); err != nil || etat != "active" {
		t.Fatalf("état avant : %q %v", etat, err)
	}
	b, err := d.Detruire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if b.DejaDetruite || b.EtatFinal != "introuvable (HTTP 404)" || f.EtatCle() != "supprimee" || len(b.Operations) != 4 ||
		!strings.Contains(b.Operations[1], "/deactivate (cessation_of_operation) → HTTP 204") || !strings.HasPrefix(b.Operations[2], "DELETE ") {
		t.Errorf("bilan %+v, état %s", b, f.EtatCle())
	}
	if len(f.Defauts()) > 0 {
		t.Errorf("requêtes non conformes : %v", f.Defauts())
	}
	// Plus aucune signature : l'empreinte d'un numéro ne peut plus être calculée.
	if _, err := e.Calculer(ctx, "+221700000000"); !errors.Is(err, ErrKMS) {
		t.Errorf("calcul après destruction : %v", err)
	}
	// Reprise : déjà détruite, sans nouvelle suppression.
	b, err = d.Detruire(ctx)
	if err != nil || !b.DejaDetruite || len(b.Operations) != 1 {
		t.Errorf("reprise : %+v %v", b, err)
	}
}

// Une panne du KMS arrête la destruction avec une erreur ; une clé déjà
// désactivée (essai interrompu) n'est pas désactivée deux fois.
func TestDestructionPannesEtReprise(t *testing.T) {
	ctx := context.Background()
	f := fauxkms.DemarrerOVH(t)
	d := destructeur(t, f)
	f.Regler(fauxkms.Reglage{Statut: http.StatusServiceUnavailable, Corps: `{"error_code":1,"errors":["indisponible"]}`})
	if _, err := d.Detruire(ctx); !errors.Is(err, ErrKMS) || f.EtatCle() != "active" {
		t.Fatalf("panne : %v, état %s", err, f.EtatCle())
	}
	if _, err := d.Etat(ctx); err == nil {
		t.Error("contrôle préalable : la panne doit être signalée")
	}
	f.Regler(fauxkms.Reglage{})
	// Désactivée à la main (ou par un essai interrompu).
	if _, _, err := d.client.appeler(ctx, http.MethodPost, d.cle+"/deactivate", map[string]string{"reason": RaisonDesactivation}); err != nil {
		t.Fatal(err)
	}
	b, err := d.Detruire(ctx)
	if err != nil || len(b.Operations) != 3 || strings.Contains(strings.Join(b.Operations, " "), "/deactivate") || f.EtatCle() != "supprimee" {
		t.Errorf("reprise : %+v %v", b, err)
	}
	if _, err := NouveauDestructeurOVH(OptionsDestructeur{PointAcces: "http://kms", KMSID: "a", CleID: "b"}); err == nil {
		t.Error("point d'accès http accepté")
	}
}
