// Commande diguee : l'exécutable unique de la plateforme.
//
//	diguee                  lance le serveur (migrations comprises)
//	diguee migrer           applique les migrations puis s'arrête
//	diguee verifier-config  affiche ce qui manque pour la production
//	diguee version          affiche la version et le SHA-256 de l'exécutable
//	diguee creer-membre -identifiant ID -nom NOM [-roles R1,R2]
//	                        crée un compte de l'espace équipe (le premier
//	                        administrateur, par exemple) et affiche une seule
//	                        fois son mot de passe et son secret TOTP
//	diguee verifier-journal [-attendu EMPREINTE]... [-precedent ANCIEN] <fichier.json|url>
//	                        vérifie un export du journal public (sans configuration ni base)
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

	"github.com/2guntan/diguee/internal/altcha"
	"github.com/2guntan/diguee/internal/antifraude"
	"github.com/2guntan/diguee/internal/base"
	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/empreinte"
	"github.com/2guntan/diguee/internal/equipe"
	"github.com/2guntan/diguee/internal/garde"
	"github.com/2guntan/diguee/internal/i18n"
	"github.com/2guntan/diguee/internal/integration"
	journalpublic "github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/questionnaire"
	"github.com/2guntan/diguee/internal/session"
	"github.com/2guntan/diguee/internal/tampon"
	"github.com/2guntan/diguee/internal/version"
	"github.com/2guntan/diguee/internal/web"
	"github.com/2guntan/diguee/internal/whatsapp"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "verifier-journal" {
		if err := verifierJournal(context.Background(), os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
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

	// Chaque intégration externe (KMS, WhatsApp, SMS, stockage, ancrage) est
	// construite ici d'après la configuration et déclare son mode dans le
	// registre, avant le passage de la garde.
	registre := &integration.Registre{}
	empreinteur, err := empreinte.Construire(cfg, registre)
	if err != nil {
		return err
	}
	chiffreurTOTP, err := equipe.ConstruireChiffreur(cfg, registre)
	if err != nil {
		return err
	}
	integrationWhatsApp, err := whatsapp.Construire(cfg, registre)
	if err != nil {
		return err
	}
	captcha, err := altcha.Construire(cfg, registre)
	if err != nil {
		return err
	}
	q := questionnaire.Courant()

	if commande == "verifier-config" {
		manques := garde.Manques(cfg, registre, q)
		for _, m := range manques {
			fmt.Println("- " + m)
		}
		if len(manques) > 0 {
			return fmt.Errorf("%d élément(s) à compléter avant la production", len(manques))
		}
		fmt.Println("configuration complète")
		return nil
	}

	if err := garde.Verifier(cfg, registre, q); err != nil {
		return err
	}
	for _, m := range garde.Manques(cfg, registre, q) {
		journal.Warn("provisoire (interdit en production)", "manque", m)
	}

	ctx, arreter := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer arreter()

	if err := empreinte.VerifierDeterminisme(ctx, empreinteur); err != nil {
		return err
	}

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

	sessionsEquipe := equipe.NouvellesSessions(nil)
	serviceEquipe := equipe.NouveauService(b, chiffreurTOTP, sessionsEquipe, nil)

	switch commande {
	case "migrer":
		return nil
	case "creer-membre":
		return creerMembre(ctx, serviceEquipe, args[1:], os.Stdout)
	case "servir":
	default:
		return fmt.Errorf("commande inconnue : %s (servir, migrer, creer-membre, verifier-config, version, verifier-journal)", commande)
	}

	// Journal public : une entrée « mise en production » à chaque démarrage sur
	// une nouvelle version (ou un nouvel exécutable), aucune sur un simple
	// redémarrage. Sans elle, le serveur ne démarre pas.
	jp := journalpublic.Nouveau(b, nil)
	mep, inscrite, err := jp.InscrireMiseEnProduction(ctx, version.Version(), sha)
	if err != nil {
		return err
	}
	if inscrite {
		journal.Info("mise en production inscrite au journal public", "entree", mep.Numero,
			"version", version.Version(), "sha256", sha)
	}

	catalogues, err := i18n.Charger()
	if err != nil {
		return err
	}
	espaceEquipe, err := equipe.NouvelEspace(equipe.OptionsEspace{
		Service: serviceEquipe, Sessions: sessionsEquipe, Magasin: b, Catalogues: catalogues, Journal: journal,
	})
	if err != nil {
		return err
	}
	// Tampon mélangé (ADR 0003) : écrit à TAMPON_TAILLE envois, au plus tard
	// TAMPON_DELAI après le premier, et à l'arrêt propre ci-dessous.
	// Anti-fraude (spec §7) : métriques agrégées par préfixe et par région,
	// avec alerte de pic ; limite par empreinte, partagée par tous les
	// parcours de vérification (WhatsApp inversé, puis codes WhatsApp et SMS).
	surveillance := antifraude.NouvelleSurveillance(nil, journal)
	limiteEmpreinte := antifraude.NouvelleLimiteEmpreinte(cfg.VerifParEmpreinte24h, nil)
	tp := tampon.Nouveau(tampon.Options{
		Ecrivain: b, Taille: cfg.TamponTaille, Delai: cfg.TamponDelai, Journal: journal,
		Surveillance: surveillance,
	})
	sessions := session.NouveauMagasin()
	// Vérification inversée par WhatsApp (ADR 0009) : jetons en mémoire,
	// webhook de Meta.
	verificateur, err := whatsapp.NouveauVerificateur(whatsapp.OptionsVerificateur{
		Empreinteur: empreinteur, Doublons: tp, Sessions: sessions,
		Limiteur: limiteEmpreinte, Surveillance: surveillance,
		Prefixes: cfg.PrefixesAutorises, Duree: cfg.JetonDuree, Journal: journal,
	})
	if err != nil {
		return err
	}
	srv, err := web.Nouveau(web.Options{
		Config: cfg, Catalogues: catalogues, Compteur: b, JournalPublic: jp,
		Version: version.Version(), SHA256: sha, Journal: journal,
		Empreinteur: empreinteur, Tampon: tp, Sessions: sessions, Questionnaire: q,
		Equipe: espaceEquipe, WhatsApp: integrationWhatsApp, Verificateur: verificateur,
		// Captcha ALTCHA ; la limite par adresse (DIGUEE_IP_PAR_HEURE) est
		// construite par le serveur web.
		Altcha: captcha,
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

	var errServeur error
	select {
	case errServeur = <-erreurs:
	case <-ctx.Done():
		journal.Info("arrêt demandé")
	}
	fin, annuler := context.WithTimeout(context.Background(), 20*time.Second)
	defer annuler()
	if err := serveur.Shutdown(fin); err != nil && !errors.Is(err, context.DeadlineExceeded) && errServeur == nil {
		errServeur = err
	}
	// Plus aucune requête en cours : le tampon est écrit, même incomplet.
	attente := tp.Attente()
	if err := tp.Fermer(fin); err != nil {
		return errors.Join(errServeur, fmt.Errorf("écriture du tampon à l'arrêt (%d envois perdus) : %w", attente, err))
	}
	journal.Info("tampon écrit à l'arrêt", "envois", attente)
	return errServeur
}
