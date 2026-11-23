package cloture

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/journal/journaltest"
	"github.com/2guntan/diguee/internal/publication"
)

var muet = slog.New(slog.NewTextHandler(io.Discard, nil))

type faux struct {
	fermetures  int
	echecTampon error
	dernier     time.Time
	publies     []time.Time
}

func (f *faux) Fermer(context.Context) error {
	f.fermetures++
	return f.echecTampon
}

func (f *faux) DernierJourReponse(context.Context) (time.Time, bool, error) {
	return f.dernier, !f.dernier.IsZero(), nil
}

func (f *faux) Publier(_ context.Context, jour time.Time) (publication.Resultat, error) {
	f.publies = append(f.publies, jour)
	js := []byte(`{"jour":"` + jour.Format(time.DateOnly) + `"}`)
	return publication.Resultat{Publication: &publication.Publication{
		Resume: publication.Resume{Jour: jour, Reponses: 1230}, JSON: js, Empreinte: sha256.Sum256(js)}, Nouvelle: true}, nil
}

func TestFermeture(t *testing.T) {
	ctx := context.Background()
	h := time.Date(2027, 5, 22, 23, 59, 0, 0, time.UTC)
	f := &faux{}
	j := journal.Nouveau(&journaltest.Memoire{}, func() time.Time { return h })
	fe := &Fermeture{Cloture: time.Date(2027, 5, 23, 0, 0, 0, 0, time.UTC), Heure: 6 * time.Hour,
		Tampon: f, Publieur: f, Reponses: f, Journal: j, Maintenant: func() time.Time { return h }, Log: muet}

	// Avant la clôture : rien.
	b, err := fe.Executer(ctx)
	if err != nil || b.Faite || !b.Prochain.Equal(fe.Cloture) || f.fermetures != 0 {
		t.Fatalf("avant : %+v %v, %d fermetures", b, err, f.fermetures)
	}
	// À la clôture : le tampon est écrit (un lot du 23), la publication
	// attend le 24 à 06:00.
	h = fe.Cloture.Add(30 * time.Second)
	f.dernier = time.Date(2027, 5, 23, 0, 0, 0, 0, time.UTC)
	b, err = fe.Executer(ctx)
	if err != nil || b.Faite || f.fermetures != 1 || !b.Prochain.Equal(time.Date(2027, 5, 24, 6, 0, 0, 0, time.UTC)) || len(f.publies) != 0 {
		t.Fatalf("à la clôture : %+v %v", b, err)
	}
	// Échec du tampon : erreur, rien d'inscrit.
	f.echecTampon = errors.New("base indisponible")
	if _, err := fe.Executer(ctx); err == nil {
		t.Fatal("l'échec du tampon doit être signalé")
	}
	f.echecTampon = nil
	// Le 24 à 06:00 : publication du 24, puis l'entrée « cloture ».
	h = time.Date(2027, 5, 24, 6, 0, 0, 0, time.UTC)
	b, err = fe.Executer(ctx)
	if err != nil || !b.Faite || b.Entree == nil || len(f.publies) != 1 || !f.publies[0].Equal(time.Date(2027, 5, 24, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("publication : %+v %v %v", b, err, f.publies)
	}
	var c ContenuFermeture
	if err := json.Unmarshal(b.Entree.Contenu, &c); err != nil || c.Etape != EtapeFermeture || c.DateCloture != "2027-05-23" ||
		c.DernierePublication != "2027-05-24" || c.ReponsesPubliees != 1230 || len(c.SHA256Instantane) != 64 {
		t.Errorf("contenu %s %v", b.Entree.Contenu, err)
	}
	if b.Entree.Type != journal.Cloture || !b.Entree.Horodatage.Equal(h) {
		t.Errorf("entrée %+v", b.Entree)
	}
	// Idempotente : plus rien ensuite.
	b, err = fe.Executer(ctx)
	if n, _ := j.Compter(ctx); err != nil || !b.Faite || b.Entree != nil || n != 1 || len(f.publies) != 1 {
		t.Errorf("relance : %+v %v, %d entrées", b, err, n)
	}

	// Serveur arrêté pendant l'heure due, redémarré le 26 à 08:00 : la
	// publication du 26 (qui porte sur toutes les réponses).
	f2 := &faux{dernier: time.Date(2027, 5, 22, 0, 0, 0, 0, time.UTC)}
	h = time.Date(2027, 5, 26, 8, 0, 0, 0, time.UTC)
	fe2 := &Fermeture{Cloture: fe.Cloture, Heure: 6 * time.Hour, Tampon: f2, Publieur: f2, Reponses: f2,
		Journal: journal.Nouveau(&journaltest.Memoire{}, func() time.Time { return h }), Maintenant: func() time.Time { return h }, Log: muet}
	if b, err := fe2.Executer(ctx); err != nil || !b.Faite || len(f2.publies) != 1 || !f2.publies[0].Equal(time.Date(2027, 5, 26, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("rattrapage : %+v %v %v", b, err, f2.publies)
	}
	// Sans lot au moment de la clôture (dernières réponses du 22) : la
	// publication du 23 suffit.
	f3 := &faux{dernier: time.Date(2027, 5, 22, 0, 0, 0, 0, time.UTC)}
	h = time.Date(2027, 5, 23, 0, 1, 0, 0, time.UTC)
	fe3 := &Fermeture{Cloture: fe.Cloture, Heure: 6 * time.Hour, Tampon: f3, Publieur: f3, Reponses: f3,
		Journal: journal.Nouveau(&journaltest.Memoire{}, func() time.Time { return h }), Maintenant: func() time.Time { return h }, Log: muet}
	if b, _ := fe3.Executer(ctx); !b.Prochain.Equal(time.Date(2027, 5, 23, 6, 0, 0, 0, time.UTC)) {
		t.Errorf("sans dernier lot : %+v", b)
	}
}
