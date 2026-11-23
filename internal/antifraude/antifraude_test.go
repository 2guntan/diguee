package antifraude

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/empreinte"
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

func TestFenetre(t *testing.T) {
	f := NouvelleFenetre(3, 3, time.Minute)
	t0 := time.Unix(1_800_000_000, 0)
	for i := range 3 {
		if f.Atteint("x", t0) {
			t.Fatalf("atteint après %d événements", i)
		}
		f.Ajouter("x", t0.Add(time.Duration(i)*time.Second))
	}
	if !f.Atteint("x", t0.Add(3*time.Second)) || f.Atteint("y", t0) {
		t.Error("seuil attendu pour x seulement")
	}
	if n := f.Ajouter("x", t0.Add(4*time.Second)); n != 3 {
		t.Errorf("au plafond, le compte reste à 3 : %d", n)
	}
	if f.Atteint("x", t0.Add(time.Minute)) {
		t.Error("le premier événement est sorti de la fenêtre")
	}
	f.Effacer("x")
	if f.Atteint("x", t0) || f.Cles() != 0 {
		t.Error("effacé")
	}
}

func TestFenetrePurgeLesClesPerimees(t *testing.T) {
	f := NouvelleFenetre(1, 1, time.Minute)
	t0 := time.Unix(1_800_000_000, 0)
	for i := range purgeMin {
		f.Ajouter(strconv.Itoa(i), t0)
	}
	f.Ajouter("neuve", t0.Add(2*time.Minute))
	if n := f.Cles(); n != 1 {
		t.Errorf("%d clés après purge, 1 attendue", n)
	}
}

func TestAnonymiseur(t *testing.T) {
	var a Anonymiseur
	t0 := time.Unix(1_800_000_000, 0)
	c := a.Cle("192.0.2.1:4242", t0)
	if strings.Contains(c, "192.0.2") || c != a.Cle("192.0.2.1:1", t0.Add(time.Hour)) || c == a.Cle("192.0.2.2:4242", t0) {
		t.Errorf("clé : %q", c)
	}
	if c == a.Cle("192.0.2.1:4242", t0.Add(24*time.Hour)) {
		t.Error("le sel doit changer chaque jour")
	}
	// IPv6 : un même préfixe /64 donne la même clé.
	if a.Cle("[2001:db8:1:2::1]:443", t0) != a.Cle("[2001:db8:1:2:ffff::9]:80", t0) ||
		a.Cle("[2001:db8:1:2::1]:443", t0) == a.Cle("[2001:db8:1:3::1]:443", t0) {
		t.Error("IPv6 : clé par préfixe /64 attendue")
	}
	if Hote("[::ffff:192.0.2.7]:1") != "192.0.2.7" {
		t.Error("adresse IPv4 dans IPv6")
	}
}

func TestLimiteEmpreinte(t *testing.T) {
	h := &horloge{t: time.Unix(1_800_000_000, 0)}
	l := NouvelleLimiteEmpreinte(3, h.maintenant)
	e1, e2 := empreinte.Empreinte{1}, empreinte.Empreinte{2}
	for i := range 3 {
		if !l.Autoriser(e1) {
			t.Fatalf("tentative %d refusée", i+1)
		}
		h.avancer(time.Hour)
	}
	if l.Autoriser(e1) {
		t.Error("la 4ᵉ tentative en 24 h doit être refusée")
	}
	if !l.Autoriser(e2) {
		t.Error("une autre empreinte n'est pas limitée")
	}
	// La première tentative sort de la fenêtre 24 h après.
	h.avancer(21 * time.Hour)
	if !l.Autoriser(e1) {
		t.Error("une tentative doit redevenir possible après 24 h")
	}
	if l.Autoriser(e1) {
		t.Error("une seule place libérée")
	}
	if l.Refusees() != 2 {
		t.Errorf("%d refus comptés", l.Refusees())
	}
	// La clé du compteur n'est pas l'empreinte.
	for k := range l.fenetre.evenements {
		if strings.Contains(k, string(e1[:8])) {
			t.Error("l'empreinte ne doit pas servir de clé telle quelle")
		}
	}
}

type journalSur struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (j *journalSur) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.buf.Write(p)
}

func (j *journalSur) String() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.buf.String()
}

func TestRalentisseurSansBlocage(t *testing.T) {
	h := &horloge{t: time.Unix(1_800_000_000, 0)}
	j := &journalSur{}
	var attentes []time.Duration
	r := NouveauRalentisseur(OptionsRalentisseur{
		ParHeure: 30, Maintenant: h.maintenant, Journal: slog.New(slog.NewJSONHandler(j, nil)),
		Attendre: func(_ context.Context, d time.Duration) { attentes = append(attentes, d) },
	})
	req := func(adresse string) *http.Request {
		q := httptest.NewRequest(http.MethodPost, "/verifier", nil)
		q.RemoteAddr = adresse
		q.Header.Set("X-Forwarded-For", "198.51.100.9") // jamais lu
		return q
	}
	for i := range 30 {
		if d := r.Freiner(req("203.0.113.5:1000")); d != 0 {
			t.Fatalf("demande %d ralentie de %v", i+1, d)
		}
	}
	var precedent time.Duration
	for i := range 40 {
		d := r.Freiner(req("203.0.113.5:1000"))
		if d <= 0 || d < precedent || d > RalentissementMax {
			t.Fatalf("demande %d : délai %v après %v", 31+i, d, precedent)
		}
		precedent = d
	}
	if precedent != RalentissementMax {
		t.Errorf("délai final %v, borne %v attendue", precedent, RalentissementMax)
	}
	if len(attentes) != 40 || r.Ralenties() != 40 || r.Alertes() != 1 {
		t.Errorf("%d attentes, %d ralenties, %d alertes", len(attentes), r.Ralenties(), r.Alertes())
	}
	// Une autre adresse n'est pas touchée.
	if d := r.Freiner(req("203.0.113.6:1000")); d != 0 {
		t.Error("autre adresse ralentie")
	}
	journal := j.String()
	if strings.Count(journal, "anti-fraude") != 1 || strings.Contains(journal, "203.0.113") || strings.Contains(journal, "198.51.100") {
		t.Errorf("alerte attendue, sans adresse : %s", journal)
	}
	for k := range r.fenetre.evenements {
		if strings.Contains(k, "203.0.113") {
			t.Error("adresse en clair dans le compteur")
		}
	}
	// Une heure plus tard, la fenêtre est vide.
	h.avancer(time.Hour)
	if d := r.Freiner(req("203.0.113.5:1000")); d != 0 {
		t.Errorf("après une heure : %v", d)
	}
}

func TestRalentisseurAttendReellement(t *testing.T) {
	r := NouveauRalentisseur(OptionsRalentisseur{ParHeure: 1, Journal: slog.New(slog.NewJSONHandler(&journalSur{}, nil))})
	q := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Freiner(q)
	ctx, annuler := context.WithCancel(context.Background())
	annuler()
	debut := time.Now()
	if d := r.Freiner(q.WithContext(ctx)); d != PasRalentissement || time.Since(debut) > 100*time.Millisecond {
		t.Errorf("une requête abandonnée n'attend pas : %v", d)
	}
	debut = time.Now()
	r.Freiner(q)
	if e := time.Since(debut); e < 2*PasRalentissement-50*time.Millisecond {
		t.Errorf("attente de %v", e)
	}
}

func TestSurveillancePicAnormal(t *testing.T) {
	h := &horloge{t: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)}
	j := &journalSur{}
	s := NouvelleSurveillance(h.maintenant, slog.New(slog.NewJSONHandler(j, nil)))
	// 30 heures régulières : 10 par heure pour « 77 ».
	for range 30 {
		for range 10 {
			s.Compter(DimensionPrefixe, "77")
		}
		h.avancer(time.Hour)
	}
	if s.Pics() != 0 {
		t.Fatal("aucun pic attendu pendant un trafic régulier")
	}
	// Heure suivante : 31 événements, plus de 3 fois la moyenne (10).
	for range 30 {
		s.Compter(DimensionPrefixe, "77")
	}
	if s.Pics() != 0 {
		t.Fatal("30 n'est pas plus de 3 fois 10")
	}
	s.Compter(DimensionPrefixe, "77")
	s.Compter(DimensionPrefixe, "77")
	if s.Pics() != 1 {
		t.Fatalf("un pic attendu, une seule alerte par heure : %d", s.Pics())
	}
	if !strings.Contains(j.String(), `"dimension":"prefixe","valeur":"77"`) {
		t.Errorf("alerte : %s", j.String())
	}
	// Une région inconnue jusque-là qui reçoit d'un coup 25 réponses.
	for range 25 {
		s.Compter(DimensionRegion, "3")
	}
	if s.Pics() != 2 {
		t.Errorf("pic sur une série nouvelle : %d", s.Pics())
	}
	inst := s.Instantane()
	if len(inst) != 2 || inst[0].Dimension != DimensionPrefixe || inst[0].HeureCourante != 32 || inst[0].Total != 332 ||
		inst[1].Valeur != "3" || inst[1].HeureCourante != 25 {
		t.Errorf("instantané : %+v", inst)
	}
}

func TestSurveillanceAttendUnHistorique(t *testing.T) {
	h := &horloge{t: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)}
	s := NouvelleSurveillance(h.maintenant, slog.New(slog.NewJSONHandler(&journalSur{}, nil)))
	h.avancer(10 * time.Hour)
	for range 1000 {
		s.Compter(DimensionPrefixe, "70")
	}
	if s.Pics() != 0 {
		t.Error("pas d'alerte avant 24 heures d'historique")
	}
	s.Compter("", "x")
	s.Compter(DimensionRegion, strings.Repeat("9", 40))
	if len(s.Instantane()) != 1 {
		t.Error("valeurs vides ou trop longues ignorées")
	}
}
