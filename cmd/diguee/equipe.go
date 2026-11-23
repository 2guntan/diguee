package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/2guntan/diguee/internal/equipe"
)

// creerMembre crée un compte de l'espace équipe depuis le serveur : c'est
// ainsi qu'est créé le premier administrateur. Le mot de passe et le secret
// TOTP sont écrits une seule fois sur out (la sortie standard), jamais dans
// le journal ; l'action est inscrite à l'audit interne.
func creerMembre(ctx context.Context, s *equipe.Service, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("creer-membre", flag.ContinueOnError)
	fs.SetOutput(out)
	identifiant := fs.String("identifiant", "", "identifiant de connexion, par exemple awa.diop")
	nom := fs.String("nom", "", "nom complet de la personne")
	roles := fs.String("roles", string(equipe.Administrateur),
		"rôles séparés par des virgules : redacteur, relecteur, administrateur")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("creer-membre : argument inattendu %q", fs.Arg(0))
	}
	var rs []equipe.Role
	for _, r := range strings.Split(*roles, ",") {
		if r = strings.TrimSpace(r); r != "" {
			rs = append(rs, equipe.Role(r))
		}
	}
	m, acces, err := s.Creer(ctx, nil, *identifiant, *nom, rs)
	if err != nil {
		return fmt.Errorf("creer-membre : %w", err)
	}
	var noms []string
	for _, r := range m.Roles {
		noms = append(noms, string(r))
	}
	fmt.Fprintf(out, `Compte créé : %s (%s), rôles : %s

Ces informations ne s'affichent qu'une seule fois. Elles ne sont conservées
nulle part en clair : notez-les maintenant, hors de tout fichier partagé.

  Identifiant   : %s
  Mot de passe  : %s
  Secret TOTP   : %s
  URI otpauth   : %s

Enregistrez le secret dans une application d'authentification (TOTP, SHA-1,
6 chiffres, 30 secondes), en saisie manuelle ou par un QR code fait hors
ligne à partir de l'URI. Connexion : https://consultation.diguee.sn/equipe/
`, m.Nom, m.Identifiant, strings.Join(noms, ", "), acces.Identifiant, acces.MotDePasse, acces.Secret, acces.URI)
	return nil
}
