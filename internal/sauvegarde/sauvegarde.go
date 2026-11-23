// Package sauvegarde fait les sauvegardes nocturnes de la base (ticket #15,
// spec §11) : un pg_dump au format « custom », chiffré par age vers une clé
// publique X25519 avant de quitter le serveur, envoyé dans un compartiment
// Object Storage dédié et conservé 30 jours.
//
// Le chiffrement se fait en flux : pg_dump écrit dans un tube lu par age, et
// seul le texte chiffré est écrit sur le disque (un fichier temporaire, le
// temps de calculer son SHA-256 et de l'envoyer). Le dump en clair n'est
// jamais écrit. La clé privée n'est jamais sur le serveur : seul le porteur
// peut déchiffrer une sauvegarde (deploiement/restaurer-sauvegarde.sh).
//
// Les sauvegardes contiennent la table participation, donc les empreintes :
// à la Clôture (spec §12, ADR 0002), SupprimerToutes les efface toutes.
package sauvegarde

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/2guntan/diguee/internal/stockage"
)

// Prefixe est le préfixe des clés des sauvegardes dans le compartiment.
const Prefixe = "sauvegardes/"

// ConservationJours est la durée de conservation (spec §11) : une
// sauvegarde datée du jour J est supprimée par la première sauvegarde
// réussie du jour J+31. Ce délai n'est pas réglable : la procédure de
// Clôture (ADR 0002) en dépend.
const ConservationJours = 30

// formatDate date les sauvegardes, en UTC, à la seconde.
const formatDate = "2006-01-02T150405Z"

var motifCle = regexp.MustCompile(`^` + Prefixe + `diguee-([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{6}Z)\.pgdump\.age$`)

// Cle renvoie la clé d'une sauvegarde faite à l'instant t, par exemple
// sauvegardes/diguee-2026-11-23T023000Z.pgdump.age.
func Cle(t time.Time) string {
	return Prefixe + "diguee-" + t.UTC().Format(formatDate) + ".pgdump.age"
}

// DateDe lit la date d'une clé de sauvegarde.
func DateDe(cle string) (time.Time, bool) {
	m := motifCle.FindStringSubmatch(cle)
	if m == nil {
		return time.Time{}, false
	}
	t, err := time.Parse(formatDate, m[1])
	return t, err == nil
}

// Sauvegarde décrit une sauvegarde présente dans le compartiment.
type Sauvegarde struct {
	Cle    string
	Date   time.Time
	Taille int64
}

// Lister renvoie les sauvegardes, de la plus ancienne à la plus récente,
// et à part les autres objets rangés sous Prefixe (que la purge des 30
// jours ne touche pas, mais que SupprimerToutes supprime).
func Lister(ctx context.Context, c stockage.Compartiment) ([]Sauvegarde, []stockage.Objet, error) {
	objets, err := c.Lister(ctx, Prefixe)
	if err != nil {
		return nil, nil, err
	}
	var ss []Sauvegarde
	var autres []stockage.Objet
	for _, o := range objets {
		if d, ok := DateDe(o.Cle); ok {
			ss = append(ss, Sauvegarde{Cle: o.Cle, Date: d, Taille: o.Taille})
		} else {
			autres = append(autres, o)
		}
	}
	slices.SortFunc(ss, func(a, b Sauvegarde) int { return a.Date.Compare(b.Date) })
	return ss, autres, nil
}

// ErrAucune signale un compartiment sans sauvegarde.
var ErrAucune = errors.New("sauvegarde : aucune sauvegarde dans le compartiment")

// Derniere renvoie la sauvegarde la plus récente.
func Derniere(ctx context.Context, c stockage.Compartiment) (Sauvegarde, error) {
	ss, _, err := Lister(ctx, c)
	if err != nil {
		return Sauvegarde{}, err
	}
	if len(ss) == 0 {
		return Sauvegarde{}, ErrAucune
	}
	return ss[len(ss)-1], nil
}

// Limite renvoie la date avant laquelle une sauvegarde est trop ancienne :
// minuit UTC, ConservationJours jours avant le jour de maintenant.
func Limite(maintenant time.Time) time.Time {
	j := maintenant.UTC().Truncate(24 * time.Hour)
	return j.AddDate(0, 0, -ConservationJours)
}

// Purger supprime les sauvegardes de plus de ConservationJours jours et
// renvoie leurs clés.
func Purger(ctx context.Context, c stockage.Compartiment, maintenant time.Time) ([]string, error) {
	ss, _, err := Lister(ctx, c)
	if err != nil {
		return nil, err
	}
	limite := Limite(maintenant)
	var supprimees []string
	for _, s := range ss {
		if !s.Date.Before(limite) {
			continue
		}
		if err := c.Supprimer(ctx, s.Cle); err != nil {
			return supprimees, err
		}
		supprimees = append(supprimees, s.Cle)
	}
	return supprimees, nil
}

// versionne est implémenté par stockage.S3.
type versionne interface {
	Versionnage(ctx context.Context) (string, error)
}

// SupprimerToutes supprime tous les objets rangés sous Prefixe, puis vérifie
// que la liste est vide. C'est l'étape 4 de la Clôture (spec §12, ADR
// 0002, ticket #16) : toutes les sauvegardes contiennent les empreintes.
// Si le compartiment garde des versions (versionnage activé ou suspendu),
// les objets supprimés resteraient lisibles : SupprimerToutes supprime
// quand même ce qu'elle voit, puis renvoie une erreur.
func SupprimerToutes(ctx context.Context, c stockage.Compartiment) (int, error) {
	objets, err := c.Lister(ctx, Prefixe)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, o := range objets {
		if err := c.Supprimer(ctx, o.Cle); err != nil {
			return n, err
		}
		n++
	}
	reste, err := c.Lister(ctx, Prefixe)
	if err != nil {
		return n, err
	}
	if len(reste) > 0 {
		return n, fmt.Errorf("sauvegarde : %d objet(s) encore présent(s) après la suppression", len(reste))
	}
	if v, ok := c.(versionne); ok {
		etat, err := v.Versionnage(ctx)
		if err != nil {
			return n, fmt.Errorf("sauvegarde : %d objet(s) supprimé(s), mais l'état du versionnage du compartiment est illisible (à vérifier à la main) : %w", n, err)
		}
		if etat != "" {
			return n, fmt.Errorf("sauvegarde : %d objet(s) supprimé(s), mais le versionnage du compartiment est %q : les anciennes versions restent à supprimer à la main", n, etat)
		}
	}
	return n, nil
}

// motifDestinataire est une clé publique age X25519 (Bech32, « age1 » puis
// 58 caractères) : ni greffon, ni clé SSH.
var motifDestinataire = regexp.MustCompile(`^age1[qpzry9x8gf2tvdw0s3jn54khce6mua7l]{58}$`)

// LireDestinataire lit le fichier de la clé publique : une seule ligne
// « age1… » (les lignes vides et les commentaires « # » sont permis).
func LireDestinataire(chemin string) (string, error) {
	b, err := os.ReadFile(chemin)
	if err != nil {
		return "", fmt.Errorf("clé publique des sauvegardes : %w", err)
	}
	var cles []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		cles = append(cles, l)
	}
	if len(cles) != 1 || !motifDestinataire.MatchString(cles[0]) {
		return "", fmt.Errorf("clé publique des sauvegardes : %s doit contenir une seule clé publique age X25519 (age1…, sortie de age-keygen -y)", chemin)
	}
	return cles[0], nil
}

// EnteteAge commence tout fichier chiffré par age en binaire, suivi ici
// d'une strophe X25519.
const EnteteAge = "age-encryption.org/v1\n-> X25519 "

// Options règle Faire.
type Options struct {
	PgDump       string // programme pg_dump
	Age          string // programme age
	URL          string // base à sauvegarder (passée à pg_dump --dbname)
	Destinataire string // clé publique age X25519 (LireDestinataire)
	Compartiment stockage.Compartiment
	DossierTemps string           // dossier du fichier chiffré (os.TempDir si vide)
	Maintenant   func() time.Time // time.Now si nil
	Journal      *slog.Logger     // messages de pg_dump et d'age en cas d'échec
}

// Resultat décrit une sauvegarde réussie.
type Resultat struct {
	Cle        string
	Date       time.Time
	Taille     int64
	SHA256     string
	Supprimees []string // sauvegardes de plus de 30 jours supprimées
}

// ErrPurge signale une sauvegarde envoyée, mais une purge des anciennes
// sauvegardes en échec : Resultat est rempli, et l'échec doit être signalé.
var ErrPurge = errors.New("sauvegarde envoyée, mais la suppression des sauvegardes de plus de 30 jours a échoué")

// sortieBornee garde les premiers octets écrits (messages d'erreur).
type sortieBornee struct{ b bytes.Buffer }

func (s *sortieBornee) Write(p []byte) (int, error) {
	if r := 4096 - s.b.Len(); r > 0 {
		s.b.Write(p[:min(len(p), r)])
	}
	return len(p), nil
}

// Faire sauvegarde la base : pg_dump | age vers un fichier temporaire
// chiffré, envoi dans le compartiment, contrôle de sa présence, puis
// suppression des sauvegardes de plus de 30 jours.
func Faire(ctx context.Context, o Options) (*Resultat, error) {
	if o.Maintenant == nil {
		o.Maintenant = time.Now
	}
	if o.Journal == nil {
		o.Journal = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if !motifDestinataire.MatchString(o.Destinataire) {
		return nil, errors.New("sauvegarde : clé publique age X25519 attendue")
	}
	debut := o.Maintenant().UTC()
	cle := Cle(debut)

	f, err := os.CreateTemp(o.DossierTemps, "diguee-sauvegarde-*.age")
	if err != nil {
		return nil, fmt.Errorf("sauvegarde : fichier temporaire : %w", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()

	taille, somme, err := chiffrer(ctx, o, f)
	if err != nil {
		return nil, err
	}
	if err := f.Sync(); err != nil {
		return nil, fmt.Errorf("sauvegarde : %w", err)
	}
	// Contrôle : le fichier est bien chiffré par age, pour une clé X25519.
	entete := make([]byte, len(EnteteAge))
	if _, err := f.ReadAt(entete, 0); err != nil || string(entete) != EnteteAge {
		return nil, errors.New("sauvegarde : la sortie d'age n'a pas l'en-tête attendu (fichier non chiffré ?) : rien n'est envoyé")
	}

	if err := o.Compartiment.Envoyer(ctx, cle, f, taille, somme); err != nil {
		return nil, fmt.Errorf("sauvegarde : envoi : %w", err)
	}
	// Contrôle : l'objet est listé, à la bonne taille.
	objets, err := o.Compartiment.Lister(ctx, cle)
	if err != nil {
		return nil, fmt.Errorf("sauvegarde : contrôle après envoi : %w", err)
	}
	if len(objets) != 1 || objets[0].Cle != cle || objets[0].Taille != taille {
		return nil, fmt.Errorf("sauvegarde : contrôle après envoi : %s absente ou de taille inattendue", cle)
	}
	r := &Resultat{Cle: cle, Date: debut, Taille: taille, SHA256: hex.EncodeToString(somme[:])}
	r.Supprimees, err = Purger(ctx, o.Compartiment, o.Maintenant())
	if err != nil {
		return r, fmt.Errorf("%w : %w", ErrPurge, err)
	}
	return r, nil
}

// chiffrer lance pg_dump et age reliés par un tube, et écrit la sortie
// d'age dans f. Elle renvoie la taille et le SHA-256 du texte chiffré.
func chiffrer(ctx context.Context, o Options, f *os.File) (int64, [sha256.Size]byte, error) {
	var somme [sha256.Size]byte
	lecture, ecriture, err := os.Pipe()
	if err != nil {
		return 0, somme, fmt.Errorf("sauvegarde : %w", err)
	}
	var erreursDump, erreursAge sortieBornee
	dump := exec.CommandContext(ctx, o.PgDump, "--format=custom", "--no-password", "--dbname="+o.URL)
	dump.Stdout = ecriture
	dump.Stderr = &erreursDump
	h := sha256.New()
	compteur := &compteur{}
	chiff := exec.CommandContext(ctx, o.Age, "--encrypt", "--recipient", o.Destinataire)
	chiff.Stdin = lecture
	chiff.Stdout = io.MultiWriter(f, h, compteur)
	chiff.Stderr = &erreursAge

	if err := chiff.Start(); err != nil {
		lecture.Close()
		ecriture.Close()
		return 0, somme, fmt.Errorf("sauvegarde : lancement d'age : %w", err)
	}
	lecture.Close()
	errDump := dump.Start()
	ecriture.Close()
	if errDump != nil {
		_ = chiff.Wait()
		return 0, somme, fmt.Errorf("sauvegarde : lancement de pg_dump : %w", errDump)
	}
	errDump = dump.Wait()
	errAge := chiff.Wait()
	// age d'abord : s'il échoue, pg_dump échoue aussi (tube fermé).
	if errAge != nil {
		o.Journal.Error("sauvegarde : age a échoué", "erreur", errAge.Error(), "sortie", strings.TrimSpace(erreursAge.b.String()))
		return 0, somme, fmt.Errorf("sauvegarde : age a échoué (%v) : rien n'est envoyé", errAge)
	}
	if errDump != nil {
		o.Journal.Error("sauvegarde : pg_dump a échoué", "erreur", errDump.Error(), "sortie", strings.TrimSpace(erreursDump.b.String()))
		return 0, somme, fmt.Errorf("sauvegarde : pg_dump a échoué (%v) : rien n'est envoyé", errDump)
	}
	copy(somme[:], h.Sum(nil))
	return compteur.n, somme, nil
}

type compteur struct{ n int64 }

func (c *compteur) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }
