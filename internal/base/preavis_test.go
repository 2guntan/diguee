package base_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/base"
	"github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/observatoire"
)

func jourDe(s string) time.Time {
	j, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return j
}

// entreeDuJour inscrit au journal public une entrée du type donné, datée du
// jour donné : de quoi tenter une publication en SQL direct.
func entreeDuJour(t *testing.T, b *base.Base, typ journal.Type, jour string) int64 {
	t.Helper()
	e, err := journal.Nouveau(b, func() time.Time { return jourDe(jour).Add(10 * time.Hour) }).
		Inscrire(context.Background(), typ, map[string]any{"essai": 1})
	if err != nil {
		t.Fatal(err)
	}
	return e.Numero
}

// T9 : un « non tenu » (ou un « partiel ») ne peut pas être publié avant le
// 15e jour après la date d'envoi enregistrée du préavis, ni par l'API, ni
// par le magasin, ni en SQL direct.
func TestNonTenuPublieSeulementApresLePreavis(t *testing.T) {
	ctx := context.Background()
	for _, v := range []observatoire.Valeur{observatoire.NonTenu, observatoire.Partiel} {
		t.Run(string(v), func(t *testing.T) {
			b, svc, st, h := obsDeTestHorloge(t, v)
			if err := svc.Valider(ctx, moussa, st); err != nil { // le 2 décembre 2026
				t.Fatal(err)
			}
			// Sans préavis : refus par l'API et en SQL direct.
			if _, err := svc.Publier(ctx, moussa, st); !errors.Is(err, observatoire.ErrSansPreavis) {
				t.Errorf("sans préavis, par l'API : %v", err)
			}
			n := entreeDuJour(t, b, journal.StatutPublie, "2026-12-02")
			_, err := b.Pool.Exec(ctx, `INSERT INTO statut_publication VALUES ($1, $2, $3, '2026-12-02', $4)`, st, string(v), moussa, n)
			if c := codeSQL(err); c != "23514 statut_preavis_echu" {
				t.Errorf("sans préavis, en SQL direct : %v (%s)", err, c)
			}
			// En mentant sur la valeur : la clé étrangère composée.
			_, err = b.Pool.Exec(ctx, `INSERT INTO statut_publication VALUES ($1, 'tenu', $2, '2026-12-02', $3)`, st, moussa, n)
			if c := codeSQL(err); !strings.HasPrefix(c, "23503") {
				t.Errorf("valeur falsifiée : %v (%s)", err, c)
			}

			// Le préavis lui-même : échéance d'au moins 15 jours, envoi après la
			// validation, pour un « non tenu » ou un « partiel » seulement.
			for nom, c := range map[string]struct{ sql, code string }{
				"échéance à 14 jours": {`INSERT INTO statut_preavis VALUES ($1, $2, '2026-12-02', '2026-12-16', 'courriel', $3, $4, '2026-12-02')`,
					"23514 statut_preavis_quinze_jours"},
				"envoi avant validation": {`INSERT INTO statut_preavis VALUES ($1, $2, '2026-12-01', '2026-12-30', 'courriel', $3, $4, '2026-12-02')`,
					"23514 statut_preavis_apres_validation"},
			} {
				_, err := b.Pool.Exec(ctx, c.sql, st, string(v), make([]byte, 32), awa)
				if got := codeSQL(err); got != c.code {
					t.Errorf("%s : %v (%s)", nom, err, got)
				}
			}
			// API : date d'envoi entre la validation et aujourd'hui.
			for _, j := range []string{"2026-12-01", "2026-12-03"} {
				if err := svc.EnregistrerPreavis(ctx, awa, st, jourDe(j), "Courriel au cabinet"); !errors.Is(err, observatoire.ErrDateEnvoi) {
					t.Errorf("envoi le %s : %v", j, err)
				}
			}
			if err := svc.EnregistrerPreavis(ctx, awa, st, jourDe("2026-12-02"), " "); !errors.Is(err, observatoire.ErrMoyen) {
				t.Errorf("sans moyen d'envoi : %v", err)
			}
			if err := svc.EnregistrerPreavis(ctx, awa, st, jourDe("2026-12-02"), "Courriel au cabinet, accusé de réception"); err != nil {
				t.Fatal(err)
			}
			if err := svc.EnregistrerPreavis(ctx, awa, st, jourDe("2026-12-02"), "Encore"); !errors.Is(err, observatoire.ErrEtape) {
				t.Errorf("second préavis : %v", err)
			}
			_, s, err := svc.Statut(ctx, st)
			if err != nil || s.PreavisEnvoye == nil || !s.PreavisEnvoye.Echeance.Equal(jourDe("2026-12-17")) ||
				s.PreavisEnvoye.ParNom != "Personne awa" || s.Etape() != observatoire.Preavis {
				t.Fatalf("préavis relu : %+v %v", s, err)
			}

			// Le 16 décembre (J+14) : refus partout.
			h.Jour("2026-12-16")
			if _, err := svc.Publier(ctx, moussa, st); !errors.Is(err, observatoire.ErrPreavisEnCours) {
				t.Errorf("J+14, par l'API : %v", err)
			}
			avant, _ := b.CompterJournal(ctx)
			_, err = b.Publier(ctx, st, observatoire.Acte{Par: moussa, Le: jourDe("2026-12-16")},
				func(ctx context.Context, jm journal.Magasin, _ *observatoire.Engagement, _ *observatoire.Statut) (int64, error) {
					e, err := journal.Nouveau(jm, h.Maintenant).Inscrire(ctx, journal.StatutPublie, map[string]any{"essai": 1})
					return e.Numero, err
				}, nil)
			if !errors.Is(err, observatoire.ErrPreavis) {
				t.Errorf("J+14, par le magasin : %v", err)
			}
			if apres, _ := b.CompterJournal(ctx); apres != avant {
				t.Errorf("J+14 : %d entrée(s) au journal après un refus", apres-avant)
			}
			n = entreeDuJour(t, b, journal.StatutPublie, "2026-12-16")
			_, err = b.Pool.Exec(ctx, `INSERT INTO statut_publication VALUES ($1, $2, $3, '2026-12-16', $4)`, st, string(v), moussa, n)
			if c := codeSQL(err); c != "23514 statut_preavis_echu" {
				t.Errorf("J+14, en SQL direct : %v (%s)", err, c)
			}
			// En antidatant… ou en postdatant la publication par rapport à son
			// entrée du journal.
			_, err = b.Pool.Exec(ctx, `INSERT INTO statut_publication VALUES ($1, $2, $3, '2026-12-17', $4)`, st, string(v), moussa, n)
			if c := codeSQL(err); c != "23514 statut_preavis_echu" {
				t.Errorf("date de publication différente de celle du journal : %v (%s)", err, c)
			}

			// Le 17 décembre (J+15) : publié, et inscrit au journal avec le préavis.
			h.Jour("2026-12-17")
			numero, err := svc.Publier(ctx, moussa, st)
			if err != nil {
				t.Fatal(err)
			}
			es, err := journal.Nouveau(b, nil).Lire(ctx, numero, numero)
			if err != nil || len(es) != 1 {
				t.Fatalf("entrée %d : %v", numero, err)
			}
			if c := string(es[0].Contenu); !strings.Contains(c, `"preavis_envoye_le":"2026-12-02"`) ||
				!strings.Contains(c, `"preavis_echeance":"2026-12-17"`) || !strings.Contains(c, `"dossier_sha256":"`) {
				t.Errorf("entrée du journal : %s", c)
			}
			f, err := svc.FichePublique(ctx, 1)
			if err != nil || len(f.Statuts) != 1 || f.Statuts[0].PreavisEnvoye == nil || f.Statuts[0].PreavisEnvoye.Moyen != "" ||
				len(f.Reponses) != 0 {
				t.Fatalf("fiche publique : %+v %v", f, err)
			}
		})
	}
}

// statutPreavise : un « non tenu » validé le 2 décembre 2026 et préavisé le
// même jour ; l'horloge reste au 2 décembre.
func statutPreavise(t *testing.T) (*base.Base, *observatoire.Service, int64, *horlogeObs) {
	t.Helper()
	ctx := context.Background()
	b, svc, st, h := obsDeTestHorloge(t, observatoire.NonTenu)
	if err := svc.Valider(ctx, moussa, st); err != nil {
		t.Fatal(err)
	}
	if err := svc.EnregistrerPreavis(ctx, moussa, st, jourDe("2026-12-02"), "Courrier au cabinet"); err != nil {
		t.Fatal(err)
	}
	return b, svc, st, h
}

const texteReponse = "Les travaux ont commencé le 3 novembre.\n\nTrois écoles ouvriront en janvier ; « le reste suivra »."

// Droit de réponse reçu pendant le préavis : vérifié par un autre membre
// (quatre yeux, dans l'API et en SQL), il est publié tel quel avec le statut,
// chacun avec son entrée du journal, dans la même transaction.
func TestDroitDeReponseAvantPublication(t *testing.T) {
	ctx := context.Background()
	b, svc, st, h := statutPreavise(t)
	saisie := observatoire.SaisieReponse{Texte: texteReponse, Canal: observatoire.AdresseOfficielle,
		CanalDetail: "courriel de cabinet@mairie.example", RecuLe: jourDe("2026-12-05")}
	h.Jour("2026-12-06")
	for nom, c := range map[string]struct {
		modifier func(*observatoire.SaisieReponse)
		err      error
	}{
		"sans texte":         {func(s *observatoire.SaisieReponse) { s.Texte = " " }, observatoire.ErrTexteReponse},
		"canal inconnu":      {func(s *observatoire.SaisieReponse) { s.Canal = "telephone" }, observatoire.ErrCanal},
		"sans référence":     {func(s *observatoire.SaisieReponse) { s.CanalDetail = "" }, observatoire.ErrCanalDetail},
		"reçu avant préavis": {func(s *observatoire.SaisieReponse) { s.RecuLe = jourDe("2026-12-01") }, observatoire.ErrDateReception},
		"reçu dans le futur": {func(s *observatoire.SaisieReponse) { s.RecuLe = jourDe("2026-12-07") }, observatoire.ErrDateReception},
		"sans date de reçu":  {func(s *observatoire.SaisieReponse) { s.RecuLe = time.Time{} }, observatoire.ErrDateReception},
	} {
		s := saisie
		c.modifier(&s)
		if _, err := svc.SaisirReponse(ctx, awa, st, s); !errors.Is(err, c.err) {
			t.Errorf("%s : %v", nom, err)
		}
	}
	r, err := svc.SaisirReponse(ctx, awa, st, saisie)
	if err != nil {
		t.Fatal(err)
	}
	// Quatre yeux : par l'API, puis en SQL direct.
	if _, err := svc.VerifierReponse(ctx, awa, r); !errors.Is(err, observatoire.ErrQuatreYeux) {
		t.Errorf("vérification par celui qui l'a saisi : %v", err)
	}
	if err := svc.RejeterReponse(ctx, awa, r, "non"); !errors.Is(err, observatoire.ErrQuatreYeux) {
		t.Errorf("rejet par celui qui l'a saisi : %v", err)
	}
	_, err = b.Pool.Exec(ctx, `INSERT INTO droit_reponse_verification VALUES ($1, $2, $2, '2026-12-06')`, r, awa)
	if c := codeSQL(err); c != "23514 droit_reponse_quatre_yeux" {
		t.Errorf("vérification en SQL direct : %v (%s)", err, c)
	}
	_, err = b.Pool.Exec(ctx, `INSERT INTO droit_reponse_verification VALUES ($1, $2, $3, '2026-12-06')`, r, moussa, awa)
	if c := codeSQL(err); !strings.HasPrefix(c, "23503") {
		t.Errorf("auteur falsifié : %v (%s)", err, c)
	}
	_, err = b.Pool.Exec(ctx, `INSERT INTO droit_reponse VALUES (9, $1, 'x', 'courrier_signe', 'x', '2026-12-01', $2, '2026-12-06')`, st, awa)
	if c := codeSQL(err); c != "23514 droit_reponse_apres_preavis" {
		t.Errorf("réponse antérieure au préavis en SQL : %v (%s)", err, c)
	}

	// Échu, mais un droit de réponse attend sa vérification : pas de
	// publication, ni par l'API ni en SQL direct.
	h.Jour("2026-12-18")
	if _, err := svc.Publier(ctx, moussa, st); !errors.Is(err, observatoire.ErrReponseAttente) {
		t.Errorf("réponse en attente, par l'API : %v", err)
	}
	n := entreeDuJour(t, b, journal.StatutPublie, "2026-12-18")
	_, err = b.Pool.Exec(ctx, `INSERT INTO statut_publication VALUES ($1, 'non_tenu', $2, '2026-12-18', $3)`, st, moussa, n)
	if c := codeSQL(err); c != "23514 droit_reponse_en_attente" {
		t.Errorf("réponse en attente, en SQL direct : %v (%s)", err, c)
	}

	numero, err := svc.VerifierReponse(ctx, moussa, r)
	if err != nil || numero != 0 {
		t.Fatalf("vérification avant publication du statut : %d %v", numero, err)
	}
	if f, _ := svc.FichePublique(ctx, 1); f != nil {
		t.Error("fiche publique avant la publication du statut")
	}
	ns, err := svc.Publier(ctx, awa, st)
	if err != nil {
		t.Fatal(err)
	}
	es, err := journal.Nouveau(b, nil).Lire(ctx, ns, ns+1)
	if err != nil || len(es) != 2 || es[0].Type != journal.StatutPublie || es[1].Type != journal.DroitDeReponse {
		t.Fatalf("journal après la publication : %+v %v", es, err)
	}
	c := string(es[1].Contenu)
	empreinte, _ := observatoire.EmpreinteReponse(&observatoire.DroitReponse{Texte: texteReponse, Canal: observatoire.AdresseOfficielle,
		RecuLe: jourDe("2026-12-05")})
	for _, attendu := range []string{`"droit_reponse":1`, `"canal":"adresse_officielle"`, `"recu_le":"2026-12-05"`,
		`"saisi_par":"Personne awa"`, `"verifie_par":"Personne moussa"`, `"texte_sha256":"` + empreinte + `"`} {
		if !strings.Contains(c, attendu) {
			t.Errorf("entrée du droit de réponse : %s absent de %s", attendu, c)
		}
	}
	if strings.Contains(c, "écoles") || strings.Contains(c, "cabinet@") {
		t.Errorf("le journal contient le texte ou la référence du canal : %s", c)
	}
	f, err := svc.FichePublique(ctx, 1)
	if err != nil || len(f.Reponses) != 1 {
		t.Fatalf("fiche : %+v %v", f, err)
	}
	pr := f.Reponses[0]
	if pr.Texte != texteReponse || pr.Canal != observatoire.AdresseOfficielle || !pr.RecuLe.Equal(jourDe("2026-12-05")) ||
		pr.CanalDetail != "" || pr.Publication == nil || pr.Publication.JournalNumero != ns+1 {
		t.Errorf("droit de réponse publié : %+v", pr)
	}
}

// Droit de réponse reçu après la publication du statut : publié dès qu'il
// est vérifié. Un droit de réponse rejeté n'est jamais publié. T8 sur les
// tables du préavis.
func TestDroitDeReponseApresPublication(t *testing.T) {
	ctx := context.Background()
	b, svc, st, h := statutPreavise(t)
	h.Jour("2026-12-17")
	if _, err := svc.Publier(ctx, moussa, st); err != nil {
		t.Fatal(err)
	}
	h.Jour("2026-12-20")
	r1, err := svc.SaisirReponse(ctx, moussa, st, observatoire.SaisieReponse{Texte: "Réponse non vérifiable.",
		Canal: observatoire.CompteOfficiel, CanalDetail: "compte inconnu", RecuLe: jourDe("2026-12-19")})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RejeterReponse(ctx, awa, r1, "Le compte n'est pas celui de l'élu."); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifierReponse(ctx, awa, r1); !errors.Is(err, observatoire.ErrEtape) {
		t.Errorf("vérification après rejet : %v", err)
	}
	if _, err := b.Pool.Exec(ctx, `INSERT INTO droit_reponse_verification VALUES ($1, $2, $3, '2026-12-20')`, r1, moussa, awa); err == nil {
		t.Error("vérification après rejet acceptée en SQL")
	}
	r2, err := svc.SaisirReponse(ctx, moussa, st, observatoire.SaisieReponse{Texte: "Réponse signée.",
		Canal: observatoire.CourrierSigne, CanalDetail: "lettre signée du maire, reçue au siège", RecuLe: jourDe("2026-12-20")})
	if err != nil {
		t.Fatal(err)
	}
	numero, err := svc.VerifierReponse(ctx, awa, r2)
	if err != nil || numero == 0 {
		t.Fatalf("vérification après publication : %d %v", numero, err)
	}
	f, err := svc.FichePublique(ctx, 1)
	if err != nil || len(f.Reponses) != 1 || f.Reponses[0].Texte != "Réponse signée." || !f.Reponses[0].Publication.Le.Equal(jourDe("2026-12-20")) {
		t.Fatalf("fiche : %+v %v", f, err)
	}
	// Une publication de droit de réponse en SQL demande une entrée du journal
	// du bon type.
	if _, err := b.Pool.Exec(ctx, `INSERT INTO droit_reponse_publication VALUES ($1, $2, '2026-12-20', 1)`, r1, awa); err == nil {
		t.Error("publication d'un droit de réponse rejeté acceptée")
	}

	// T8 : ajout seul sur les tables du préavis et du droit de réponse.
	for table, set := range map[string]string{"statut_preavis": "echeance = envoye_le", "droit_reponse": "texte = 'x'",
		"droit_reponse_verification": "verifie_le = verifie_le", "droit_reponse_rejet": "motif = 'x'",
		"droit_reponse_publication": "publie_le = publie_le"} {
		for _, sql := range []string{"UPDATE " + table + " SET " + set, "DELETE FROM " + table} {
			if _, err := b.Pool.Exec(ctx, sql); codeSQL(err) != "42501 " {
				t.Errorf("%s : %v", sql, err)
			}
		}
		if _, err := b.Pool.Exec(ctx, "TRUNCATE "+table+" CASCADE"); err == nil {
			t.Errorf("TRUNCATE %s accepté", table)
		}
	}
}

// Le dossier de l'élu : un PDF pour un « non tenu » validé seulement ; une
// fois le préavis enregistré, la copie conservée, identique à son SHA-256.
func TestDossierDuPreavis(t *testing.T) {
	ctx := context.Background()
	_, svc, st, _ := obsDeTestHorloge(t, observatoire.NonTenu)
	if _, err := svc.Dossier(ctx, st); !errors.Is(err, observatoire.ErrEtape) {
		t.Errorf("dossier d'un statut proposé : %v", err)
	}
	if err := svc.Valider(ctx, moussa, st); err != nil {
		t.Fatal(err)
	}
	avant, err := svc.Dossier(ctx, st)
	if err != nil || !strings.HasPrefix(string(avant), "%PDF-1.4") {
		t.Fatalf("dossier : %v", err)
	}
	if err := svc.EnregistrerPreavis(ctx, awa, st, jourDe("2026-12-02"), "Courriel"); err != nil {
		t.Fatal(err)
	}
	apres, err := svc.Dossier(ctx, st)
	_, s, _ := svc.Statut(ctx, st)
	if err != nil || string(apres) != string(avant) || s.PreavisEnvoye.Dossier.String() != sha256Hex(apres) {
		t.Errorf("dossier conservé : %v", err)
	}
	// Un « tenu » n'a pas de dossier.
	_, svc2, st2 := obsDeTest(t, observatoire.Tenu)
	if err := svc2.Valider(ctx, moussa, st2); err != nil {
		t.Fatal(err)
	}
	if _, err := svc2.Dossier(ctx, st2); !errors.Is(err, observatoire.ErrEtape) {
		t.Errorf("dossier d'un tenu : %v", err)
	}
	if err := svc2.EnregistrerPreavis(ctx, awa, st2, jourDe("2026-12-02"), "x"); !errors.Is(err, observatoire.ErrEtape) {
		t.Errorf("préavis d'un tenu : %v", err)
	}
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
