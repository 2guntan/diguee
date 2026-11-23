package base

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/observatoire"
	"github.com/2guntan/diguee/internal/stockage"
)

// verrouObservatoire sérialise les écritures de l'observatoire : la
// numérotation (max + 1, sans séquence) et les règles du circuit en
// dépendent. Il est pris avant celui du journal public, jamais après.
const verrouObservatoire = 0x6f6273657276 // « observ »

// requeteObs lit dans le pool ou dans une transaction.
type requeteObs interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func jourObs(t time.Time) pgtype.Date {
	t = t.UTC()
	return pgtype.Date{Time: time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), Valid: true}
}

func depuisDate(d pgtype.Date) time.Time { return d.Time.UTC() }

// erreurObs traduit les refus de la base en erreurs de l'observatoire.
func erreurObs(err error) error {
	var pe *pgconn.PgError
	if !errors.As(err, &pe) {
		return err
	}
	switch {
	case strings.HasSuffix(pe.ConstraintName, "quatre_yeux"):
		return observatoire.ErrQuatreYeux
	case pe.ConstraintName == "statut_preavis_echu", pe.ConstraintName == "statut_preavis_quinze_jours":
		return observatoire.ErrPreavis
	case pe.ConstraintName == "droit_reponse_en_attente":
		return observatoire.ErrReponseAttente
	case pe.Code == "23505", pe.Code == "23503", pe.Code == "23514":
		return fmt.Errorf("%w : %s", observatoire.ErrEtape, pe.Message)
	}
	return err
}

// ecrireObs exécute f dans une transaction, sous le verrou de l'observatoire.
func (b *Base) ecrireObs(ctx context.Context, f func(pgx.Tx) error) error {
	err := pgx.BeginFunc(ctx, b.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", verrouObservatoire); err != nil {
			return err
		}
		return f(tx)
	})
	return erreurObs(err)
}

func existe(ctx context.Context, tx pgx.Tx, requete string, args ...any) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, "SELECT EXISTS ("+requete+")", args...).Scan(&ok)
	return ok, err
}

func sourcesExistent(ctx context.Context, tx pgx.Tx, ids []int64) error {
	for _, id := range ids {
		ok, err := existe(ctx, tx, "SELECT 1 FROM source WHERE id = $1", id)
		if err != nil {
			return err
		}
		if !ok {
			return observatoire.ErrSourceInconnue
		}
	}
	return nil
}

// AjouterElu implémente observatoire.Magasin.
func (b *Base) AjouterElu(ctx context.Context, e observatoire.Elu) (int64, error) {
	var id int64
	err := b.ecrireObs(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO elu (id, nom, fonction, mandat_debut, mandat_fin, contact_officiel, saisi_par, saisi_le)
			SELECT coalesce(max(id), 0) + 1, $1, $2, $3, $4, $5, $6::uuid, $7 FROM elu RETURNING id`,
			e.Nom, e.Fonction, jourObs(e.Mandat.Debut), jourObs(e.Mandat.Fin), e.ContactOfficiel, e.Saisi.Par, jourObs(e.Saisi.Le)).Scan(&id)
	})
	return id, err
}

const colonnesElu = `l.id, l.nom, l.fonction, l.mandat_debut, l.mandat_fin, l.contact_officiel, l.saisi_par::text, ml.nom, l.saisi_le`

func champsElu(e *observatoire.Elu, debut, fin, saisi *pgtype.Date) []any {
	return []any{&e.ID, &e.Nom, &e.Fonction, debut, fin, &e.ContactOfficiel, &e.Saisi.Par, &e.Saisi.ParNom, saisi}
}

// Elus implémente observatoire.Magasin.
func (b *Base) Elus(ctx context.Context) ([]observatoire.Elu, error) {
	return lireElus(ctx, b.Pool, `SELECT `+colonnesElu+` FROM elu l JOIN membre ml ON ml.id = l.saisi_par ORDER BY l.nom, l.id`)
}

func lireElus(ctx context.Context, q requeteObs, requete string, args ...any) ([]observatoire.Elu, error) {
	lignes, err := q.Query(ctx, requete, args...)
	if err != nil {
		return nil, err
	}
	defer lignes.Close()
	var es []observatoire.Elu
	for lignes.Next() {
		var (
			e                 observatoire.Elu
			debut, fin, saisi pgtype.Date
		)
		if err := lignes.Scan(champsElu(&e, &debut, &fin, &saisi)...); err != nil {
			return nil, err
		}
		e.Mandat = observatoire.Mandat{Debut: depuisDate(debut), Fin: depuisDate(fin)}
		e.Saisi.Le = depuisDate(saisi)
		es = append(es, e)
	}
	return es, lignes.Err()
}

// AjouterSource implémente observatoire.Magasin.
func (b *Base) AjouterSource(ctx context.Context, s observatoire.Source) (int64, error) {
	var id int64
	err := b.ecrireObs(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO source (id, url, titre, date_source, archive_sha256, archive_type, archive_taille, ajoute_par, ajoute_le)
			SELECT coalesce(max(id), 0) + 1, $1, $2, $3, $4, $5, $6, $7::uuid, $8 FROM source RETURNING id`,
			s.URL, s.Titre, jourObs(s.Date), s.Archive[:], s.TypeArchive, s.Taille, s.Ajoutee.Par, jourObs(s.Ajoutee.Le)).Scan(&id)
	})
	return id, err
}

const colonnesSource = `s.id, s.url, s.titre, s.date_source, s.archive_sha256, s.archive_type, s.archive_taille, s.ajoute_par::text, ms.nom, s.ajoute_le`

func lireSource(r pgx.Rows, extra ...any) (observatoire.Source, error) {
	var (
		s           observatoire.Source
		date, ajout pgtype.Date
		sha         []byte
	)
	err := r.Scan(append(extra, &s.ID, &s.URL, &s.Titre, &date, &sha, &s.TypeArchive, &s.Taille, &s.Ajoutee.Par, &s.Ajoutee.ParNom, &ajout)...)
	if err != nil {
		return s, err
	}
	if len(sha) != len(s.Archive) {
		return s, fmt.Errorf("source %d : empreinte mal formée", s.ID)
	}
	copy(s.Archive[:], sha)
	s.Date, s.Ajoutee.Le = depuisDate(date), depuisDate(ajout)
	return s, nil
}

// Sources implémente observatoire.Magasin.
func (b *Base) Sources(ctx context.Context) ([]observatoire.Source, error) {
	lignes, err := b.Pool.Query(ctx, `SELECT `+colonnesSource+` FROM source s JOIN membre ms ON ms.id = s.ajoute_par ORDER BY s.id DESC`)
	if err != nil {
		return nil, err
	}
	defer lignes.Close()
	var ss []observatoire.Source
	for lignes.Next() {
		s, err := lireSource(lignes)
		if err != nil {
			return nil, err
		}
		ss = append(ss, s)
	}
	return ss, lignes.Err()
}

// AjouterEngagement implémente observatoire.Magasin.
func (b *Base) AjouterEngagement(ctx context.Context, e observatoire.Engagement, sources []int64) (int64, error) {
	var id int64
	err := b.ecrireObs(ctx, func(tx pgx.Tx) error {
		ok, err := existe(ctx, tx, "SELECT 1 FROM elu WHERE id = $1", e.Elu.ID)
		if err != nil {
			return err
		}
		if !ok {
			return observatoire.ErrEluInconnu
		}
		if err := sourcesExistent(ctx, tx, sources); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO engagement (id, elu_id, citation, contexte, date_declaration, echeance, cree_par, cree_le)
			SELECT coalesce(max(id), 0) + 1, $1, $2, $3, $4, $5, $6::uuid, $7 FROM engagement RETURNING id`,
			e.Elu.ID, e.Citation, e.Contexte, jourObs(e.DateDeclaration), jourObs(e.Echeance), e.Cree.Par, jourObs(e.Cree.Le)).Scan(&id); err != nil {
			return err
		}
		for _, s := range sources {
			if _, err := tx.Exec(ctx, "INSERT INTO engagement_source (engagement_id, source_id) VALUES ($1, $2)", id, s); err != nil {
				return err
			}
		}
		return nil
	})
	return id, err
}

// LierSource implémente observatoire.Magasin ; un lien qui existe déjà est
// sans effet.
func (b *Base) LierSource(ctx context.Context, engagement, source int64) error {
	return b.ecrireObs(ctx, func(tx pgx.Tx) error {
		if err := sourcesExistent(ctx, tx, []int64{source}); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO engagement_source (engagement_id, source_id) VALUES ($1, $2)
			ON CONFLICT DO NOTHING`, engagement, source)
		return err
	})
}

// executerUne exécute une insertion qui doit toucher une ligne, sinon
// renvoie siAucune.
func executerUne(ctx context.Context, tx pgx.Tx, siAucune error, requete string, args ...any) error {
	tag, err := tx.Exec(ctx, requete, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return siAucune
	}
	return nil
}

// ValiderEngagement implémente observatoire.Magasin. Le rédacteur est
// recopié de l'engagement : la contrainte engagement_quatre_yeux compare.
func (b *Base) ValiderEngagement(ctx context.Context, engagement int64, a observatoire.Acte) error {
	return b.ecrireObs(ctx, func(tx pgx.Tx) error {
		return executerUne(ctx, tx, observatoire.ErrIntrouvable, `INSERT INTO engagement_validation (engagement_id, cree_par, valide_par, valide_le)
			SELECT id, cree_par, $2::uuid, $3 FROM engagement WHERE id = $1`, engagement, a.Par, jourObs(a.Le))
	})
}

// RedigerStatut implémente observatoire.Magasin.
func (b *Base) RedigerStatut(ctx context.Context, s observatoire.Statut, preuves []int64) (int64, error) {
	var id int64
	err := b.ecrireObs(ctx, func(tx pgx.Tx) error {
		ok, err := existe(ctx, tx, "SELECT 1 FROM engagement_validation WHERE engagement_id = $1", s.EngagementID)
		if err != nil {
			return err
		}
		if !ok {
			return observatoire.ErrNonValide
		}
		if err := sourcesExistent(ctx, tx, preuves); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO statut (id, engagement_id, valeur, explication, redige_par, redige_le)
			SELECT coalesce(max(id), 0) + 1, $1, $2, $3, $4::uuid, $5 FROM statut RETURNING id`,
			s.EngagementID, string(s.Valeur), s.Explication, s.Redige.Par, jourObs(s.Redige.Le)).Scan(&id); err != nil {
			return err
		}
		for _, p := range preuves {
			if _, err := tx.Exec(ctx, "INSERT INTO statut_preuve (statut_id, source_id) VALUES ($1, $2)", id, p); err != nil {
				return err
			}
		}
		return nil
	})
	return id, err
}

// AjouterPreuve implémente observatoire.Magasin ; une preuve déjà liée est
// sans effet.
func (b *Base) AjouterPreuve(ctx context.Context, statut, source int64) error {
	return b.ecrireObs(ctx, func(tx pgx.Tx) error {
		if err := sourcesExistent(ctx, tx, []int64{source}); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO statut_preuve (statut_id, source_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, statut, source)
		return err
	})
}

// Proposer implémente observatoire.Magasin : seul le rédacteur propose.
func (b *Base) Proposer(ctx context.Context, statut int64, a observatoire.Acte) error {
	return b.ecrireObs(ctx, func(tx pgx.Tx) error {
		return executerUne(ctx, tx, observatoire.ErrPasLeRedacteur, `INSERT INTO statut_proposition (statut_id, redige_par, propose_le)
			SELECT id, redige_par, $3 FROM statut WHERE id = $1 AND redige_par = $2::uuid`, statut, a.Par, jourObs(a.Le))
	})
}

// Valider implémente observatoire.Magasin. Le rédacteur est recopié du
// statut : la contrainte statut_quatre_yeux compare (T7).
func (b *Base) Valider(ctx context.Context, statut int64, a observatoire.Acte) error {
	return b.ecrireObs(ctx, func(tx pgx.Tx) error {
		return executerUne(ctx, tx, observatoire.ErrIntrouvable, `INSERT INTO statut_validation (statut_id, redige_par, valide_par, valide_le)
			SELECT id, redige_par, $2::uuid, $3 FROM statut WHERE id = $1`, statut, a.Par, jourObs(a.Le))
	})
}

// Rejeter implémente observatoire.Magasin.
func (b *Base) Rejeter(ctx context.Context, statut int64, a observatoire.Acte, motif string) error {
	return b.ecrireObs(ctx, func(tx pgx.Tx) error {
		return executerUne(ctx, tx, observatoire.ErrIntrouvable, `INSERT INTO statut_rejet (statut_id, redige_par, rejete_par, rejete_le, motif)
			SELECT id, redige_par, $2::uuid, $3, $4 FROM statut WHERE id = $1`, statut, a.Par, jourObs(a.Le), motif)
	})
}

// Publier implémente observatoire.Magasin : sous le verrou, l'engagement est
// relu, l'entrée du journal public inscrite, puis la publication ; enfin
// chaque droit de réponse vérifié au statut est publié avec son entrée, dans
// la même transaction (tout ou rien).
func (b *Base) Publier(ctx context.Context, statut int64, a observatoire.Acte, j observatoire.Journaliser, jr observatoire.JournaliserReponse) (int64, error) {
	var numero int64
	err := b.ecrireObs(ctx, func(tx pgx.Tx) error {
		e, st, err := statutEnTransaction(ctx, tx, statut)
		if err != nil {
			return err
		}
		if numero, err = j(ctx, journalTx{tx}, e, st); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO statut_publication (statut_id, valeur, publie_par, publie_le, journal_numero)
			VALUES ($1, $2, $3::uuid, $4, $5)`, statut, string(st.Valeur), a.Par, jourObs(a.Le), numero); err != nil {
			return err
		}
		if e, st, err = statutEnTransaction(ctx, tx, statut); err != nil {
			return err
		}
		for i := range e.Reponses {
			if r := &e.Reponses[i]; r.StatutID == statut && r.Etape() == observatoire.ReponseVerifiee {
				if err := publierReponse(ctx, tx, e, st, r, a, jr); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return numero, nil
}

// statutEnTransaction relit, dans la transaction, un statut et son
// engagement complet.
func statutEnTransaction(ctx context.Context, tx pgx.Tx, statut int64) (*observatoire.Engagement, *observatoire.Statut, error) {
	eid, err := engagementDuStatut(ctx, tx, statut)
	if err != nil {
		return nil, nil, err
	}
	e, err := lireEngagement(ctx, tx, eid, false)
	if err != nil {
		return nil, nil, err
	}
	for i := range e.Statuts {
		if e.Statuts[i].ID == statut {
			return e, &e.Statuts[i], nil
		}
	}
	return nil, nil, observatoire.ErrIntrouvable
}

// publierReponse inscrit l'entrée du journal d'un droit de réponse vérifié,
// puis sa publication.
func publierReponse(ctx context.Context, tx pgx.Tx, e *observatoire.Engagement, st *observatoire.Statut, r *observatoire.DroitReponse,
	a observatoire.Acte, jr observatoire.JournaliserReponse) error {
	n, err := jr(ctx, journalTx{tx}, e, st, r)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO droit_reponse_publication (droit_reponse_id, publie_par, publie_le, journal_numero)
		VALUES ($1, $2::uuid, $3, $4)`, r.ID, a.Par, jourObs(a.Le), n)
	return err
}

// EnregistrerPreavis implémente observatoire.Magasin. La valeur est
// recopiée du statut : la base n'accepte qu'un « non tenu » ou un « partiel ».
func (b *Base) EnregistrerPreavis(ctx context.Context, statut int64, p observatoire.PreavisEnvoye) error {
	return b.ecrireObs(ctx, func(tx pgx.Tx) error {
		return executerUne(ctx, tx, observatoire.ErrIntrouvable, `INSERT INTO statut_preavis (statut_id, valeur, envoye_le, echeance, moyen,
			dossier_sha256, enregistre_par, enregistre_le)
			SELECT id, valeur, $2, $3, $4, $5, $6::uuid, $7 FROM statut WHERE id = $1`,
			statut, jourObs(p.EnvoyeLe), jourObs(p.Echeance), p.Moyen, p.Dossier[:], p.Par, jourObs(p.Le))
	})
}

// SaisirReponse implémente observatoire.Magasin.
func (b *Base) SaisirReponse(ctx context.Context, r observatoire.DroitReponse) (int64, error) {
	var id int64
	err := b.ecrireObs(ctx, func(tx pgx.Tx) error {
		ok, err := existe(ctx, tx, "SELECT 1 FROM statut_preavis WHERE statut_id = $1", r.StatutID)
		if err != nil {
			return err
		}
		if !ok {
			return observatoire.ErrSansPreavis
		}
		return tx.QueryRow(ctx, `INSERT INTO droit_reponse (id, statut_id, texte, canal, canal_detail, recu_le, saisi_par, saisi_le)
			SELECT coalesce(max(id), 0) + 1, $1, $2, $3, $4, $5, $6::uuid, $7 FROM droit_reponse RETURNING id`,
			r.StatutID, r.Texte, string(r.Canal), r.CanalDetail, jourObs(r.RecuLe), r.Saisi.Par, jourObs(r.Saisi.Le)).Scan(&id)
	})
	return id, err
}

// EngagementDeLaReponse implémente observatoire.Magasin.
func (b *Base) EngagementDeLaReponse(ctx context.Context, reponse int64) (int64, error) {
	var id int64
	err := b.Pool.QueryRow(ctx, `SELECT st.engagement_id FROM droit_reponse d JOIN statut st ON st.id = d.statut_id
		WHERE d.id = $1`, reponse).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, observatoire.ErrIntrouvable
	}
	return id, err
}

// VerifierReponse implémente observatoire.Magasin. Le membre qui l'a saisi
// est recopié : la contrainte droit_reponse_quatre_yeux compare.
func (b *Base) VerifierReponse(ctx context.Context, reponse int64, a observatoire.Acte, jr observatoire.JournaliserReponse) (int64, error) {
	var numero int64
	err := b.ecrireObs(ctx, func(tx pgx.Tx) error {
		var statut int64
		if err := tx.QueryRow(ctx, `INSERT INTO droit_reponse_verification (droit_reponse_id, saisi_par, verifie_par, verifie_le)
			SELECT id, saisi_par, $2::uuid, $3 FROM droit_reponse WHERE id = $1
			RETURNING (SELECT statut_id FROM droit_reponse WHERE id = $1)`, reponse, a.Par, jourObs(a.Le)).Scan(&statut); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return observatoire.ErrIntrouvable
			}
			return err
		}
		e, st, err := statutEnTransaction(ctx, tx, statut)
		if err != nil || st.Publication == nil {
			return err // statut pas encore publié : la réponse le sera avec lui
		}
		for i := range e.Reponses {
			if r := &e.Reponses[i]; r.ID == reponse {
				return publierReponse(ctx, tx, e, st, r, a, func(ctx context.Context, m journal.Magasin, e *observatoire.Engagement,
					s *observatoire.Statut, r *observatoire.DroitReponse) (int64, error) {
					n, err := jr(ctx, m, e, s, r)
					numero = n
					return n, err
				})
			}
		}
		return observatoire.ErrIntrouvable
	})
	if err != nil {
		return 0, err
	}
	return numero, nil
}

// RejeterReponse implémente observatoire.Magasin.
func (b *Base) RejeterReponse(ctx context.Context, reponse int64, a observatoire.Acte, motif string) error {
	return b.ecrireObs(ctx, func(tx pgx.Tx) error {
		return executerUne(ctx, tx, observatoire.ErrIntrouvable, `INSERT INTO droit_reponse_rejet (droit_reponse_id, saisi_par, rejete_par, rejete_le, motif)
			SELECT id, saisi_par, $2::uuid, $3, $4 FROM droit_reponse WHERE id = $1`, reponse, a.Par, jourObs(a.Le), motif)
	})
}

func engagementDuStatut(ctx context.Context, q requeteObs, statut int64) (int64, error) {
	var id int64
	err := q.QueryRow(ctx, "SELECT engagement_id FROM statut WHERE id = $1", statut).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, observatoire.ErrIntrouvable
	}
	return id, err
}

// EngagementDuStatut implémente observatoire.Magasin.
func (b *Base) EngagementDuStatut(ctx context.Context, statut int64) (int64, error) {
	return engagementDuStatut(ctx, b.Pool, statut)
}

// Engagement implémente observatoire.Magasin.
func (b *Base) Engagement(ctx context.Context, id int64) (*observatoire.Engagement, error) {
	return lireEngagement(ctx, b.Pool, id, false)
}

// Engagements implémente observatoire.Magasin.
func (b *Base) Engagements(ctx context.Context) ([]observatoire.Engagement, error) {
	return b.engagementsParIDs(ctx, "SELECT id FROM engagement ORDER BY id DESC", false)
}

func (b *Base) engagementsParIDs(ctx context.Context, requete string, public bool, args ...any) ([]observatoire.Engagement, error) {
	lignes, err := b.Pool.Query(ctx, requete, args...)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(lignes, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	es := make([]observatoire.Engagement, 0, len(ids))
	for _, id := range ids {
		e, err := lireEngagement(ctx, b.Pool, id, public)
		if err != nil {
			return nil, err
		}
		es = append(es, *e)
	}
	return es, nil
}

// lireEngagement lit un engagement complet. public : seulement les statuts
// publiés, dans l'ordre de publication, et sans le contact de l'élu.
func lireEngagement(ctx context.Context, q requeteObs, id int64, public bool) (*observatoire.Engagement, error) {
	var (
		e                                      observatoire.Engagement
		decl, ech, cree, debut, fin, saisi, vl pgtype.Date
		vpar, vnom                             pgtype.Text
	)
	champs := []any{&e.ID, &e.Citation, &e.Contexte, &decl, &ech, &e.Cree.Par, &e.Cree.ParNom, &cree}
	champs = append(champs, champsElu(&e.Elu, &debut, &fin, &saisi)...)
	champs = append(champs, &vpar, &vnom, &vl)
	err := q.QueryRow(ctx, `SELECT e.id, e.citation, e.contexte, e.date_declaration, e.echeance, e.cree_par::text, mc.nom, e.cree_le,
		`+colonnesElu+`, v.valide_par::text, mv.nom, v.valide_le
		FROM engagement e
		JOIN membre mc ON mc.id = e.cree_par
		JOIN elu l ON l.id = e.elu_id
		JOIN membre ml ON ml.id = l.saisi_par
		LEFT JOIN engagement_validation v ON v.engagement_id = e.id
		LEFT JOIN membre mv ON mv.id = v.valide_par
		WHERE e.id = $1`, id).Scan(champs...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, observatoire.ErrIntrouvable
	}
	if err != nil {
		return nil, err
	}
	e.DateDeclaration, e.Echeance, e.Cree.Le = depuisDate(decl), depuisDate(ech), depuisDate(cree)
	e.Elu.Mandat = observatoire.Mandat{Debut: depuisDate(debut), Fin: depuisDate(fin)}
	e.Elu.Saisi.Le = depuisDate(saisi)
	if vpar.Valid {
		e.Validation = &observatoire.Acte{Par: vpar.String, ParNom: vnom.String, Le: depuisDate(vl)}
	}
	if public {
		e.Elu.ContactOfficiel = ""
	}

	lignes, err := q.Query(ctx, `SELECT `+colonnesSource+` FROM engagement_source es
		JOIN source s ON s.id = es.source_id JOIN membre ms ON ms.id = s.ajoute_par
		WHERE es.engagement_id = $1 ORDER BY s.id`, id)
	if err != nil {
		return nil, err
	}
	for lignes.Next() {
		s, err := lireSource(lignes)
		if err != nil {
			lignes.Close()
			return nil, err
		}
		e.Sources = append(e.Sources, s)
	}
	lignes.Close()
	if err := lignes.Err(); err != nil {
		return nil, err
	}

	filtre, ordre := "", "st.id"
	if public {
		filtre, ordre = " AND pu.statut_id IS NOT NULL", "pu.journal_numero"
	}
	lignes, err = q.Query(ctx, `SELECT st.id, st.valeur, st.explication, st.redige_par::text, mr.nom, st.redige_le,
		p.propose_le,
		v.valide_par::text, mv.nom, v.valide_le,
		r.rejete_par::text, mj.nom, r.rejete_le, r.motif,
		pu.publie_par::text, mp.nom, pu.publie_le, pu.journal_numero,
		pv.enregistre_par::text, mpv.nom, pv.enregistre_le, pv.envoye_le, pv.echeance, pv.moyen, pv.dossier_sha256
		FROM statut st
		JOIN membre mr ON mr.id = st.redige_par
		LEFT JOIN statut_proposition p ON p.statut_id = st.id
		LEFT JOIN statut_validation v ON v.statut_id = st.id
		LEFT JOIN membre mv ON mv.id = v.valide_par
		LEFT JOIN statut_rejet r ON r.statut_id = st.id
		LEFT JOIN membre mj ON mj.id = r.rejete_par
		LEFT JOIN statut_publication pu ON pu.statut_id = st.id
		LEFT JOIN membre mp ON mp.id = pu.publie_par
		LEFT JOIN statut_preavis pv ON pv.statut_id = st.id
		LEFT JOIN membre mpv ON mpv.id = pv.enregistre_par
		WHERE st.engagement_id = $1`+filtre+` ORDER BY `+ordre, id)
	if err != nil {
		return nil, err
	}
	indice := map[int64]int{}
	for lignes.Next() {
		var (
			st                     observatoire.Statut
			valeur                 string
			redige, pl, vl, rl, ul pgtype.Date
			vpar, vnom, rpar, rnom pgtype.Text
			motif, upar, unom      pgtype.Text
			numero                 pgtype.Int8
			ppar, pnom, pmoyen     pgtype.Text
			pl2, penv, pech        pgtype.Date
			pdossier               []byte
		)
		if err := lignes.Scan(&st.ID, &valeur, &st.Explication, &st.Redige.Par, &st.Redige.ParNom, &redige, &pl,
			&vpar, &vnom, &vl, &rpar, &rnom, &rl, &motif, &upar, &unom, &ul, &numero,
			&ppar, &pnom, &pl2, &penv, &pech, &pmoyen, &pdossier); err != nil {
			lignes.Close()
			return nil, err
		}
		st.EngagementID, st.Valeur, st.Redige.Le = id, observatoire.Valeur(valeur), depuisDate(redige)
		if pl.Valid {
			st.Proposition = &observatoire.Acte{Par: st.Redige.Par, ParNom: st.Redige.ParNom, Le: depuisDate(pl)}
		}
		if vpar.Valid {
			st.Validation = &observatoire.Acte{Par: vpar.String, ParNom: vnom.String, Le: depuisDate(vl)}
		}
		if rpar.Valid {
			st.Rejet = &observatoire.Rejet{Acte: observatoire.Acte{Par: rpar.String, ParNom: rnom.String, Le: depuisDate(rl)}, Motif: motif.String}
		}
		if upar.Valid {
			st.Publication = &observatoire.Publication{Acte: observatoire.Acte{Par: upar.String, ParNom: unom.String, Le: depuisDate(ul)},
				JournalNumero: numero.Int64}
		}
		if ppar.Valid {
			p := &observatoire.PreavisEnvoye{Acte: observatoire.Acte{Par: ppar.String, ParNom: pnom.String, Le: depuisDate(pl2)},
				EnvoyeLe: depuisDate(penv), Echeance: depuisDate(pech), Moyen: pmoyen.String}
			copy(p.Dossier[:], pdossier)
			if public {
				p.Moyen, p.Par, p.ParNom = "", "", ""
			}
			st.PreavisEnvoye = p
		}
		indice[st.ID] = len(e.Statuts)
		e.Statuts = append(e.Statuts, st)
	}
	lignes.Close()
	if err := lignes.Err(); err != nil {
		return nil, err
	}

	lignes, err = q.Query(ctx, `SELECT sp.statut_id, `+colonnesSource+` FROM statut_preuve sp
		JOIN statut st ON st.id = sp.statut_id
		JOIN source s ON s.id = sp.source_id JOIN membre ms ON ms.id = s.ajoute_par
		WHERE st.engagement_id = $1 ORDER BY s.id`, id)
	if err != nil {
		return nil, err
	}
	for lignes.Next() {
		var sid int64
		s, err := lireSource(lignes, &sid)
		if err != nil {
			lignes.Close()
			return nil, err
		}
		if i, ok := indice[sid]; ok {
			e.Statuts[i].Preuves = append(e.Statuts[i].Preuves, s)
		}
	}
	lignes.Close()
	if err := lignes.Err(); err != nil {
		return nil, err
	}
	if e.Reponses, err = lireReponses(ctx, q, id, public); err != nil {
		return nil, err
	}
	return &e, nil
}

// lireReponses lit les droits de réponse aux statuts d'un engagement.
// public : seulement les publiés, dans l'ordre de publication, sans la
// référence du canal.
func lireReponses(ctx context.Context, q requeteObs, engagement int64, public bool) ([]observatoire.DroitReponse, error) {
	filtre, ordre := "", "d.id"
	if public {
		filtre, ordre = " AND pu.droit_reponse_id IS NOT NULL", "pu.journal_numero"
	}
	lignes, err := q.Query(ctx, `SELECT d.id, d.statut_id, d.texte, d.canal, d.canal_detail, d.recu_le,
		d.saisi_par::text, ms.nom, d.saisi_le,
		v.verifie_par::text, mv.nom, v.verifie_le,
		r.rejete_par::text, mr.nom, r.rejete_le, r.motif,
		pu.publie_par::text, mp.nom, pu.publie_le, pu.journal_numero
		FROM droit_reponse d
		JOIN statut st ON st.id = d.statut_id
		JOIN membre ms ON ms.id = d.saisi_par
		LEFT JOIN droit_reponse_verification v ON v.droit_reponse_id = d.id
		LEFT JOIN membre mv ON mv.id = v.verifie_par
		LEFT JOIN droit_reponse_rejet r ON r.droit_reponse_id = d.id
		LEFT JOIN membre mr ON mr.id = r.rejete_par
		LEFT JOIN droit_reponse_publication pu ON pu.droit_reponse_id = d.id
		LEFT JOIN membre mp ON mp.id = pu.publie_par
		WHERE st.engagement_id = $1`+filtre+` ORDER BY `+ordre, engagement)
	if err != nil {
		return nil, err
	}
	defer lignes.Close()
	var rs []observatoire.DroitReponse
	for lignes.Next() {
		var (
			r                       observatoire.DroitReponse
			canal                   string
			recu, saisi, vl, rl, ul pgtype.Date
			vpar, vnom, rpar, rnom  pgtype.Text
			motif, upar, unom       pgtype.Text
			numero                  pgtype.Int8
		)
		if err := lignes.Scan(&r.ID, &r.StatutID, &r.Texte, &canal, &r.CanalDetail, &recu, &r.Saisi.Par, &r.Saisi.ParNom, &saisi,
			&vpar, &vnom, &vl, &rpar, &rnom, &rl, &motif, &upar, &unom, &ul, &numero); err != nil {
			return nil, err
		}
		r.Canal, r.RecuLe, r.Saisi.Le = observatoire.Canal(canal), depuisDate(recu), depuisDate(saisi)
		if vpar.Valid {
			r.Verification = &observatoire.Acte{Par: vpar.String, ParNom: vnom.String, Le: depuisDate(vl)}
		}
		if rpar.Valid {
			r.Rejet = &observatoire.Rejet{Acte: observatoire.Acte{Par: rpar.String, ParNom: rnom.String, Le: depuisDate(rl)}, Motif: motif.String}
		}
		if upar.Valid {
			r.Publication = &observatoire.Publication{Acte: observatoire.Acte{Par: upar.String, ParNom: unom.String, Le: depuisDate(ul)},
				JournalNumero: numero.Int64}
		}
		if public {
			r.CanalDetail = ""
		}
		rs = append(rs, r)
	}
	return rs, lignes.Err()
}

// publicsSQL : les engagements publics (validés, avec au moins un statut
// publié) et la valeur de leur dernier statut publié.
const publicsSQL = `SELECT e.id, e.elu_id, (SELECT st.valeur FROM statut st JOIN statut_publication pu ON pu.statut_id = st.id
		WHERE st.engagement_id = e.id ORDER BY pu.journal_numero DESC LIMIT 1) AS valeur
	FROM engagement e
	WHERE EXISTS (SELECT 1 FROM statut st JOIN statut_publication pu ON pu.statut_id = st.id WHERE st.engagement_id = e.id)`

// ListePublique implémente observatoire.Magasin.
func (b *Base) ListePublique(ctx context.Context, f observatoire.Filtre) (*observatoire.Liste, error) {
	var debut, fin pgtype.Date
	if f.Mandat != nil {
		debut, fin = jourObs(f.Mandat.Debut), jourObs(f.Mandat.Fin)
	}
	conditions := `FROM (` + publicsSQL + `) p JOIN elu l ON l.id = p.elu_id
		WHERE ($1::bigint = 0 OR l.id = $1) AND ($2::date IS NULL OR (l.mandat_debut = $2 AND l.mandat_fin = $3))
		AND ($4::text = '' OR p.valeur = $4)`
	args := []any{f.Elu, debut, fin, string(f.Valeur)}
	l := &observatoire.Liste{}
	if err := b.Pool.QueryRow(ctx, "SELECT count(*) "+conditions, args...).Scan(&l.Total); err != nil {
		return nil, err
	}
	es, err := b.engagementsParIDs(ctx, "SELECT p.id "+conditions+" ORDER BY p.id DESC LIMIT $5 OFFSET $6", true,
		append(args, observatoire.ParPage, (f.Page-1)*observatoire.ParPage)...)
	if err != nil {
		return nil, err
	}
	for i := range es {
		if d := es[i].Dernier(); d != nil {
			es[i].Statuts = []observatoire.Statut{*d}
		}
	}
	l.Engagements = es
	if l.Elus, err = lireElus(ctx, b.Pool, `SELECT `+colonnesElu+` FROM elu l JOIN membre ml ON ml.id = l.saisi_par
		WHERE l.id IN (SELECT elu_id FROM (`+publicsSQL+`) p) ORDER BY l.nom, l.id`); err != nil {
		return nil, err
	}
	vus := map[string]bool{}
	for i := range l.Elus {
		l.Elus[i].ContactOfficiel = ""
		if m := l.Elus[i].Mandat; !vus[m.Cle()] {
			vus[m.Cle()] = true
			l.Mandats = append(l.Mandats, m)
		}
	}
	return l, nil
}

// FichePublique implémente observatoire.Magasin.
func (b *Base) FichePublique(ctx context.Context, id int64) (*observatoire.Engagement, error) {
	e, err := lireEngagement(ctx, b.Pool, id, true)
	if err != nil {
		return nil, err
	}
	if e.Validation == nil || len(e.Statuts) == 0 {
		return nil, observatoire.ErrIntrouvable
	}
	return e, nil
}

// TypeArchive implémente observatoire.Magasin.
func (b *Base) TypeArchive(ctx context.Context, e stockage.Empreinte, publique bool) (string, error) {
	requete := `SELECT s.archive_type FROM source s WHERE s.archive_sha256 = $1`
	if publique {
		requete += ` AND (EXISTS (SELECT 1 FROM engagement_source es JOIN statut st ON st.engagement_id = es.engagement_id
				JOIN statut_publication pu ON pu.statut_id = st.id WHERE es.source_id = s.id)
			OR EXISTS (SELECT 1 FROM statut_preuve sp JOIN statut_publication pu ON pu.statut_id = sp.statut_id
				WHERE sp.source_id = s.id))`
	}
	var typ string
	err := b.Pool.QueryRow(ctx, requete+" ORDER BY s.id LIMIT 1", e[:]).Scan(&typ)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", observatoire.ErrIntrouvable
	}
	return typ, err
}

var _ observatoire.Magasin = (*Base)(nil)
