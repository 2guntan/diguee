package publication_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/base"
	"github.com/2guntan/diguee/internal/base/basetest"
	"github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/publication"
	"github.com/2guntan/diguee/internal/questionnaire"
)

var (
	hier        = time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	aujourdhui  = hier.AddDate(0, 0, 1)
	demain      = hier.AddDate(0, 0, 2)
	rienEnPlace = func(*publication.Instantane) ([]byte, error) { return []byte("<p>instantané</p>"), nil }
)

func reponses(n int) []questionnaire.Reponse {
	rs := make([]questionnaire.Reponse, n)
	for i := range rs {
		rs[i] = questionnaire.Reponse{Version: 1, Q1: 1, Q2: 2, Q3: int16(i % 5), Q4: 1, Q5: 1, Q6: 1,
			Q7: []int16{1}, Themes: []int16{1, 2, 3}, Region: int16(1 + i%2), TrancheAge: 2, Sexe: 1, Inscrit: 1}
	}
	return rs
}

func ecrire(t *testing.T, b *base.Base, jour time.Time, n int) {
	t.Helper()
	if err := b.EcrireLot(context.Background(), jour, nil, reponses(n)); err != nil {
		t.Fatal(err)
	}
}

func publieur(b *base.Base) *publication.Publieur {
	h := time.Date(2026, 12, 2, 6, 0, 0, 0, time.UTC)
	return &publication.Publieur{Magasin: b, Questionnaire: questionnaire.V1(), SeuilPublication: 1000, SeuilCase: 30,
		Rendu: rienEnPlace, Maintenant: func() time.Time { return h }}
}

func publier(t *testing.T, p *publication.Publieur, jour time.Time) publication.Resultat {
	t.Helper()
	r, err := p.Publier(context.Background(), jour)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func entreesPublication(t *testing.T, b *base.Base) []journal.Entree {
	t.Helper()
	es, err := journal.Nouveau(b, nil).Toutes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ps []journal.Entree
	for _, e := range es {
		if e.Type == journal.PublicationQuotidienne {
			ps = append(ps, e)
		}
	}
	return ps
}

// Avec 999 réponses, le compteur seul ; à partir de 1 000, les résultats
// apparaissent à la publication suivante. Une réponse écrite aujourd'hui
// n'apparaît que dans la publication de demain.
func TestSeuilEtReponsesDuJour(t *testing.T) {
	b := basetest.Ouvrir(t)
	p := publieur(b)
	ecrire(t, b, hier, 999)
	ecrire(t, b, aujourdhui, 1) // écrite le jour même de la publication

	r := publier(t, p, aujourdhui)
	if !r.Nouvelle || r.Publication.ResultatsPublies || r.Publication.Reponses != 990 {
		t.Fatalf("publication d'aujourd'hui : %+v", r.Publication.Resume)
	}
	inst, err := r.Publication.LireInstantane()
	if err != nil || inst.ResultatsPublies || len(inst.Tableaux) != 0 || inst.Jour != "2026-12-02" || inst.DernierJourInclus != "2026-12-01" {
		t.Fatalf("instantané : %+v, %v", inst, err)
	}
	r = publier(t, p, demain)
	if !r.Nouvelle || !r.Publication.ResultatsPublies || r.Publication.Reponses != 1000 {
		t.Fatalf("publication de demain : %+v", r.Publication.Resume)
	}
	if inst, _ := r.Publication.LireInstantane(); len(inst.Tableaux) == 0 {
		t.Error("tableaux attendus au-delà du seuil")
	}
	// Le compteur de l'accueil suit la dernière publication.
	d, err := b.DernierResume(context.Background())
	if err != nil || d == nil || !d.Jour.Equal(demain) || d.Reponses != 1000 || !d.ResultatsPublies {
		t.Errorf("dernier résumé : %+v, %v", d, err)
	}
	rs, err := b.Resumes(context.Background())
	if err != nil || len(rs) != 2 || !rs[0].Jour.Equal(demain) || !rs[1].Jour.Equal(aujourdhui) {
		t.Errorf("résumés : %+v, %v", rs, err)
	}
}

// Une publication par jour, même appelée plusieurs fois en même temps ; une
// entrée au journal par publication, qui porte l'empreinte de l'instantané.
func TestUnePublicationParJourEtUneEntreeAuJournal(t *testing.T) {
	b := basetest.Ouvrir(t)
	p := publieur(b)
	ecrire(t, b, hier, 1200)

	var (
		mu       sync.Mutex
		nouveaux int
		wg       sync.WaitGroup
	)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := p.Publier(context.Background(), aujourdhui)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if r.Nouvelle {
				nouveaux++
			}
		}()
	}
	wg.Wait()
	if nouveaux != 1 {
		t.Fatalf("%d publications créées, une seule attendue", nouveaux)
	}
	publier(t, p, demain)
	es := entreesPublication(t, b)
	if len(es) != 2 {
		t.Fatalf("%d entrées « publication quotidienne », 2 attendues", len(es))
	}
	for i, jour := range []time.Time{aujourdhui, demain} {
		pub, err := b.LirePublication(context.Background(), jour)
		if err != nil || pub == nil {
			t.Fatal(err)
		}
		var c publication.ContenuJournal
		if err := json.Unmarshal(es[i].Contenu, &c); err != nil {
			t.Fatal(err)
		}
		if c.Jour != jour.Format(time.DateOnly) || c.Reponses != 1200 || !c.ResultatsPublies ||
			c.SeuilPublication != 1000 || c.SeuilCase != 30 || c.Tableaux != 46 {
			t.Errorf("contenu de l'entrée : %+v", c)
		}
		if h := sha256.Sum256(pub.JSON); c.SHA256Instantane != journal.Empreinte(h).String() || pub.Empreinte != journal.Empreinte(h) {
			t.Errorf("empreinte de l'instantané : %s", c.SHA256Instantane)
		}
	}
	// Le journal reste vérifiable.
	tous, _ := journal.Nouveau(b, nil).Toutes(context.Background())
	if rap, err := journal.VerifierChaine(tous); err != nil {
		t.Errorf("chaîne : %v (%+v)", err, rap)
	}
}

// Un instantané publié ne change plus jamais : ni par la base (UPDATE,
// DELETE, TRUNCATE refusés), ni par une nouvelle publication du même jour.
func TestInstantaneImmuable(t *testing.T) {
	b := basetest.Ouvrir(t)
	p := publieur(b)
	ecrire(t, b, hier, 1100)
	avant := publier(t, p, aujourdhui).Publication

	ctx := context.Background()
	for _, sql := range []string{
		"UPDATE publication SET reponses = 0",
		"UPDATE publication SET instantane = '{}'",
		"DELETE FROM publication",
		"TRUNCATE publication",
	} {
		if _, err := b.Pool.Exec(ctx, sql); err == nil || !strings.Contains(err.Error(), "ajout seul") {
			t.Errorf("%s : %v", sql, err)
		}
	}
	// Des réponses arrivent encore pour hier (lot écrit tard) : la
	// publication du jour reste celle qui a été publiée.
	ecrire(t, b, hier, 300)
	r := publier(t, p, aujourdhui)
	if r.Nouvelle || string(r.Publication.JSON) != string(avant.JSON) || string(r.Publication.HTML) != string(avant.HTML) ||
		r.Publication.Empreinte != avant.Empreinte {
		t.Error("la publication du jour a changé")
	}
	if n := len(entreesPublication(t, b)); n != 1 {
		t.Errorf("%d entrées au journal", n)
	}
}

// Tout ou rien : si la mise en page échoue, ni publication ni entrée.
func TestEchecSansTrace(t *testing.T) {
	b := basetest.Ouvrir(t)
	p := publieur(b)
	p.Rendu = func(*publication.Instantane) ([]byte, error) { return nil, errors.New("panne") }
	ecrire(t, b, hier, 10)
	if _, err := p.Publier(context.Background(), aujourdhui); err == nil {
		t.Fatal("erreur attendue")
	}
	if d, err := b.DernierResume(context.Background()); err != nil || d != nil {
		t.Errorf("publication enregistrée malgré l'échec : %+v, %v", d, err)
	}
	if n := len(entreesPublication(t, b)); n != 0 {
		t.Errorf("%d entrées au journal", n)
	}
}

// Le planificateur publie le jour dû : avant l'heure, la veille.
func TestPlanificateurRattrape(t *testing.T) {
	b := basetest.Ouvrir(t)
	ecrire(t, b, hier, 5)
	h := time.Date(2026, 12, 2, 5, 59, 0, 0, time.UTC)
	pl := &publication.Planificateur{Publieur: publieur(b), Heure: 6 * time.Hour, Maintenant: func() time.Time { return h }}
	r, err := pl.Executer(context.Background())
	if err != nil || !r.Nouvelle || !r.Publication.Jour.Equal(hier) || r.Publication.Reponses != 0 {
		t.Fatalf("05:59 : %+v, %v", r.Publication, err)
	}
	h = h.Add(time.Minute)
	r, err = pl.Executer(context.Background())
	if err != nil || !r.Nouvelle || !r.Publication.Jour.Equal(aujourdhui) {
		t.Fatalf("06:00 : %+v, %v", r.Publication, err)
	}
	if r, _ = pl.Executer(context.Background()); r.Nouvelle {
		t.Error("une seule publication par jour")
	}
}
