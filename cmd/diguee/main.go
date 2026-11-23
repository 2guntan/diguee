// Commande diguee : l'exécutable unique de la plateforme.
//
//	diguee                  lance le serveur (migrations comprises)
//	diguee migrer           applique les migrations puis s'arrête
//	diguee verifier-config  affiche ce qui manque pour la production
//	diguee version          affiche la version et le SHA-256 de l'exécutable
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/2guntan/diguee/internal/base"
	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/garde"
	"github.com/2guntan/diguee/internal/i18n"
	"github.com/2guntan/diguee/internal/integration"
	"github.com/2guntan/diguee/internal/version"
	"github.com/2guntan/diguee/internal/web"
)

func main() {
	journal := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := lancer(os.Args[1:], journal); err != nil {
		journal.Error(err.Error())
		os.Exit(1)
	}
}

func lancer(args []string, journal *slog.Logger) error {
	commande := "servir"
	if len(args) > 0 {
		commande = args[0]
	}

	sha, err := version.SHA256Executable()
	if err != nil {
		return fmt.Errorf("calcul du SHA-256 de l'exécutable : %w", err)
	}
	if commande == "version" {
		fmt.Printf("%s\nsha256 %s\n", version.Version(), sha)
		return nil
	}

	cfg, err := config.Charger(os.Getenv)
	if err != nil {
		return fmt.Errorf("configuration invalide :\n%w", err)
	}

	// Chaque intégration externe (KMS, WhatsApp, SMS, stockage, ancrage) sera
	// construite ici d'après la configuration et déclarera son mode dans le
	// registre, avant le passage de la garde.
	registre := &integration.Registre{}

	if commande == "verifier-config" {
		manques := garde.Manques(cfg, registre)
		for _, m := range manques {
			fmt.Println("- " + m)
		}
		if len(manques) > 0 {
			return fmt.Errorf("%d élément(s) à compléter avant la production", len(manques))
		}
		fmt.Println("configuration complète")
		return nil
	}

	if err := garde.Verifier(cfg, registre); err != nil {
		return err
	}
	for _, m := range garde.Manques(cfg, registre) {
		journal.Warn("provisoire (interdit en production)", "manque", m)
	}

	ctx, arreter := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer arreter()

	b, err := base.Ouvrir(ctx, cfg.PostgresURL)
	if err != nil {
		return err
	}
	defer b.Fermer()
	appliquees, err := b.Migrer(ctx)
	if err != nil {
		return fmt.Errorf("migrations : %w", err)
	}
	for _, m := range appliquees {
		journal.Info("migration appliquée", "migration", m)
	}

	switch commande {
	case "migrer":
		return nil
	case "servir":
	default:
		return fmt.Errorf("commande inconnue : %s (servir, migrer, verifier-config, version)", commande)
	}

	catalogues, err := i18n.Charger()
	if err != nil {
		return err
	}
	srv, err := web.Nouveau(web.Options{
		Config: cfg, Catalogues: catalogues, Compteur: b,
		Version: version.Version(), SHA256: sha, Journal: journal,
	})
	if err != nil {
		return err
	}

	serveur := &http.Server{
		Addr:              cfg.Adresse,
		Handler:           srv,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		// Les erreurs internes de net/http citent l'adresse IP du client : elles
		// ne sont pas journalisées (spec E6).
		ErrorLog: log.New(io.Discard, "", 0),
	}
	erreurs := make(chan error, 1)
	go func() { erreurs <- serveur.ListenAndServe() }()
	journal.Info("serveur démarré", "adresse", cfg.Adresse, "env", cfg.Env,
		"version", version.Version(), "sha256", sha)

	select {
	case err := <-erreurs:
		return err
	case <-ctx.Done():
	}
	journal.Info("arrêt demandé")
	fin, annuler := context.WithTimeout(context.Background(), 20*time.Second)
	defer annuler()
	if err := serveur.Shutdown(fin); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}
