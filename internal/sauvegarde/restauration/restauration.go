// Package restauration contrôle une base restaurée depuis une sauvegarde
// (ticket #15, deploiement/restaurer-sauvegarde.sh) : lignes par table et
// chaîne du journal public. Séparé du paquet sauvegarde, qui est lu par
// l'espace équipe et ne dépend pas de la base.
package restauration

import (
	"context"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/2guntan/diguee/internal/base"
	"github.com/2guntan/diguee/internal/journal"
)

// TablesPrincipales doivent exister dans une base restaurée, quelle que
// soit la migration de la sauvegarde ; les autres tables sont comptées.
var TablesPrincipales = []string{"schema_migrations", "participation", "reponse", "journal_public", "membre"}

// Table est le nombre de lignes d'une table restaurée.
type Table struct {
	Nom    string
	Lignes int64
}

// Controle est le résultat du contrôle d'une base restaurée.
type Controle struct {
	Tables    []Table
	Migration int             // dernière migration appliquée
	Journal   journal.Rapport // chaîne du journal public, vérifiée
	Derniere  *journal.Entree // dernière entrée du journal
	Duree     time.Duration   // durée du contrôle
	Manque    []string        // tables principales absentes
	// ParticipationsDetruites : la table participation est absente parce
	// que la Clôture l'a supprimée (entrée du journal public).
	ParticipationsDetruites bool
	parNom                  map[string]*Table // index
}

// Lignes renvoie le nombre de lignes d'une table (-1 si elle est absente).
func (c *Controle) Lignes(nom string) int64 {
	if t, ok := c.parNom[nom]; ok {
		return t.Lignes
	}
	return -1
}

// Controler vérifie une base restaurée (deploiement/restaurer-sauvegarde.sh) :
// nombre de lignes de chaque table du schéma courant, présence des tables
// principales, dernière migration, et chaîne du journal public, dont
// l'export (format de /journal.json) est écrit dans export pour être relu
// par « diguee verifier-journal ». Elle ne modifie rien.
func Controler(ctx context.Context, url string, export io.Writer) (*Controle, error) {
	debut := time.Now()
	b, err := base.OuvrirVariable(ctx, url, "-base")
	if err != nil {
		return nil, err
	}
	defer b.Fermer()
	c := &Controle{parNom: map[string]*Table{}}

	lignes, err := b.Pool.Query(ctx, `SELECT table_name FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_type = 'BASE TABLE' ORDER BY table_name`)
	if err != nil {
		return nil, err
	}
	noms, err := pgx.CollectRows(lignes, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	for _, nom := range noms {
		var n int64
		if err := b.Pool.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{nom}.Sanitize()).Scan(&n); err != nil {
			return nil, fmt.Errorf("table %s : %w", nom, err)
		}
		c.Tables = append(c.Tables, Table{Nom: nom, Lignes: n})
	}
	for i := range c.Tables {
		c.parNom[c.Tables[i].Nom] = &c.Tables[i]
	}
	// Après la Clôture (ticket #16), la table participation n'existe plus :
	// son absence est attendue si le journal public porte l'entrée de sa
	// suppression.
	detruites := false
	if slices.Contains(noms, "journal_public") && !slices.Contains(noms, "participation") {
		if detruites, err = b.ParticipationsDetruites(ctx); err != nil {
			return c, fmt.Errorf("journal public : %w", err)
		}
	}
	c.ParticipationsDetruites = detruites
	for _, t := range TablesPrincipales {
		if t == "participation" && detruites {
			continue
		}
		if !slices.Contains(noms, t) {
			c.Manque = append(c.Manque, t)
		}
	}
	if len(c.Manque) > 0 {
		return c, fmt.Errorf("base restaurée incomplète : tables absentes : %v", c.Manque)
	}
	if err := b.Pool.QueryRow(ctx, "SELECT coalesce(max(version), 0) FROM schema_migrations").Scan(&c.Migration); err != nil {
		return c, fmt.Errorf("table des migrations : %w", err)
	}

	es, err := journal.Nouveau(b, time.Now).Toutes(ctx)
	if err != nil {
		return c, fmt.Errorf("journal public : %w", err)
	}
	c.Journal, err = journal.VerifierChaine(es)
	if err != nil {
		return c, fmt.Errorf("journal public NON VALIDE : %w", err)
	}
	if len(es) > 0 {
		c.Derniere = &es[len(es)-1]
	}
	if export != nil {
		if err := journal.Exporter(export, es); err != nil {
			return c, fmt.Errorf("export du journal : %w", err)
		}
	}
	c.Duree = time.Since(debut)
	return c, nil
}
