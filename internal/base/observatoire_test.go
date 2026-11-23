package base_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/2guntan/diguee/internal/base"
	"github.com/2guntan/diguee/internal/base/basetest"
	"github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/observatoire"
	"github.com/2guntan/diguee/internal/stockage"
)

func membreSQL(t *testing.T, b *base.Base, id, identifiant string) {
	t.Helper()
	if _, err := b.Pool.Exec(context.Background(), `INSERT INTO membre (id, identifiant, nom, roles, mot_de_passe, totp_secret_chiffre)
		VALUES ($1, $2, $3, ARRAY['redacteur', 'relecteur'], 'pbkdf2-sha256$x', '\x00')`, id, identifiant, "Personne "+identifiant); err != nil {
		t.Fatal(err)
	}
}

const (
	awa    = "00000000-0000-4000-8000-00000000000a"
	moussa = "00000000-0000-4000-8000-00000000000b"
)

// obsDeTest prépare un élu, une source, un engagement validé et un statut
// « tenu » proposé par awa ; renvoie l'identifiant du statut.
func obsDeTest(t *testing.T, valeur observatoire.Valeur) (*base.Base, *observatoire.Service, int64) {
	t.Helper()
	b := basetest.Ouvrir(t)
	membreSQL(t, b, awa, "awa")
	membreSQL(t, b, moussa, "moussa")
	ctx := context.Background()
	d, err := stockage.NouveauDossier(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := observatoire.NouveauService(b, d, 1<<20, func() time.Time { return time.Date(2026, 12, 2, 9, 30, 0, 0, time.UTC) })
	jour := func(s string) time.Time { j, _ := time.Parse(time.DateOnly, s); return j }
	elu, err := svc.SaisirElu(ctx, awa, observatoire.SaisieElu{Nom: "Élu de test", Fonction: "Maire",
		Mandat: observatoire.Mandat{Debut: jour("2022-01-01"), Fin: jour("2027-01-01")}})
	if err != nil {
		t.Fatal(err)
	}
	src, err := svc.SaisirSource(ctx, awa, observatoire.SaisieSource{URL: "https://exemple.sn/discours", Titre: "Discours",
		Date: jour("2024-04-02"), Fichier: []byte("%PDF-1.4 discours")})
	if err != nil {
		t.Fatal(err)
	}
	eng, err := svc.SaisirEngagement(ctx, awa, observatoire.SaisieEngagement{Elu: elu, Citation: "Nous construirons 10 écoles.",
		DateDeclaration: jour("2024-04-02"), Echeance: jour("2026-12-31"), Sources: []int64{src.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ValiderEngagement(ctx, moussa, eng); err != nil {
		t.Fatal(err)
	}
	st, err := svc.RedigerStatut(ctx, awa, eng, valeur, "Les écoles sont ouvertes.", []int64{src.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Proposer(ctx, awa, st); err != nil {
		t.Fatal(err)
	}
	return b, svc, st
}

func codeSQL(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code + " " + pe.ConstraintName
	}
	return ""
}

// T7 : valider sa propre saisie échoue, par l'API comme en SQL direct.
func TestValiderSaPropreSaisieEchoue(t *testing.T) {
	ctx := context.Background()
	b, svc, st := obsDeTest(t, observatoire.Tenu)
	if err := svc.Valider(ctx, awa, st); !errors.Is(err, observatoire.ErrQuatreYeux) {
		t.Errorf("API : %v", err)
	}
	// Le magasin seul (sans le contrôle du service) : refus de la base.
	if err := b.Valider(ctx, st, observatoire.Acte{Par: awa, Le: time.Now()}); !errors.Is(err, observatoire.ErrQuatreYeux) {
		t.Errorf("magasin : %v", err)
	}
	// SQL direct, en recopiant le vrai rédacteur : la contrainte CHECK.
	_, err := b.Pool.Exec(ctx, `INSERT INTO statut_validation (statut_id, redige_par, valide_par, valide_le)
		VALUES ($1, $2, $2, '2026-12-02')`, st, awa)
	if c := codeSQL(err); c != "23514 statut_quatre_yeux" {
		t.Errorf("SQL direct : %v (%s)", err, c)
	}
	// En mentant sur le rédacteur : la clé étrangère composée.
	_, err = b.Pool.Exec(ctx, `INSERT INTO statut_validation (statut_id, redige_par, valide_par, valide_le)
		VALUES ($1, $2, $3, '2026-12-02')`, st, moussa, awa)
	if c := codeSQL(err); !strings.HasPrefix(c, "23503") {
		t.Errorf("rédacteur falsifié : %v (%s)", err, c)
	}
	// Même règle pour un engagement.
	if _, err := b.Pool.Exec(ctx, `INSERT INTO engagement (id, elu_id, citation, contexte, date_declaration, echeance, cree_par, cree_le)
		VALUES (2, 1, 'x', '', '2024-01-01', '2025-01-01', $1, '2026-12-02')`, awa); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Pool.Exec(ctx, "INSERT INTO engagement_source VALUES (2, 1)"); err != nil {
		t.Fatal(err)
	}
	_, err = b.Pool.Exec(ctx, `INSERT INTO engagement_validation VALUES (2, $1, $1, '2026-12-02')`, awa)
	if c := codeSQL(err); c != "23514 engagement_quatre_yeux" {
		t.Errorf("engagement en SQL direct : %v (%s)", err, c)
	}
	if err := svc.ValiderEngagement(ctx, awa, 2); !errors.Is(err, observatoire.ErrQuatreYeux) {
		t.Errorf("engagement par l'API : %v", err)
	}
	// Un autre relecteur valide, et publie.
	if err := svc.Valider(ctx, moussa, st); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Publier(ctx, awa, st); err != nil {
		t.Fatal(err)
	}
}

// T8 : UPDATE, DELETE et TRUNCATE échouent sur les statuts et toutes les
// tables de l'observatoire, pour le propriétaire des tables.
func TestStatutsEnAjoutSeul(t *testing.T) {
	ctx := context.Background()
	b, svc, st := obsDeTest(t, observatoire.Tenu)
	if err := svc.Valider(ctx, moussa, st); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Publier(ctx, moussa, st); err != nil {
		t.Fatal(err)
	}
	tables := []string{"elu", "source", "engagement", "engagement_source", "engagement_validation", "statut",
		"statut_preuve", "statut_proposition", "statut_validation", "statut_publication"}
	for _, table := range tables {
		for _, sql := range []string{
			"UPDATE " + table + " SET " + map[string]string{"elu": "nom = 'x'", "source": "titre = 'x'", "engagement": "citation = 'x'",
				"engagement_source": "source_id = source_id", "engagement_validation": "valide_le = valide_le", "statut": "valeur = 'non_tenu'",
				"statut_preuve": "source_id = source_id", "statut_proposition": "propose_le = propose_le",
				"statut_validation": "valide_le = valide_le", "statut_publication": "publie_le = publie_le"}[table],
			"DELETE FROM " + table,
		} {
			_, err := b.Pool.Exec(ctx, sql)
			if c := codeSQL(err); c != "42501 " {
				t.Errorf("%s : %v (%s)", sql, err, c)
			}
		}
		if _, err := b.Pool.Exec(ctx, "TRUNCATE "+table+" CASCADE"); err == nil {
			t.Errorf("TRUNCATE %s accepté", table)
		}
	}
	var n int
	if err := b.Pool.QueryRow(ctx, "SELECT count(*) FROM statut WHERE valeur = 'tenu'").Scan(&n); err != nil || n != 1 {
		t.Errorf("statut modifié : %d, %v", n, err)
	}
}

// « Non tenu » et « partiel » ne sont pas publiables sans préavis (ticket
// #14), ni par l'API ni en SQL direct ; ils peuvent être validés.
func TestNonTenuEtPartielNonPubliables(t *testing.T) {
	ctx := context.Background()
	for _, v := range []observatoire.Valeur{observatoire.NonTenu, observatoire.Partiel} {
		b, svc, st := obsDeTest(t, v)
		if err := svc.Valider(ctx, moussa, st); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Publier(ctx, moussa, st); !errors.Is(err, observatoire.ErrPreavis) {
			t.Errorf("%s par l'API : %v", v, err)
		}
		// Le magasin seul, avec une entrée du journal inscrite pour l'occasion :
		// la contrainte refuse, et la transaction (journal compris) est annulée.
		_, err := b.Publier(ctx, st, observatoire.Acte{Par: moussa, Le: time.Now()},
			func(ctx context.Context, jm journal.Magasin, _ *observatoire.Engagement, _ *observatoire.Statut) (int64, error) {
				e, err := journal.Nouveau(jm, nil).Inscrire(ctx, journal.StatutPublie, map[string]any{"essai": 1})
				return e.Numero, err
			})
		if !errors.Is(err, observatoire.ErrPreavis) {
			t.Errorf("%s par le magasin : %v", v, err)
		}
		if n, _ := b.CompterJournal(ctx); n != 0 {
			t.Errorf("%s : %d entrée(s) au journal après un refus", v, n)
		}
		if _, err := journal.Nouveau(b, nil).Inscrire(ctx, journal.StatutPublie, map[string]any{"essai": 1}); err != nil {
			t.Fatal(err)
		}
		_, err = b.Pool.Exec(ctx, `INSERT INTO statut_publication VALUES ($1, $2, $3, '2026-12-02', 1)`, st, string(v), moussa)
		if c := codeSQL(err); c != "23514 statut_publiable_sans_preavis" {
			t.Errorf("%s en SQL direct : %v (%s)", v, err, c)
		}
		// En mentant sur la valeur : la clé étrangère composée.
		_, err = b.Pool.Exec(ctx, `INSERT INTO statut_publication VALUES ($1, 'tenu', $2, '2026-12-02', 1)`, st, moussa)
		if c := codeSQL(err); !strings.HasPrefix(c, "23503") {
			t.Errorf("%s, valeur falsifiée : %v (%s)", v, err, c)
		}
	}
}

// L'ordre du circuit est imposé par la base : pas de validation sans
// proposition, pas de publication sans validation ni entrée de journal,
// pas de proposition sans preuve, pas de preuve ajoutée après proposition.
func TestOrdreDuCircuitEnBase(t *testing.T) {
	ctx := context.Background()
	b, svc, st := obsDeTest(t, observatoire.Tenu)
	brouillon, err := svc.RedigerStatut(ctx, awa, 1, observatoire.EnCours, "En cours.", nil)
	if err != nil {
		t.Fatal(err)
	}
	for nom, sql := range map[string]string{
		"validation sans proposition": `INSERT INTO statut_validation VALUES (2, '` + awa + `', '` + moussa + `', '2026-12-02')`,
		"proposition sans preuve":     `INSERT INTO statut_proposition VALUES (2, '` + awa + `', '2026-12-02')`,
		"preuve après proposition":    `INSERT INTO statut_preuve VALUES (1, 1)`,
		"publication sans validation": `INSERT INTO statut_publication VALUES (1, 'tenu', '` + moussa + `', '2026-12-02', 1)`,
		"source après validation":     `INSERT INTO engagement_source VALUES (1, 1)`,
	} {
		if _, err := b.Pool.Exec(ctx, sql); err == nil {
			t.Errorf("%s acceptée", nom)
		}
	}
	if brouillon != 2 {
		t.Fatalf("brouillon %d", brouillon)
	}
	if err := svc.Valider(ctx, moussa, st); err != nil {
		t.Fatal(err)
	}
	if err := svc.Rejeter(ctx, moussa, st, "trop tard"); !errors.Is(err, observatoire.ErrEtape) {
		t.Errorf("rejet après validation : %v", err)
	}
	if _, err := b.Pool.Exec(ctx, `INSERT INTO statut_rejet VALUES ($1, $2, $3, '2026-12-02', 'x')`, st, awa, moussa); err == nil {
		t.Error("rejet après validation accepté en SQL")
	}
	// Publication sans entrée de journal correspondante.
	if _, err := b.Pool.Exec(ctx, `INSERT INTO statut_publication VALUES ($1, 'tenu', $2, '2026-12-02', 99)`, st, moussa); err == nil {
		t.Error("publication sans entrée de journal acceptée")
	}
	if err := svc.Proposer(ctx, moussa, brouillon); !errors.Is(err, observatoire.ErrPasLeRedacteur) {
		t.Errorf("proposition par un autre : %v", err)
	}
	if err := svc.Proposer(ctx, awa, brouillon); !errors.Is(err, observatoire.ErrSansPreuve) {
		t.Errorf("proposition sans preuve : %v", err)
	}
}
