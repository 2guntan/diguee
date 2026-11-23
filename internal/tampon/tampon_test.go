package tampon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/empreinte"
	"github.com/2guntan/diguee/internal/questionnaire"
)

// baseFactice imite la base : participations écrites et lots reçus.
type baseFactice struct {
	mu      sync.Mutex
	ecrites map[empreinte.Empreinte]bool
	lots    [][]empreinte.Empreinte
	reps    [][]questionnaire.Reponse
	jours   []time.Time
	panne   bool
}

func (b *baseFactice) ParticipationExiste(_ context.Context, e empreinte.Empreinte) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ecrites[e], nil
}

func (b *baseFactice) EcrireLot(_ context.Context, jour time.Time, es []empreinte.Empreinte, rs []questionnaire.Reponse) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.panne {
		return errors.New("base en panne")
	}
	if b.ecrites == nil {
		b.ecrites = map[empreinte.Empreinte]bool{}
	}
	for _, e := range es {
		if b.ecrites[e] {
			return errors.New("doublon écrit")
		}
		b.ecrites[e] = true
	}
	b.lots = append(b.lots, es)
	b.reps = append(b.reps, rs)
	b.jours = append(b.jours, jour)
	return nil
}

// minuterieManuelle retient la dernière minuterie lancée, que le test déclenche.
type minuterieManuelle struct {
	mu      sync.Mutex
	delai   time.Duration
	f       func()
	annulee bool
	lancees int
}

func (m *minuterieManuelle) lancer(d time.Duration, f func()) func() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.delai, m.f, m.annulee = d, f, false
	m.lancees++
	return func() bool { m.mu.Lock(); defer m.mu.Unlock(); m.annulee = true; return true }
}

func (m *minuterieManuelle) declencher() {
	m.mu.Lock()
	f := m.f
	m.mu.Unlock()
	f()
}

func nouveau(t *testing.T, taille int) (*Tampon, *baseFactice, *minuterieManuelle) {
	t.Helper()
	b := &baseFactice{}
	m := &minuterieManuelle{}
	tp := Nouveau(Options{
		Ecrivain: b, Taille: taille, Delai: 30 * time.Minute, Minuterie: m.lancer,
		Maintenant: func() time.Time { return time.Date(2027, 2, 3, 17, 42, 13, 0, time.UTC) },
		Journal:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return tp, b, m
}

func emp(i int) empreinte.Empreinte { return empreinte.Empreinte{byte(i), byte(i >> 8), 1} }

func rep(i int) questionnaire.Reponse {
	return questionnaire.Reponse{Version: 1, Q3: int16(i), Q7: []int16{}, Themes: []int16{}}
}

func TestVidageATaille(t *testing.T) {
	ctx := context.Background()
	tp, b, _ := nouveau(t, 20)
	for i := range 19 {
		if err := tp.Ajouter(ctx, emp(i), rep(i)); err != nil {
			t.Fatal(err)
		}
	}
	if len(b.lots) != 0 || tp.Attente() != 19 {
		t.Fatalf("rien ne doit être écrit avant 20 envois : %d lots, %d en attente", len(b.lots), tp.Attente())
	}
	if err := tp.Ajouter(ctx, emp(19), rep(19)); err != nil {
		t.Fatal(err)
	}
	if len(b.lots) != 1 || len(b.lots[0]) != 20 || len(b.reps[0]) != 20 || tp.Attente() != 0 {
		t.Fatalf("un lot de 20 attendu : %d lots, %d en attente", len(b.lots), tp.Attente())
	}
	if j := b.jours[0]; j.Format(time.DateOnly) != "2027-02-03" {
		t.Errorf("jour %v", j)
	}
}

func TestVidageAuDelai(t *testing.T) {
	ctx := context.Background()
	tp, b, m := nouveau(t, 20)
	_ = tp.Ajouter(ctx, emp(1), rep(1))
	if m.lancees != 1 || m.delai != 30*time.Minute {
		t.Fatalf("minuterie : %d lancées, délai %v", m.lancees, m.delai)
	}
	_ = tp.Ajouter(ctx, emp(2), rep(2))
	if m.lancees != 1 {
		t.Error("le délai court depuis le premier envoi, pas depuis le dernier")
	}
	m.declencher()
	if len(b.lots) != 1 || len(b.lots[0]) != 2 || tp.Attente() != 0 {
		t.Fatalf("lot de 2 attendu au délai : %+v", b.lots)
	}
	// Un nouvel envoi relance une minuterie ; l'ancienne, si elle se
	// déclenchait encore, n'écrirait rien.
	_ = tp.Ajouter(ctx, emp(3), rep(3))
	if m.lancees != 2 {
		t.Errorf("%d minuteries lancées", m.lancees)
	}
}

func TestMinuteriePerimeeSansEffet(t *testing.T) {
	ctx := context.Background()
	tp, b, m := nouveau(t, 2)
	_ = tp.Ajouter(ctx, emp(1), rep(1))
	ancienne := m.f
	_ = tp.Ajouter(ctx, emp(2), rep(2)) // taille atteinte : écrit, minuterie annulée
	if !m.annulee {
		t.Error("la minuterie devrait être annulée après l'écriture")
	}
	_ = tp.Ajouter(ctx, emp(3), rep(3))
	ancienne()
	if len(b.lots) != 1 || tp.Attente() != 1 {
		t.Errorf("la minuterie périmée ne doit pas écrire le lot suivant : %d lots", len(b.lots))
	}
}

func TestFermerEcritLeTampon(t *testing.T) {
	ctx := context.Background()
	tp, b, _ := nouveau(t, 20)
	for i := range 3 {
		_ = tp.Ajouter(ctx, emp(i), rep(i))
	}
	if err := tp.Fermer(ctx); err != nil {
		t.Fatal(err)
	}
	if len(b.lots) != 1 || len(b.lots[0]) != 3 {
		t.Fatalf("lot de 3 attendu à la fermeture : %+v", b.lots)
	}
	if err := tp.Ajouter(ctx, emp(9), rep(9)); !errors.Is(err, ErrFerme) {
		t.Errorf("après la fermeture : %v", err)
	}
}

func TestDoublonDansLeTamponEtEnBase(t *testing.T) {
	ctx := context.Background()
	tp, _, _ := nouveau(t, 20)
	_ = tp.Ajouter(ctx, emp(1), rep(1))
	if deja, _ := tp.DejaParticipe(ctx, emp(1)); !deja {
		t.Error("l'empreinte du tampon devrait être repérée")
	}
	if err := tp.Ajouter(ctx, emp(1), rep(2)); !errors.Is(err, ErrDejaParticipe) {
		t.Errorf("doublon dans le tampon : %v", err)
	}
	if err := tp.Vider(ctx); err != nil {
		t.Fatal(err)
	}
	if deja, _ := tp.DejaParticipe(ctx, emp(1)); !deja {
		t.Error("l'empreinte écrite en base devrait être repérée")
	}
	if err := tp.Ajouter(ctx, emp(1), rep(3)); !errors.Is(err, ErrDejaParticipe) {
		t.Errorf("doublon en base : %v", err)
	}
	if deja, _ := tp.DejaParticipe(ctx, emp(2)); deja {
		t.Error("faux doublon")
	}
}

func TestPanneDeLaBaseGardeLeLot(t *testing.T) {
	ctx := context.Background()
	tp, b, m := nouveau(t, 2)
	b.panne = true
	_ = tp.Ajouter(ctx, emp(1), rep(1))
	if err := tp.Ajouter(ctx, emp(2), rep(2)); err != nil {
		t.Fatalf("l'envoi doit rester accepté : %v", err)
	}
	if tp.Attente() != 2 {
		t.Fatalf("le lot doit rester en mémoire : %d", tp.Attente())
	}
	m.declencher() // nouvel essai, encore en panne : minuterie relancée
	if m.lancees != 2 {
		t.Errorf("%d minuteries", m.lancees)
	}
	b.panne = false
	m.declencher()
	if len(b.lots) != 1 || len(b.lots[0]) != 2 || tp.Attente() != 0 {
		t.Errorf("le lot devrait être écrit au nouvel essai : %+v", b.lots)
	}
}

// Ajouts concurrents de la même empreinte : une seule passe.
func TestConcurrence(t *testing.T) {
	ctx := context.Background()
	tp, b, _ := nouveau(t, 7)
	var wg sync.WaitGroup
	var mu sync.Mutex
	acceptes := 0
	for i := range 400 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if tp.Ajouter(ctx, emp(i%100), rep(i)) == nil {
				mu.Lock()
				acceptes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	_ = tp.Vider(ctx)
	if acceptes != 100 || len(b.ecrites) != 100 {
		t.Errorf("%d acceptés, %d écrits ; 100 attendus", acceptes, len(b.ecrites))
	}
}

// Le mélange change l'ordre : sur 50 lots de 20, l'ordre d'arrivée n'est
// presque jamais conservé, et les deux listes sont mélangées indépendamment.
func TestMelange(t *testing.T) {
	ctx := context.Background()
	tp, b, _ := nouveau(t, 20)
	for i := range 1000 {
		_ = tp.Ajouter(ctx, emp(i), rep(i))
	}
	conserves, apparies := 0, 0
	for k, lot := range b.lots {
		for j, e := range lot {
			if e == emp(k*20+j) {
				conserves++
			}
			// La j-ième participation et la j-ième réponse viennent-elles du même envoi ?
			if int(b.reps[k][j].Q3) == int(e[0])+int(e[1])<<8 {
				apparies++
			}
		}
	}
	// Au hasard, 1 position conservée et 1 appariement par lot en moyenne
	// (50 sur 1 000), avec un écart type d'environ 7.
	if conserves > 100 || apparies > 100 {
		t.Errorf("%d positions conservées, %d appariements sur 1 000 : le mélange est insuffisant", conserves, apparies)
	}
}

// Avec la vraie minuterie et un délai court (DIGUEE_TAMPON_DELAI est
// configurable), le tampon s'écrit seul.
func TestDelaiReel(t *testing.T) {
	b := &baseFactice{}
	tp := Nouveau(Options{Ecrivain: b, Taille: 20, Delai: 20 * time.Millisecond,
		Journal: slog.New(slog.NewTextHandler(io.Discard, nil))})
	_ = tp.Ajouter(context.Background(), emp(1), rep(1))
	for range 200 {
		if tp.Attente() == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.lots) != 1 {
		t.Fatalf("%d lots écrits après le délai", len(b.lots))
	}
}

type surveillanceFactice struct {
	comptes map[string]int
}

func (s *surveillanceFactice) Compter(dimension, valeur string) { s.comptes[dimension+"="+valeur]++ }

func TestRegionsCompteesALEcritureDuLot(t *testing.T) {
	ctx := context.Background()
	b := &baseFactice{}
	m := &minuterieManuelle{}
	s := &surveillanceFactice{comptes: map[string]int{}}
	tp := Nouveau(Options{Ecrivain: b, Taille: 3, Delai: time.Hour, Minuterie: m.lancer, Surveillance: s,
		Journal: slog.New(slog.NewTextHandler(io.Discard, nil))})
	for i := range 2 {
		r := rep(i)
		r.Region = int16(4 + i%2)
		if err := tp.Ajouter(ctx, emp(i), r); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.comptes) != 0 {
		t.Fatal("rien n'est compté à l'envoi d'une réponse")
	}
	r := rep(2)
	r.Region = 4
	if err := tp.Ajouter(ctx, emp(2), r); err != nil {
		t.Fatal(err)
	}
	if s.comptes["region=4"] != 2 || s.comptes["region=5"] != 1 || len(s.comptes) != 2 {
		t.Errorf("comptes : %v", s.comptes)
	}
}
