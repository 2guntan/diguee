package cloture_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/base"
	"github.com/2guntan/diguee/internal/base/basetest"
	"github.com/2guntan/diguee/internal/cloture"
	"github.com/2guntan/diguee/internal/empreinte"
	"github.com/2guntan/diguee/internal/empreinte/fauxkms"
	"github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/publication"
	"github.com/2guntan/diguee/internal/questionnaire"
	"github.com/2guntan/diguee/internal/sauvegarde"
	"github.com/2guntan/diguee/internal/sauvegarde/restauration"
	"github.com/2guntan/diguee/internal/sauvegarde/sauvegardetest"
	"github.com/2guntan/diguee/internal/stockage"
	"github.com/2guntan/diguee/internal/tampon"
)

type horloge struct {
	mu sync.Mutex
	t  time.Time
}

func (h *horloge) maintenant() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.t
}

func (h *horloge) regler(t time.Time) {
	h.mu.Lock()
	h.t = t
	h.mu.Unlock()
}

func jour(a, m, j, heure, minute int) time.Time {
	return time.Date(a, time.Month(m), j, heure, minute, 0, 0, time.UTC)
}

// dechiffrerEnSQL déchiffre une sauvegarde et la convertit en script SQL
// (pg_restore -f -) : tout ce qu'elle contient, lisible.
func dechiffrerEnSQL(t *testing.T, c stockage.Compartiment, cle, identite string) string {
	t.Helper()
	var chiffre bytes.Buffer
	if err := c.Recuperer(context.Background(), cle, &chiffre); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", `age --decrypt --identity "$1" | pg_restore --file=-`, "sql", identite)
	cmd.Stdin = &chiffre
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("déchiffrement de %s : %v", cle, err)
	}
	return string(out)
}

// etapesInscrites renvoie les étapes des entrées « cloture », dans l'ordre.
func etapesInscrites(t *testing.T, j *journal.Journal) (etapes []string, pv *journal.Entree) {
	t.Helper()
	es, err := j.Toutes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.VerifierChaine(es); err != nil {
		t.Fatalf("chaîne du journal : %v", err)
	}
	for i := range es {
		switch es[i].Type {
		case journal.Cloture:
			var c struct{ Etape string }
			_ = json.Unmarshal(es[i].Contenu, &c)
			etapes = append(etapes, c.Etape)
		case journal.ProcesVerbal:
			pv = &es[i]
		}
	}
	return etapes, pv
}

type sortieSure struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *sortieSure) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *sortieSure) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// Simulation de bout en bout de la Clôture (ticket #16, spec T12) : vraie
// base PostgreSQL, vraies sauvegardes (pg_dump, age) dans un compartiment
// local, faux KMS d'OVHcloud. Une panne du KMS arrête la procédure avec une
// alerte, sans sauter d'étape ; la relance reprend à l'étape en échec ;
// l'étape manuelle du volume la met en attente. À la fin : plus aucune
// empreinte, ni en base ni dans les sauvegardes ; réponses et publications
// intactes ; procès-verbal au journal, chaîne valide.
func TestSimulationDeBoutEnBout(t *testing.T) {
	sauvegardetest.Programmes(t)
	ctx := context.Background()
	url := basetest.NouvelleBase(t)
	b, err := base.Ouvrir(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Fermer()
	if _, err := b.Migrer(ctx); err != nil {
		t.Fatal(err)
	}
	h := &horloge{t: jour(2027, 5, 21, 10, 0)}
	j := journal.Nouveau(b, h.maintenant)
	q := questionnaire.Courant()
	publieur := &publication.Publieur{Magasin: b, Questionnaire: q, SeuilPublication: 1, SeuilCase: 1,
		Rendu:      func(*publication.Instantane) ([]byte, error) { return []byte("<p>instantané</p>"), nil },
		Maintenant: h.maintenant}
	tp := tampon.Nouveau(tampon.Options{Ecrivain: b, Taille: 3, Delai: 24 * time.Hour, Maintenant: h.maintenant})

	// Cinq participants : trois écrits le 21, deux encore dans le tampon à
	// la clôture.
	hmac := empreinte.NouveauHMACLocal(nil)
	var empreintes []string
	for i := range 5 {
		e, err := hmac.Calculer(ctx, "+22177000000"+string(rune('0'+i)))
		if err != nil {
			t.Fatal(err)
		}
		empreintes = append(empreintes, hex.EncodeToString(e[:]))
		if i == 3 {
			h.regler(jour(2027, 5, 22, 20, 0))
		}
		if err := tp.Ajouter(ctx, e, questionnaire.Reponse{Version: q.Version, Q3: int16(1 + i%2), Region: 1}); err != nil {
			t.Fatal(err)
		}
	}
	// Publication du 22, et deux sauvegardes de nuit, qui contiennent les
	// empreintes ; un objet inattendu sous sauvegardes/.
	h.regler(jour(2027, 5, 22, 6, 0))
	if _, err := publieur.Publier(ctx, jour(2027, 5, 22, 0, 0)); err != nil {
		t.Fatal(err)
	}
	identite, dest := sauvegardetest.Cles(t)
	compartiment := &stockage.Dossier{Chemin: t.TempDir()}
	faire := func(ctx context.Context) (*sauvegarde.Resultat, error) {
		return sauvegarde.Faire(ctx, sauvegarde.Options{PgDump: "pg_dump", Age: "age", URL: url, Destinataire: dest,
			Compartiment: compartiment, DossierTemps: t.TempDir(), Maintenant: h.maintenant})
	}
	var anciennes []string
	for _, d := range []time.Time{jour(2027, 5, 21, 2, 30), jour(2027, 5, 22, 2, 30)} {
		h.regler(d)
		r, err := faire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		anciennes = append(anciennes, r.Cle)
	}
	if err := compartiment.Envoyer(ctx, "sauvegardes/copie-a-la-main.age", strings.NewReader("x"), 1, sha256.Sum256([]byte("x"))); err != nil {
		t.Fatal(err)
	}
	// Témoin : une sauvegarde d'avant la clôture contient bien les empreintes.
	if sql := dechiffrerEnSQL(t, compartiment, anciennes[1], identite); !strings.Contains(sql, empreintes[0]) {
		t.Fatal("témoin : l'empreinte devrait être lisible dans une sauvegarde d'avant la clôture")
	}

	// Faux KMS : la clé de l'empreinte, et l'identité de clôture.
	kms := fauxkms.DemarrerOVH(t)
	tlsCfg, err := empreinte.ChargerCertificat(kms.Certificat, kms.CleClient, kms.AC)
	if err != nil {
		t.Fatal(err)
	}
	destructeur, err := empreinte.NouveauDestructeurOVH(empreinte.OptionsDestructeur{PointAcces: kms.URL, KMSID: kms.KMSID, CleID: kms.CleID, TLS: tlsCfg})
	if err != nil {
		t.Fatal(err)
	}
	var attestation *cloture.AttestationVolume
	var alertes sortieSure
	var sortie sortieSure
	controles := 0
	p := &cloture.Procedure{
		Base: b, Cloture: jour(2027, 5, 23, 0, 0), Sauvegardes: compartiment, Sauvegarder: faire,
		Cle: destructeur, ModeCle: "ovh",
		Volume:     func(context.Context) (*cloture.AttestationVolume, error) { return attestation, nil },
		Controler:  func(ctx context.Context) error { controles++; _, err := destructeur.Etat(ctx); return err },
		Maintenant: h.maintenant, Log: slog.New(slog.NewTextHandler(&alertes, nil)), Sortie: &sortie,
	}

	// La procédure refuse de commencer avant la clôture, puis avant que le
	// serveur ait inscrit la fermeture.
	h.regler(jour(2027, 5, 22, 23, 0))
	if _, err := p.Executer(ctx); err == nil {
		t.Fatal("procédure acceptée avant la fermeture")
	}

	// Fermeture par le serveur : le dernier tampon est écrit à la clôture,
	// la dernière publication le 24 à 06:00.
	fe := &cloture.Fermeture{Cloture: p.Cloture, Heure: 6 * time.Hour, Tampon: tp, Publieur: publieur, Reponses: b,
		Journal: j, Maintenant: h.maintenant, Log: slog.New(slog.NewTextHandler(&alertes, nil))}
	h.regler(jour(2027, 5, 23, 0, 0).Add(20 * time.Second))
	if bf, err := fe.Executer(ctx); err != nil || bf.Faite {
		t.Fatalf("fermeture à la clôture : %+v %v", bf, err)
	}
	if tp.Attente() != 0 {
		t.Fatal("le dernier tampon n'est pas écrit")
	}
	e6, _ := hmac.Calculer(ctx, "+221770000009")
	if err := tp.Ajouter(ctx, e6, questionnaire.Reponse{Version: q.Version, Q3: 1}); !errors.Is(err, tampon.ErrFerme) {
		t.Fatalf("envoi après la clôture : %v", err)
	}
	h.regler(jour(2027, 5, 23, 7, 0))
	if _, err := p.Executer(ctx); err == nil || !strings.Contains(err.Error(), "fermeture n'est pas inscrite") {
		t.Fatalf("avant l'inscription de la fermeture : %v", err)
	}
	h.regler(jour(2027, 5, 24, 6, 0))
	if bf, err := fe.Executer(ctx); err != nil || !bf.Faite {
		t.Fatalf("fermeture : %+v %v", bf, err)
	}
	reponsesAvant, _ := b.CompterReponses(ctx)
	if reponsesAvant != 5 {
		t.Fatalf("%d réponses", reponsesAvant)
	}
	resumesAvant, err := b.Resumes(ctx)
	if err != nil || len(resumesAvant) != 2 {
		t.Fatalf("publications %v %v", resumesAvant, err)
	}
	instantanes := map[string]string{}
	for _, r := range resumesAvant {
		pub, _ := b.LirePublication(ctx, r.Jour)
		instantanes[r.Jour.Format(time.DateOnly)] = pub.Empreinte.String()
	}

	// Premier essai : le KMS est en panne. La procédure va jusqu'à la
	// nouvelle sauvegarde, puis s'arrête sur la clé, avec une alerte.
	kms.Regler(fauxkms.Reglage{Statut: http.StatusServiceUnavailable})
	h.regler(jour(2027, 5, 24, 9, 0))
	// Le contrôle préalable voit la panne : rien n'est supprimé.
	_, err = p.Executer(ctx)
	var ee *cloture.ErreurEtape
	if !errors.As(err, &ee) || ee.Etape != cloture.EtapeSuppressionParticipant || !strings.Contains(err.Error(), "contrôle préalable") {
		t.Fatalf("contrôle préalable : %v", err)
	}
	if existe, _ := b.TableExiste(ctx, "participation"); !existe {
		t.Fatal("participation supprimée malgré l'échec du contrôle préalable")
	}
	if etapes, _ := etapesInscrites(t, j); strings.Join(etapes, ",") != "fermeture,verification" {
		t.Fatalf("étapes après le contrôle préalable : %v", etapes)
	}
	// Le KMS répond au contrôle, puis tombe pendant la procédure.
	kms.Regler(fauxkms.Reglage{})
	p.Controler = func(ctx context.Context) error {
		controles++
		_, err := destructeur.Etat(ctx)
		kms.Regler(fauxkms.Reglage{Statut: http.StatusServiceUnavailable})
		return err
	}
	_, err = p.Executer(ctx)
	if !errors.As(err, &ee) || ee.Etape != cloture.EtapeDestructionCle {
		t.Fatalf("panne du KMS : %v", err)
	}
	if !strings.Contains(alertes.String(), "ALERTE clôture") || !strings.Contains(sortie.String(), "ÉCHEC") {
		t.Errorf("alerte absente : %s", alertes.String())
	}
	etapes, pv := etapesInscrites(t, j)
	if strings.Join(etapes, ",") != "fermeture,verification,suppression_participations,liberation_espace,suppression_sauvegardes,nouvelle_sauvegarde" || pv != nil {
		t.Fatalf("étapes après la panne : %v", etapes)
	}
	if kms.EtatCle() != "active" {
		t.Fatal("la clé ne devait pas être touchée")
	}

	// Le KMS revient : la relance reprend à la clé, puis attend le volume.
	kms.Regler(fauxkms.Reglage{})
	h.regler(jour(2027, 5, 24, 10, 0))
	bilan, err := p.Executer(ctx)
	if !errors.Is(err, cloture.ErrAttente) || strings.Join(bilan.Faites, ",") != cloture.EtapeDestructionCle || kms.EtatCle() != "supprimee" {
		t.Fatalf("reprise : %+v %v, clé %s", bilan, err, kms.EtatCle())
	}
	// Une attestation trop ancienne (essai antérieur) ne compte pas.
	attestation = &cloture.AttestationVolume{Date: jour(2027, 5, 24, 9, 0), SHA256CleChiffree: strings.Repeat("a", 64), SHA256EnteteLUKS: strings.Repeat("b", 64)}
	if _, err := p.Executer(ctx); !errors.Is(err, cloture.ErrAttente) {
		t.Fatalf("attestation ancienne : %v", err)
	}
	attestation.Date, attestation.Rechiffrement, attestation.EspaceLibreEfface = jour(2027, 5, 24, 11, 0), true, true
	h.regler(jour(2027, 5, 24, 11, 30))
	bilan, err = p.Executer(ctx)
	if err != nil || bilan.ProcesVerbal == nil || strings.Join(bilan.Faites, ",") != "renouvellement_volume,proces_verbal" {
		t.Fatalf("fin : %+v %v", bilan, err)
	}
	if controles != 2 {
		t.Errorf("contrôle préalable fait %d fois, 2 attendues (avant la suppression seulement)", controles)
	}
	// Relancée, la procédure constate qu'elle est terminée.
	if bilan, err := p.Executer(ctx); err != nil || !bilan.DejaTerminee {
		t.Errorf("relance après la fin : %+v %v", bilan, err)
	}

	// Procès-verbal et chaîne.
	etapes, pv = etapesInscrites(t, j)
	if strings.Join(etapes, ",") != strings.Join(cloture.Etapes(), ",") || pv == nil {
		t.Fatalf("étapes %v, procès-verbal %v", etapes, pv)
	}
	var c cloture.ContenuProcesVerbal
	if err := json.Unmarshal(pv.Contenu, &c); err != nil {
		t.Fatal(err)
	}
	if c.Date != "2027-05-24" || c.DateCloture != "2027-05-23" || c.ParticipationsSupprimees != 5 || c.SauvegardesSupprimees != 3 ||
		c.Participation != "absente" || c.Reponses != 5 || len(c.Operations) != len(cloture.Etapes()) || c.DernierePublication != "2027-05-24" ||
		!strings.Contains(c.CleEmpreinte, "introuvable") || c.VolumeRenouvele != "2027-05-24T11:00:00Z" {
		t.Errorf("procès-verbal %s", pv.Contenu)
	}
	for _, l := range c.Tables {
		if l.Nom == "participation" {
			t.Error("participation listée au procès-verbal")
		}
	}

	// T12 : plus aucune empreinte en base…
	if existe, _ := b.TableExiste(ctx, "participation"); existe {
		t.Fatal("participation existe encore")
	}
	dump, err := exec.Command("pg_dump", "--dbname="+url).Output()
	if err != nil {
		t.Fatal(err)
	}
	// … ni dans les sauvegardes : il n'en reste qu'une, faite après la
	// suppression de la table.
	ss, autres, err := sauvegarde.Lister(ctx, compartiment)
	if err != nil || len(ss) != 1 || len(autres) != 0 || ss[0].Cle == anciennes[0] || ss[0].Cle == anciennes[1] {
		t.Fatalf("sauvegardes restantes %v %v %v", ss, autres, err)
	}
	sql := dechiffrerEnSQL(t, compartiment, ss[0].Cle, identite)
	for _, texte := range []string{string(dump), sql} {
		if strings.Contains(texte, "CREATE TABLE public.participation") {
			t.Error("table participation dans une copie de la base")
		}
		for _, e := range empreintes {
			if strings.Contains(texte, e) {
				t.Errorf("empreinte %s… encore présente", e[:8])
			}
		}
	}
	// La sauvegarde restante se restaure et passe le contrôle mensuel.
	cible := basetest.NouvelleBase(t)
	var chiffre bytes.Buffer
	if err := compartiment.Recuperer(ctx, ss[0].Cle, &chiffre); err != nil {
		t.Fatal(err)
	}
	restaurer := exec.Command("sh", "-c", `age --decrypt --identity "$1" | pg_restore --no-owner --no-acl --exit-on-error --single-transaction --dbname="$2"`, "r", identite, cible)
	restaurer.Stdin = &chiffre
	if out, err := restaurer.CombinedOutput(); err != nil {
		t.Fatalf("restauration : %v %s", err, out)
	}
	ctl, err := restauration.Controler(ctx, cible, nil)
	if err != nil || !ctl.ParticipationsDetruites || ctl.Lignes("reponse") != 5 {
		t.Errorf("contrôle de la restauration : %+v %v", ctl, err)
	}

	// Réponses et publications intactes.
	if n, _ := b.CompterReponses(ctx); n != reponsesAvant {
		t.Errorf("réponses : %d, %d avant", n, reponsesAvant)
	}
	for jourPub, sha := range instantanes {
		d, _ := time.Parse(time.DateOnly, jourPub)
		pub, err := b.LirePublication(ctx, d)
		if err != nil || pub == nil || pub.Empreinte.String() != sha {
			t.Errorf("publication du %s modifiée", jourPub)
		}
	}
	// Le journal ne contient aucune empreinte de numéro.
	es, _ := j.Toutes(ctx)
	for _, e := range es {
		for _, emp := range empreintes {
			if strings.Contains(string(e.Contenu), emp) {
				t.Errorf("empreinte dans l'entrée %d", e.Numero)
			}
		}
	}
	// Après la clôture, les droits de l'application se réaccordent sans
	// participation (diguee migrer d'une nouvelle version).
	if _, err := b.Migrer(ctx); err != nil {
		t.Fatal(err)
	}
	if existe, _ := b.TableExiste(ctx, "participation"); existe {
		t.Error("une migration a recréé participation")
	}
}

// Une étape inscrite hors de l'ordre (journal incohérent) arrête tout.
func TestJournalIncoherent(t *testing.T) {
	ctx := context.Background()
	b := basetest.Ouvrir(t)
	h := &horloge{t: jour(2027, 5, 24, 9, 0)}
	j := journal.Nouveau(b, h.maintenant)
	for _, e := range []string{cloture.EtapeFermeture, cloture.EtapeLiberationEspace} {
		if _, err := j.Inscrire(ctx, journal.Cloture, map[string]string{"etape": e}); err != nil {
			t.Fatal(err)
		}
	}
	p := &cloture.Procedure{Base: b, Cloture: jour(2027, 5, 23, 0, 0), Sauvegardes: &stockage.Dossier{Chemin: t.TempDir()},
		Sauvegarder: func(context.Context) (*sauvegarde.Resultat, error) { return nil, errors.New("jamais") },
		ModeCle:     "fake", SansVolume: true, Maintenant: h.maintenant}
	if _, err := p.Executer(ctx); err == nil || !strings.Contains(err.Error(), "désordre") {
		t.Fatalf("journal incohérent : %v", err)
	}
	if existe, _ := b.TableExiste(ctx, "participation"); !existe {
		t.Error("participation supprimée malgré un journal incohérent")
	}
}
