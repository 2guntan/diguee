package base

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/2guntan/diguee/internal/equipe"
)

// verrouEquipe sérialise les écritures sur les comptes et l'audit : la
// règle « au moins un administrateur actif » et la numérotation de l'audit
// en dépendent.
const verrouEquipe = 0x657175697065 // « equipe »

const colonnesMembre = "id::text, identifiant, nom, roles, actif"

func versTextes(rs []equipe.Role) []string {
	ss := make([]string, len(rs))
	for i, r := range rs {
		ss[i] = string(r)
	}
	return ss
}

func versRoles(ss []string) []equipe.Role {
	rs := make([]equipe.Role, len(ss))
	for i, s := range ss {
		rs[i] = equipe.Role(s)
	}
	return rs
}

func lireMembre(row pgx.Row, extra ...any) (equipe.Membre, error) {
	var m equipe.Membre
	var roles []string
	err := row.Scan(append([]any{&m.ID, &m.Identifiant, &m.Nom, &roles, &m.Actif}, extra...)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return equipe.Membre{}, equipe.ErrInconnu
	}
	m.Roles = versRoles(roles)
	return m, err
}

// ecrireEquipe exécute f sous le verrou de l'équipe, puis inscrit l'action à
// l'audit, dans la même transaction.
func (b *Base) ecrireEquipe(ctx context.Context, a equipe.Action, f func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, b.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", verrouEquipe); err != nil {
			return err
		}
		if f != nil {
			if err := f(tx); err != nil {
				return err
			}
		}
		return auditer(ctx, tx, a)
	})
}

func auditer(ctx context.Context, tx pgx.Tx, a equipe.Action) error {
	j := pgtype.Date{Time: time.Date(a.Jour.Year(), a.Jour.Month(), a.Jour.Day(), 0, 0, 0, 0, time.UTC), Valid: true}
	acteur := pgtype.Text{String: a.Acteur, Valid: a.Acteur != ""}
	cible := pgtype.Text{String: a.Cible, Valid: a.Cible != ""} // vide : action sur la plateforme
	var roles []string
	if a.Roles != nil {
		roles = versTextes(a.Roles)
	}
	_, err := tx.Exec(ctx, `INSERT INTO audit_equipe (numero, jour, type, acteur, cible, roles)
		SELECT coalesce(max(numero), 0) + 1, $1, $2, $3::uuid, $4::uuid, $5 FROM audit_equipe`,
		j, string(a.Type), acteur, cible, roles)
	if err != nil {
		return fmt.Errorf("audit de l'équipe : %w", err)
	}
	return nil
}

// verifierAdministrateurs renvoie ErrDernierAdministrateur s'il ne reste
// aucun administrateur actif.
func verifierAdministrateurs(ctx context.Context, tx pgx.Tx) error {
	var n int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM membre WHERE actif AND 'administrateur' = ANY (roles)").Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return equipe.ErrDernierAdministrateur
	}
	return nil
}

// membreActifPourMaj verrouille la ligne du membre et vérifie qu'il est actif.
func membreActifPourMaj(ctx context.Context, tx pgx.Tx, id string) error {
	var actif bool
	err := tx.QueryRow(ctx, "SELECT actif FROM membre WHERE id = $1::uuid FOR UPDATE", id).Scan(&actif)
	switch {
	case errors.Is(err, pgx.ErrNoRows) || err != nil && estUUIDInvalide(err):
		return equipe.ErrInconnu
	case err != nil:
		return err
	case !actif:
		return equipe.ErrInactif
	}
	return nil
}

func estUUIDInvalide(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "22P02" // invalid_text_representation
}

// CreerMembre implémente equipe.Magasin.
func (b *Base) CreerMembre(ctx context.Context, c equipe.Compte, a equipe.Action) error {
	return b.ecrireEquipe(ctx, a, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO membre (id, identifiant, nom, roles, mot_de_passe, totp_secret_chiffre, actif)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, true)`,
			c.ID, c.Identifiant, c.Nom, versTextes(c.Roles), c.MotDePasse, c.SecretChiffre)
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" && pe.ConstraintName == "membre_identifiant_key" {
			return equipe.ErrIdentifiantPris
		}
		return err
	})
}

// CompteParIdentifiant implémente equipe.Magasin.
func (b *Base) CompteParIdentifiant(ctx context.Context, identifiant string) (equipe.Compte, error) {
	var c equipe.Compte
	m, err := lireMembre(b.Pool.QueryRow(ctx, "SELECT "+colonnesMembre+
		", mot_de_passe, totp_secret_chiffre FROM membre WHERE identifiant = $1", identifiant),
		&c.MotDePasse, &c.SecretChiffre)
	c.Membre = m
	return c, err
}

// MembreParID implémente equipe.Magasin.
func (b *Base) MembreParID(ctx context.Context, id string) (equipe.Membre, error) {
	m, err := lireMembre(b.Pool.QueryRow(ctx, "SELECT "+colonnesMembre+" FROM membre WHERE id = $1::uuid", id))
	if err != nil && estUUIDInvalide(err) {
		return equipe.Membre{}, equipe.ErrInconnu
	}
	return m, err
}

// ListerMembres implémente equipe.Magasin : actifs d'abord, puis par identifiant.
func (b *Base) ListerMembres(ctx context.Context) ([]equipe.Membre, error) {
	lignes, err := b.Pool.Query(ctx, "SELECT "+colonnesMembre+" FROM membre ORDER BY actif DESC, identifiant")
	if err != nil {
		return nil, err
	}
	defer lignes.Close()
	var ms []equipe.Membre
	for lignes.Next() {
		m, err := lireMembre(lignes)
		if err != nil {
			return nil, err
		}
		ms = append(ms, m)
	}
	return ms, lignes.Err()
}

// DesactiverMembre implémente equipe.Magasin.
func (b *Base) DesactiverMembre(ctx context.Context, id string, a equipe.Action) error {
	return b.ecrireEquipe(ctx, a, func(tx pgx.Tx) error {
		if err := membreActifPourMaj(ctx, tx, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE membre SET actif = false WHERE id = $1::uuid", id); err != nil {
			return err
		}
		return verifierAdministrateurs(ctx, tx)
	})
}

// ChangerRoles implémente equipe.Magasin.
func (b *Base) ChangerRoles(ctx context.Context, id string, roles []equipe.Role, a equipe.Action) error {
	return b.ecrireEquipe(ctx, a, func(tx pgx.Tx) error {
		if err := membreActifPourMaj(ctx, tx, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE membre SET roles = $2 WHERE id = $1::uuid", id, versTextes(roles)); err != nil {
			return err
		}
		return verifierAdministrateurs(ctx, tx)
	})
}

// RemplacerAcces implémente equipe.Magasin.
func (b *Base) RemplacerAcces(ctx context.Context, id, motDePasse string, secretChiffre []byte, a equipe.Action) error {
	return b.ecrireEquipe(ctx, a, func(tx pgx.Tx) error {
		if err := membreActifPourMaj(ctx, tx, id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "UPDATE membre SET mot_de_passe = $2, totp_secret_chiffre = $3 WHERE id = $1::uuid",
			id, motDePasse, secretChiffre)
		return err
	})
}

// Auditer implémente equipe.Magasin.
func (b *Base) Auditer(ctx context.Context, a equipe.Action) error {
	return b.ecrireEquipe(ctx, a, nil)
}

// ListerAudit implémente equipe.Magasin : les plus récentes d'abord.
func (b *Base) ListerAudit(ctx context.Context, limite int) ([]equipe.Action, error) {
	lignes, err := b.Pool.Query(ctx, `SELECT a.numero, a.jour, a.type, coalesce(a.acteur::text, ''), coalesce(a.cible::text, ''),
			coalesce(ac.identifiant, ''), coalesce(ci.identifiant, ''), a.roles
		FROM audit_equipe a
		LEFT JOIN membre ac ON ac.id = a.acteur
		LEFT JOIN membre ci ON ci.id = a.cible
		ORDER BY a.numero DESC LIMIT $1`, limite)
	if err != nil {
		return nil, err
	}
	defer lignes.Close()
	var as []equipe.Action
	for lignes.Next() {
		var a equipe.Action
		var jour pgtype.Date
		var typ string
		var roles []string
		if err := lignes.Scan(&a.Numero, &jour, &typ, &a.Acteur, &a.Cible, &a.ActeurIdentifiant, &a.CibleIdentifiant, &roles); err != nil {
			return nil, err
		}
		a.Jour, a.Type = jour.Time, equipe.TypeAction(typ)
		if roles != nil {
			a.Roles = versRoles(roles)
		}
		as = append(as, a)
	}
	return as, lignes.Err()
}

var _ equipe.Magasin = (*Base)(nil)
