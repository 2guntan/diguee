package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/empreinte"
)

// cleVolume gère la clé du volume chiffré (LUKS) des données PostgreSQL,
// chiffrée par le KMS d'OVHcloud (ticket #7, deploiement/README.md) :
//
//	diguee cle-volume creer -sortie FICHIER
//	    tire une clé de 64 octets, la fait chiffrer par le KMS, vérifie le
//	    déchiffrement, et écrit seulement le texte chiffré dans FICHIER
//	    (qui ne doit pas exister) ; la clé en clair n'est jamais écrite ;
//	diguee cle-volume dechiffrer -entree FICHIER [-essais N]
//	    fait déchiffrer le texte chiffré par le KMS et écrit la clé brute sur
//	    la sortie standard, qui doit être un tube (vers cryptsetup), jamais
//	    un terminal ; réessaie N fois à délai croissant (réseau pas encore
//	    prêt au démarrage).
//
// Variables : DIGUEE_OVH_KMS_POINT_ACCES, DIGUEE_OVH_KMS_ID,
// DIGUEE_OVH_KMS_VOLUME_CLE_ID, DIGUEE_OVH_KMS_VOLUME_CERTIFICAT,
// DIGUEE_OVH_KMS_VOLUME_CLE_CLIENT, et facultatives DIGUEE_OVH_KMS_AC,
// DIGUEE_KMS_DELAI. Rien d'autre de la configuration n'est lu.
func cleVolume(ctx context.Context, args []string, getenv func(string) string, sortie, messages io.Writer) error {
	if len(args) == 0 {
		return errors.New("sous-commande attendue : creer ou dechiffrer")
	}
	fs := flag.NewFlagSet("cle-volume "+args[0], flag.ContinueOnError)
	fs.SetOutput(messages)
	fichierSortie := fs.String("sortie", "", "fichier où écrire le texte chiffré (creer)")
	fichierEntree := fs.String("entree", "", "fichier du texte chiffré (dechiffrer)")
	essais := fs.Int("essais", 10, "nombre d'essais auprès du KMS (dechiffrer)")
	pause := fs.Duration("pause", 2*time.Second, "pause avant le deuxième essai, doublée ensuite jusqu'à 30 s (dechiffrer)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("argument inattendu : %s", fs.Arg(0))
	}
	v, err := config.ChargerVolume(getenv)
	if err != nil {
		return fmt.Errorf("configuration :\n%w", err)
	}
	tlsCfg, err := empreinte.ChargerCertificat(v.Certificat, v.CleClient, v.AC)
	if err != nil {
		return fmt.Errorf("KMS OVH (DIGUEE_OVH_KMS_VOLUME_CERTIFICAT, DIGUEE_OVH_KMS_VOLUME_CLE_CLIENT, DIGUEE_OVH_KMS_AC) : %w", err)
	}
	kms, err := empreinte.NouvelleCleVolume(empreinte.OptionsCleVolume{
		PointAcces: v.PointAcces, KMSID: v.ID, CleID: v.CleID, TLS: tlsCfg, Delai: v.Delai,
	})
	if err != nil {
		return err
	}

	switch args[0] {
	case "creer":
		if *fichierSortie == "" {
			return errors.New("-sortie est obligatoire")
		}
		cle := make([]byte, empreinte.TailleCleVolume)
		if _, err := rand.Read(cle); err != nil {
			return err
		}
		defer clear(cle)
		chiffre, err := kms.VerifierAllerRetour(ctx, cle)
		if err != nil {
			return err
		}
		if err := ecrireExclusif(*fichierSortie, []byte(chiffre+"\n")); err != nil {
			return err
		}
		fmt.Fprintf(messages, "clé du volume tirée, chiffrée par le KMS et vérifiée : texte chiffré dans %s\n", *fichierSortie)
		return nil

	case "dechiffrer":
		if *fichierEntree == "" {
			return errors.New("-entree est obligatoire")
		}
		if f, ok := sortie.(*os.File); ok {
			if st, err := f.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 {
				return errors.New("la sortie standard est un terminal : rediriger vers cryptsetup (--key-file=-)")
			}
		}
		brut, err := os.ReadFile(*fichierEntree)
		if err != nil {
			return err
		}
		var cle []byte
		attente := *pause
		for i := 1; ; i++ {
			cle, err = kms.Dechiffrer(ctx, string(brut))
			if err == nil || i >= max(1, *essais) || !errors.Is(err, empreinte.ErrKMS) {
				break
			}
			fmt.Fprintf(messages, "essai %d/%d : %v ; nouvel essai dans %s\n", i, *essais, err, attente)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(attente):
			}
			attente = min(2*attente, 30*time.Second)
		}
		if err != nil {
			return err
		}
		defer clear(cle)
		_, err = sortie.Write(cle)
		return err
	}
	return fmt.Errorf("sous-commande inconnue : %s (creer ou dechiffrer)", args[0])
}

// ecrireExclusif écrit un nouveau fichier (0600), refusé s'il existe déjà :
// remplacer le texte chiffré d'un volume existant le rendrait illisible.
func ecrireExclusif(chemin string, contenu []byte) error {
	f, err := os.OpenFile(chemin, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s existe déjà : refus de le remplacer", chemin)
		}
		return err
	}
	if _, err := f.Write(contenu); err != nil {
		f.Close()
		os.Remove(chemin)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if d, err := os.Open(filepath.Dir(chemin)); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}
