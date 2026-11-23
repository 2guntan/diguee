package base_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/base"
	"github.com/2guntan/diguee/internal/base/basetest"
	"github.com/2guntan/diguee/internal/empreinte"
	"github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/questionnaire"
)

// Clôture (ticket #16) : la table participation disparaît avec l'entrée du
// journal qui le dit, dans la même transaction ; ensuite, les droits de
// l'application se réaccordent sans elle, et le serveur garde exactement
// ses droits. Une table supprimée sans cette entrée est une erreur.
func TestSuppressionDesParticipations(t *testing.T) {
	ctx := context.Background()
	app := basetest.OuvrirApplication(t)
	b := app.Proprietaire
	jour := time.Date(2027, 5, 21, 0, 0, 0, 0, time.UTC)
	if err := b.EcrireLot(ctx, jour, []empreinte.Empreinte{{1}, {2}}, []questionnaire.Reponse{{Version: 1, Q3: 1}, {Version: 1, Q3: 2}}); err != nil {
		t.Fatal(err)
	}
	if d, ok, err := b.DernierJourReponse(ctx); err != nil || !ok || !d.Equal(jour) {
		t.Fatalf("dernier jour %v %v %v", d, ok, err)
	}
	// Le rôle de l'application ne peut ni vider ni supprimer la table.
	for _, o := range []string{"TRUNCATE participation", "DROP TABLE participation", "DELETE FROM participation"} {
		if _, err := app.App.Pool.Exec(ctx, o); err == nil {
			t.Errorf("%s accepté pour le rôle de l'application", o)
		}
	}
	horloge := func() time.Time { return time.Date(2027, 5, 24, 9, 0, 0, 0, time.UTC) }
	// Un échec de l'inscription annule la suppression.
	err := b.SupprimerParticipations(ctx, func(context.Context, int64, journal.Magasin) error { return context.Canceled })
	if err == nil {
		t.Fatal("échec de l'inscription non signalé")
	}
	if existe, _ := b.TableExiste(ctx, "participation"); !existe {
		t.Fatal("table supprimée sans son entrée au journal")
	}
	var lignes int64
	err = b.SupprimerParticipations(ctx, func(ctx context.Context, n int64, m journal.Magasin) error {
		lignes = n
		_, err := journal.Nouveau(m, horloge).Inscrire(ctx, journal.Cloture, map[string]any{"etape": base.EtapeSuppressionParticipations})
		return err
	})
	if err != nil || lignes != 2 {
		t.Fatalf("suppression : %d %v", lignes, err)
	}
	if existe, _ := b.TableExiste(ctx, "participation"); existe {
		t.Fatal("participation existe encore")
	}
	if ok, err := b.ParticipationsDetruites(ctx); err != nil || !ok {
		t.Fatalf("ParticipationsDetruites %v %v", ok, err)
	}
	ordres, err := b.LibererEspace(ctx)
	if err != nil || ordres[len(ordres)-1] != "CHECKPOINT" || !strings.Contains(strings.Join(ordres, ";"), `VACUUM (FULL, ANALYZE) "reponse"`) {
		t.Fatalf("libération %v %v", ordres, err)
	}
	if n, _ := b.CompterReponses(ctx); n != 2 {
		t.Errorf("%d réponses", n)
	}
	// Les droits se réaccordent sans participation, et le serveur démarre.
	if err := b.AccorderDroits(ctx, base.RoleApplication); err != nil {
		t.Fatal(err)
	}
	if err := app.App.VerifierRoleApplication(ctx); err != nil {
		t.Error(err)
	}
	if err := app.App.VerifierSchemaAJour(ctx); err != nil {
		t.Error(err)
	}
	ls, err := b.CompterTables(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range ls {
		if l.Nom == "participation" {
			t.Error("participation comptée")
		}
	}

	// Sans l'entrée du journal, une table participation absente est refusée.
	autre := basetest.OuvrirApplication(t)
	if _, err := autre.Proprietaire.Pool.Exec(ctx, "DROP TABLE participation"); err != nil {
		t.Fatal(err)
	}
	if err := autre.Proprietaire.AccorderDroits(ctx, base.RoleApplication); err == nil || !strings.Contains(err.Error(), "participation") {
		t.Errorf("table supprimée à la main : %v", err)
	}
}
