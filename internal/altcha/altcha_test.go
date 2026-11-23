package altcha

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/integration"
)

type horloge struct {
	mu sync.Mutex
	t  time.Time
}

func (h *horloge) maintenant() time.Time { h.mu.Lock(); defer h.mu.Unlock(); return h.t }
func (h *horloge) avancer(d time.Duration) {
	h.mu.Lock()
	h.t = h.t.Add(d)
	h.mu.Unlock()
}

func nouveau(t *testing.T, max int) (*Altcha, *horloge) {
	t.Helper()
	h := &horloge{t: time.Unix(1_800_000_000, 0)}
	a, err := Nouveau(Options{Cle: bytes.Repeat([]byte{7}, 32), Difficulte: 1000, Maintenant: h.maintenant, MaxEnMemoire: max})
	if err != nil {
		t.Fatal(err)
	}
	return a, h
}

func defiResolu(t *testing.T, a *Altcha) (Defi, string) {
	t.Helper()
	d, err := a.NouveauDefi()
	if err != nil {
		t.Fatal(err)
	}
	s, err := Resoudre(d)
	if err != nil {
		t.Fatal(err)
	}
	return d, s
}

func TestDefiAuFormatAltcha(t *testing.T) {
	a, h := nouveau(t, 0)
	d, s := defiResolu(t, a)
	if d.Algorithm != "SHA-256" || d.MaxNumber != 1000 || len(d.Challenge) != 64 || len(d.Signature) != 64 {
		t.Fatalf("défi : %+v", d)
	}
	if !strings.Contains(d.Salt, "?expires=") {
		t.Errorf("le sel doit porter l'expiration : %q", d.Salt)
	}
	if e, ok := expiration(d.Salt); !ok || !e.Equal(h.maintenant().Add(DureeDefaut)) {
		t.Errorf("expiration %v", e)
	}
	brut, _ := base64.StdEncoding.DecodeString(s)
	var champs map[string]any
	if err := json.Unmarshal(brut, &champs); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"algorithm", "challenge", "number", "salt", "signature"} {
		if _, ok := champs[c]; !ok {
			t.Errorf("champ %q absent de la solution", c)
		}
	}
	if err := a.Verifier(s); err != nil {
		t.Fatalf("solution valide refusée : %v", err)
	}
}

func TestSolutionUnique(t *testing.T) {
	a, _ := nouveau(t, 0)
	_, s := defiResolu(t, a)
	if err := a.Verifier(s); err != nil {
		t.Fatal(err)
	}
	if err := a.Verifier(s); !errors.Is(err, ErrDejaUtilisee) {
		t.Errorf("solution réutilisée : %v", err)
	}
}

func TestSolutionsRefusees(t *testing.T) {
	a, h := nouveau(t, 0)
	autre, _ := Nouveau(Options{Cle: bytes.Repeat([]byte{8}, 32), Difficulte: 1000, Maintenant: h.maintenant})
	encoder := func(s Solution) string {
		b, _ := json.Marshal(s)
		return base64.StdEncoding.EncodeToString(b)
	}
	d, _ := defiResolu(t, a)
	var bonne Solution
	sol, _ := Resoudre(d)
	brut, _ := base64.StdEncoding.DecodeString(sol)
	_ = json.Unmarshal(brut, &bonne)

	_, signeeAilleurs := defiResolu(t, autre)
	fausse := bonne
	fausse.Number = (bonne.Number + 1) % 1001
	mauvaisAlgo := bonne
	mauvaisAlgo.Algorithm = "SHA-1"
	sansExpiration := bonne
	sansExpiration.Salt = strings.Split(bonne.Salt, "?")[0]
	selModifie := bonne
	selModifie.Salt = strings.Replace(bonne.Salt, "expires=", "expires=9", 1)
	cas := map[string]struct {
		charge string
		err    error
	}{
		"vide":                 {"", ErrAbsente},
		"pas du base64":        {"%%%", ErrInvalide},
		"pas du JSON":          {base64.StdEncoding.EncodeToString([]byte("x")), ErrInvalide},
		"signée par un autre":  {signeeAilleurs, ErrInvalide},
		"mauvais nombre":       {encoder(fausse), ErrInvalide},
		"mauvais algorithme":   {encoder(mauvaisAlgo), ErrInvalide},
		"sel sans expiration":  {encoder(sansExpiration), ErrInvalide},
		"expiration repoussée": {encoder(selModifie), ErrInvalide},
		"trop longue":          {strings.Repeat("A", 4096), ErrInvalide},
	}
	for nom, c := range cas {
		if err := a.Verifier(c.charge); !errors.Is(err, c.err) {
			t.Errorf("%s : %v, attendu %v", nom, err, c.err)
		}
	}
	// La bonne solution reste utilisable : les essais ratés ne la consomment pas.
	h.avancer(DureeDefaut - time.Second)
	if err := a.Verifier(sol); err != nil {
		t.Errorf("bonne solution : %v", err)
	}
	_, tard := defiResolu(t, a)
	h.avancer(DureeDefaut)
	if err := a.Verifier(tard); !errors.Is(err, ErrExpiree) {
		t.Errorf("défi expiré : %v", err)
	}
}

func TestMemoireBornee(t *testing.T) {
	a, h := nouveau(t, 3)
	for range 3 {
		_, s := defiResolu(t, a)
		if err := a.Verifier(s); err != nil {
			t.Fatal(err)
		}
	}
	_, s := defiResolu(t, a)
	if err := a.Verifier(s); !errors.Is(err, ErrSaturee) {
		t.Fatalf("mémoire pleine : %v", err)
	}
	// Une fois les défis expirés, leurs solutions sont oubliées sans risque
	// de rejeu (le défi lui-même est refusé).
	h.avancer(DureeDefaut)
	_, s = defiResolu(t, a)
	if err := a.Verifier(s); err != nil {
		t.Fatalf("après expiration : %v", err)
	}
	if n := len(a.utilises); n != 1 {
		t.Errorf("%d solutions en mémoire, 1 attendue", n)
	}
}

func TestConstruireDeclareLaCleDeDeveloppement(t *testing.T) {
	cfg, err := config.Charger(func(k string) string {
		return map[string]string{"DIGUEE_POSTGRES_URL": "x"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &integration.Registre{}
	if _, err := Construire(cfg, r); err != nil {
		t.Fatal(err)
	}
	if fs := r.Fausses(); len(fs) != 1 || fs[0].Nom != "captcha_altcha" || fs[0].Variable != "DIGUEE_CLE_ALTCHA" {
		t.Errorf("sans clé, le captcha doit être déclaré « fake » : %+v", fs)
	}
	cfg.CleAltcha = bytes.Repeat([]byte{1}, 32)
	r = &integration.Registre{}
	if _, err := Construire(cfg, r); err != nil || len(r.Fausses()) != 0 {
		t.Errorf("avec clé : %v %+v", err, r.Fausses())
	}
}
