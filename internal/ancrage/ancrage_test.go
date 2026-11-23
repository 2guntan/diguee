package ancrage

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/journal/journaltest"
)

// fauxServeurCalendrier imite un calendrier public OpenTimestamps : POST
// /digest renvoie un engagement partiel (nonce du calendrier, SHA-256,
// attestation en attente vers sa propre adresse) ; GET /timestamp/<hex>
// renvoie 404 tant que l'engagement n'est pas « dans un bloc », puis le
// chemin jusqu'à une attestation Bitcoin.
type fauxServeurCalendrier struct {
	*httptest.Server
	mu          sync.Mutex
	engagements map[string]bool
	enPanne     atomic.Bool
	mine        atomic.Bool // les engagements sont inscrits dans le bloc 870000
	soumissions atomic.Int32
	corrompu    atomic.Bool
}

func nouveauCalendrier(t *testing.T) *fauxServeurCalendrier {
	t.Helper()
	c := &fauxServeurCalendrier{engagements: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /digest", func(w http.ResponseWriter, r *http.Request) {
		c.soumissions.Add(1)
		if c.enPanne.Load() {
			http.Error(w, "panne", http.StatusServiceUnavailable)
			return
		}
		if r.Header.Get("Accept") != TypeOTS {
			http.Error(w, "accept", http.StatusBadRequest)
			return
		}
		d, _ := io.ReadAll(r.Body)
		if len(d) != 32 {
			http.Error(w, "condensat", http.StatusBadRequest)
			return
		}
		if c.corrompu.Load() {
			_, _ = w.Write([]byte{0xf0, 0x10, 0x01})
			return
		}
		h := &Horodatage{Message: d}
		s, _ := h.Ajouter(Op{Tag: OpAppend, Arg: []byte(fmt.Sprintf("nonce-%s", c.URL[len(c.URL)-5:]))})
		s, _ = s.Ajouter(Op{Tag: OpSHA256})
		s, _ = s.Ajouter(Op{Tag: OpPrepend, Arg: []byte{0x5f, 0x00, 0x00, 0x01}})
		s.Attester(EnAttente(c.URL))
		c.mu.Lock()
		c.engagements[hex.EncodeToString(s.Message)] = true
		c.mu.Unlock()
		b, _ := h.Encoder()
		w.Header().Set("Content-Type", TypeOTS)
		_, _ = w.Write(b)
	})
	mux.HandleFunc("GET /timestamp/{engagement}", func(w http.ResponseWriter, r *http.Request) {
		if c.enPanne.Load() {
			http.Error(w, "panne", http.StatusServiceUnavailable)
			return
		}
		e := r.PathValue("engagement")
		c.mu.Lock()
		connu := c.engagements[e]
		c.mu.Unlock()
		if !connu || !c.mine.Load() {
			http.NotFound(w, r)
			return
		}
		m, _ := hex.DecodeString(e)
		h := &Horodatage{Message: m}
		s, _ := h.Ajouter(Op{Tag: OpAppend, Arg: bytes.Repeat([]byte{0xaa}, 32)})
		s, _ = s.Ajouter(Op{Tag: OpSHA256})
		s.Attester(Bitcoin(870000))
		b, _ := h.Encoder()
		_, _ = w.Write(b)
	})
	c.Server = httptest.NewTLSServer(mux)
	t.Cleanup(c.Close)
	return c
}

func (c *fauxServeurCalendrier) calendrier() *CalendrierHTTP {
	return &CalendrierHTTP{Base: c.URL, Client: c.Client()}
}

// fauxServeurArchive imite « Save Page Now » d'archive.org.
type fauxServeurArchive struct {
	*httptest.Server
	mode    atomic.Value // "entete", "tache", "page", "panne", "429"
	depots  atomic.Int32
	suivis  atomic.Int32
	derrier atomic.Value
}

func nouvelleArchive(t *testing.T, mode string) *fauxServeurArchive {
	t.Helper()
	a := &fauxServeurArchive{}
	a.mode.Store(mode)
	statut := func(w http.ResponseWriter, r *http.Request) {
		if a.suivis.Add(1) < 2 {
			_, _ = io.WriteString(w, `{"status":"pending"}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"status":"success","timestamp":"20261123061501","original_url":%q}`, a.derrier.Load())
	}
	sauver := func(w http.ResponseWriter, r *http.Request) {
		page := strings.TrimPrefix(r.URL.Path, "/save/")
		a.derrier.Store(page)
		switch a.mode.Load().(string) {
		case "panne":
			http.Error(w, "panne", http.StatusBadGateway)
			return
		case "429":
			http.Error(w, "trop de demandes", http.StatusTooManyRequests)
			return
		}
		a.depots.Add(1)
		switch a.mode.Load().(string) {
		case "entete":
			w.Header().Set("Content-Location", "/web/20261123061500/"+page)
			_, _ = io.WriteString(w, "<html>ok</html>")
		case "redirection":
			http.Redirect(w, r, "/web/20261123061502/"+page, http.StatusFound)
		case "page":
			_, _ = fmt.Fprintf(w, `<html><a href="/web/20201010101010/https://autre.example/">autre</a> <a href="/web/20261123061503/%s">copie</a></html>`, page)
		case "tache":
			_, _ = io.WriteString(w, `<html><script>spn.watchJob("spn2-0123456789abcdef0123456789abcdef01234567", "/_static/",6000);</script></html>`)
		}
	}
	// Sans le nettoyage des chemins de ServeMux : /save/https://… contient « // ».
	a.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/save/status/") {
			r.SetPathValue("tache", strings.TrimPrefix(r.URL.Path, "/save/status/"))
			statut(w, r)
			return
		}
		sauver(w, r)
	}))
	t.Cleanup(a.Close)
	return a
}

func (a *fauxServeurArchive) archiveur() *ArchiveHTTP {
	return &ArchiveHTTP{Base: a.URL, Client: a.Client(), Attente: time.Millisecond, Essais: 5}
}

// memoire est un ancrage.Magasin en mémoire, en ajout seul.
type memoire struct {
	mu       sync.Mutex
	preuves  []Preuve
	archives []Archive
	panne    atomic.Bool
}

func (m *memoire) compter() (preuves, archives int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.preuves), len(m.archives)
}

func (m *memoire) EnregistrerPreuve(_ context.Context, p Preuve) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, q := range m.preuves {
		if q.Jour.Equal(p.Jour) && q.Revision == p.Revision {
			return false, nil
		}
	}
	m.preuves = append(m.preuves, p)
	return true, nil
}

func (m *memoire) EnregistrerArchive(_ context.Context, a Archive) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.archives {
		if b.Jour.Equal(a.Jour) && b.Cible == a.Cible {
			return false, nil
		}
	}
	m.archives = append(m.archives, a)
	return true, nil
}

func (m *memoire) LirePreuve(_ context.Context, jour time.Time) (*Preuve, error) {
	if m.panne.Load() {
		return nil, errors.New("base indisponible")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var r *Preuve
	for i, p := range m.preuves {
		if p.Jour.Equal(jour) && (r == nil || p.Revision > r.Revision) {
			r = &m.preuves[i]
		}
	}
	if r == nil {
		return nil, nil
	}
	c := *r
	return &c, nil
}

func (m *memoire) Ancrages(ctx context.Context, limite int) ([]Etat, error) {
	m.mu.Lock()
	var jours []time.Time
	for _, p := range m.preuves {
		jours = append(jours, p.Jour)
	}
	for _, a := range m.archives {
		jours = append(jours, a.Jour)
	}
	archives := slices.Clone(m.archives)
	m.mu.Unlock()
	slices.SortFunc(jours, func(a, b time.Time) int { return b.Compare(a) })
	jours = slices.Compact(jours)
	jours = jours[:min(limite, len(jours))]
	var es []Etat
	for _, j := range jours {
		p, _ := m.LirePreuve(ctx, j)
		e := Etat{Jour: j, Preuve: p}
		for _, a := range archives {
			if a.Jour.Equal(j) {
				e.Archives = append(e.Archives, a)
			}
		}
		es = append(es, e)
	}
	return es, nil
}

type horloge struct {
	mu sync.Mutex
	t  time.Time
}

func (h *horloge) Maintenant() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.t
}

func (h *horloge) Avancer(d time.Duration) {
	h.mu.Lock()
	h.t = h.t.Add(d)
	h.mu.Unlock()
}

type journalSur struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (j *journalSur) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.buf.Write(p)
}

func (j *journalSur) String() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.buf.String()
}

type banc struct {
	ancreur     *Ancreur
	magasin     *memoire
	journal     *journal.Journal
	calendriers []*fauxServeurCalendrier
	archive     *fauxServeurArchive
	horloge     *horloge
	log         *journalSur
}

func nouveauBanc(t *testing.T, modeArchive string) *banc {
	t.Helper()
	b := &banc{magasin: &memoire{}, horloge: &horloge{t: time.Date(2026, 11, 23, 6, 20, 0, 0, time.UTC)}, log: &journalSur{}}
	b.journal = journal.Nouveau(&journaltest.Memoire{}, b.horloge.Maintenant)
	if _, err := b.journal.Inscrire(t.Context(), journal.PublicationQuotidienne, map[string]any{"jour": "2026-11-23", "reponses": 1230}); err != nil {
		t.Fatal(err)
	}
	var cs []Calendrier
	var urls []string
	for range 3 {
		c := nouveauCalendrier(t)
		b.calendriers = append(b.calendriers, c)
		cs = append(cs, c.calendrier())
		urls = append(urls, c.URL)
	}
	b.archive = nouvelleArchive(t, modeArchive)
	ouvrir := func(uri string) (Calendrier, bool) {
		for _, c := range b.calendriers {
			if c.URL == uri && Admis(uri, urls) {
				return c.calendrier(), true
			}
		}
		return nil, false
	}
	a, err := Nouveau(Options{Mode: "reel", Magasin: b.magasin, Journal: b.journal, Calendriers: cs, MinCalendriers: 2,
		Ouvrir: ouvrir, Archiveur: b.archive.archiveur(), URLPublique: "https://consultation.diguee.sn",
		Heure: 6*time.Hour + 15*time.Minute, Maintenant: b.horloge.Maintenant,
		ReessaiMin: 5 * time.Millisecond, ReessaiMax: 20 * time.Millisecond,
		Log: slog.New(slog.NewJSONHandler(b.log, nil))})
	if err != nil {
		t.Fatal(err)
	}
	b.ancreur = a
	return b
}

var jour23 = time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)

// Chaque jour, une preuve OpenTimestamps de l'empreinte de tête est obtenue
// auprès des calendriers, puis les pages sont déposées sur archive.org.
func TestAncrageQuotidien(t *testing.T) {
	b := nouveauBanc(t, "entete")
	ctx := t.Context()
	bilan, err := b.ancreur.Executer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bilan.Jour.Equal(jour23) || !bilan.Nouvelle || bilan.Preuve == nil || len(bilan.Archives) != 2 {
		t.Fatalf("bilan : %+v", bilan)
	}
	n, _ := b.journal.Compter(ctx)
	tete, _ := b.journal.Lire(ctx, n, n)
	p := bilan.Preuve
	if p.Numero != n || p.Empreinte != tete[0].Empreinte || p.Calendriers != 3 || p.Bitcoin || p.Revision != 0 {
		t.Fatalf("preuve : %+v", p)
	}
	f, err := LireFichier(p.OTS)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(f.Condensat, tete[0].Empreinte[:]) || len(f.Horodatage.Calendriers()) != 3 {
		t.Fatalf("fichier .ots : condensat %x, calendriers %v", f.Condensat, f.Horodatage.Calendriers())
	}
	for _, c := range b.calendriers {
		if c.soumissions.Load() != 1 {
			t.Errorf("%s : %d soumissions", c.URL, c.soumissions.Load())
		}
	}
	liens := map[Cible]string{}
	for _, a := range bilan.Archives {
		liens[a.Cible] = a.Lien
	}
	if liens[CibleJournal] != b.archive.URL+"/web/20261123061500/https://consultation.diguee.sn/journal" ||
		liens[CibleExport] != b.archive.URL+"/web/20261123061500/https://consultation.diguee.sn/journal.json" {
		t.Fatalf("liens : %v", liens)
	}

	// Une seconde exécution le même jour ne refait rien.
	bilan2, err := b.ancreur.Executer(ctx)
	if err != nil || bilan2.Nouvelle || len(bilan2.Archives) != 0 || b.archive.depots.Load() != 2 {
		t.Fatalf("seconde exécution : %+v, %v", bilan2, err)
	}

	// Le lendemain, une nouvelle preuve, sur la nouvelle tête.
	b.horloge.Avancer(24 * time.Hour)
	if _, err := b.journal.Inscrire(ctx, journal.PublicationQuotidienne, map[string]any{"jour": "2026-11-24", "reponses": 1300}); err != nil {
		t.Fatal(err)
	}
	bilan3, err := b.ancreur.Executer(ctx)
	if err != nil || !bilan3.Nouvelle || bilan3.Preuve.Numero != n+1 || !bilan3.Jour.Equal(jour23.AddDate(0, 0, 1)) {
		t.Fatalf("lendemain : %+v, %v", bilan3, err)
	}
	// Avant l'heure de l'ancrage, le jour dû reste la veille.
	b.horloge.Avancer(23*time.Hour + 50*time.Minute) // 25 novembre, 06:10
	if j := b.ancreur.JourDu(b.horloge.Maintenant()); !j.Equal(jour23.AddDate(0, 0, 1)) {
		t.Errorf("jour dû avant l'heure : %v", j)
	}
}

// Une panne d'une partie des calendriers est tolérée et signalée ; sous le
// minimum, rien n'est enregistré, l'échec est signalé et réessayé.
func TestCalendriersEnPanne(t *testing.T) {
	b := nouveauBanc(t, "entete")
	ctx := t.Context()
	b.calendriers[0].enPanne.Store(true)
	bilan, err := b.ancreur.Executer(ctx)
	if err != nil || bilan.Preuve == nil || bilan.Preuve.Calendriers != 2 {
		t.Fatalf("un calendrier en panne : %+v, %v", bilan, err)
	}
	if !strings.Contains(b.log.String(), "calendrier OpenTimestamps sans réponse") {
		t.Error("panne d'un calendrier non signalée")
	}

	b2 := nouveauBanc(t, "entete")
	b2.calendriers[0].enPanne.Store(true)
	b2.calendriers[1].corrompu.Store(true) // réponse invalide
	bilan, err = b2.ancreur.Executer(ctx)
	// archive.org, lui, a répondu : ses dépôts sont gardés.
	if err == nil || bilan.Preuve != nil || len(b2.magasin.preuves) != 0 || len(b2.magasin.archives) != 2 {
		t.Fatalf("deux calendriers sur trois en échec : %+v, %v", bilan, err)
	}
	if !strings.Contains(err.Error(), "1 calendrier(s) sur 3 ont répondu, 2 exigés") || !strings.Contains(err.Error(), "réponse invalide") {
		t.Errorf("erreur : %v", err)
	}
	es := b2.ancreur.Echecs()
	if len(es) != 1 || es[0].Service != ServiceOTS || es[0].Essais != 1 {
		t.Fatalf("échecs : %+v", es)
	}
	if !strings.Contains(b2.log.String(), `"level":"WARN","msg":"ancrage : échec, nouvel essai plus tard"`) {
		t.Errorf("échec non signalé : %s", b2.log.String())
	}
	// Les calendriers reviennent : l'essai suivant réussit, l'échec s'efface.
	b2.calendriers[0].enPanne.Store(false)
	b2.calendriers[1].corrompu.Store(false)
	if bilan, err := b2.ancreur.Executer(ctx); err != nil || !bilan.Nouvelle {
		t.Fatalf("après la panne : %+v, %v", bilan, err)
	}
	if len(b2.ancreur.Echecs()) != 0 {
		t.Error("échec toujours affiché")
	}
}

// archive.org injoignable : la preuve est gardée, le dépôt réessayé par la
// tâche avec un délai croissant, et signalé, sans arrêt ; il réussit quand
// archive.org revient.
func TestArchiveEnPanneReessayee(t *testing.T) {
	b := nouveauBanc(t, "panne")
	ctx, annuler := context.WithCancel(t.Context())
	fini := make(chan struct{})
	go func() {
		defer close(fini)
		b.ancreur.Lancer(ctx)
	}()
	attendre(t, func() bool { return strings.Count(b.log.String(), "nouvel essai programmé") >= 3 })
	if p, a := b.magasin.compter(); p != 1 || a != 0 {
		t.Fatalf("preuves %d, archives %d", p, a)
	}
	es := b.ancreur.Echecs()
	if len(es) != 1 || es[0].Service != ServiceArchive || es[0].Essais < 3 || !strings.Contains(es[0].Erreur, "statut 502") {
		t.Fatalf("échecs : %+v", es)
	}
	b.archive.mode.Store("tache")
	attendre(t, func() bool { _, a := b.magasin.compter(); return a == 2 })
	annuler()
	<-fini
	if p, _ := b.magasin.compter(); p != 1 {
		t.Errorf("%d preuves : la preuve du jour a été refaite", p)
	}
	for _, a := range b.magasin.archives {
		if !strings.HasPrefix(a.Lien, b.archive.URL+"/web/20261123061501/https://consultation.diguee.sn/journal") {
			t.Errorf("lien : %s", a.Lien)
		}
	}
	// Délai croissant : 5, 10, 20, 20 ms…
	for _, d := range []string{`"dans":"5ms"`, `"dans":"10ms"`, `"dans":"20ms"`} {
		if !strings.Contains(b.log.String(), d) {
			t.Errorf("délai %s absent", d)
		}
	}
	if len(b.ancreur.Echecs()) != 0 {
		t.Error("échec toujours affiché après le retour d'archive.org")
	}
}

// Une base injoignable ne fait pas paniquer la tâche : elle réessaie.
func TestBaseInjoignable(t *testing.T) {
	b := nouveauBanc(t, "entete")
	b.magasin.panne.Store(true)
	if _, err := b.ancreur.Executer(t.Context()); err == nil || !strings.Contains(err.Error(), "base indisponible") {
		t.Fatalf("erreur attendue : %v", err)
	}
}

// Les trois formes de réponse de « Save Page Now » donnent le lien de la copie.
func TestFormesDeReponseArchive(t *testing.T) {
	for mode, ts := range map[string]string{"entete": "20261123061500", "redirection": "20261123061502",
		"page": "20261123061503", "tache": "20261123061501"} {
		a := nouvelleArchive(t, mode)
		lien, err := a.archiveur().Archiver(t.Context(), "https://consultation.diguee.sn/journal")
		if err != nil || lien != a.URL+"/web/"+ts+"/https://consultation.diguee.sn/journal" {
			t.Errorf("%s : %q, %v", mode, lien, err)
		}
	}
	a := nouvelleArchive(t, "429")
	if _, err := a.archiveur().Archiver(t.Context(), "https://consultation.diguee.sn/journal"); err == nil {
		t.Error("429 accepté")
	}
}

// Les jours suivants, la preuve est complétée de l'attestation Bitcoin
// donnée par les calendriers (comme « ots upgrade ») : nouvelle révision.
func TestMiseANiveau(t *testing.T) {
	b := nouveauBanc(t, "entete")
	ctx := t.Context()
	if _, err := b.ancreur.Executer(ctx); err != nil {
		t.Fatal(err)
	}
	b.horloge.Avancer(24 * time.Hour)
	// Pas encore dans un bloc : rien ne change, sans erreur.
	if bilan, err := b.ancreur.Executer(ctx); err != nil || bilan.MisesANiveau != 0 {
		t.Fatalf("avant le bloc : %+v, %v", bilan, err)
	}
	for _, c := range b.calendriers {
		c.mine.Store(true)
	}
	b.calendriers[2].enPanne.Store(true) // une panne n'empêche pas les autres
	bilan, err := b.ancreur.Executer(ctx)
	if err != nil || bilan.MisesANiveau != 1 {
		t.Fatalf("mise à niveau : %+v, %v", bilan, err)
	}
	p, _ := b.magasin.LirePreuve(ctx, jour23)
	if p.Revision != 1 || !p.Bitcoin || !slices.Equal(p.Blocs(), []uint64{870000}) {
		t.Fatalf("preuve mise à niveau : révision %d, bitcoin %v, blocs %v", p.Revision, p.Bitcoin, p.Blocs())
	}
	f, err := LireFichier(p.OTS)
	if err != nil || len(f.Horodatage.Calendriers()) != 3 {
		t.Fatalf("fichier mis à niveau : %v", err)
	}
	if !strings.Contains(b.log.String(), "preuve OpenTimestamps complétée") {
		t.Error("mise à niveau non journalisée")
	}
	// Le surlendemain, seule la preuve de la veille est mise à niveau : une
	// preuve complète n'est plus redemandée.
	b.horloge.Avancer(24 * time.Hour)
	if bilan, _ := b.ancreur.Executer(ctx); bilan.MisesANiveau != 1 {
		t.Errorf("surlendemain : %d mises à niveau", bilan.MisesANiveau)
	}
	if p, _ := b.magasin.LirePreuve(ctx, jour23); p.Revision != 1 {
		t.Error("preuve complète remise à niveau")
	}
}

// La mise à niveau ne suit que des calendriers admis (l'URI vient d'une
// réponse extérieure).
func TestCalendriersAdmis(t *testing.T) {
	conf := []string{"https://a.pool.opentimestamps.org"}
	for uri, ok := range map[string]bool{
		"https://alice.btc.calendar.opentimestamps.org": true,
		"https://finney.calendar.eternitywall.com":      true,
		"https://a.pool.opentimestamps.org":             true,
		"http://alice.btc.calendar.opentimestamps.org":  false,
		"https://calendar.opentimestamps.org.evil.com":  false,
		"https://169.254.169.254":                       false,
		"https://evil.com/.calendar.opentimestamps.org": false,
	} {
		if Admis(uri, conf) != ok {
			t.Errorf("%s : admis = %v", uri, !ok)
		}
	}
}

// Le texte hebdomadaire reprend l'empreinte du premier jour ancré de chaque
// semaine.
func TestSemaines(t *testing.T) {
	var etats []Etat
	for i := 20; i >= 0; i-- { // du 3 au 23 novembre 2026, les plus récents d'abord
		j := time.Date(2026, 11, 3+i, 0, 0, 0, 0, time.UTC)
		e := Etat{Jour: j}
		if i != 13 { // pas de preuve le lundi 16
			e.Preuve = &Preuve{Jour: j, Numero: int64(100 + i)}
		}
		etats = append(etats, e)
	}
	ss := Semaines(etats, "https://consultation.diguee.sn", 3)
	if len(ss) != 3 {
		t.Fatalf("%d semaines", len(ss))
	}
	attendus := []struct{ lundi, jour int }{{23, 23}, {16, 17}, {9, 9}}
	for i, a := range attendus {
		if ss[i].Lundi.Day() != a.lundi || ss[i].Jour.Day() != a.jour {
			t.Errorf("semaine %d : lundi %v, jour %v", i, ss[i].Lundi, ss[i].Jour)
		}
	}
	if ss[0].LienJournal != "https://consultation.diguee.sn/journal" ||
		ss[0].LienPreuve != "https://consultation.diguee.sn/journal/ancrages/2026-11-23.ots" {
		t.Errorf("liens : %+v", ss[0])
	}
}

// Les faux services (mode fake) produisent une preuve valide, sans réseau.
func TestModeFake(t *testing.T) {
	b := nouveauBanc(t, "entete")
	ctx := t.Context()
	a, err := Nouveau(Options{Mode: "fake", Magasin: b.magasin, Journal: b.journal,
		Calendriers: []Calendrier{FauxCalendrier{Nom: "a"}, FauxCalendrier{Nom: "b"}},
		Archiveur:   FausseArchive{}, URLPublique: "https://consultation.diguee.sn",
		Maintenant: b.horloge.Maintenant, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	bilan, err := a.Executer(ctx)
	if err != nil || bilan.Preuve == nil || len(bilan.Archives) != 2 {
		t.Fatalf("mode fake : %+v, %v", bilan, err)
	}
	if _, err := LireFichier(bilan.Preuve.OTS); err != nil {
		t.Fatal(err)
	}
	tab, err := a.Tableau(ctx)
	if err != nil || len(tab.Semaines) != 1 || len(tab.Jours) != 1 || tab.Mode != "fake" {
		t.Fatalf("tableau : %+v, %v", tab, err)
	}
}

// La plateforme ne publie jamais sur X (ADR 0010) : aucun hôte de X n'apparaît
// dans le code du serveur (cmd et internal : Go, gabarits, catalogues, SQL),
// hors tests. Le site vitrine statique (site/) peut, lui, pointer vers le
// compte de Diguee : un lien n'est pas un appel.
func TestAucunAppelAX(t *testing.T) {
	hotes := regexp.MustCompile(`(?i)(^|[^a-z0-9.-])((api|upload|stream)\.)?(twitter\.com|x\.com|t\.co|twimg\.com)\b`)
	n := 0
	for _, racine := range []string{filepath.Join("..", "..", "cmd"), filepath.Join("..", "..", "internal")} {
		err := filepath.WalkDir(racine, func(chemin string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if nom := d.Name(); nom == ".git" || nom == "bin" || nom == "testdata" || nom == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			ext := filepath.Ext(chemin)
			if strings.HasSuffix(chemin, "_test.go") || !slices.Contains([]string{".go", ".html", ".json", ".js", ".sql"}, ext) {
				return nil
			}
			b, err := os.ReadFile(chemin)
			if err != nil {
				return err
			}
			n++
			if m := hotes.Find(b); m != nil {
				t.Errorf("%s cite un hôte de X : %q", chemin, m)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if n < 50 {
		t.Fatalf("seulement %d fichiers parcourus", n)
	}
	if !hotes.MatchString("https://api.twitter.com/2/tweets") || !hotes.MatchString(`"https://x.com/compose"`) {
		t.Fatal("le motif ne reconnaît pas les hôtes de X")
	}
}

func attendre(t *testing.T, ok func() bool) {
	t.Helper()
	fin := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(fin) {
			t.Fatal("délai dépassé")
		}
		time.Sleep(2 * time.Millisecond)
	}
}
