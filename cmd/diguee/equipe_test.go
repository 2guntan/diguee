package main

import (
	"bytes"
	"context"
	"encoding/base32"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/base/basetest"
	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/equipe"
	"github.com/2guntan/diguee/internal/integration"
	"github.com/2guntan/diguee/internal/totp"
)

// En production, la clé de développement des secrets TOTP est refusée.
func TestProductionRefuseLaCleTOTPFactice(t *testing.T) {
	valeursCompletes(t)
	t.Setenv("DIGUEE_ENV", "production")
	t.Setenv("DIGUEE_POSTGRES_URL", "postgres://personne@127.0.0.1:1/aucune")
	msg := "l'intégration chiffrement_totp est en mode fake (réglage : DIGUEE_CLE_TOTP)"
	if err := lancer([]string{"creer-membre"}, silencieux()); err == nil || !strings.Contains(err.Error(), msg) {
		t.Fatalf("refus attendu : %v", err)
	}
	t.Setenv("DIGUEE_CLE_TOTP", strings.Repeat("ab", 32))
	if err := lancer(nil, silencieux()); err == nil || strings.Contains(err.Error(), "chiffrement_totp") {
		t.Fatalf("avec une clé, seul le reste doit bloquer : %v", err)
	}
}

// La commande crée le premier administrateur et affiche une seule fois des
// accès qui permettent de se connecter.
func TestCommandeCreerMembre(t *testing.T) {
	b := basetest.Ouvrir(t)
	ch, err := equipe.ConstruireChiffreur(&config.Config{}, &integration.Registre{})
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 12, 1, 10, 0, 0, 0, time.UTC)
	maintenant := t0
	horloge := func() time.Time { return maintenant }
	s := equipe.NouveauService(b, ch, equipe.NouvellesSessions(horloge), horloge)

	var out bytes.Buffer
	if err := creerMembre(context.Background(), s, []string{"-identifiant", "Awa.Diop", "-nom", "Awa Diop"}, &out); err != nil {
		t.Fatal(err)
	}
	mdp := regexp.MustCompile(`Mot de passe  : (\S+)`).FindStringSubmatch(out.String())
	sec := regexp.MustCompile(`Secret TOTP   : ([A-Z2-7]+)`).FindStringSubmatch(out.String())
	if mdp == nil || sec == nil || !strings.Contains(out.String(), "otpauth://totp/Diguee:awa.diop?") ||
		!strings.Contains(out.String(), "rôles : administrateur") {
		t.Fatalf("sortie :\n%s", out.String())
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(sec[1])
	if err != nil {
		t.Fatal(err)
	}
	maintenant = t0.Add(totp.Periode)
	m, err := s.Connecter(context.Background(), "awa.diop", mdp[1], totp.Code(secret, totp.Pas(maintenant)), "192.0.2.1:1")
	if err != nil || !m.A(equipe.Administrateur) {
		t.Fatalf("connexion du premier administrateur : %v %+v", err, m)
	}
	as, err := b.ListerAudit(context.Background(), 10)
	if err != nil || len(as) != 2 || as[1].Type != equipe.ActionCreationCommande || as[1].Acteur != "" {
		t.Errorf("audit : %+v %v", as, err)
	}

	for _, args := range [][]string{
		{"-identifiant", "awa.diop", "-nom", "Autre"},           // déjà pris
		{"-nom", "Sans identifiant"},                            // identifiant manquant
		{"-identifiant", "x2", "-nom", "Y", "-roles", "chef"},   // rôle inconnu
		{"-identifiant", "x3", "-nom", "Y", "argument-en-trop"}, // argument inattendu
	} {
		out.Reset()
		if err := creerMembre(context.Background(), s, args, &out); err == nil {
			t.Errorf("%v accepté", args)
		}
	}
	out.Reset()
	if err := creerMembre(context.Background(), s, []string{"-identifiant", "moussa", "-nom", "Moussa", "-roles", "redacteur, relecteur"}, &out); err != nil ||
		!strings.Contains(out.String(), "rôles : redacteur, relecteur") {
		t.Errorf("rôles multiples : %v\n%s", err, out.String())
	}
}
