package base_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/ancrage"
	"github.com/2guntan/diguee/internal/journal"
)

// Les ancrages sont gardés jour par jour, en ajout seul ; la dernière
// révision d'une preuve fait foi, et la tête ancrée doit être une entrée du
// journal avec son empreinte.
func TestAncragesEnBase(t *testing.T) {
	ctx := context.Background()
	b, _, j := journalDeTest(t)
	e, err := j.Inscrire(ctx, journal.Ouverture, map[string]string{"jour": "2027-01-15"})
	if err != nil {
		t.Fatal(err)
	}
	j1 := time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC)
	j2 := j1.AddDate(0, 0, 1)
	ots := append(append([]byte{}, ancrage.EnTeteOTS...), bytes.Repeat([]byte{1}, 40)...)
	p := ancrage.Preuve{Jour: j1, Numero: e.Numero, Empreinte: e.Empreinte, OTS: ots, Calendriers: 3}
	if ok, err := b.EnregistrerPreuve(ctx, p); err != nil || !ok {
		t.Fatalf("enregistrement : %v, %v", ok, err)
	}
	if ok, err := b.EnregistrerPreuve(ctx, p); err != nil || ok {
		t.Fatalf("doublon : %v, %v", ok, err)
	}
	faux := p
	faux.Jour, faux.Empreinte = j2, journal.Empreinte{9}
	if _, err := b.EnregistrerPreuve(ctx, faux); err == nil || !strings.Contains(err.Error(), "n'a pas cette empreinte") {
		t.Fatalf("tête étrangère au journal acceptée : %v", err)
	}
	p2 := p
	p2.Revision, p2.Bitcoin, p2.OTS = 1, true, append(ots, 2)
	if _, err := b.EnregistrerPreuve(ctx, p2); err != nil {
		t.Fatal(err)
	}
	lue, err := b.LirePreuve(ctx, j1)
	if err != nil || lue.Revision != 1 || !lue.Bitcoin || !bytes.Equal(lue.OTS, p2.OTS) || lue.Empreinte != e.Empreinte || !lue.Jour.Equal(j1) {
		t.Fatalf("lecture : %+v, %v", lue, err)
	}
	if lue, err := b.LirePreuve(ctx, j2); err != nil || lue != nil {
		t.Fatalf("jour sans preuve : %+v, %v", lue, err)
	}
	for _, a := range []ancrage.Archive{
		{Jour: j1, Cible: ancrage.CibleJournal, Lien: "https://web.archive.org/web/20270115061500/https://consultation.diguee.sn/journal"},
		{Jour: j2, Cible: ancrage.CibleExport, Lien: "https://web.archive.org/web/20270116061500/https://consultation.diguee.sn/journal.json"},
	} {
		if ok, err := b.EnregistrerArchive(ctx, a); err != nil || !ok {
			t.Fatalf("archive : %v, %v", ok, err)
		}
		if ok, err := b.EnregistrerArchive(ctx, a); err != nil || ok {
			t.Fatalf("archive en double : %v, %v", ok, err)
		}
	}
	if _, err := b.EnregistrerArchive(ctx, ancrage.Archive{Jour: j1, Cible: "autre", Lien: "https://x"}); err == nil {
		t.Error("cible inconnue acceptée")
	}
	etats, err := b.Ancrages(ctx, 10)
	if err != nil || len(etats) != 2 {
		t.Fatalf("ancrages : %+v, %v", etats, err)
	}
	if !etats[0].Jour.Equal(j2) || etats[0].Preuve != nil || etats[0].Archive(ancrage.CibleExport) == nil ||
		!etats[1].Jour.Equal(j1) || etats[1].Preuve == nil || etats[1].Preuve.Revision != 1 || etats[1].Archive(ancrage.CibleJournal) == nil {
		t.Fatalf("ancrages : %+v", etats)
	}
	if etats, _ := b.Ancrages(ctx, 1); len(etats) != 1 || !etats[0].Jour.Equal(j2) {
		t.Fatalf("limite : %+v", etats)
	}
	for _, q := range []string{
		"UPDATE ancrage_preuve SET bitcoin = true", "DELETE FROM ancrage_preuve", "TRUNCATE ancrage_preuve",
		"UPDATE ancrage_archive SET lien = 'https://autre'", "DELETE FROM ancrage_archive", "TRUNCATE ancrage_archive",
	} {
		if _, err := b.Pool.Exec(ctx, q); err == nil || !strings.Contains(err.Error(), "ajout seul") {
			t.Errorf("%q devrait être refusé : %v", q, err)
		}
	}
}
