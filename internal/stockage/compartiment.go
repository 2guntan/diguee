package stockage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Objet décrit un objet d'un Compartiment.
type Objet struct {
	Cle     string
	Taille  int64
	Modifie time.Time
}

// Compartiment range des objets sous des clés choisies par l'appelant. C'est
// l'usage des sauvegardes de la base (ticket #15) : datées, listées, et
// supprimées au bout de 30 jours ou à la Clôture, à la différence des
// archives (rangées sous leur SHA-256, jamais supprimées).
//
// Implémenté par S3 (Object Storage d'OVHcloud) et Dossier (développement).
type Compartiment interface {
	// Envoyer dépose taille octets lus dans contenu sous la clé ; empreinte
	// est leur SHA-256, signé avec la requête (le serveur refuse un contenu
	// altéré en route). Un objet existant sous la même clé est remplacé.
	Envoyer(ctx context.Context, cle string, contenu io.ReadSeeker, taille int64, empreinte [sha256.Size]byte) error
	// Recuperer écrit le contenu de l'objet dans w ; ErrIntrouvable s'il
	// n'existe pas.
	Recuperer(ctx context.Context, cle string, w io.Writer) error
	// Lister renvoie tous les objets dont la clé commence par prefixe,
	// triés par clé.
	Lister(ctx context.Context, prefixe string) ([]Objet, error)
	// Supprimer supprime l'objet ; sans effet s'il n'existe pas.
	Supprimer(ctx context.Context, cle string) error
}

// motifCle limite les clés à des caractères sans ambiguïté, ni en chemin de
// fichier ni en URL.
var motifCle = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,511}$`)

// VerifierCle refuse une clé vide, absolue, avec « .. » ou des caractères
// hors de [A-Za-z0-9._/-].
func VerifierCle(cle string) error {
	if !motifCle.MatchString(cle) || strings.Contains(cle, "..") || strings.Contains(cle, "//") {
		return fmt.Errorf("stockage : clé %q refusée", cle)
	}
	return nil
}

// verifierPrefixe accepte le préfixe vide.
func verifierPrefixe(p string) error {
	if p == "" {
		return nil
	}
	return VerifierCle(p)
}

// ---------------------------------------------------------------- S3

func (s *S3) adresseCle(cle string) string {
	return strings.TrimRight(s.PointAcces, "/") + "/" + s.Compartiment + "/" + cle
}

func (s *S3) adresseCompartiment() string {
	return strings.TrimRight(s.PointAcces, "/") + "/" + s.Compartiment
}

// Envoyer implémente Compartiment : PUT de l'objet en une fois (5 Gio au
// plus pour S3), corps signé.
func (s *S3) Envoyer(ctx context.Context, cle string, contenu io.ReadSeeker, taille int64, empreinte [sha256.Size]byte) error {
	if err := VerifierCle(cle); err != nil {
		return err
	}
	if _, err := contenu.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("stockage S3 : %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.adresseCle(cle), io.NopCloser(io.LimitReader(contenu, taille)))
	if err != nil {
		return fmt.Errorf("stockage S3 : %w", err)
	}
	req.ContentLength = taille
	if taille == 0 {
		req.Body = http.NoBody
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	s.signer(req, hex.EncodeToString(empreinte[:]))
	res, err := s.client().Do(req)
	if err != nil {
		return fmt.Errorf("stockage S3 : envoi : %w", nettoyer(err))
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return erreurS3("envoi", res)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
	return nil
}

// Recuperer implémente Compartiment : GET de l'objet, écrit dans w.
func (s *S3) Recuperer(ctx context.Context, cle string, w io.Writer) error {
	if err := VerifierCle(cle); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.adresseCle(cle), nil)
	if err != nil {
		return fmt.Errorf("stockage S3 : %w", err)
	}
	s.signer(req, hashVide)
	res, err := s.client().Do(req)
	if err != nil {
		return fmt.Errorf("stockage S3 : récupération : %w", nettoyer(err))
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return ErrIntrouvable
	default:
		return erreurS3("récupération", res)
	}
	if _, err := io.Copy(w, res.Body); err != nil {
		return fmt.Errorf("stockage S3 : récupération : %w", nettoyer(err))
	}
	return nil
}

// listeS3 est la réponse de ListObjectsV2.
type listeS3 struct {
	Tronquee bool   `xml:"IsTruncated"`
	Suite    string `xml:"NextContinuationToken"`
	Contenus []struct {
		Cle     string `xml:"Key"`
		Taille  int64  `xml:"Size"`
		Modifie string `xml:"LastModified"`
	} `xml:"Contents"`
}

// Lister implémente Compartiment : ListObjectsV2, page par page.
func (s *S3) Lister(ctx context.Context, prefixe string) ([]Objet, error) {
	if err := verifierPrefixe(prefixe); err != nil {
		return nil, err
	}
	var objets []Objet
	suite := ""
	for page := 0; ; page++ {
		if page > 10000 {
			return nil, errors.New("stockage S3 : liste : trop de pages")
		}
		q := "list-type=2&prefix=" + encoderURI(prefixe, true)
		if suite != "" {
			q += "&continuation-token=" + encoderURI(suite, true)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.adresseCompartiment()+"?"+q, nil)
		if err != nil {
			return nil, fmt.Errorf("stockage S3 : %w", err)
		}
		s.signer(req, hashVide)
		res, err := s.client().Do(req)
		if err != nil {
			return nil, fmt.Errorf("stockage S3 : liste : %w", nettoyer(err))
		}
		if res.StatusCode != http.StatusOK {
			err := erreurS3("liste", res)
			res.Body.Close()
			return nil, err
		}
		var l listeS3
		err = xml.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&l)
		res.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("stockage S3 : liste : réponse illisible : %w", err)
		}
		for _, c := range l.Contenus {
			m, err := time.Parse(time.RFC3339Nano, c.Modifie)
			if err != nil {
				return nil, fmt.Errorf("stockage S3 : liste : date %q illisible", c.Modifie)
			}
			objets = append(objets, Objet{Cle: c.Cle, Taille: c.Taille, Modifie: m.UTC()})
		}
		if !l.Tronquee {
			break
		}
		if l.Suite == "" || l.Suite == suite {
			return nil, errors.New("stockage S3 : liste tronquée sans jeton de suite")
		}
		suite = l.Suite
	}
	slices.SortFunc(objets, func(a, b Objet) int { return strings.Compare(a.Cle, b.Cle) })
	return objets, nil
}

// Supprimer implémente Compartiment : DELETE de l'objet (204 attendu ; S3
// répond de même pour un objet absent).
func (s *S3) Supprimer(ctx context.Context, cle string) error {
	if err := VerifierCle(cle); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.adresseCle(cle), nil)
	if err != nil {
		return fmt.Errorf("stockage S3 : %w", err)
	}
	s.signer(req, hashVide)
	res, err := s.client().Do(req)
	if err != nil {
		return fmt.Errorf("stockage S3 : suppression : %w", nettoyer(err))
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusNoContent, http.StatusOK, http.StatusNotFound:
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
		return nil
	}
	return erreurS3("suppression", res)
}

// Versionnage renvoie l'état du versionnage du bucket (GetBucketVersioning) :
// "" s'il n'a jamais été activé, sinon "Enabled" ou "Suspended". Avec le
// versionnage, un objet supprimé reste lisible dans ses versions : la purge
// des sauvegardes à la Clôture ne serait pas complète.
func (s *S3) Versionnage(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.adresseCompartiment()+"?versioning=", nil)
	if err != nil {
		return "", fmt.Errorf("stockage S3 : %w", err)
	}
	s.signer(req, hashVide)
	res, err := s.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("stockage S3 : versionnage : %w", nettoyer(err))
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", erreurS3("versionnage", res)
	}
	var v struct {
		Etat string `xml:"Status"`
	}
	if err := xml.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&v); err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("stockage S3 : versionnage : réponse illisible : %w", err)
	}
	return v.Etat, nil
}

// ---------------------------------------------------------------- Dossier

func (d *Dossier) fichierCle(cle string) string {
	return filepath.Join(d.Chemin, filepath.FromSlash(cle))
}

// Envoyer implémente Compartiment : fichier temporaire, puis renommage.
func (d *Dossier) Envoyer(_ context.Context, cle string, contenu io.ReadSeeker, taille int64, empreinte [sha256.Size]byte) error {
	if err := VerifierCle(cle); err != nil {
		return err
	}
	cible := d.fichierCle(cle)
	if err := os.MkdirAll(filepath.Dir(cible), 0o700); err != nil {
		return fmt.Errorf("stockage : %w", err)
	}
	if _, err := contenu.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("stockage : %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(cible), ".depot-*")
	if err != nil {
		return fmt.Errorf("stockage : %w", err)
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(contenu, taille))
	if err == nil && (n != taille || [sha256.Size]byte(h.Sum(nil)) != empreinte) {
		err = errors.New("le contenu ne correspond pas à sa taille ou à son SHA-256")
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), cible)
	}
	if err != nil {
		return fmt.Errorf("stockage : %w", err)
	}
	return nil
}

// Recuperer implémente Compartiment.
func (d *Dossier) Recuperer(_ context.Context, cle string, w io.Writer) error {
	if err := VerifierCle(cle); err != nil {
		return err
	}
	f, err := os.Open(d.fichierCle(cle))
	if errors.Is(err, fs.ErrNotExist) {
		return ErrIntrouvable
	}
	if err != nil {
		return fmt.Errorf("stockage : %w", err)
	}
	defer f.Close()
	if _, err := io.Copy(w, f); err != nil {
		return fmt.Errorf("stockage : %w", err)
	}
	return nil
}

// Lister implémente Compartiment (les fichiers temporaires sont ignorés).
func (d *Dossier) Lister(_ context.Context, prefixe string) ([]Objet, error) {
	if err := verifierPrefixe(prefixe); err != nil {
		return nil, err
	}
	var objets []Objet
	err := filepath.WalkDir(d.Chemin, func(chemin string, e fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && chemin == d.Chemin {
				return fs.SkipAll
			}
			return err
		}
		if e.IsDir() || strings.HasPrefix(e.Name(), ".depot-") {
			return nil
		}
		rel, err := filepath.Rel(d.Chemin, chemin)
		if err != nil {
			return err
		}
		cle := filepath.ToSlash(rel)
		if !strings.HasPrefix(cle, prefixe) {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		objets = append(objets, Objet{Cle: cle, Taille: info.Size(), Modifie: info.ModTime().UTC()})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("stockage : %w", err)
	}
	slices.SortFunc(objets, func(a, b Objet) int { return strings.Compare(a.Cle, b.Cle) })
	return objets, nil
}

// Supprimer implémente Compartiment.
func (d *Dossier) Supprimer(_ context.Context, cle string) error {
	if err := VerifierCle(cle); err != nil {
		return err
	}
	if err := os.Remove(d.fichierCle(cle)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stockage : %w", err)
	}
	return nil
}
