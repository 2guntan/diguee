// Commande diguee : l'exécutable unique de la plateforme.
//
//	diguee                  lance le serveur ; en développement, applique aussi
//	                        les migrations ; en production, vérifie que le
//	                        schéma est à jour et que son rôle PostgreSQL n'a
//	                        que les droits de l'application
//	diguee migrer           applique les migrations avec le rôle propriétaire
//	                        (DIGUEE_POSTGRES_URL_MIGRATIONS) et accorde ses
//	                        droits au rôle de l'application, puis s'arrête
//	diguee verifier-config  affiche ce qui manque pour la production
//	diguee version          affiche la version et le SHA-256 de l'exécutable
//	diguee creer-membre -identifiant ID -nom NOM [-roles R1,R2]
//	                        crée un compte de l'espace équipe (le premier
//	                        administrateur, par exemple) et affiche une seule
//	                        fois son mot de passe et son secret TOTP
//	diguee publier          produit la publication quotidienne due (la dernière
//	                        si elle manque) ; sans effet si elle existe déjà
//	diguee ancrer           ancre le journal public pour le jour dû
//	                        (OpenTimestamps, archive.org) s'il ne l'est pas
//	                        encore, et met à niveau les preuves précédentes
//	diguee verifier-journal [-attendu EMPREINTE]... [-precedent ANCIEN] <fichier.json|url>
//	                        vérifie un export du journal public (sans configuration ni base)
//	diguee cle-volume creer -sortie FICHIER | dechiffrer -entree FICHIER
//	                        clé du volume chiffré (LUKS), chiffrée par le KMS
//	                        d'OVHcloud (voir deploiement/README.md)
//	diguee sauvegarde faire | lister | recuperer | supprimer-toutes | controler
//	                        sauvegardes chiffrées de la base (age), conservées
//	                        30 jours dans l'Object Storage (ticket #15)
//	diguee cloturer [-sans-volume]
//	                        procédure de destruction de la Clôture, avec le rôle
//	                        propriétaire (ticket #16) ; reprend à l'étape qui manque
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
	"github.com/2guntan/diguee/internal/ancrage"
	"github.com/2guntan/diguee/internal/antifraude"
	"github.com/2guntan/diguee/internal/base"
	"github.com/2guntan/diguee/internal/cloture"
	"github.com/2guntan/diguee/internal/codes"
	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/empreinte"
	"github.com/2guntan/diguee/internal/equipe"
	"github.com/2guntan/diguee/internal/garde"
	"github.com/2guntan/diguee/internal/https"
	"github.com/2guntan/diguee/internal/i18n"
	"github.com/2guntan/diguee/internal/integration"
	journalpublic "github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/masque"
	"github.com/2guntan/diguee/internal/observatoire"
	"github.com/2guntan/diguee/internal/publication"
	"github.com/2guntan/diguee/internal/questionnaire"
	"github.com/2guntan/diguee/internal/sauvegarde"
	"github.com/2guntan/diguee/internal/session"
	"github.com/2guntan/diguee/internal/sms"
	"github.com/2guntan/diguee/internal/stockage"
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
	if len(os.Args) > 1 && os.Args[1] == "cle-volume" {
		if err := cleVolume(context.Background(), os.Args[2:], os.Getenv, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, "diguee cle-volume :", err)
			os.Exit(1)
		}
		return
	}
	// Défense en profondeur (spec E6, T3) : toute IP ou tout numéro qui
	// apparaîtrait dans un message ou une erreur est masqué.
	journal := slog.New(masque.Nouveau(slog.NewJSONHandler(os.Stderr, nil)))
	if len(os.Args) > 1 && os.Args[1] == "sauvegarde" {
		ctx, arreter := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		err := sauvegarder(ctx, os.Args[2:], os.Getenv, os.Stdout, journal)
		arreter()
		if err != nil {
			journal.Error("diguee sauvegarde : " + err.Error())
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "cloturer" {
		ctx, arreter := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		err := cloturer(ctx, os.Args[2:], os.Getenv, os.Stdout, journal)
		arreter()
		if err != nil {
			journal.Error("diguee cloturer : " + err.Error())
			os.Exit(1)
		}
		return
	}
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
	if commande == "migrer" {
		// Ni intégration ni garde : seulement la base, avec le rôle
		// propriétaire.
		return migrer(context.Background(), cfg, journal)
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
	integrationSMS, err := sms.Construire(cfg, registre, os.Stderr)
	if err != nil {
		return err
	}
	captcha, err := altcha.Construire(cfg, registre)
	if err != nil {
		return err
	}
	integrationAncrage, err := ancrage.Construire(cfg, registre)
	if err != nil {
		return err
	}
	// Stockage d'objets des copies archivées des sources de l'observatoire.
	archives, err := stockage.Construire(cfg, registre)
	if err != nil {
		return err
	}
	q := questionnaire.Courant()
	// Composition de la population (ANSD), comparée à celle de l'échantillon
	// dans les publications ; nil tant qu'elle vaut « À COMPLÉTER ».
	population, err := publication.ChargerPopulation(cfg.PopulationANSD)
	if err != nil {
		return err
	}

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
	if err := preparerBase(ctx, cfg, b, journal); err != nil {
		return err
	}

	sessionsEquipe := equipe.NouvellesSessions(nil)
	serviceEquipe := equipe.NouveauService(b, chiffreurTOTP, sessionsEquipe, nil)

	catalogues, err := i18n.Charger()
	if err != nil {
		return err
	}
	// Publication quotidienne (spec §8) : chaque jour à
	// DIGUEE_HEURE_PUBLICATION, sur les réponses des jours antérieurs ; une
	// seule par jour, sous le verrou de la base.
	rendu, err := web.RendreInstantane(catalogues, q)
	if err != nil {
		return err
	}
	planificateur := &publication.Planificateur{
		Publieur: &publication.Publieur{
			Magasin: b, Questionnaire: q, SeuilPublication: cfg.SeuilPublication, SeuilCase: cfg.SeuilCase,
			Population: population, Rendu: rendu,
		},
		Heure: cfg.HeurePublication, Journal: journal,
	}

	// Ancrage extérieur du journal public (ADR 0010, ticket #12) : chaque
	// jour, DIGUEE_ANCRAGE_DECALAGE après la publication, OpenTimestamps et
	// archive.org ; nouveaux essais à délai croissant en cas d'échec.
	jp := journalpublic.Nouveau(b, nil)
	ancreur, err := ancrage.Nouveau(ancrage.Options{
		Mode: integrationAncrage.Mode, Magasin: b, Journal: jp,
		Calendriers: integrationAncrage.Calendriers, MinCalendriers: integrationAncrage.MinCalendriers,
		Ouvrir: integrationAncrage.Ouvrir, Archiveur: integrationAncrage.Archiveur,
		URLPublique: cfg.URLPublique, Heure: (cfg.HeurePublication + cfg.Ancrage.Decalage) % (24 * time.Hour),
		Log: journal,
	})
	if err != nil {
		return err
	}

	switch commande {
	case "creer-membre":
		return creerMembre(ctx, serviceEquipe, args[1:], os.Stdout)
	case "publier":
		return publier(ctx, planificateur, os.Stdout)
	case "ancrer":
		return ancrer(ctx, ancreur, os.Stdout)
	case "servir":
	default:
		return fmt.Errorf("commande inconnue : %s (servir, migrer, creer-membre, publier, ancrer, verifier-config, version, verifier-journal, cle-volume, sauvegarde, cloturer)", commande)
	}

	// Journal public : une entrée « mise en production » à chaque démarrage sur
	// une nouvelle version (ou un nouvel exécutable), aucune sur un simple
	// redémarrage. Sans elle, le serveur ne démarre pas.
	mep, inscrite, err := jp.InscrireMiseEnProduction(ctx, version.Version(), sha)
	if err != nil {
		return err
	}
	if inscrite {
		journal.Info("mise en production inscrite au journal public", "entree", mep.Numero,
			"version", version.Version(), "sha256", sha)
	}

	// Rattrapage : la publication due est produite avant d'ouvrir le serveur,
	// pour que l'accueil affiche le compteur du jour. Un échec n'empêche pas
	// de démarrer : la tâche quotidienne réessaie.
	_, _ = planificateur.Executer(ctx)
	planifiee := make(chan struct{})
	go func() {
		defer close(planifiee)
		planificateur.Lancer(ctx)
	}()
	// L'ancrage du jour qui manque est rattrapé aussitôt, en tâche de fond :
	// un service extérieur lent ou injoignable ne retarde pas le démarrage.
	ancree := make(chan struct{})
	go func() {
		defer close(ancree)
		ancreur.Lancer(ctx)
	}()
	// Coupe-circuit financier du parcours SMS (spec §7, ticket #10), en
	// mémoire : suspension automatique sur un pic ou au plafond horaire,
	// réactivation après DIGUEE_SMS_REACTIVATION ou par un administrateur
	// (/equipe/sms, inscrite à l'audit).
	// Observatoire (ticket #13) : saisie et circuit des statuts dans
	// l'espace équipe, pages publiques sous /observatoire ; chaque
	// publication de statut est inscrite au journal public.
	obs := observatoire.NouveauService(b, archives, cfg.ArchiveTailleMax, nil)
	// Préavis (ticket #14) : la plateforme prépare le dossier de l'élu en
	// PDF ; un membre l'envoie lui-même et enregistre la date d'envoi.
	obs.ReglerPreavis(observatoire.ReglagesPreavis{Jours: cfg.PreavisJours, AdresseReponse: cfg.AdresseReponse.Valeur,
		Textes: catalogues.Choisir(i18n.Reference)})
	optionsEspace := equipe.OptionsEspace{
		Service: serviceEquipe, Sessions: sessionsEquipe, Magasin: b, Catalogues: catalogues, Journal: journal,
		Ancrage: ancreur, Observatoire: obs,
		ServirArchive: func(w http.ResponseWriter, r *http.Request, introuvable func()) {
			web.ServirArchive(w, r, obs, false, journal.Error, introuvable)
		},
	}
	// Sauvegardes (ticket #15) : l'état écrit par diguee-sauvegarde.service
	// est affiché aux administrateurs, en alerte si la dernière a échoué.
	if cfg.SauvegardeEtat != "" {
		chemin := cfg.SauvegardeEtat
		optionsEspace.Sauvegardes = func() (*sauvegarde.Etat, error) { return sauvegarde.LireEtat(chemin) }
	}
	var coupeSMS *antifraude.CoupeCircuit
	if integrationSMS.Envoyeur != nil {
		coupeSMS = antifraude.NouveauCoupeCircuit(antifraude.OptionsCoupeCircuit{
			Facteur: cfg.SMSCoupeCircuit, Plancher: cfg.SMSCoupeCircuitPlancher,
			PlafondHoraire: cfg.SMSParHeure, Reactivation: cfg.SMSReactivation, Journal: journal,
		})
		optionsEspace.CoupeCircuitSMS = coupeSMS
	}
	espaceEquipe, err := equipe.NouvelEspace(optionsEspace)
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
	// Clôture (ticket #16) : à DIGUEE_CLOTURE, le parcours affiche
	// « Consultation close » et plus aucun jeton ni code n'est délivré ;
	// le tampon est écrit une dernière fois, la dernière publication suit à
	// l'heure habituelle, puis la fermeture est inscrite au journal public.
	ouverte := func() bool { return cfg.Phase(time.Now()) == config.PhaseOuverte }
	fermee := make(chan struct{})
	if !cfg.Cloture.Provisoire() {
		fermeture := &cloture.Fermeture{
			Cloture: cfg.Cloture.Jour, Heure: cfg.HeurePublication, Tampon: tp, Publieur: planificateur.Publieur,
			Reponses: b, Journal: jp, Log: journal,
		}
		go func() {
			defer close(fermee)
			fermeture.Lancer(ctx)
		}()
	} else {
		close(fermee)
	}
	sessions := session.NouveauMagasin()
	// Vérification inversée par WhatsApp (ADR 0009) : jetons en mémoire,
	// webhook de Meta.
	verificateur, err := whatsapp.NouveauVerificateur(whatsapp.OptionsVerificateur{
		Empreinteur: empreinteur, Doublons: tp, Sessions: sessions,
		Limiteur: limiteEmpreinte, Surveillance: surveillance,
		Prefixes: cfg.PrefixesAutorises, Duree: cfg.JetonDuree, Journal: journal, Ouverte: ouverte,
	})
	if err != nil {
		return err
	}
	// Code envoyé par WhatsApp, en secours (spec §5.2) : même limite par
	// empreinte, même surveillance et même détection des doublons que la
	// vérification inversée.
	var codesWhatsApp *codes.Verificateur
	if integrationWhatsApp.Envoyeur != nil {
		codesWhatsApp, err = codes.Nouveau(codes.Options{
			Canal: "WhatsApp", Envoyeur: integrationWhatsApp.Envoyeur,
			Empreinteur: empreinteur, Doublons: tp, Sessions: sessions,
			Limiteur: limiteEmpreinte, Surveillance: surveillance, Prefixes: cfg.PrefixesAutorises,
			Duree: cfg.CodeDuree, Essais: cfg.CodeEssais, Journal: journal, Ouverte: ouverte,
		})
		if err != nil {
			return err
		}
	}
	// Code par SMS, en dernier recours (spec §5.3) : la même limite par
	// empreinte, partagée par tous les canaux, plus une limite propre au
	// SMS, moins d'essais, et le coupe-circuit.
	var codesSMS *codes.Verificateur
	if integrationSMS.Envoyeur != nil {
		codesSMS, err = codes.Nouveau(codes.Options{
			Canal: "SMS", Envoyeur: integrationSMS.Envoyeur,
			Empreinteur: empreinteur, Doublons: tp, Sessions: sessions,
			Limiteur: limiteEmpreinte, LimiteurCanal: antifraude.NouvelleLimiteEmpreinte(cfg.SMSParEmpreinte24h, nil),
			Disjoncteur: coupeSMS, Surveillance: surveillance, Prefixes: cfg.PrefixesAutorises,
			Duree: cfg.CodeDuree, Essais: cfg.SMSCodeEssais, Journal: journal, Ouverte: ouverte,
		})
		if err != nil {
			return err
		}
	}
	srv, err := web.Nouveau(web.Options{
		Config: cfg, Catalogues: catalogues, Publications: b, JournalPublic: jp, Ancrages: b, Observatoire: obs,
		Version: version.Version(), SHA256: sha, Journal: journal,
		Empreinteur: empreinteur, Tampon: tp, Sessions: sessions, Questionnaire: q,
		Equipe: espaceEquipe, WhatsApp: integrationWhatsApp, Verificateur: verificateur,
		// Captcha ALTCHA ; la limite par adresse (DIGUEE_IP_PAR_HEURE) est
		// construite par le serveur web.
		Altcha: captcha, Codes: codesWhatsApp, SMS: integrationSMS, CodesSMS: codesSMS,
	})
	if err != nil {
		return err
	}

	// HTTP simple (développement) ou HTTPS avec un certificat Let's
	// Encrypt (production, ADR 0007) : le TLS s'arrête sur ce serveur.
	lancement, err := construireServeurs(cfg, srv, journal)
	if err != nil {
		return err
	}
	erreurs, err := lancement.demarrer()
	if err != nil {
		return err
	}
	journal.Info("serveur démarré", "adresse", lancement.adresse, "tls", cfg.TLS.Mode, "env", cfg.Env,
		"version", version.Version(), "sha256", sha)

	var errServeur error
	select {
	case errServeur = <-erreurs:
	case <-ctx.Done():
		journal.Info("arrêt demandé")
	}
	fin, annuler := context.WithTimeout(context.Background(), 20*time.Second)
	defer annuler()
	if err := lancement.arreter(fin); err != nil && !errors.Is(err, context.DeadlineExceeded) && errServeur == nil {
		errServeur = err
	}
	// Plus aucune requête en cours : le tampon est écrit, même incomplet.
	attente := tp.Attente()
	if err := tp.Fermer(fin); err != nil {
		return errors.Join(errServeur, fmt.Errorf("écriture du tampon à l'arrêt (%d envois perdus) : %w", attente, err))
	}
	journal.Info("tampon écrit à l'arrêt", "envois", attente)
	arreter()
	<-planifiee
	<-ancree
	<-fermee
	return errServeur
}

// publier lance à la main la publication quotidienne due ; sans effet si
// elle existe déjà (celle du serveur, par exemple).
func publier(ctx context.Context, pl *publication.Planificateur, sortie io.Writer) error {
	r, err := pl.Executer(ctx)
	if err != nil {
		return err
	}
	p := r.Publication
	etat := "déjà faite, inchangée"
	if r.Nouvelle {
		etat = "créée et inscrite au journal public"
	}
	detail := "compteur seul"
	if p.ResultatsPublies {
		detail = "résultats détaillés"
	}
	fmt.Fprintf(sortie, "publication du %s : %s (%d réponses, %s)\nsha256 de l'instantané %s\n",
		p.Jour.Format(time.DateOnly), etat, p.Reponses, detail, p.Empreinte)
	return nil
}

// ancrer lance à la main l'ancrage du jour dû ; sans effet sur ce qui est
// déjà ancré. Une erreur signale un service qui n'a pas répondu.
func ancrer(ctx context.Context, a *ancrage.Ancreur, sortie io.Writer) error {
	bilan, err := a.Executer(ctx)
	jour := bilan.Jour.Format(time.DateOnly)
	if p := bilan.Preuve; p != nil {
		etat := "déjà obtenue"
		if bilan.Nouvelle {
			etat = "obtenue"
		}
		fmt.Fprintf(sortie, "ancrage du %s : preuve OpenTimestamps %s (entrée %d, %d calendrier(s))\nempreinte %s\n",
			jour, etat, p.Numero, p.Calendriers, p.Empreinte)
	}
	for _, ar := range bilan.Archives {
		fmt.Fprintf(sortie, "archive.org /%s : %s\n", ar.Cible, ar.Lien)
	}
	if bilan.MisesANiveau > 0 {
		fmt.Fprintf(sortie, "%d preuve(s) complétée(s) d'une attestation Bitcoin\n", bilan.MisesANiveau)
	}
	return err
}

// lancement réunit le ou les serveurs HTTP du mode choisi.
type lancement struct {
	adresse  string
	demarrer func() (<-chan error, error)
	arreter  func(context.Context) error
}

// construireServeurs prépare le serveur HTTP simple (DIGUEE_TLS=non) ou les
// serveurs HTTPS et HTTP du mode ACME.
func construireServeurs(cfg *config.Config, site http.Handler, journal *slog.Logger) (*lancement, error) {
	if cfg.TLS.Mode == "acme" {
		s, err := https.Construire(https.Options{
			URLPublique: cfg.URLPublique, Cache: cfg.TLS.Cache, Annuaire: cfg.TLS.Annuaire, Courriel: cfg.TLS.Courriel,
			AdresseHTTPS: cfg.TLS.AdresseHTTPS, AdresseHTTP: cfg.TLS.AdresseHTTP, Journal: journal,
		}, site)
		if err != nil {
			return nil, err
		}
		return &lancement{adresse: cfg.TLS.AdresseHTTPS + " " + cfg.TLS.AdresseHTTP, demarrer: s.Lancer, arreter: s.Arreter}, nil
	}
	serveur := &http.Server{
		Addr:              cfg.Adresse,
		Handler:           site,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		// Les erreurs internes de net/http citent l'adresse IP du client : elles
		// ne sont pas journalisées (spec E6).
		ErrorLog: log.New(io.Discard, "", 0),
	}
	return &lancement{
		adresse: cfg.Adresse,
		demarrer: func() (<-chan error, error) {
			erreurs := make(chan error, 1)
			go func() { erreurs <- serveur.ListenAndServe() }()
			return erreurs, nil
		},
		arreter: serveur.Shutdown,
	}, nil
}

// preparerBase : en production, le serveur n'applique pas les migrations (il
// n'a pas les droits du propriétaire) ; il vérifie que le schéma est à jour
// et refuse de démarrer si son rôle est propriétaire, superutilisateur ou a
// des droits de trop (ticket #7). En développement, il applique les
// migrations, avec DIGUEE_POSTGRES_URL_MIGRATIONS si elle est donnée.
func preparerBase(ctx context.Context, cfg *config.Config, b *base.Base, journal *slog.Logger) error {
	if cfg.Env == config.Production {
		if err := b.VerifierSchemaAJour(ctx); err != nil {
			return err
		}
		return b.VerifierRoleApplication(ctx)
	}
	if cfg.PostgresURLMigrations == "" || cfg.PostgresURLMigrations == cfg.PostgresURL {
		return appliquerMigrations(ctx, b, false, journal)
	}
	return migrer(ctx, cfg, journal)
}

// migrer applique les migrations avec le rôle propriétaire
// (DIGUEE_POSTGRES_URL_MIGRATIONS, obligatoire en production), puis accorde
// au rôle de l'application exactement ses droits.
func migrer(ctx context.Context, cfg *config.Config, journal *slog.Logger) error {
	url, nom := cfg.PostgresURLMigrations, "DIGUEE_POSTGRES_URL_MIGRATIONS"
	if url == "" {
		if cfg.Env == config.Production {
			return errors.New("DIGUEE_POSTGRES_URL_MIGRATIONS (rôle propriétaire) est obligatoire pour diguee migrer en production")
		}
		url, nom = cfg.PostgresURL, "DIGUEE_POSTGRES_URL"
	}
	b, err := base.OuvrirVariable(ctx, url, nom)
	if err != nil {
		return err
	}
	defer b.Fermer()
	return appliquerMigrations(ctx, b, cfg.Env == config.Production, journal)
}

// appliquerMigrations migre puis accorde les droits de l'application. Sans
// le rôle base.RoleApplication, les droits ne sont pas accordés : refus en
// production, rien en développement (base locale à un seul rôle).
func appliquerMigrations(ctx context.Context, b *base.Base, exigerRole bool, journal *slog.Logger) error {
	appliquees, err := b.Migrer(ctx)
	if err != nil {
		return fmt.Errorf("migrations : %w", err)
	}
	for _, m := range appliquees {
		journal.Info("migration appliquée", "migration", m)
	}
	existe, err := b.RoleExiste(ctx, base.RoleApplication)
	if err != nil {
		return err
	}
	if !existe {
		if exigerRole {
			return fmt.Errorf("%w : %s (voir deploiement/postgresql/roles.sql)", base.ErrRoleAbsent, base.RoleApplication)
		}
		return nil
	}
	if err := b.AccorderDroits(ctx, base.RoleApplication); err != nil {
		return err
	}
	journal.Info("droits de l'application accordés", "role", base.RoleApplication)
	return nil
}
