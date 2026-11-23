package equipe

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/integration"
)

func TestChiffrementLieAuMembre(t *testing.T) {
	c, err := NouveauChiffreur(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("12345678901234567890")
	x, err := c.Chiffrer("membre-a", secret)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(x, secret) {
		t.Fatal("le secret apparaît en clair")
	}
	if y, _ := c.Chiffrer("membre-a", secret); bytes.Equal(x, y) {
		t.Error("deux chiffrements identiques : nonce réutilisé ?")
	}
	if s, err := c.Dechiffrer("membre-a", x); err != nil || !bytes.Equal(s, secret) {
		t.Fatalf("déchiffrement : %v", err)
	}
	if _, err := c.Dechiffrer("membre-b", x); err == nil {
		t.Error("un secret recopié sur un autre membre ne doit pas se déchiffrer")
	}
	autre, _ := NouveauChiffreur(bytes.Repeat([]byte{8}, 32))
	if _, err := autre.Dechiffrer("membre-a", x); err == nil {
		t.Error("déchiffré avec une autre clé")
	}
	if _, err := NouveauChiffreur([]byte("courte")); err == nil {
		t.Error("clé courte acceptée")
	}
}

// Sans DIGUEE_CLE_TOTP, la clé de développement est déclarée « fake » : la
// garde la refuse en production.
func TestChiffreurDeclareDansLeRegistre(t *testing.T) {
	for _, cas := range []struct {
		cle  []byte
		mode string
	}{{nil, integration.ModeFake}, {bytes.Repeat([]byte{1}, 32), "aes-gcm"}} {
		r := &integration.Registre{}
		if _, err := ConstruireChiffreur(&config.Config{CleTOTP: cas.cle}, r); err != nil {
			t.Fatal(err)
		}
		ds := r.Declarations()
		if len(ds) != 1 || ds[0].Nom != "chiffrement_totp" || ds[0].Mode != cas.mode || ds[0].Variable != "DIGUEE_CLE_TOTP" {
			t.Errorf("déclaration : %+v", ds)
		}
	}
}

func TestMotDePasse(t *testing.T) {
	defer IterationsPourTests(1000)()
	m, err := NouveauMotDePasse()
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 23 || strings.Count(m, "-") != 3 || strings.Trim(m, alphabetMotDePasse+"-") != "" {
		t.Errorf("mot de passe %q", m)
	}
	if m2, _ := NouveauMotDePasse(); m2 == m {
		t.Error("deux tirages identiques")
	}
	e, err := EmpreinteMotDePasse(m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(e, m) || strings.Contains(e, strings.ReplaceAll(m, "-", "")) {
		t.Error("le mot de passe apparaît dans l'empreinte")
	}
	for _, s := range []string{m, strings.ToUpper(m), strings.ReplaceAll(m, "-", " ")} {
		if !VerifierMotDePasse(e, s) {
			t.Errorf("saisie %q refusée", s)
		}
	}
	for _, s := range []string{"", m + "a", m[:22]} {
		if VerifierMotDePasse(e, s) {
			t.Errorf("saisie %q acceptée", s)
		}
	}
	if VerifierMotDePasse("n'importe quoi", m) {
		t.Error("empreinte mal formée acceptée")
	}
}

func TestLimiteur(t *testing.T) {
	l := NouveauLimiteur(3, time.Minute)
	t0 := time.Unix(1_800_000_000, 0)
	for i := range 3 {
		if l.Bloque("x", t0) {
			t.Fatalf("bloqué après %d échecs", i)
		}
		l.Echec("x", t0.Add(time.Duration(i)*time.Second))
	}
	if !l.Bloque("x", t0.Add(3*time.Second)) || l.Bloque("y", t0) {
		t.Error("blocage attendu pour x seulement")
	}
	if l.Bloque("x", t0.Add(time.Minute)) {
		t.Error("le premier échec est sorti de la fenêtre")
	}
	l.Effacer("x")
	if l.Bloque("x", t0) {
		t.Error("effacé")
	}
}

func TestSelSourceSansAdresse(t *testing.T) {
	var s selSource
	t0 := time.Unix(1_800_000_000, 0)
	a := s.cle("192.0.2.1", t0)
	if strings.Contains(a, "192") || a != s.cle("192.0.2.1", t0.Add(time.Hour)) || a == s.cle("192.0.2.2", t0) {
		t.Errorf("clé de source : %q", a)
	}
	if a == s.cle("192.0.2.1", t0.Add(24*time.Hour)) {
		t.Error("le sel doit changer chaque jour")
	}
}

func TestSessionsHuitHeures(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	maintenant := t0
	s := NouvellesSessions(func() time.Time { return maintenant })
	rec := httptest.NewRecorder()
	if err := s.Ouvrir(rec, httptest.NewRequest(http.MethodPost, "/equipe/connexion", nil), "m1"); err != nil {
		t.Fatal(err)
	}
	c := rec.Result().Cookies()[0]
	if c.Name != NomCookie || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/equipe" || c.MaxAge != 8*3600 {
		t.Errorf("cookie : %+v", c)
	}
	req := httptest.NewRequest(http.MethodGet, "/equipe/", nil)
	req.AddCookie(c)
	maintenant = t0.Add(8*time.Hour - time.Second)
	if id, csrf, ok := s.Lire(req); !ok || id != "m1" || csrf == "" {
		t.Fatal("session valide refusée")
	}
	maintenant = t0.Add(8 * time.Hour)
	if _, _, ok := s.Lire(req); ok {
		t.Error("session de plus de 8 heures acceptée (la lecture ne la prolonge pas)")
	}
	// Une nouvelle connexion depuis le même navigateur ferme l'ancienne session.
	maintenant = t0
	rec = httptest.NewRecorder()
	if err := s.Ouvrir(rec, req, "m1"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := s.Lire(req); ok || s.Nombre("m1") != 1 {
		t.Errorf("ancienne session encore ouverte : %d", s.Nombre("m1"))
	}
}
