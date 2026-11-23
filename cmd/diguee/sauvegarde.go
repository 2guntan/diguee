package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/sauvegarde"
	"github.com/2guntan/diguee/internal/sauvegarde/restauration"
	"github.com/2guntan/diguee/internal/stockage"
)

// usageSauvegarde décrit « diguee sauvegarde » (ticket #15).
const usageSauvegarde = `usage : diguee sauvegarde <sous-commande>
  faire                       pg_dump chiffré par age, envoyé dans le compartiment des
                              sauvegardes, puis suppression de celles de plus de 30 jours
                              (diguee-sauvegarde.service, chaque nuit)
  lister                      liste les sauvegardes, de la plus ancienne à la plus récente
  recuperer [-cle CLE] -sortie FICHIER
                              télécharge une sauvegarde chiffrée (la plus récente par défaut) ;
                              à déchiffrer avec la clé privée, hors du serveur
  supprimer-toutes -confirmer supprime toutes les sauvegardes (Clôture, spec §12 étape 4)
  controler -base URL [-journal FICHIER]
                              contrôle une base restaurée : lignes par table, chaîne du
                              journal public (export écrit dans FICHIER)
Variables : DIGUEE_SAUVEGARDE_* (deploiement/README.md, § Sauvegardes).`

// sauvegarder implémente « diguee sauvegarde ».
func sauvegarder(ctx context.Context, args []string, getenv func(string) string, sortie io.Writer, journal *slog.Logger) error {
	if len(args) == 0 {
		fmt.Fprintln(sortie, usageSauvegarde)
		return errors.New("sous-commande attendue")
	}
	fs := flag.NewFlagSet("sauvegarde "+args[0], flag.ContinueOnError)
	fs.SetOutput(sortie)
	cle := fs.String("cle", "", "clé de la sauvegarde (recuperer ; la plus récente par défaut)")
	fichier := fs.String("sortie", "", "fichier à créer (recuperer)")
	confirmer := fs.Bool("confirmer", false, "confirme la suppression de toutes les sauvegardes (supprimer-toutes)")
	urlBase := fs.String("base", "", "adresse de la base restaurée (controler)")
	export := fs.String("journal", "", "fichier où écrire l'export du journal public (controler)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("argument inattendu : %s", fs.Arg(0))
	}

	if args[0] == "controler" {
		return controlerRestauration(ctx, *urlBase, *export, sortie)
	}

	faire := args[0] == "faire"
	s, err := config.ChargerSauvegarde(getenv, faire)
	if err != nil {
		err = fmt.Errorf("configuration :\n%w", err)
		if faire {
			enregistrerEtat(getenv("DIGUEE_SAUVEGARDE_ETAT"), nil, err, journal)
		}
		return err
	}
	c, err := compartiment(s)
	if err != nil {
		return err
	}

	switch args[0] {
	case "faire":
		r, err := faireSauvegarde(ctx, s, c, journal)
		enregistrerEtat(s.Etat, r, err, journal)
		if err != nil {
			return err
		}
		fmt.Fprintf(sortie, "sauvegarde envoyée : %s (%d octets, SHA-256 %s)\n", r.Cle, r.Taille, r.SHA256)
		for _, k := range r.Supprimees {
			fmt.Fprintf(sortie, "supprimée (plus de %d jours) : %s\n", sauvegarde.ConservationJours, k)
		}
		return nil

	case "lister":
		ss, autres, err := sauvegarde.Lister(ctx, c)
		if err != nil {
			return err
		}
		for _, x := range ss {
			fmt.Fprintf(sortie, "%s\t%d\t%s\n", x.Date.Format(time.RFC3339), x.Taille, x.Cle)
		}
		for _, o := range autres {
			fmt.Fprintf(sortie, "(objet inattendu)\t%d\t%s\n", o.Taille, o.Cle)
		}
		fmt.Fprintf(sortie, "%d sauvegarde(s)\n", len(ss))
		return nil

	case "recuperer":
		if *fichier == "" {
			return errors.New("-sortie est obligatoire")
		}
		if *cle == "" {
			d, err := sauvegarde.Derniere(ctx, c)
			if err != nil {
				return err
			}
			*cle = d.Cle
		}
		if err := recupererSauvegarde(ctx, c, *cle, *fichier); err != nil {
			return err
		}
		fmt.Fprintf(sortie, "sauvegarde chiffrée %s écrite dans %s\n", *cle, *fichier)
		return nil

	case "supprimer-toutes":
		if !*confirmer {
			return errors.New("supprimer-toutes efface toutes les sauvegardes : ajouter -confirmer")
		}
		n, err := sauvegarde.SupprimerToutes(ctx, c)
		if err != nil {
			return err
		}
		journal.Warn("sauvegarde : toutes les sauvegardes ont été supprimées", "nombre", n)
		fmt.Fprintf(sortie, "%d objet(s) supprimé(s) ; le compartiment ne contient plus aucune sauvegarde\n", n)
		return nil
	}
	fmt.Fprintln(sortie, usageSauvegarde)
	return fmt.Errorf("sous-commande inconnue : %s", args[0])
}

// compartiment construit le compartiment des sauvegardes.
func compartiment(s *config.Sauvegarde) (stockage.Compartiment, error) {
	if s.Stockage == "ovh" {
		return &stockage.S3{PointAcces: s.PointAcces, Region: s.Region, Compartiment: s.Compartiment,
			CleAcces: s.CleAcces, CleSecrete: s.CleSecrete, Client: &http.Client{Timeout: s.Delai}}, nil
	}
	if err := os.MkdirAll(s.Dossier, 0o700); err != nil {
		return nil, fmt.Errorf("DIGUEE_SAUVEGARDE_DOSSIER : %w", err)
	}
	return &stockage.Dossier{Chemin: s.Dossier}, nil
}

func faireSauvegarde(ctx context.Context, s *config.Sauvegarde, c stockage.Compartiment, journal *slog.Logger) (*sauvegarde.Resultat, error) {
	dest, err := sauvegarde.LireDestinataire(s.ClePublique)
	if err != nil {
		return nil, err
	}
	r, err := sauvegarde.Faire(ctx, sauvegarde.Options{
		PgDump: s.PgDump, Age: s.Age, URL: s.PostgresURL, Destinataire: dest,
		Compartiment: c, DossierTemps: s.DossierTemps, Journal: journal,
	})
	if r != nil {
		journal.Info("sauvegarde envoyée", "cle", r.Cle, "octets", r.Taille, "sha256", r.SHA256, "supprimees", len(r.Supprimees))
	}
	return r, err
}

// enregistrerEtat écrit le fichier d'état lu par l'espace équipe ; son
// échec est journalisé sans masquer celui de la sauvegarde.
func enregistrerEtat(chemin string, r *sauvegarde.Resultat, err error, journal *slog.Logger) {
	if chemin == "" {
		return
	}
	if e := sauvegarde.Enregistrer(chemin, time.Now(), r, err); e != nil {
		journal.Error("sauvegarde : fichier d'état non écrit", "erreur", e.Error())
	}
}

// recupererSauvegarde télécharge une sauvegarde chiffrée dans un nouveau
// fichier (0600), et vérifie qu'il commence par l'en-tête d'age.
func recupererSauvegarde(ctx context.Context, c stockage.Compartiment, cle, chemin string) (err error) {
	f, err := os.OpenFile(chemin, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(chemin)
		}
	}()
	if err := c.Recuperer(ctx, cle, f); err != nil {
		return err
	}
	entete := make([]byte, len(sauvegarde.EnteteAge))
	if _, err := f.ReadAt(entete, 0); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if string(entete) != sauvegarde.EnteteAge {
		return fmt.Errorf("%s ne commence pas par l'en-tête d'age : fichier refusé", cle)
	}
	return f.Sync()
}

// controlerRestauration implémente « diguee sauvegarde controler ».
func controlerRestauration(ctx context.Context, url, export string, sortie io.Writer) error {
	if url == "" {
		return errors.New("-base est obligatoire")
	}
	var w io.Writer
	if export != "" {
		f, err := os.OpenFile(export, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	c, err := restauration.Controler(ctx, url, w)
	if c != nil {
		for _, t := range c.Tables {
			fmt.Fprintf(sortie, "%-28s %d\n", t.Nom, t.Lignes)
		}
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(sortie, "dernière migration : %d\n", c.Migration)
	fmt.Fprintf(sortie, "journal public valide : %d entrée(s)", c.Journal.Entrees)
	if c.Derniere != nil {
		fmt.Fprintf(sortie, ", la dernière n° %d du %s, empreinte de tête %s", c.Derniere.Numero, c.Derniere.Horodatage.Format(time.RFC3339), c.Journal.Tete)
	}
	fmt.Fprintln(sortie)
	return nil
}
