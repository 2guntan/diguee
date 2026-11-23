package empreinte

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/2guntan/diguee/internal/integration"
)

// Tests contre un vrai KMS, sautés par défaut. Ils lisent les mêmes variables
// que le serveur (voir « À faire par le porteur », §1.1) :
//
//	DIGUEE_TEST_KMS_REEL=ovh DIGUEE_OVH_KMS_POINT_ACCES=… DIGUEE_OVH_KMS_ID=… \
//	DIGUEE_OVH_KMS_CLE_ID=… DIGUEE_OVH_KMS_CERTIFICAT=… DIGUEE_OVH_KMS_CLE_CLIENT=… \
//	go test ./internal/empreinte -run TestKMSReel -v -count=1
//
// (DIGUEE_TEST_KMS_REEL=gcp avec les variables DIGUEE_GCP_KMS_* pour le repli.)
// Les numéros utilisés sont des valeurs de contrôle, pas de vrais numéros.
func TestKMSReel(t *testing.T) {
	mode := os.Getenv("DIGUEE_TEST_KMS_REEL")
	if mode == "" {
		t.Skip("DIGUEE_TEST_KMS_REEL absente (ovh ou gcp) : test contre le vrai KMS sauté")
	}
	env := map[string]string{"DIGUEE_EMPREINTEUR": mode}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "DIGUEE_OVH_KMS_") || strings.HasPrefix(k, "DIGUEE_GCP_KMS_") || k == "DIGUEE_KMS_DELAI" {
			env[k] = v
		}
	}
	var r integration.Registre
	e, err := Construire(configAvec(t, env), &r)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := VerifierDeterminisme(ctx, e); err != nil {
		t.Fatalf("test de démarrage (T5) : %v", err)
	}
	const a, b = "+221700000000", "+221700000001"
	var premieres [2]Empreinte
	for i := range 5 {
		ea, err := e.Calculer(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		eb, err := e.Calculer(ctx, b)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			premieres = [2]Empreinte{ea, eb}
		}
		if ea != premieres[0] || eb != premieres[1] {
			t.Fatalf("appel %d : la signature n'est pas déterministe", i+1)
		}
	}
	if premieres[0] == premieres[1] {
		t.Fatal("deux numéros différents donnent la même empreinte")
	}
	t.Logf("KMS %s : 5 × 2 signatures, déterministes et distinctes (empreinte de contrôle %x…)", mode, premieres[0][:4])
}
