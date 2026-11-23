package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"

	"github.com/2guntan/diguee/internal/base"
	"github.com/2guntan/diguee/internal/cloture"
	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/empreinte"
	"github.com/2guntan/diguee/internal/sauvegarde"
)

// usageCloturer décrit « diguee cloturer » (ticket #16).
const usageCloturer = `usage : diguee cloturer [-sans-volume]
  Procédure de destruction de la Clôture (spec §12, ADR 0002), avec le rôle
  propriétaire (DIGUEE_POSTGRES_URL_MIGRATIONS) : vérification, suppression de
  la table participation, VACUUM FULL et CHECKPOINT, suppression de toutes les
  sauvegardes puis nouvelle sauvegarde, destruction de la clé de l'empreinte
  dans le KMS, renouvellement des clés du volume (attestation de volume.sh
  renouveler), procès-verbal. Chaque étape est inscrite au journal public ;
  relancée, la procédure reprend à la première étape qui manque.
  -sans-volume   pas de volume chiffré (développement seulement)
Variables : celles du serveur, DIGUEE_POSTGRES_URL_MIGRATIONS, DIGUEE_SAUVEGARDE_*,
DIGUEE_CLOTURE_KMS_CERTIFICAT, DIGUEE_CLOTURE_KMS_CLE_CLIENT, DIGUEE_CLOTURE_ATTESTATION_VOLUME
(deploiement/README.md §12).`

// cloturer implémente « diguee cloturer ». Une procédure en attente d'une
// étape manuelle n'est pas une erreur ; un échec l'est (l'unité systemd
// déclenche alors l'alerte).
func cloturer(ctx context.Context, args []string, getenv func(string) string, sortie io.Writer, journal *slog.Logger) error {
	fs := flag.NewFlagSet("cloturer", flag.ContinueOnError)
	fs.SetOutput(sortie)
	sansVolume := fs.Bool("sans-volume", false, "pas de volume chiffré (développement seulement)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(sortie, usageCloturer)
		return fmt.Errorf("argument inattendu : %s", fs.Arg(0))
	}
	p, fermer, err := preparerCloture(ctx, getenv, *sansVolume, sortie, journal)
	if err != nil {
		return err
	}
	defer fermer()
	b, err := p.Executer(ctx)
	switch {
	case errors.Is(err, cloture.ErrAttente):
		return nil
	case err != nil:
		return err
	case b.DejaTerminee:
		fmt.Fprintf(sortie, "clôture déjà terminée : procès-verbal à l'entrée %d du journal public (empreinte %s)\n",
			b.ProcesVerbal.Numero, b.ProcesVerbal.Empreinte)
	default:
		fmt.Fprintln(sortie, "CLÔTURE TERMINÉE. Restent à effacer à la main : instantanés du VPS, copies locales de sauvegardes (docs/À faire par le porteur.md).")
	}
	return nil
}

// preparerCloture construit la procédure d'après la configuration.
func preparerCloture(ctx context.Context, getenv func(string) string, sansVolume bool, sortie io.Writer, journal *slog.Logger) (*cloture.Procedure, func(), error) {
	cfg, err := config.Charger(getenv)
	if err != nil {
		return nil, nil, fmt.Errorf("configuration invalide :\n%w", err)
	}
	production := cfg.Env == config.Production
	if cfg.Cloture.Provisoire() {
		return nil, nil, errors.New("DIGUEE_CLOTURE est provisoire : pas de clôture possible")
	}
	if production && sansVolume {
		return nil, nil, errors.New("-sans-volume est refusé en production")
	}
	url, nom := cfg.PostgresURLMigrations, "DIGUEE_POSTGRES_URL_MIGRATIONS"
	if url == "" {
		if production {
			return nil, nil, errors.New("DIGUEE_POSTGRES_URL_MIGRATIONS (rôle propriétaire) est obligatoire pour diguee cloturer en production")
		}
		url, nom = cfg.PostgresURL, "DIGUEE_POSTGRES_URL"
	}
	s, err := config.ChargerSauvegarde(getenv, true)
	if err != nil {
		return nil, nil, fmt.Errorf("configuration des sauvegardes :\n%w", err)
	}
	if production && s.Stockage != "ovh" {
		return nil, nil, errors.New("DIGUEE_SAUVEGARDE_STOCKAGE doit valoir ovh en production")
	}
	c, err := config.ChargerCloture(getenv, cfg.Empreinteur == "ovh")
	if err != nil {
		return nil, nil, fmt.Errorf("configuration de la clôture :\n%w", err)
	}
	compartimentSauvegardes, err := compartiment(s)
	if err != nil {
		return nil, nil, err
	}
	p := &cloture.Procedure{
		Cloture: cfg.Cloture.Jour, Sauvegardes: compartimentSauvegardes, ModeCle: cfg.Empreinteur,
		SansVolume: sansVolume, Log: journal, Sortie: sortie,
		Sauvegarder: func(ctx context.Context) (*sauvegarde.Resultat, error) {
			// Le fichier d'état de l'espace équipe n'est pas mis à jour : il
			// appartient à l'utilisateur diguee-sauvegarde ; la sauvegarde
			// de nuit suivante le fera.
			return faireSauvegarde(ctx, s, compartimentSauvegardes, journal)
		},
		Volume: func(context.Context) (*cloture.AttestationVolume, error) { return lireAttestation(c.AttestationVolume) },
	}
	var destructeur *empreinte.DestructeurOVH
	switch cfg.Empreinteur {
	case "ovh":
		tlsCfg, err := empreinte.ChargerCertificat(c.KMSCertificat, c.KMSCleClient, cfg.OVHKMS.AC)
		if err != nil {
			return nil, nil, fmt.Errorf("KMS OVH (DIGUEE_CLOTURE_KMS_CERTIFICAT, DIGUEE_CLOTURE_KMS_CLE_CLIENT) : %w", err)
		}
		destructeur, err = empreinte.NouveauDestructeurOVH(empreinte.OptionsDestructeur{
			PointAcces: cfg.OVHKMS.PointAcces, KMSID: cfg.OVHKMS.ID, CleID: cfg.OVHKMS.CleID, TLS: tlsCfg, Delai: cfg.KMSDelai,
		})
		if err != nil {
			return nil, nil, err
		}
		p.Cle = destructeur
	case "fake":
		if production {
			return nil, nil, errors.New("DIGUEE_EMPREINTEUR=fake est refusé en production")
		}
	default:
		return nil, nil, fmt.Errorf("DIGUEE_EMPREINTEUR=%s : destruction de la clé non prise en charge par diguee cloturer (à faire dans la console du KMS, puis adapter la procédure)", cfg.Empreinteur)
	}
	p.Controler = func(ctx context.Context) error {
		if _, err := sauvegarde.LireDestinataire(s.ClePublique); err != nil {
			return err
		}
		for _, prog := range []string{s.PgDump, s.Age} {
			if _, err := exec.LookPath(prog); err != nil {
				return fmt.Errorf("programme %s introuvable : %w", prog, err)
			}
		}
		if _, err := compartimentSauvegardes.Lister(ctx, sauvegarde.Prefixe); err != nil {
			return fmt.Errorf("compartiment des sauvegardes : %w", err)
		}
		if destructeur != nil {
			etat, err := destructeur.Etat(ctx)
			if err != nil {
				return err
			}
			fmt.Fprintf(sortie, "  clé de l'empreinte dans le KMS : %s\n", etat)
		}
		return nil
	}
	b, err := base.OuvrirVariable(ctx, url, nom)
	if err != nil {
		return nil, nil, err
	}
	if production {
		// Le rôle propriétaire possède les tables : le contrôle du rôle de
		// l'application doit échouer.
		if err := b.VerifierRoleApplication(ctx); err == nil {
			b.Fermer()
			return nil, nil, errors.New("DIGUEE_POSTGRES_URL_MIGRATIONS n'est pas le rôle propriétaire (il n'a que les droits de l'application)")
		}
	}
	if err := b.VerifierSchemaAJour(ctx); err != nil {
		b.Fermer()
		return nil, nil, err
	}
	p.Base = b
	return p, b.Fermer, nil
}

// lireAttestation lit l'attestation de « volume.sh renouveler » ; nil si
// elle n'existe pas encore.
func lireAttestation(chemin string) (*cloture.AttestationVolume, error) {
	brut, err := os.ReadFile(chemin)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("attestation du volume : %w", err)
	}
	var a cloture.AttestationVolume
	if err := json.Unmarshal(brut, &a); err != nil {
		return nil, fmt.Errorf("attestation du volume %s : %w", chemin, err)
	}
	if a.Date.IsZero() {
		return nil, fmt.Errorf("attestation du volume %s : date absente", chemin)
	}
	return &a, nil
}
