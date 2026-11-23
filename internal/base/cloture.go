package base

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/2guntan/diguee/internal/journal"
)

// Clôture (ticket #16, spec §12, ADR 0002) : ce que la procédure demande à la
// base. Tout se fait avec le rôle propriétaire (« diguee cloturer »), sauf
// DernierJourReponse et ParticipationsDetruites, qui ne font que lire.

// EtapeSuppressionParticipations est la valeur du champ « etape » de
// l'entrée « cloture » du journal public inscrite, dans la même transaction,
// avec la suppression de la table participation (paquet cloture).
const EtapeSuppressionParticipations = "suppression_participations"

// requeteParticipationsDetruites dit si le journal public porte l'entrée de
// la suppression de participation. C'est la seule façon admise pour la table
// d'être absente : sans cette entrée, une table absente est une erreur.
const requeteParticipationsDetruites = `SELECT EXISTS (SELECT 1 FROM journal_public
	WHERE type = 'cloture' AND contenu::jsonb ->> 'etape' = '` + EtapeSuppressionParticipations + `')`

// ParticipationsDetruites indique si la table participation a été supprimée
// par la Clôture (entrée du journal public).
func (b *Base) ParticipationsDetruites(ctx context.Context) (bool, error) {
	var oui bool
	err := b.Pool.QueryRow(ctx, requeteParticipationsDetruites).Scan(&oui)
	return oui, err
}

// TableExiste indique si la table existe dans le schéma courant.
func (b *Base) TableExiste(ctx context.Context, nom string) (bool, error) {
	var oui bool
	err := b.Pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", pgx.Identifier{nom}.Sanitize()).Scan(&oui)
	return oui, err
}

// DernierJourReponse renvoie le jour le plus récent des réponses, et false
// s'il n'y a aucune réponse.
func (b *Base) DernierJourReponse(ctx context.Context) (time.Time, bool, error) {
	var j pgtype.Date
	if err := b.Pool.QueryRow(ctx, "SELECT max(jour) FROM reponse").Scan(&j); err != nil {
		return time.Time{}, false, err
	}
	if !j.Valid {
		return time.Time{}, false, nil
	}
	return time.Date(j.Time.Year(), j.Time.Month(), j.Time.Day(), 0, 0, 0, 0, time.UTC), true, nil
}

// SupprimerParticipations supprime la table participation et inscrit,
// dans la même transaction, l'entrée du journal public que prépare
// inscrire (qui reçoit le nombre de lignes supprimées et le journal vu de la
// transaction) : la table disparaît avec son entrée, ou pas du tout. La
// table est d'abord verrouillée en exclusif (aucune lecture ni écriture
// concurrente), vidée (TRUNCATE), puis supprimée. Si elle n'existe plus,
// lignes vaut -1 et inscrire est appelée quand même (reprise après une
// suppression dont l'entrée manquerait, ce qui ne peut arriver que si
// quelqu'un a supprimé la table à la main).
func (b *Base) SupprimerParticipations(ctx context.Context, inscrire func(ctx context.Context, lignes int64, j journal.Magasin) error) error {
	return pgx.BeginFunc(ctx, b.Pool, func(tx pgx.Tx) error {
		var existe bool
		if err := tx.QueryRow(ctx, "SELECT to_regclass('participation') IS NOT NULL").Scan(&existe); err != nil {
			return err
		}
		lignes := int64(-1)
		if existe {
			if _, err := tx.Exec(ctx, "LOCK TABLE participation IN ACCESS EXCLUSIVE MODE"); err != nil {
				return fmt.Errorf("verrou de participation : %w", err)
			}
			if err := tx.QueryRow(ctx, "SELECT count(*) FROM participation").Scan(&lignes); err != nil {
				return err
			}
			for _, o := range OrdresSuppressionParticipations {
				if _, err := tx.Exec(ctx, o); err != nil {
					return fmt.Errorf("%s : %w", o, err)
				}
			}
		}
		return inscrire(ctx, lignes, journalTx{tx})
	})
}

// OrdresSuppressionParticipations sont les ordres SQL de la suppression,
// dans l'ordre (leur SHA-256 est inscrit au procès-verbal).
var OrdresSuppressionParticipations = []string{"TRUNCATE participation", "DROP TABLE participation"}

// LibererEspace rend au disque l'espace des tables du schéma (VACUUM FULL,
// qui réécrit chaque table dans de nouveaux fichiers et supprime les
// anciens), puis force un point de contrôle (CHECKPOINT) : les fichiers de
// la table supprimée et les anciens segments du journal d'écriture (WAL)
// peuvent alors être effacés ou recyclés par PostgreSQL. Elle renvoie les
// ordres exécutés. CHECKPOINT demande le rôle prédéfini pg_checkpoint
// (deploiement/postgresql/roles.sql) ou un superutilisateur.
func (b *Base) LibererEspace(ctx context.Context) ([]string, error) {
	tables, err := tablesDuSchema(ctx, b.Pool)
	if err != nil {
		return nil, err
	}
	var ordres []string
	for _, t := range tables {
		ordres = append(ordres, "VACUUM (FULL, ANALYZE) "+pgx.Identifier{t}.Sanitize())
	}
	ordres = append(ordres, "CHECKPOINT")
	for _, o := range ordres {
		if _, err := b.Pool.Exec(ctx, o); err != nil {
			return nil, fmt.Errorf("%s : %w", o, err)
		}
	}
	return ordres, nil
}

// LignesTable est le nombre de lignes d'une table.
type LignesTable struct {
	Nom    string `json:"nom"`
	Lignes int64  `json:"lignes"`
}

// CompterTables renvoie le nombre de lignes de chaque table du schéma
// courant, par ordre alphabétique.
func (b *Base) CompterTables(ctx context.Context) ([]LignesTable, error) {
	tables, err := tablesDuSchema(ctx, b.Pool)
	if err != nil {
		return nil, err
	}
	ls := make([]LignesTable, 0, len(tables))
	for _, t := range tables {
		var n int64
		if err := b.Pool.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{t}.Sanitize()).Scan(&n); err != nil {
			return nil, fmt.Errorf("table %s : %w", t, err)
		}
		ls = append(ls, LignesTable{Nom: t, Lignes: n})
	}
	return ls, nil
}

// verrouCloture est la clé du verrou consultatif de session tenu pendant
// toute la procédure de clôture : deux « diguee cloturer » ne tournent
// jamais en même temps.
const verrouCloture = 0x636c6f74757265 // « cloture »

// VerrouCloture prend le verrou de la procédure de clôture sur une connexion
// réservée, sans attendre. liberer le rend (et rend la connexion). Si une
// autre procédure le tient, ok vaut false.
func (b *Base) VerrouCloture(ctx context.Context) (liberer func(), ok bool, err error) {
	c, err := b.Pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	if err := c.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", int64(verrouCloture)).Scan(&ok); err != nil || !ok {
		c.Release()
		return nil, false, err
	}
	return func() {
		_, _ = c.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", int64(verrouCloture))
		c.Release()
	}, true, nil
}
