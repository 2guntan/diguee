package base

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Rôles PostgreSQL (ticket #7, recommandation des tickets #3, #4, #13 et #14).
//
// En production, deux rôles de connexion se partagent la base :
//
//   - le rôle propriétaire (diguee_proprietaire dans deploiement/) possède le
//     schéma et toutes les tables ; il ne sert qu'à « diguee migrer »
//     (DIGUEE_POSTGRES_URL_MIGRATIONS), et plus tard à la Clôture (#16), qui
//     supprime la table participation ;
//   - le rôle de l'application (diguee_app), membre du rôle de groupe
//     RoleApplication, n'a que les droits de la table droitsApplication :
//     SELECT et INSERT sur les tables en ajout seul, la modification de
//     quatre colonnes de membre, rien d'autre. Il ne peut ni modifier ni
//     supprimer une ligne protégée, ni vider une table, ni désactiver un
//     déclencheur (ALTER TABLE demande d'être propriétaire), ni créer d'objet.
//
// « diguee migrer » réapplique ces droits après chaque migration
// (AccorderDroits), et « diguee » (servir) refuse de démarrer en production
// si son rôle est propriétaire, superutilisateur ou a des droits de trop
// (VerifierRoleApplication).

// RoleApplication est le rôle de groupe (NOLOGIN) qui reçoit les droits de
// l'application ; le rôle de connexion du serveur en est membre. Il est créé
// par deploiement/postgresql/roles.sql.
const RoleApplication = "diguee_application"

// droitApplication décrit les droits du rôle de l'application sur une table.
type droitApplication struct {
	table  string
	droits string // fragment GRANT, écrit ici et jamais lu d'ailleurs
}

// colonnesMembreModifiables sont les seules colonnes de membre que
// l'application modifie (rôles, réinitialisation de l'accès, désactivation).
var colonnesMembreModifiables = []string{"roles", "mot_de_passe", "totp_secret_chiffre", "actif"}

// droitsApplication est la politique complète : toute table du schéma doit y
// figurer, sinon AccorderDroits échoue (une nouvelle migration doit dire ce
// que l'application a le droit d'y faire).
var droitsApplication = []droitApplication{
	{"schema_migrations", "SELECT"},
	// Consultation (0001). participation est supprimée à la Clôture par le
	// seul rôle propriétaire.
	{"participation", "SELECT, INSERT"},
	{"reponse", "SELECT, INSERT"},
	// Journal public (0002).
	{"journal_public", "SELECT, INSERT"},
	// Espace équipe (0003) : jamais de DELETE sur membre.
	{"membre", "SELECT, INSERT, UPDATE (" + strings.Join(colonnesMembreModifiables, ", ") + ")"},
	{"audit_equipe", "SELECT, INSERT"},
	// Publication quotidienne (0005) et ancrage (0006).
	{"publication", "SELECT, INSERT"},
	{"ancrage_preuve", "SELECT, INSERT"},
	{"ancrage_archive", "SELECT, INSERT"},
	// Observatoire (0007).
	{"elu", "SELECT, INSERT"},
	{"source", "SELECT, INSERT"},
	{"engagement", "SELECT, INSERT"},
	{"engagement_source", "SELECT, INSERT"},
	{"engagement_validation", "SELECT, INSERT"},
	{"statut", "SELECT, INSERT"},
	{"statut_preuve", "SELECT, INSERT"},
	{"statut_proposition", "SELECT, INSERT"},
	{"statut_validation", "SELECT, INSERT"},
	{"statut_rejet", "SELECT, INSERT"},
	{"statut_publication", "SELECT, INSERT"},
	// Préavis et droit de réponse (0008).
	{"statut_preavis", "SELECT, INSERT"},
	{"droit_reponse", "SELECT, INSERT"},
	{"droit_reponse_verification", "SELECT, INSERT"},
	{"droit_reponse_rejet", "SELECT, INSERT"},
	{"droit_reponse_publication", "SELECT, INSERT"},
}

// TablesApplication renvoie les tables de la politique, dans l'ordre.
func TablesApplication() []string {
	ts := make([]string, len(droitsApplication))
	for i, d := range droitsApplication {
		ts[i] = d.table
	}
	return ts
}

// ErrRoleAbsent signale que le rôle de l'application n'existe pas.
var ErrRoleAbsent = errors.New("rôle de l'application absent")

// RoleExiste indique si un rôle existe.
func (b *Base) RoleExiste(ctx context.Context, role string) (bool, error) {
	var existe bool
	err := b.Pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", role).Scan(&existe)
	return existe, err
}

// tablesDuSchema liste les tables (ordinaires et partitionnées) du schéma
// courant.
func tablesDuSchema(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT c.relname FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND c.relkind IN ('r', 'p')
		ORDER BY c.relname`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// AccorderDroits donne au rôle role exactement les droits de la politique
// sur le schéma courant, dans une transaction : tous ses droits sur le
// schéma, ses tables et ses séquences sont d'abord retirés, puis seuls ceux
// de droitsApplication sont accordés. Elle échoue si le schéma contient une
// table absente de la politique, ou l'inverse. À lancer avec le rôle
// propriétaire, après Migrer.
func (b *Base) AccorderDroits(ctx context.Context, role string) error {
	existe, err := b.RoleExiste(ctx, role)
	if err != nil {
		return err
	}
	if !existe {
		return fmt.Errorf("%w : %s (voir deploiement/postgresql/roles.sql)", ErrRoleAbsent, role)
	}
	return pgx.BeginFunc(ctx, b.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", verrouMigrations); err != nil {
			return err
		}
		tables, err := tablesDuSchema(ctx, tx)
		if err != nil {
			return err
		}
		politique := TablesApplication()
		// Après la Clôture (ticket #16), participation n'existe plus : elle
		// sort de la politique, seulement si le journal public porte
		// l'entrée de sa suppression.
		if !slices.Contains(tables, "participation") {
			var detruites bool
			if err := tx.QueryRow(ctx, requeteParticipationsDetruites).Scan(&detruites); err != nil {
				return err
			}
			if detruites {
				politique = slices.DeleteFunc(politique, func(t string) bool { return t == "participation" })
			}
		}
		var ecarts []string
		for _, t := range tables {
			if !slices.Contains(politique, t) {
				ecarts = append(ecarts, "table "+t+" absente de la politique des droits (internal/base/droits.go)")
			}
		}
		for _, t := range politique {
			if !slices.Contains(tables, t) {
				ecarts = append(ecarts, "table "+t+" de la politique absente du schéma (migrations non appliquées ?)")
			}
		}
		if len(ecarts) > 0 {
			return fmt.Errorf("droits de l'application :\n- %s", strings.Join(ecarts, "\n- "))
		}
		var schema string
		if err := tx.QueryRow(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
			return err
		}
		s, r := pgx.Identifier{schema}.Sanitize(), pgx.Identifier{role}.Sanitize()
		ordres := []string{
			"REVOKE ALL ON ALL TABLES IN SCHEMA " + s + " FROM " + r,
			"REVOKE ALL ON ALL SEQUENCES IN SCHEMA " + s + " FROM " + r,
			"REVOKE ALL ON SCHEMA " + s + " FROM " + r,
			"GRANT USAGE ON SCHEMA " + s + " TO " + r,
		}
		for _, d := range droitsApplication {
			if !slices.Contains(politique, d.table) {
				continue
			}
			ordres = append(ordres, "GRANT "+d.droits+" ON "+pgx.Identifier{schema, d.table}.Sanitize()+" TO "+r)
		}
		for _, o := range ordres {
			if _, err := tx.Exec(ctx, o); err != nil {
				return fmt.Errorf("%s : %w", o, err)
			}
		}
		return nil
	})
}

// VerifierRoleApplication contrôle que la connexion n'a que les droits de
// l'application : ni superutilisateur (rôle courant ou rôle de session), ni
// droits de création de rôles ou de bases, ni contournement des politiques
// de lignes, ni réplication ; ni propriétaire (même par appartenance) du
// schéma ou d'une de ses tables ; ni droit de créer dans le schéma ; ni
// UPDATE, DELETE, TRUNCATE, REFERENCES ou TRIGGER sur une table (la
// modification de quelques colonnes de membre passe par des droits de
// colonne). Elle renvoie une erreur qui liste tous les écarts.
func (b *Base) VerifierRoleApplication(ctx context.Context) error {
	var ecarts []string
	for _, qui := range []string{"current_user", "session_user"} {
		var nom string
		var super, createrole, createdb, bypassrls, replication bool
		err := b.Pool.QueryRow(ctx, `SELECT rolname, rolsuper, rolcreaterole, rolcreatedb, rolbypassrls, rolreplication
			FROM pg_roles WHERE rolname = `+qui).Scan(&nom, &super, &createrole, &createdb, &bypassrls, &replication)
		if err != nil {
			return fmt.Errorf("lecture du rôle de connexion : %w", err)
		}
		for _, a := range []struct {
			ok  bool
			nom string
		}{{super, "SUPERUSER"}, {createrole, "CREATEROLE"}, {createdb, "CREATEDB"}, {bypassrls, "BYPASSRLS"}, {replication, "REPLICATION"}} {
			if a.ok {
				ecarts = append(ecarts, fmt.Sprintf("le rôle %s a l'attribut %s", nom, a.nom))
			}
		}
	}
	var schemaProprio, schemaCreer bool
	if err := b.Pool.QueryRow(ctx, `SELECT pg_has_role(current_user, n.nspowner, 'MEMBER'),
			has_schema_privilege(current_user, n.oid, 'CREATE')
		FROM pg_namespace n WHERE n.nspname = current_schema()`).Scan(&schemaProprio, &schemaCreer); err != nil {
		return fmt.Errorf("lecture des droits sur le schéma : %w", err)
	}
	if schemaProprio {
		ecarts = append(ecarts, "le rôle est propriétaire du schéma (ou membre de son propriétaire)")
	}
	if schemaCreer {
		ecarts = append(ecarts, "le rôle peut créer des objets dans le schéma")
	}
	rows, err := b.Pool.Query(ctx, `SELECT c.relname,
			pg_has_role(current_user, c.relowner, 'MEMBER'),
			array_remove(ARRAY[
				CASE WHEN has_table_privilege(current_user, c.oid, 'UPDATE') THEN 'UPDATE' END,
				CASE WHEN has_table_privilege(current_user, c.oid, 'DELETE') THEN 'DELETE' END,
				CASE WHEN has_table_privilege(current_user, c.oid, 'TRUNCATE') THEN 'TRUNCATE' END,
				CASE WHEN has_table_privilege(current_user, c.oid, 'REFERENCES') THEN 'REFERENCES' END,
				CASE WHEN has_table_privilege(current_user, c.oid, 'TRIGGER') THEN 'TRIGGER' END
			], NULL)
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND c.relkind IN ('r', 'p')
		ORDER BY c.relname`)
	if err != nil {
		return fmt.Errorf("lecture des droits sur les tables : %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var table string
		var proprio bool
		var trop []string
		if err := rows.Scan(&table, &proprio, &trop); err != nil {
			return err
		}
		if proprio {
			ecarts = append(ecarts, "le rôle est propriétaire de la table "+table+" (ou membre de son propriétaire)")
		}
		if len(trop) > 0 {
			ecarts = append(ecarts, "droits de trop sur "+table+" : "+strings.Join(trop, ", "))
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(ecarts) > 0 {
		return fmt.Errorf("rôle PostgreSQL de l'application refusé en production (utiliser le rôle diguee_app, voir deploiement/README.md) :\n- %s",
			strings.Join(ecarts, "\n- "))
	}
	return nil
}

// VerifierSchemaAJour renvoie une erreur si les migrations embarquées ne sont
// pas toutes appliquées, ou si la base est plus récente que l'exécutable. En
// production, le serveur n'applique pas les migrations : « diguee migrer »
// le fait avec le rôle propriétaire.
func (b *Base) VerifierSchemaAJour(ctx context.Context) error {
	ms, err := Migrations()
	if err != nil {
		return err
	}
	var derniere int
	if err := b.Pool.QueryRow(ctx, "SELECT coalesce(max(version), 0) FROM schema_migrations").Scan(&derniere); err != nil {
		return fmt.Errorf("schéma non migré (lancer diguee migrer avec DIGUEE_POSTGRES_URL_MIGRATIONS) : %w", err)
	}
	switch attendue := len(ms); {
	case derniere < attendue:
		return fmt.Errorf("schéma en version %d, %d attendue : lancer diguee migrer avec DIGUEE_POSTGRES_URL_MIGRATIONS", derniere, attendue)
	case derniere > attendue:
		return fmt.Errorf("schéma en version %d, plus récent que cet exécutable (%d)", derniere, attendue)
	}
	return nil
}
