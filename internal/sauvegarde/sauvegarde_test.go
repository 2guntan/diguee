package sauvegarde_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/2guntan/diguee/internal/base"
	"github.com/2guntan/diguee/internal/base/basetest"
	"github.com/2guntan/diguee/internal/empreinte"
	"github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/questionnaire"
	"github.com/2guntan/diguee/internal/sauvegarde"
	"github.com/2guntan/diguee/internal/sauvegarde/restauration"
	"github.com/2guntan/diguee/internal/sauvegarde/sauvegardetest"
	"github.com/2guntan/diguee/internal/stockage"
	"github.com/2guntan/diguee/internal/stockage/fauxs3"
)

func TestCleEtDate(t *testing.T) {
	m := time.Date(2026, 11, 23, 2, 30, 5, 999, time.FixedZone("x", 3600))
	cle := sauvegarde.Cle(m)
	if cle != "sauvegardes/diguee-2026-11-23T013005Z.pgdump.age" {
		t.Fatal(cle)
	}
	if d, ok := sauvegarde.DateDe(cle); !ok || !d.Equal(m.Truncate(time.Second)) {
		t.Errorf("%v %v", d, ok)
	}
	for _, k := range []string{"sauvegardes/autre", "ailleurs/diguee-2026-11-23T013005Z.pgdump.age", "sauvegardes/diguee-2026-11-23T013005Z.pgdump"} {
		if _, ok := sauvegarde.DateDe(k); ok {
			t.Errorf("%s reconnue", k)
		}
	}
	if l := sauvegarde.Limite(m); !l.Equal(time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("limite %v", l)
	}
}

func deposer(t *testing.T, c stockage.Compartiment, cle string) {
	t.Helper()
	b := []byte(sauvegarde.EnteteAge + cle)
	if err := c.Envoyer(context.Background(), cle, bytes.NewReader(b), int64(len(b)), sha256.Sum256(b)); err != nil {
		t.Fatal(err)
	}
}

// Les sauvegardes de plus de 30 jours sont supprimées, les autres gardées ;
// les objets inattendus ne sont pas touchés par la purge, mais le sont par
// SupprimerToutes.
func TestPurgerEtSupprimerToutes(t *testing.T) {
	for nom, c := range map[string]stockage.Compartiment{
		"dossier": &stockage.Dossier{Chemin: t.TempDir()},
		"s3":      fauxs3.Nouveau(t).Client(),
	} {
		t.Run(nom, func(t *testing.T) {
			ctx := context.Background()
			maintenant := time.Date(2026, 11, 23, 2, 30, 0, 0, time.UTC)
			var gardees []string
			for j := 0; j <= 40; j++ {
				cle := sauvegarde.Cle(maintenant.AddDate(0, 0, -j).Add(-time.Minute))
				deposer(t, c, cle)
				if j <= sauvegarde.ConservationJours {
					gardees = append([]string{cle}, gardees...)
				}
			}
			deposer(t, c, "sauvegardes/note-manuelle")
			deposer(t, c, "ailleurs/diguee-2000-01-01T000000Z.pgdump.age")
			supprimees, err := sauvegarde.Purger(ctx, c, maintenant)
			if err != nil {
				t.Fatal(err)
			}
			if len(supprimees) != 10 {
				t.Errorf("%d supprimées : %v", len(supprimees), supprimees)
			}
			ss, autres, err := sauvegarde.Lister(ctx, c)
			if err != nil {
				t.Fatal(err)
			}
			var cles []string
			for _, s := range ss {
				cles = append(cles, s.Cle)
			}
			if strings.Join(cles, ",") != strings.Join(gardees, ",") {
				t.Errorf("gardées :\n%v\nattendu :\n%v", cles, gardees)
			}
			// La plus ancienne gardée a 30 jours.
			if d, _ := sauvegarde.DateDe(cles[0]); maintenant.Sub(d) < 30*24*time.Hour || maintenant.Sub(d) > 31*24*time.Hour {
				t.Errorf("plus ancienne : %v", d)
			}
			if len(autres) != 1 || autres[0].Cle != "sauvegardes/note-manuelle" {
				t.Errorf("autres %v", autres)
			}
			if d, err := sauvegarde.Derniere(ctx, c); err != nil || d.Cle != gardees[len(gardees)-1] {
				t.Errorf("dernière %v %v", d, err)
			}

			n, err := sauvegarde.SupprimerToutes(ctx, c)
			if err != nil || n != len(gardees)+1 {
				t.Fatalf("SupprimerToutes : %d %v", n, err)
			}
			if os, _ := c.Lister(ctx, sauvegarde.Prefixe); len(os) != 0 {
				t.Errorf("reste %v", os)
			}
			if os, _ := c.Lister(ctx, "ailleurs/"); len(os) != 1 {
				t.Errorf("objet hors du préfixe touché : %v", os)
			}
			if _, err := sauvegarde.Derniere(ctx, c); !errors.Is(err, sauvegarde.ErrAucune) {
				t.Errorf("compartiment vide : %v", err)
			}
		})
	}
}

// Avec le versionnage, les objets supprimés resteraient lisibles :
// SupprimerToutes le signale.
func TestSupprimerToutesVersionnage(t *testing.T) {
	f := fauxs3.Nouveau(t)
	c := f.Client()
	deposer(t, c, sauvegarde.Cle(time.Now()))
	f.Modifier(func(s *fauxs3.Serveur) { s.Versionnage = "Suspended" })
	n, err := sauvegarde.SupprimerToutes(context.Background(), c)
	if n != 1 || err == nil || !strings.Contains(err.Error(), "versionnage") {
		t.Errorf("%d %v", n, err)
	}
	if len(f.Cles()) != 0 {
		t.Error("objet non supprimé")
	}
}

func TestLireDestinataire(t *testing.T) {
	_, dest := sauvegardetest.Cles(t)
	dir := t.TempDir()
	for contenu, ok := range map[string]bool{
		dest + "\n": true,
		"# créée le 2026-11-23\n\n" + dest + "\n": true,
		"":                        false,
		dest + "\n" + dest + "\n": false,
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl x": false,
		"age1yubikey1qwt50d05nh5vutpdzmlg5wn80xq5negm4uj9ghv0snvdd3yysf5yw3rhl3t":            false,
		strings.ToUpper(dest): false,
	} {
		f := filepath.Join(dir, "cle.pub")
		os.WriteFile(f, []byte(contenu), 0o600)
		got, err := sauvegarde.LireDestinataire(f)
		if ok != (err == nil) || (ok && got != dest) {
			t.Errorf("%q : %q %v", contenu, got, err)
		}
	}
}

// fauxPgDump écrit un script qui imite pg_dump : il écrit marqueur (en
// clair) sur sa sortie, ou échoue.
func fauxPgDump(t *testing.T, marqueur string, echec bool) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "pg_dump")
	script := "#!/bin/sh\nfor i in $(seq 1 2000); do printf '%s %05d\\n' '" + marqueur + "' \"$i\"; done\n"
	if echec {
		script = "#!/bin/sh\nprintf 'PGDMP début' ; echo 'pg_dump: error: connexion refusée' >&2; exit 1\n"
	}
	if err := os.WriteFile(f, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return f
}

func dechiffrer(identite string, chiffre []byte) ([]byte, error) {
	cmd := exec.Command("age", "--decrypt", "--identity", identite)
	cmd.Stdin = bytes.NewReader(chiffre)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%v : %s", err, stderr.String())
	}
	return out, nil
}

// fichiersDe liste les fichiers d'un dossier (récursivement).
func fichiersDe(t *testing.T, dir string) []string {
	var fs_ []string
	_ = filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			fs_ = append(fs_, p)
		}
		return nil
	})
	return fs_
}

// La sauvegarde est chiffrée en flux : aucun fichier en clair n'est écrit,
// l'objet envoyé n'est lisible qu'avec la clé privée.
func TestFaireChiffreSansClairSurDisque(t *testing.T) {
	sauvegardetest.Programmes(t, "age", "age-keygen")
	identite, dest := sauvegardetest.Cles(t)
	autreIdentite, _ := sauvegardetest.Cles(t)
	const marqueur = "DONNEES-EN-CLAIR-participation-0123456789abcdef"
	temps := t.TempDir()
	f := fauxs3.Nouveau(t)
	maintenant := time.Date(2026, 11, 23, 2, 30, 0, 0, time.UTC)
	ancienne := sauvegarde.Cle(maintenant.AddDate(0, 0, -31))
	f.Deposer(ancienne, []byte("ancienne"), maintenant.AddDate(0, 0, -31))

	r, err := sauvegarde.Faire(context.Background(), sauvegarde.Options{
		PgDump: fauxPgDump(t, marqueur, false), Age: "age", URL: "postgres://inutile", Destinataire: dest,
		Compartiment: f.Client(), DossierTemps: temps, Maintenant: func() time.Time { return maintenant },
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Cle != sauvegarde.Cle(maintenant) || len(r.Supprimees) != 1 || r.Supprimees[0] != ancienne {
		t.Errorf("résultat %+v", r)
	}
	if l := fichiersDe(t, temps); len(l) != 0 {
		t.Errorf("fichiers restés dans le dossier temporaire : %v", l)
	}
	chiffre, ok := f.Objet(r.Cle)
	if !ok || int64(len(chiffre)) != r.Taille || fmt.Sprintf("%x", sha256.Sum256(chiffre)) != r.SHA256 {
		t.Fatalf("objet %v, %d octets", ok, len(chiffre))
	}
	if !bytes.HasPrefix(chiffre, []byte(sauvegarde.EnteteAge)) || bytes.Contains(chiffre, []byte("DONNEES-EN-CLAIR")) {
		t.Error("l'objet envoyé n'est pas chiffré")
	}
	if _, err := dechiffrer(autreIdentite, chiffre); err == nil {
		t.Error("déchiffré avec une autre clé privée")
	}
	clair, err := dechiffrer(identite, chiffre)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(clair, []byte(marqueur+" 00001\n")) || !bytes.HasSuffix(clair, []byte(marqueur+" 02000\n")) {
		t.Errorf("contenu déchiffré inattendu : %.80q", clair)
	}
	if strings.Join(f.Cles(), ",") != r.Cle {
		t.Errorf("objets %v", f.Cles())
	}
}

// Une sauvegarde qui échoue n'envoie rien, ne laisse aucun fichier, et
// l'échec est inscrit dans l'état (alerte dans l'espace équipe).
func TestFaireSignaleLesEchecs(t *testing.T) {
	sauvegardetest.Programmes(t, "age", "age-keygen")
	_, dest := sauvegardetest.Cles(t)
	maintenant := time.Date(2026, 11, 23, 2, 30, 0, 0, time.UTC)
	etat := filepath.Join(t.TempDir(), "etat.json")

	// Une réussite d'abord, pour l'état.
	f := fauxs3.Nouveau(t)
	ok := sauvegarde.Options{PgDump: fauxPgDump(t, "x", false), Age: "age", Destinataire: dest, Compartiment: f.Client(),
		DossierTemps: t.TempDir(), Maintenant: func() time.Time { return maintenant }}
	r, err := sauvegarde.Faire(context.Background(), ok)
	if err != nil {
		t.Fatal(err)
	}
	if err := sauvegarde.Enregistrer(etat, maintenant, r, nil); err != nil {
		t.Fatal(err)
	}
	e, err := sauvegarde.LireEtat(etat)
	if err != nil || !e.Reussie || e.EnAlerte(maintenant.Add(time.Hour)) || !e.EnAlerte(maintenant.Add(28*time.Hour)) {
		t.Fatalf("état %+v %v", e, err)
	}
	if st, _ := os.Stat(etat); st.Mode().Perm() != 0o644 {
		t.Errorf("droits de l'état %v", st.Mode())
	}

	cas := map[string]func(o *sauvegarde.Options){
		"pg_dump échoue":    func(o *sauvegarde.Options) { o.PgDump = fauxPgDump(t, "", true) },
		"pg_dump absent":    func(o *sauvegarde.Options) { o.PgDump = filepath.Join(t.TempDir(), "absent") },
		"age absent":        func(o *sauvegarde.Options) { o.Age = filepath.Join(t.TempDir(), "absent") },
		"age échoue":        func(o *sauvegarde.Options) { o.Age = "false" },
		"clé publique":      func(o *sauvegarde.Options) { o.Destinataire = "age1abc" },
		"stockage en panne": func(o *sauvegarde.Options) { f.Modifier(func(s *fauxs3.Serveur) { s.Panne = true }) },
	}
	for nom, modifier := range cas {
		t.Run(nom, func(t *testing.T) {
			f.Modifier(func(s *fauxs3.Serveur) { s.Panne = false })
			avant := f.Cles()
			o := ok
			o.DossierTemps = t.TempDir()
			o.Maintenant = func() time.Time { return maintenant.Add(24 * time.Hour) }
			modifier(&o)
			r, err := sauvegarde.Faire(context.Background(), o)
			if err == nil || r != nil {
				t.Fatalf("pas d'échec : %+v", r)
			}
			f.Modifier(func(s *fauxs3.Serveur) { s.Panne = false })
			if apres := f.Cles(); strings.Join(apres, ",") != strings.Join(avant, ",") {
				t.Errorf("objets changés : %v", apres)
			}
			if l := fichiersDe(t, o.DossierTemps); len(l) != 0 {
				t.Errorf("fichiers restés : %v", l)
			}
			if err := sauvegarde.Enregistrer(etat, maintenant.Add(24*time.Hour), r, err); err != nil {
				t.Fatal(err)
			}
			e, _ := sauvegarde.LireEtat(etat)
			if e.Reussie || e.Erreur == "" || e.DerniereReussite == nil || !e.DerniereReussite.Equal(maintenant) ||
				!e.EnAlerte(maintenant.Add(24*time.Hour)) {
				t.Errorf("état %+v", e)
			}
		})
	}
	var absent *sauvegarde.Etat
	if !absent.EnAlerte(maintenant) {
		t.Error("sans état : pas d'alerte")
	}
}

// Sauvegarde d'une vraie base avec pg_dump, puis restauration dans une base
// vide (age --decrypt | pg_restore, comme deploiement/restaurer-sauvegarde.sh)
// et contrôle : lignes et chaîne du journal public.
func TestSauvegardeEtRestaurationDUneVraieBase(t *testing.T) {
	sauvegardetest.Programmes(t)
	ctx := context.Background()
	source := basetest.NouvelleBase(t)
	b, err := base.Ouvrir(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Fermer()
	if _, err := b.Migrer(ctx); err != nil {
		t.Fatal(err)
	}
	jour := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	if err := b.EcrireLot(ctx, jour, []empreinte.Empreinte{{1}, {2}, {3}},
		[]questionnaire.Reponse{{Version: 1, Q1: 1, Q3: 2, Q7: []int16{1, 3}, Themes: []int16{1, 2, 3}, Region: 15}, {Version: 1}, {Version: 1, Q1: 2}}); err != nil {
		t.Fatal(err)
	}
	horloge := time.Date(2026, 11, 21, 6, 0, 0, 0, time.UTC)
	j := journal.Nouveau(b, func() time.Time { return horloge })
	for i := range 4 {
		horloge = horloge.Add(time.Hour)
		if _, err := j.Inscrire(ctx, journal.Methodologie, map[string]any{"rang": i}); err != nil {
			t.Fatal(err)
		}
	}

	// pg_dump avec un rôle comme diguee_sauvegarde (deploiement/postgresql/
	// role-sauvegarde.sql) : lecture seule par pg_read_all_data.
	urlSauvegarde := roleSauvegarde(t, source)
	if out, err := exec.Command("psql", "-XAtq", "-v", "ON_ERROR_STOP=1", urlSauvegarde, "-c",
		"INSERT INTO participation VALUES (decode(repeat('ab', 32), 'hex'))").CombinedOutput(); err == nil {
		t.Errorf("le rôle de la sauvegarde peut écrire : %s", out)
	}

	identite, dest := sauvegardetest.Cles(t)
	c := &stockage.Dossier{Chemin: t.TempDir()}
	o := sauvegarde.Options{PgDump: "pg_dump", Age: "age", URL: urlSauvegarde, Destinataire: dest, Compartiment: c, DossierTemps: t.TempDir(),
		Maintenant: func() time.Time { return time.Date(2026, 11, 22, 2, 30, 0, 0, time.UTC) }}
	if _, err := sauvegarde.Faire(ctx, o); err != nil {
		t.Fatal(err)
	}
	// Une entrée de plus, puis la sauvegarde du lendemain : c'est elle qui
	// doit être restaurée.
	horloge = horloge.Add(time.Hour)
	derniere, err := j.Inscrire(ctx, journal.Methodologie, map[string]any{"rang": "dernier"})
	if err != nil {
		t.Fatal(err)
	}
	o.Maintenant = func() time.Time { return time.Date(2026, 11, 23, 2, 30, 0, 0, time.UTC) }
	r, err := sauvegarde.Faire(ctx, o)
	if err != nil {
		t.Fatal(err)
	}

	d, err := sauvegarde.Derniere(ctx, c)
	if err != nil || d.Cle != r.Cle {
		t.Fatalf("dernière %v %v", d, err)
	}
	var chiffre bytes.Buffer
	if err := c.Recuperer(ctx, d.Cle, &chiffre); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(chiffre.Bytes(), []byte("PGDMP")) {
		t.Error("en-tête de pg_dump visible : sauvegarde non chiffrée")
	}
	cible := basetest.NouvelleBase(t)
	restaurer := exec.Command("sh", "-c", `age --decrypt --identity "$1" | pg_restore --no-owner --no-acl --exit-on-error --single-transaction --dbname="$2"`,
		"restaurer", identite, cible)
	restaurer.Stdin = &chiffre
	if out, err := restaurer.CombinedOutput(); err != nil {
		t.Fatalf("restauration : %v\n%s", err, out)
	}

	var export bytes.Buffer
	ctl, err := restauration.Controler(ctx, cible, &export)
	if err != nil {
		t.Fatal(err)
	}
	if ctl.Lignes("participation") != 3 || ctl.Lignes("reponse") != 3 || ctl.Lignes("journal_public") != 5 || ctl.Lignes("absente") != -1 {
		t.Errorf("tables %+v", ctl.Tables)
	}
	if ctl.Journal.Entrees != 5 || ctl.Journal.Tete != derniere.Empreinte || ctl.Migration == 0 {
		t.Errorf("journal %+v, migration %d", ctl.Journal, ctl.Migration)
	}
	es, rap, err := journal.LireExport(&export)
	if err != nil || rap.Tete != derniere.Empreinte || len(es) != 5 {
		t.Errorf("export %v %v", rap, err)
	}
	// Une base vide n'est pas une restauration réussie.
	if _, err := restauration.Controler(ctx, basetest.NouvelleBase(t), nil); err == nil || !strings.Contains(err.Error(), "tables absentes") {
		t.Errorf("base vide : %v", err)
	}
}

// roleTestSauvegarde imite diguee_sauvegarde dans la base de test.
const roleTestSauvegarde = "diguee_test_sauvegarde"

// roleSauvegarde crée (au besoin) un rôle de connexion membre de
// pg_read_all_data et de rien d'autre, lui ouvre la base (fermée à PUBLIC,
// comme en production) et renvoie l'URL de la base avec ce rôle.
func roleSauvegarde(t *testing.T, adresse string) string {
	t.Helper()
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, adresse)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "SELECT pg_advisory_lock(7015)"); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "SELECT pg_advisory_unlock(7015)")
	var existe bool
	if err := admin.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", roleTestSauvegarde).Scan(&existe); err != nil {
		t.Fatal(err)
	}
	if !existe {
		if _, err := admin.Exec(ctx, "CREATE ROLE "+roleTestSauvegarde+" LOGIN PASSWORD '"+roleTestSauvegarde+
			"' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS IN ROLE pg_read_all_data"); err != nil {
			t.Fatal(err)
		}
	}
	u, err := url.Parse(adresse)
	if err != nil {
		t.Fatal(err)
	}
	nom := pgx.Identifier{strings.TrimPrefix(u.Path, "/")}.Sanitize()
	for _, ordre := range []string{
		"REVOKE ALL ON DATABASE " + nom + " FROM PUBLIC",
		"GRANT CONNECT ON DATABASE " + nom + " TO " + roleTestSauvegarde,
		"REVOKE ALL ON SCHEMA public FROM PUBLIC",
		"ALTER DEFAULT PRIVILEGES REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC",
	} {
		if _, err := admin.Exec(ctx, ordre); err != nil {
			t.Fatalf("%s : %v", ordre, err)
		}
	}
	u.User = url.UserPassword(roleTestSauvegarde, roleTestSauvegarde)
	return u.String()
}
