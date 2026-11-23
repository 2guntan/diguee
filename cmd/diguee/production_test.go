package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/base/basetest"
	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/masque"
)

// T3 sur l'exécutable (ticket #7) : 200 envois complets sur le serveur lancé
// comme en production pour la base (rôle de l'application, migrations par le
// rôle propriétaire), puis le journal technique (JSON, masqueur compris, tel
// que diguee l'écrit sur sa sortie d'erreur) ne contient ni IP ni numéro,
// d'après les motifs de deploiement/controle-journaux.sh, script compris.
func Test200EnvoisJournalSansIPNiNumero(t *testing.T) {
	if testing.Short() {
		t.Skip("200 envois")
	}
	a := basetest.OuvrirApplication(t)
	valeursCompletes(t)
	t.Setenv("DIGUEE_POSTGRES_URL", a.URLApp)
	t.Setenv("DIGUEE_POSTGRES_URL_MIGRATIONS", a.Schema.URL)
	t.Setenv("DIGUEE_IP_PAR_HEURE", "100000")
	t.Setenv("DIGUEE_ALTCHA_DIFFICULTE", "1000")
	t.Setenv("DIGUEE_TAMPON_TAILLE", "20")
	t.Setenv("DIGUEE_TAMPON_DELAI", "30m")

	sortie := &tamponSur{}
	racine, arreter := demarrerAvec(t, slog.New(masque.Nouveau(slog.NewJSONHandler(sortie, nil))))
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	const nb = 200
	var numeros []string
	for i := range nb {
		chiffres := fmt.Sprintf("77%07d", 1000000+i*4099)
		numeros = append(numeros, chiffres)
		expediteur := "+221 " + chiffres[:2] + " " + chiffres[2:5] + " " + chiffres[5:7] + " " + chiffres[7:]
		webhook, res, cookie := verifierParWhatsApp(t, client, racine, expediteur)
		if webhook != http.StatusOK || res.StatusCode != http.StatusSeeOther || cookie == nil {
			t.Fatalf("envoi %d : webhook %d, vérification %d", i, webhook, res.StatusCode)
		}
		form := url.Values{"q1": {"1"}, "q2": {"2"}, "q3": {"1"}, "q4": {"1"}, "q5": {"2"}, "q6": {"1"},
			"q7": {"2"}, "themes": {"1", "2", "3"}, "region": {fmt.Sprint(1 + i%14)}, "tranche_age": {"2"},
			"sexe": {fmt.Sprint(i % 2)}, "inscrit": {"1"}, "majeur": {"1"}}
		req, _ := http.NewRequest(http.MethodPost, racine+"/envoyer", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		r, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/merci" {
			t.Fatalf("envoi %d : %d", i, r.StatusCode)
		}
	}
	if err := arreter(); err != nil {
		t.Fatalf("arrêt : %v", err)
	}
	var np, nr int
	if err := a.Proprietaire.Pool.QueryRow(context.Background(),
		"SELECT (SELECT count(*) FROM participation), (SELECT count(*) FROM reponse)").Scan(&np, &nr); err != nil || np != nb || nr != nb {
		t.Fatalf("%d participations, %d réponses, %v", np, nr, err)
	}

	journal := sortie.String()
	if strings.Count(journal, `"msg":"requête"`) < nb*5 {
		t.Fatalf("journal trop court pour être probant : %d octets", len(journal))
	}
	for _, n := range numeros {
		// Le numéro entier, ses 7 derniers chiffres isolés (pas au milieu
		// d'une heure en nanosecondes ou d'un SHA-256), sa forme espacée.
		for _, forme := range []string{`(?:^|[^0-9])` + n + `(?:$|[^0-9])`, `(?:^|[^0-9a-f])` + n[2:] + `(?:$|[^0-9a-f])`, n[:2] + " " + n[2:5] + " "} {
			if regexp.MustCompile(forme).MatchString(journal) {
				t.Fatalf("le journal contient un numéro (%s…)", n[:4])
			}
		}
	}
	// Le masqueur n'a rien eu à masquer : le code lui-même n'écrit rien. Seule
	// exception, l'adresse d'écoute du test (127.0.0.1:port ; « :443 » en
	// production), masquée elle aussi.
	for _, l := range strings.Split(journal, "\n") {
		if strings.Contains(l, `"msg":"serveur démarré"`) {
			continue
		}
		if strings.Contains(l, masque.Remplacement) || masque.Contient(l) {
			t.Errorf("ligne masquée : %.300s", l)
		}
	}
	for _, interdit := range []string{"127.0.0.1", "[::1]", "DIGUEE ", "wamid", `"from"`} {
		if strings.Contains(journal, interdit) {
			t.Errorf("le journal contient %q", interdit)
		}
	}

	// Le script de contrôle du serveur, sur ce même journal.
	script, err := filepath.Abs("../../deploiement/controle-journaux.sh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash absent : script de contrôle non lancé")
	}
	dir := t.TempDir()
	fichier := filepath.Join(dir, "diguee.json")
	if err := os.WriteFile(fichier, []byte(journal), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("bash", script, "--fichier", fichier).CombinedOutput(); err != nil {
		t.Fatalf("controle-journaux.sh : %v\n%s", err, out)
	}
	// Et il voit bien une adresse ou un numéro ajoutés.
	for _, ligne := range []string{`{"msg":"dial tcp 203.0.113.9:443"}`, `{"numero":"+221 ` + numeros[7][:2] + " " + numeros[7][2:] + `"}`} {
		f := filepath.Join(dir, "altere.json")
		os.WriteFile(f, []byte(journal+ligne+"\n"), 0o600)
		out, err := exec.Command("bash", script, "--fichier", f).CombinedOutput()
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 || !strings.Contains(string(out), "INTERDIT") {
			t.Errorf("ligne %s non détectée : %v\n%s", ligne, err, out)
		}
	}
}

// En production, le serveur n'applique pas les migrations et refuse le rôle
// propriétaire (ou superutilisateur) ; « diguee migrer » exige l'adresse du
// propriétaire (ticket #7).
func TestProductionRoleDeLApplication(t *testing.T) {
	ctx := context.Background()
	a := basetest.OuvrirApplication(t)
	prod := &config.Config{Env: config.Production, PostgresURL: a.URLApp}
	err := preparerBase(ctx, prod, a.Proprietaire, silencieux())
	if err == nil || !strings.Contains(err.Error(), "refusé en production") || !strings.Contains(err.Error(), "SUPERUSER") {
		t.Fatalf("propriétaire accepté : %v", err)
	}
	if err := preparerBase(ctx, prod, a.App, silencieux()); err != nil {
		t.Fatalf("rôle de l'application refusé : %v", err)
	}
	if err := migrer(ctx, prod, silencieux()); err == nil || !strings.Contains(err.Error(), "DIGUEE_POSTGRES_URL_MIGRATIONS") {
		t.Errorf("migrer sans l'adresse du propriétaire : %v", err)
	}
	// Schéma en retard : le serveur ne migre pas, il refuse.
	vide := basetest.OuvrirVide(t)
	if err := preparerBase(ctx, prod, vide, silencieux()); err == nil || !strings.Contains(err.Error(), "diguee migrer") {
		t.Errorf("schéma non migré accepté : %v", err)
	}
	// Avec l'adresse du propriétaire, migrer accorde les droits.
	prod.PostgresURLMigrations = a.Schema.URL
	if err := migrer(ctx, prod, silencieux()); err != nil {
		t.Errorf("migrer : %v", err)
	}
}

// DIGUEE_TLS=acme : le port HTTP redirige vers l'adresse publique en HTTPS,
// et le port HTTPS refuse un nom non servi (sans appel à Let's Encrypt).
func TestServeurACME(t *testing.T) {
	_, sc := basetest.OuvrirSchema(t)
	valeursCompletes(t)
	t.Setenv("DIGUEE_POSTGRES_URL", sc.URL)
	t.Setenv("DIGUEE_TLS", "acme")
	adrHTTPS, adrHTTP := adresseLibre(t), adresseLibre(t)
	t.Setenv("DIGUEE_ADRESSE_HTTPS", adrHTTPS)
	t.Setenv("DIGUEE_ADRESSE_HTTP", adrHTTP)
	t.Setenv("DIGUEE_ACME_CACHE", t.TempDir())
	t.Setenv("DIGUEE_ACME_ANNUAIRE", "https://127.0.0.1:1/directory")
	t.Setenv("DIGUEE_URL_PUBLIQUE", "https://consultation.diguee.test")
	fin := make(chan error, 1)
	go func() { fin <- lancer(nil, silencieux()) }()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var res *http.Response
	var err error
	for range 100 {
		if res, err = client.Get("http://" + adrHTTP + "/journal?page=2"); err == nil {
			break
		}
		select {
		case e := <-fin:
			t.Fatalf("arrêt du serveur : %v", e)
		default:
		}
		attendreUnPeu()
	}
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMovedPermanently || res.Header.Get("Location") != "https://consultation.diguee.test/journal?page=2" {
		t.Errorf("redirection : %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	if _, err := http.Get("https://" + adrHTTPS + "/"); err == nil {
		t.Error("HTTPS sans nom servi accepté")
	}
	if err := arreterServeur(fin); err != nil {
		t.Fatal(err)
	}
}

func attendreUnPeu() { time.Sleep(50 * time.Millisecond) }

// arreterServeur envoie SIGTERM au processus de test et attend la fin de
// lancer.
func arreterServeur(fin <-chan error) error {
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		return err
	}
	select {
	case err := <-fin:
		return err
	case <-time.After(20 * time.Second):
		return errors.New("le serveur ne s'est pas arrêté")
	}
}
