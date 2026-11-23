package session

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/empreinte"
)

func TestCycleDeVie(t *testing.T) {
	maintenant := time.Date(2027, 2, 1, 10, 0, 0, 0, time.UTC)
	m := NouveauMagasin()
	m.Maintenant = func() time.Time { return maintenant }

	rec := httptest.NewRecorder()
	e := empreinte.Empreinte{7}
	if err := m.Creer(rec, e); err != nil {
		t.Fatal(err)
	}
	cs := rec.Result().Cookies()
	if len(cs) != 1 {
		t.Fatalf("cookies : %v", cs)
	}
	c := cs[0]
	if c.Name != NomCookie || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || len(c.Value) < 40 {
		t.Errorf("cookie : %+v", c)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)
	if etat, ok := m.Lire(req); !ok || etat.Empreinte != e {
		t.Fatalf("lecture : %v %v", etat, ok)
	}
	// L'activité prolonge la session ; 30 minutes d'inactivité l'expirent.
	maintenant = maintenant.Add(25 * time.Minute)
	if _, ok := m.Lire(req); !ok {
		t.Error("session expirée trop tôt")
	}
	maintenant = maintenant.Add(31 * time.Minute)
	if _, ok := m.Lire(req); ok {
		t.Error("la session devrait avoir expiré")
	}
	if m.Nombre() != 0 {
		t.Error("la session expirée devrait être supprimée")
	}

	// Destruction à l'envoi.
	rec = httptest.NewRecorder()
	_ = m.Creer(rec, e)
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(rec.Result().Cookies()[0])
	rec = httptest.NewRecorder()
	m.Detruire(rec, req)
	if _, ok := m.Lire(req); ok || m.Nombre() != 0 {
		t.Error("la session devrait être détruite")
	}
	if c := rec.Result().Cookies(); len(c) != 1 || c[0].MaxAge >= 0 {
		t.Errorf("le cookie devrait être effacé : %v", c)
	}

	// Un identifiant inconnu ne donne rien.
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: NomCookie, Value: "inconnu"})
	if _, ok := m.Lire(req); ok {
		t.Error("identifiant inconnu accepté")
	}
}

func TestPurgeALaCreation(t *testing.T) {
	maintenant := time.Date(2027, 2, 1, 10, 0, 0, 0, time.UTC)
	m := NouveauMagasin()
	m.Maintenant = func() time.Time { return maintenant }
	for range 5 {
		_ = m.Creer(httptest.NewRecorder(), empreinte.Empreinte{})
	}
	maintenant = maintenant.Add(time.Hour)
	_ = m.Creer(httptest.NewRecorder(), empreinte.Empreinte{})
	if m.Nombre() != 1 {
		t.Errorf("%d sessions, 1 attendue", m.Nombre())
	}
}

// La session ne peut contenir que l'empreinte (et l'heure du dernier accès) :
// aucun champ ne peut garder le numéro une fois l'empreinte calculée (ticket #5).
func TestEtatSansNumero(t *testing.T) {
	typ := reflect.TypeOf(Etat{})
	for i := range typ.NumField() {
		f := typ.Field(i)
		switch f.Type {
		case reflect.TypeOf(empreinte.Empreinte{}), reflect.TypeOf(time.Time{}):
		default:
			t.Errorf("champ %s de type %s : la session ne doit contenir que l'empreinte", f.Name, f.Type)
		}
	}
}
