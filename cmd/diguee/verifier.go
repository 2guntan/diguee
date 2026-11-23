package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/2guntan/diguee/internal/journal"
)

// tailleMaxExport borne la lecture d'un export (fichier ou adresse).
const tailleMaxExport = 256 << 20

// listeEmpreintes reçoit les options -attendu, répétables.
type listeEmpreintes []journal.Empreinte

func (l *listeEmpreintes) String() string { return fmt.Sprint(*l) }

func (l *listeEmpreintes) Set(s string) error {
	e, err := journal.LireEmpreinte(strings.ToLower(strings.TrimSpace(s)))
	if err != nil {
		return err
	}
	*l = append(*l, e)
	return nil
}

// verifierJournal implémente « diguee verifier-journal » : il ne lit ni la
// configuration ni la base, et sert à n'importe qui.
//
//	diguee verifier-journal [-attendu EMPREINTE]... [-precedent ANCIEN] <fichier.json|url>
func verifierJournal(ctx context.Context, args []string, sortie io.Writer) error {
	fs := flag.NewFlagSet("verifier-journal", flag.ContinueOnError)
	fs.SetOutput(sortie)
	var attendues listeEmpreintes
	fs.Var(&attendues, "attendu", "empreinte publiée hors de nos serveurs, qui doit figurer dans le journal (répétable)")
	precedent := fs.String("precedent", "", "export téléchargé plus tôt (fichier ou url), qui doit être le début exact du journal")
	fs.Usage = func() {
		fmt.Fprintln(sortie, "usage : diguee verifier-journal [-attendu EMPREINTE]... [-precedent ANCIEN] <fichier.json|url>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("un fichier ou une adresse du journal est attendu")
	}
	es, rap, err := lireExport(ctx, fs.Arg(0))
	if err != nil {
		return fmt.Errorf("journal NON VALIDE : %w", err)
	}
	if *precedent != "" {
		ancien, _, err := lireExport(ctx, *precedent)
		if err != nil {
			return fmt.Errorf("export précédent : %w", err)
		}
		if err := journal.VerifierPrefixe(ancien, es); err != nil {
			return fmt.Errorf("journal NON VALIDE : %w", err)
		}
		fmt.Fprintf(sortie, "les %d entrées de l'export précédent sont inchangées\n", len(ancien))
	}
	for _, a := range attendues {
		if !journal.Contient(es, a) {
			return fmt.Errorf("journal NON VALIDE : l'empreinte %s n'y figure pas (entrée réécrite ou retirée)", a)
		}
		fmt.Fprintf(sortie, "empreinte %s trouvée\n", a)
	}
	pluriel := "s"
	if rap.Entrees < 2 {
		pluriel = ""
	}
	fmt.Fprintf(sortie, "journal valide : %d entrée%s\n", rap.Entrees, pluriel)
	if rap.Entrees > 0 {
		fmt.Fprintf(sortie, "empreinte de tête : %s\n", rap.Tete)
	}
	return nil
}

// lireExport lit un export depuis un fichier ou une adresse http(s).
func lireExport(ctx context.Context, source string) ([]journal.Entree, journal.Rapport, error) {
	var r io.Reader
	if strings.HasPrefix(source, "https://") || strings.HasPrefix(source, "http://") {
		ctx, annuler := context.WithTimeout(ctx, 2*time.Minute)
		defer annuler()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return nil, journal.Rapport{}, err
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, journal.Rapport{}, err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return nil, journal.Rapport{}, fmt.Errorf("%s : statut %d", source, res.StatusCode)
		}
		r = res.Body
	} else {
		f, err := os.Open(source)
		if err != nil {
			return nil, journal.Rapport{}, err
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, tailleMaxExport+1))
	if err != nil {
		return nil, journal.Rapport{}, err
	}
	if len(b) > tailleMaxExport {
		return nil, journal.Rapport{}, fmt.Errorf("%s : plus de %d octets", source, tailleMaxExport)
	}
	return journal.LireExport(bytes.NewReader(b))
}
