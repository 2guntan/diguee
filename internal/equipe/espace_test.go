package equipe_test

import (
	"bytes"
	"context"
	"encoding/base32"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/antifraude"
	"github.com/2guntan/diguee/internal/base"
	"github.com/2guntan/diguee/internal/base/basetest"
	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/equipe"
	"github.com/2guntan/diguee/internal/i18n"
	"github.com/2guntan/diguee/internal/integration"
	"github.com/2guntan/diguee/internal/totp"
	"github.com/2guntan/diguee/internal/web"
)

// horloge est une heure réglable, sûre pour un usage concurrent.
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
	defer h.mu.Unlock()
	h.t = h.t.Add(d)
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

// banc : l'espace équipe monté dans le serveur web, comme en production.
type banc struct {
	h        http.Handler
	base     *base.Base
	schema   basetest.Schema
	service  *equipe.Service
	sessions *equipe.Sessions
	horloge  *horloge
	journal  *journalSur
	coupe    *antifraude.CoupeCircuit // coupe-circuit du parcours SMS
}

func nouveauBanc(t *testing.T) *banc {
	t.Helper()
	t.Cleanup(equipe.IterationsPourTests(1000))
	b, sc := basetest.OuvrirMigree(t)
	cfg, err := config.Charger(func(k string) string {
		return map[string]string{"DIGUEE_POSTGRES_URL": "postgres://localhost/diguee"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	ch, err := equipe.ConstruireChiffreur(cfg, &integration.Registre{})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := i18n.Charger()
	if err != nil {
		t.Fatal(err)
	}
	hl := &horloge{t: time.Date(2026, 12, 1, 10, 0, 7, 0, time.UTC)}
	j := &journalSur{}
	journal := slog.New(slog.NewJSONHandler(j, nil))
	ss := equipe.NouvellesSessions(hl.Maintenant)
	svc := equipe.NouveauService(b, ch, ss, hl.Maintenant)
	coupe := antifraude.NouveauCoupeCircuit(antifraude.OptionsCoupeCircuit{Facteur: 3, Plancher: 20, PlafondHoraire: 2,
		Reactivation: time.Hour, Maintenant: hl.Maintenant, Journal: journal})
	esp, err := equipe.NouvelEspace(equipe.OptionsEspace{Service: svc, Sessions: ss, Magasin: b, Catalogues: cs, Journal: journal,
		CoupeCircuitSMS: coupe})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := web.Nouveau(web.Options{
		Config: cfg, Catalogues: cs, Publications: b, Version: "v0.0.0-test", SHA256: strings.Repeat("ab", 32),
		Journal: journal, Equipe: esp,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Le service refuse les codes du pas de démarrage (anti-rejeu après un
	// redémarrage) : on commence au pas suivant.
	hl.Avancer(totp.Periode)
	return &banc{h: srv, base: b, schema: sc, service: svc, sessions: ss, horloge: hl, journal: j, coupe: coupe}
}

// compte est un membre créé pour le test, avec ses accès.
type compte struct {
	equipe.Membre
	acces equipe.Acces
}

func (b *banc) creer(t *testing.T, identifiant string, roles ...equipe.Role) compte {
	t.Helper()
	m, a, err := b.service.Creer(context.Background(), nil, identifiant, "Personne "+identifiant, roles)
	if err != nil {
		t.Fatal(err)
	}
	return compte{m, a}
}

func secretDe(t *testing.T, a equipe.Acces) []byte {
	t.Helper()
	s, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(a.Secret)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (b *banc) code(t *testing.T, a equipe.Acces) string {
	return totp.Code(secretDe(t, a), totp.Pas(b.horloge.Maintenant()))
}

// client imite un navigateur sans JavaScript, avec ses cookies.
type client struct {
	t       *testing.T
	b       *banc
	adresse string
	cookies map[string]*http.Cookie
	entetes map[string]string
}

func (b *banc) client(t *testing.T) *client {
	return &client{t: t, b: b, adresse: "192.0.2.44:5555", cookies: map[string]*http.Cookie{}, entetes: map[string]string{}}
}

type reponse struct {
	statut   int
	corps    string
	location string
}

var reCSRF = regexp.MustCompile(`name="csrf" value="([^"]*)"`)

func (c *client) faire(req *http.Request) reponse {
	c.t.Helper()
	req.RemoteAddr = c.adresse
	req.Header.Set("X-Forwarded-For", "198.51.100.7")
	for k, v := range c.entetes {
		req.Header.Set(k, v)
	}
	for _, ck := range c.cookies {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	c.b.h.ServeHTTP(rec, req)
	res := rec.Result()
	for _, ck := range res.Cookies() {
		if ck.MaxAge < 0 {
			delete(c.cookies, ck.Name)
		} else {
			c.cookies[ck.Name] = ck
		}
	}
	corps, _ := io.ReadAll(res.Body)
	return reponse{statut: res.StatusCode, corps: string(corps), location: res.Header.Get("Location")}
}

func (c *client) get(chemin string) reponse {
	return c.faire(httptest.NewRequest(http.MethodGet, chemin, nil))
}

func (c *client) post(chemin string, form url.Values) reponse {
	req := httptest.NewRequest(http.MethodPost, chemin, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.faire(req)
}

// csrf lit le jeton du premier formulaire de la page.
func (c *client) csrf(chemin string) string {
	c.t.Helper()
	p := c.get(chemin)
	m := reCSRF.FindStringSubmatch(p.corps)
	if m == nil {
		c.t.Fatalf("%s : pas de jeton CSRF (statut %d)", chemin, p.statut)
	}
	return m[1]
}

func (c *client) connecter(identifiant, mdp, code string) reponse {
	c.t.Helper()
	return c.post("/equipe/connexion", url.Values{
		"csrf": {c.csrf("/equipe/connexion")}, "identifiant": {identifiant}, "mot_de_passe": {mdp}, "code": {code},
	})
}

func (c *client) connecterCompte(cp compte) {
	c.t.Helper()
	if p := c.connecter(cp.Identifiant, cp.acces.MotDePasse, c.b.code(c.t, cp.acces)); p.statut != http.StatusSeeOther || p.location != "/equipe/" {
		c.t.Fatalf("connexion de %s : %d %s", cp.Identifiant, p.statut, p.location)
	}
}

func (c *client) connecte() bool {
	return c.get("/equipe/").statut == http.StatusOK
}

func TestConnexionImpossibleSansTOTPValide(t *testing.T) {
	b := nouveauBanc(t)
	awa := b.creer(t, "awa.diop", equipe.Administrateur, equipe.Redacteur)
	c := b.client(t)
	bon := b.code(t, awa.acces)
	faux := "000000"
	if faux == bon {
		faux = "111111"
	}
	precedent := totp.Code(secretDe(t, awa.acces), totp.Pas(b.horloge.Maintenant())-3)
	for nom, essai := range map[string][3]string{
		"sans code":             {awa.Identifiant, awa.acces.MotDePasse, ""},
		"code faux":             {awa.Identifiant, awa.acces.MotDePasse, faux},
		"code trop ancien":      {awa.Identifiant, awa.acces.MotDePasse, precedent},
		"mot de passe faux":     {awa.Identifiant, "abcde-abcde-abcde-abcde", bon},
		"identifiant inconnu":   {"personne", awa.acces.MotDePasse, bon},
		"sans mot de passe":     {awa.Identifiant, "", bon},
		"identifiant mal formé": {"<script>", awa.acces.MotDePasse, bon},
	} {
		p := c.connecter(essai[0], essai[1], essai[2])
		if p.statut != http.StatusUnauthorized || c.cookies[equipe.NomCookie] != nil {
			t.Errorf("%s : statut %d, session %v", nom, p.statut, c.cookies[equipe.NomCookie] != nil)
		}
		if !strings.Contains(p.corps, "Connexion refusée") {
			t.Errorf("%s : message générique attendu", nom)
		}
	}
	// Le code bon a été consommé par l'essai au mauvais mot de passe : il faut
	// attendre le suivant (et la fin du blocage après 5 échecs).
	b.horloge.Avancer(equipe.FenetreCompte)
	c.connecterCompte(awa)
	if !c.connecte() {
		t.Fatal("pas de session après connexion")
	}
}

func TestCodeDejaUtiliseRefuse(t *testing.T) {
	b := nouveauBanc(t)
	awa := b.creer(t, "awa.diop", equipe.Redacteur)
	code := b.code(t, awa.acces)
	c1 := b.client(t)
	if p := c1.connecter(awa.Identifiant, awa.acces.MotDePasse, code); p.statut != http.StatusSeeOther {
		t.Fatalf("première connexion : %d", p.statut)
	}
	// Même code, même pas, depuis un autre navigateur : refusé.
	c2 := b.client(t)
	if p := c2.connecter(awa.Identifiant, awa.acces.MotDePasse, code); p.statut != http.StatusUnauthorized {
		t.Fatalf("code rejoué : %d", p.statut)
	}
	// Toujours refusé au pas suivant (il reste dans la tolérance), mais le
	// nouveau code passe.
	b.horloge.Avancer(totp.Periode)
	if p := c2.connecter(awa.Identifiant, awa.acces.MotDePasse, code); p.statut != http.StatusUnauthorized {
		t.Fatalf("code rejoué au pas suivant : %d", p.statut)
	}
	c2.connecterCompte(awa)

	// Deux connexions simultanées avec le même code : une seule passe.
	b.horloge.Avancer(totp.Periode)
	code = b.code(t, awa.acces)
	var wg sync.WaitGroup
	var mu sync.Mutex
	reussies := 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := b.service.Connecter(context.Background(), awa.Identifiant, awa.acces.MotDePasse, code, "192.0.2.9:1")
			if err == nil {
				mu.Lock()
				reussies++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if reussies != 1 {
		t.Errorf("%d connexions avec le même code", reussies)
	}
}

// Après un redémarrage, l'anti-rejeu en mémoire est vide : un code du pas de
// démarrage (ou d'avant) est refusé, car il a pu servir juste avant.
func TestAntiRejeuApresRedemarrage(t *testing.T) {
	b := nouveauBanc(t)
	awa := b.creer(t, "awa.diop", equipe.Redacteur)
	cfg := &config.Config{}
	ch, _ := equipe.ConstruireChiffreur(cfg, &integration.Registre{})
	redemarre := equipe.NouveauService(b.base, ch, b.sessions, b.horloge.Maintenant)
	if _, err := redemarre.Connecter(context.Background(), awa.Identifiant, awa.acces.MotDePasse, b.code(t, awa.acces), "192.0.2.9:1"); err == nil {
		t.Fatal("code du pas de démarrage accepté")
	}
	b.horloge.Avancer(totp.Periode)
	if _, err := redemarre.Connecter(context.Background(), awa.Identifiant, awa.acces.MotDePasse, b.code(t, awa.acces), "192.0.2.9:1"); err != nil {
		t.Fatalf("code suivant refusé : %v", err)
	}
}

func TestCompteDesactiveFermeSessionsEtNeSeConnectePlus(t *testing.T) {
	b := nouveauBanc(t)
	admin := b.creer(t, "admin", equipe.Administrateur)
	moussa := b.creer(t, "moussa", equipe.Redacteur, equipe.Relecteur)
	cm1, cm2 := b.client(t), b.client(t)
	cm1.connecterCompte(moussa)
	b.horloge.Avancer(totp.Periode)
	cm2.connecterCompte(moussa)
	if b.sessions.Nombre(moussa.ID) != 2 || !cm1.connecte() || !cm2.connecte() {
		t.Fatal("deux sessions attendues")
	}
	ca := b.client(t)
	ca.connecterCompte(admin)
	p := ca.post("/equipe/membres/"+moussa.ID+"/desactiver", url.Values{"csrf": {ca.csrf("/equipe/membres")}})
	if p.statut != http.StatusSeeOther {
		t.Fatalf("désactivation : %d %s", p.statut, p.corps)
	}
	if n := b.sessions.Nombre(moussa.ID); n != 0 {
		t.Errorf("%d session(s) encore ouverte(s)", n)
	}
	for _, c := range []*client{cm1, cm2} {
		if p := c.get("/equipe/"); p.statut != http.StatusSeeOther || p.location != "/equipe/connexion" {
			t.Errorf("session d'un compte désactivé : %d", p.statut)
		}
	}
	b.horloge.Avancer(totp.Periode)
	if p := cm1.connecter(moussa.Identifiant, moussa.acces.MotDePasse, b.code(t, moussa.acces)); p.statut != http.StatusUnauthorized {
		t.Errorf("compte désactivé connecté : %d", p.statut)
	}
	// La base refuse de le réactiver.
	if _, err := b.base.Pool.Exec(context.Background(), "UPDATE membre SET actif = true WHERE id = $1", moussa.ID); err == nil {
		t.Error("réactivation acceptée en SQL")
	}
	if _, err := b.base.Pool.Exec(context.Background(), "DELETE FROM membre WHERE id = $1", moussa.ID); err == nil {
		t.Error("suppression acceptée en SQL")
	}
}

// Même si la désactivation passait par ailleurs (SQL direct), la session
// serait refusée : le compte est relu à chaque requête.
func TestCompteReluAChaqueRequete(t *testing.T) {
	b := nouveauBanc(t)
	b.creer(t, "admin", equipe.Administrateur)
	moussa := b.creer(t, "moussa", equipe.Redacteur, equipe.Administrateur)
	c := b.client(t)
	c.connecterCompte(moussa)
	if p := c.get("/equipe/membres"); p.statut != http.StatusOK {
		t.Fatalf("membres : %d", p.statut)
	}
	if _, err := b.base.Pool.Exec(context.Background(), "UPDATE membre SET roles = '{redacteur}' WHERE id = $1", moussa.ID); err != nil {
		t.Fatal(err)
	}
	if p := c.get("/equipe/membres"); p.statut != http.StatusForbidden {
		t.Errorf("rôle retiré mais page servie : %d", p.statut)
	}
	if _, err := b.base.Pool.Exec(context.Background(), "UPDATE membre SET actif = false WHERE id = $1", moussa.ID); err != nil {
		t.Fatal(err)
	}
	if p := c.get("/equipe/"); p.statut != http.StatusSeeOther {
		t.Errorf("compte désactivé en base mais page servie : %d", p.statut)
	}
	if b.sessions.Nombre(moussa.ID) != 0 {
		t.Error("session non fermée")
	}
}

func TestPagesInaccessiblesSansSessionNiRole(t *testing.T) {
	b := nouveauBanc(t)
	admin := b.creer(t, "admin", equipe.Administrateur)
	red := b.creer(t, "redac", equipe.Redacteur, equipe.Relecteur)
	gets := []string{"/equipe/", "/equipe/membres", "/equipe/audit"}
	posts := []string{"/equipe/membres", "/equipe/membres/" + admin.ID + "/roles", "/equipe/membres/" + admin.ID + "/desactiver",
		"/equipe/membres/" + admin.ID + "/reinitialiser", "/equipe/deconnexion"}

	anonyme := b.client(t)
	anonyme.cookies[equipe.NomCookie] = &http.Cookie{Name: equipe.NomCookie, Value: "forge"}
	for _, c := range []*client{b.client(t), anonyme} {
		for _, g := range gets {
			if p := c.get(g); p.statut != http.StatusSeeOther || p.location != "/equipe/connexion" {
				t.Errorf("GET %s sans session : %d %s", g, p.statut, p.location)
			}
		}
		for _, chemin := range posts {
			if p := c.post(chemin, url.Values{"identifiant": {"intrus"}, "nom": {"X"}, "roles": {"administrateur"}}); p.statut != http.StatusSeeOther {
				t.Errorf("POST %s sans session : %d", chemin, p.statut)
			}
		}
	}

	c := b.client(t)
	c.connecterCompte(red)
	if p := c.get("/equipe/"); p.statut != http.StatusOK || !strings.Contains(p.corps, "Rédacteur") {
		t.Errorf("accueil d'un rédacteur : %d", p.statut)
	}
	if strings.Contains(c.get("/equipe/").corps, `href="/equipe/membres"`) {
		t.Error("lien d'administration montré à un non-administrateur")
	}
	jeton := c.csrf("/equipe/")
	for _, g := range gets[1:] {
		if p := c.get(g); p.statut != http.StatusForbidden {
			t.Errorf("GET %s par un rédacteur : %d", g, p.statut)
		}
	}
	for _, chemin := range posts[:4] {
		if p := c.post(chemin, url.Values{"csrf": {jeton}, "identifiant": {"intrus"}, "nom": {"X"}, "roles": {"administrateur"}}); p.statut != http.StatusForbidden {
			t.Errorf("POST %s par un rédacteur : %d", chemin, p.statut)
		}
	}
	ms, _ := b.base.ListerMembres(context.Background())
	if len(ms) != 2 || !ms[0].A(equipe.Administrateur) && !ms[1].A(equipe.Administrateur) {
		t.Errorf("comptes modifiés : %+v", ms)
	}

	// Après 8 heures, la session est fermée, même utilisée entre-temps.
	b.horloge.Avancer(4 * time.Hour)
	if !c.connecte() {
		t.Fatal("session de 4 h refusée")
	}
	b.horloge.Avancer(4 * time.Hour)
	if c.connecte() {
		t.Error("session de 8 h acceptée")
	}
}

func TestFormulairesProtegesContreLeCSRF(t *testing.T) {
	b := nouveauBanc(t)
	admin := b.creer(t, "admin", equipe.Administrateur)
	c := b.client(t)
	c.connecterCompte(admin)
	jeton := c.csrf("/equipe/membres")
	form := func(csrf string) url.Values {
		return url.Values{"csrf": {csrf}, "identifiant": {"nouveau"}, "nom": {"Nouveau"}, "roles": {"redacteur"}}
	}
	for nom, f := range map[string]url.Values{"sans jeton": form(""), "jeton faux": form("faux")} {
		if p := c.post("/equipe/membres", f); p.statut != http.StatusForbidden {
			t.Errorf("%s : %d", nom, p.statut)
		}
	}
	for nom, e := range map[string]map[string]string{
		"autre origine":   {"Origin": "https://exemple.invalid"},
		"autre site":      {"Sec-Fetch-Site": "cross-site"},
		"site apparenté":  {"Sec-Fetch-Site": "same-site"},
		"origine opaque":  {"Origin": "null"},
		"origine en http": {"Origin": "http://autre.example"},
	} {
		c.entetes = e
		if p := c.post("/equipe/membres", form(jeton)); p.statut != http.StatusForbidden {
			t.Errorf("%s : %d", nom, p.statut)
		}
	}
	c.entetes = map[string]string{"Origin": "http://example.com", "Sec-Fetch-Site": "same-origin"}
	if p := c.post("/equipe/membres", form(jeton)); p.statut != http.StatusOK {
		t.Fatalf("formulaire valide refusé : %d", p.statut)
	}
	if ms, _ := b.base.ListerMembres(context.Background()); len(ms) != 2 {
		t.Errorf("%d comptes : seul le formulaire valide doit créer", len(ms))
	}

	// Connexion : il faut le jeton du formulaire, lié à son cookie.
	n := b.client(t)
	b.horloge.Avancer(totp.Periode)
	if p := n.post("/equipe/connexion", url.Values{"identifiant": {"admin"}, "mot_de_passe": {admin.acces.MotDePasse}, "code": {b.code(t, admin.acces)}}); p.statut != http.StatusForbidden {
		t.Errorf("connexion sans jeton : %d", p.statut)
	}
	autre := b.client(t)
	jetonAutre := autre.csrf("/equipe/connexion")
	n.csrf("/equipe/connexion")
	if p := n.post("/equipe/connexion", url.Values{"csrf": {jetonAutre}, "identifiant": {"admin"}, "mot_de_passe": {admin.acces.MotDePasse}, "code": {b.code(t, admin.acces)}}); p.statut != http.StatusForbidden {
		t.Errorf("connexion avec le jeton d'un autre navigateur : %d", p.statut)
	}
	n.connecterCompte(admin)
}

func TestEchecsDeConnexionLimites(t *testing.T) {
	b := nouveauBanc(t)
	awa := b.creer(t, "awa.diop", equipe.Redacteur)
	c := b.client(t)
	for i := range equipe.EssaisParCompte {
		if p := c.connecter(awa.Identifiant, "mauvais-mot-de-passe", "123456"); p.statut != http.StatusUnauthorized {
			t.Fatalf("essai %d : %d", i, p.statut)
		}
	}
	// Bloqué, même avec les bons accès, et depuis une autre adresse.
	b.horloge.Avancer(totp.Periode)
	c.adresse = "203.0.113.5:4444"
	p := c.connecter(awa.Identifiant, awa.acces.MotDePasse, b.code(t, awa.acces))
	if p.statut != http.StatusTooManyRequests || c.cookies[equipe.NomCookie] != nil {
		t.Fatalf("compte non bloqué : %d", p.statut)
	}
	if !strings.Contains(p.corps, "Trop de tentatives") {
		t.Error("message de blocage absent")
	}
	// Un blocage n'est pas un échec : il ne prolonge pas la fenêtre.
	b.horloge.Avancer(equipe.FenetreCompte)
	c.connecterCompte(awa)

	// Par adresse : au-delà de EssaisParSource échecs, tous identifiants
	// confondus, l'adresse est bloquée, mais pas les autres.
	b.horloge.Avancer(totp.Periode)
	moussa := b.creer(t, "moussa", equipe.Relecteur)
	s := b.client(t)
	s.adresse = "198.51.100.20:1000"
	for i := range equipe.EssaisParSource {
		s.connecter("inconnu"+string(rune('a'+i)), "x", "123456")
	}
	if p := s.connecter(moussa.Identifiant, moussa.acces.MotDePasse, b.code(t, moussa.acces)); p.statut != http.StatusTooManyRequests {
		t.Fatalf("adresse non bloquée : %d", p.statut)
	}
	autre := b.client(t)
	autre.adresse = "198.51.100.21:1000"
	autre.connecterCompte(moussa)
}

func TestAdministrationDesComptesEtAudit(t *testing.T) {
	b := nouveauBanc(t)
	admin := b.creer(t, "admin", equipe.Administrateur)
	ca := b.client(t)
	ca.connecterCompte(admin)
	jeton := ca.csrf("/equipe/membres")

	// Création par le formulaire : les accès sont montrés une fois.
	p := ca.post("/equipe/membres", url.Values{"csrf": {jeton}, "identifiant": {" Fatou.Sall "}, "nom": {"Fatou Sall"}, "roles": {"redacteur", "relecteur"}})
	if p.statut != http.StatusOK {
		t.Fatalf("création : %d %s", p.statut, p.corps)
	}
	mdp := regexp.MustCompile(`<code>([a-z2-9]{5}(?:-[a-z2-9]{5}){3})</code>`).FindStringSubmatch(p.corps)
	sec := regexp.MustCompile(`<code class="empreinte">([A-Z2-7]{32})</code>`).FindStringSubmatch(p.corps)
	if mdp == nil || sec == nil || !strings.Contains(p.corps, "otpauth://totp/Diguee:fatou.sall?") {
		t.Fatalf("accès non affichés : %s", p.corps)
	}
	fatouAcces := equipe.Acces{Identifiant: "fatou.sall", MotDePasse: mdp[1], Secret: sec[1]}
	cf := b.client(t)
	b.horloge.Avancer(totp.Periode)
	if p := cf.connecter("fatou.sall", fatouAcces.MotDePasse, b.code(t, fatouAcces)); p.statut != http.StatusSeeOther {
		t.Fatalf("connexion du compte créé : %d", p.statut)
	}
	var fatou equipe.Membre
	ms, _ := b.base.ListerMembres(context.Background())
	for _, m := range ms {
		if m.Identifiant == "fatou.sall" {
			fatou = m
		}
	}
	if fatou.Nom != "Fatou Sall" || len(fatou.Roles) != 2 {
		t.Fatalf("compte créé : %+v", fatou)
	}

	// Erreurs de saisie.
	for nom, f := range map[string]url.Values{
		"identifiant pris": {"identifiant": {"admin"}, "nom": {"X"}, "roles": {"redacteur"}},
		"identifiant faux": {"identifiant": {"é"}, "nom": {"X"}, "roles": {"redacteur"}},
		"sans rôle":        {"identifiant": {"zz"}, "nom": {"X"}},
		"rôle inconnu":     {"identifiant": {"zz"}, "nom": {"X"}, "roles": {"chef"}},
		"sans nom":         {"identifiant": {"zz"}, "nom": {" "}, "roles": {"redacteur"}},
	} {
		f.Set("csrf", jeton)
		if p := ca.post("/equipe/membres", f); p.statut != http.StatusBadRequest || !strings.Contains(p.corps, `class="erreur"`) {
			t.Errorf("%s : %d", nom, p.statut)
		}
	}

	// Rôles : changés et relus à la requête suivante de la session ouverte.
	if p := ca.post("/equipe/membres/"+fatou.ID+"/roles", url.Values{"csrf": {jeton}, "roles": {"relecteur", "administrateur"}}); p.statut != http.StatusSeeOther {
		t.Fatalf("rôles : %d", p.statut)
	}
	if p := cf.get("/equipe/membres"); p.statut != http.StatusOK {
		t.Errorf("nouvel administrateur refusé : %d", p.statut)
	}
	// Il doit rester un administrateur actif ; on ne se désactive pas soi-même.
	if p := cf.post("/equipe/membres/"+fatou.ID+"/roles", url.Values{"csrf": {cf.csrf("/equipe/")}, "roles": {"relecteur"}}); p.statut != http.StatusSeeOther {
		t.Fatalf("retrait de son propre rôle (un autre administrateur reste) : %d", p.statut)
	}
	if p := ca.post("/equipe/membres/"+admin.ID+"/roles", url.Values{"csrf": {jeton}, "roles": {"redacteur"}}); p.statut != http.StatusBadRequest || !strings.Contains(p.corps, "au moins un administrateur") {
		t.Errorf("dernier administrateur retiré : %d", p.statut)
	}
	if p := ca.post("/equipe/membres/"+admin.ID+"/desactiver", url.Values{"csrf": {jeton}}); p.statut != http.StatusBadRequest {
		t.Errorf("auto-désactivation : %d", p.statut)
	}
	if p := ca.post("/equipe/membres/inexistant/desactiver", url.Values{"csrf": {jeton}}); p.statut != http.StatusBadRequest {
		t.Errorf("compte inconnu : %d", p.statut)
	}
	if err := b.service.Desactiver(context.Background(), fatou, admin.ID); err == nil {
		t.Error("un non-administrateur a désactivé un compte")
	}

	// Réinitialisation : nouveaux accès, anciens refusés, sessions fermées.
	p = ca.post("/equipe/membres/"+fatou.ID+"/reinitialiser", url.Values{"csrf": {jeton}})
	if p.statut != http.StatusOK || b.sessions.Nombre(fatou.ID) != 0 {
		t.Fatalf("réinitialisation : %d, %d session(s)", p.statut, b.sessions.Nombre(fatou.ID))
	}
	b.horloge.Avancer(totp.Periode)
	if p := cf.connecter("fatou.sall", fatouAcces.MotDePasse, b.code(t, fatouAcces)); p.statut != http.StatusUnauthorized {
		t.Errorf("anciens accès acceptés : %d", p.statut)
	}

	// Audit : chaque action, avec son auteur, au jour près.
	as, err := b.base.ListerAudit(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for i := len(as) - 1; i >= 0; i-- {
		types = append(types, string(as[i].Type))
		if as[i].Jour.Hour() != 0 || as[i].Jour.Minute() != 0 {
			t.Errorf("audit plus fin que le jour : %v", as[i].Jour)
		}
	}
	attendu := "creation_commande connexion creation connexion changement_roles changement_roles reinitialisation_acces"
	if got := strings.Join(types, " "); got != attendu {
		t.Errorf("audit :\n%s\nattendu :\n%s", got, attendu)
	}
	if as[0].ActeurIdentifiant != "admin" || as[0].CibleIdentifiant != "fatou.sall" {
		t.Errorf("dernière action : %+v", as[0])
	}
	if p := ca.get("/equipe/audit"); p.statut != http.StatusOK || !strings.Contains(p.corps, "Réinitialisation de l") || !strings.Contains(p.corps, "commande diguee creer-membre") {
		t.Errorf("page d'audit : %d", p.statut)
	}
	if _, err := b.base.Pool.Exec(context.Background(), "UPDATE audit_equipe SET type = 'connexion'"); err == nil {
		t.Error("audit modifiable")
	}
	if _, err := b.base.Pool.Exec(context.Background(), "DELETE FROM audit_equipe"); err == nil {
		t.Error("audit supprimable")
	}
}

// Les journaux techniques, l'audit et la base ne contiennent ni adresse IP,
// ni mot de passe, ni secret TOTP, ni jeton de session.
func TestAucuneIPNiSecretJournalise(t *testing.T) {
	b := nouveauBanc(t)
	admin := b.creer(t, "admin", equipe.Administrateur)
	c := b.client(t)
	c.connecter("admin", "faux", "000000")
	c.connecter("personne", "faux", "000000")
	c.connecterCompte(admin)
	c.get("/equipe/membres")
	c.get("/equipe/audit")
	p := c.post("/equipe/membres", url.Values{"csrf": {c.csrf("/equipe/")}, "identifiant": {"awa"}, "nom": {"Awa"}, "roles": {"redacteur"}})
	if p.statut != http.StatusOK {
		t.Fatalf("création : %d", p.statut)
	}
	journal := b.journal.String()
	if !strings.Contains(journal, `"chemin":"/equipe/connexion"`) {
		t.Fatalf("journal d'accès vide : %s", journal)
	}
	var contenu strings.Builder
	lignes, err := b.base.Pool.Query(context.Background(),
		"SELECT row_to_json(m)::text FROM membre m UNION ALL SELECT row_to_json(a)::text FROM audit_equipe a")
	if err != nil {
		t.Fatal(err)
	}
	for lignes.Next() {
		var l string
		_ = lignes.Scan(&l)
		contenu.WriteString(l + "\n")
	}
	lignes.Close()
	reIP := regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b|192\.0\.2\.|198\.51\.100\.|\[?::1\]?`)
	session := c.cookies[equipe.NomCookie].Value
	for nom, src := range map[string]string{"journal": journal, "base": contenu.String()} {
		if m := reIP.FindString(src); m != "" {
			t.Errorf("%s : adresse IP %q", nom, m)
		}
		for _, secret := range []string{admin.acces.MotDePasse, admin.acces.Secret, session, "faux"} {
			if strings.Contains(src, secret) {
				t.Errorf("%s : contient un secret %q", nom, secret)
			}
		}
	}
	if strings.Contains(journal, "admin") || strings.Contains(journal, "personne") {
		t.Error("journal : identifiant de connexion")
	}
}

func TestSecretTOTPStockeChiffre(t *testing.T) {
	b := nouveauBanc(t)
	awa := b.creer(t, "awa", equipe.Redacteur)
	var chiffre []byte
	var mdp string
	if err := b.base.Pool.QueryRow(context.Background(), "SELECT totp_secret_chiffre, mot_de_passe FROM membre WHERE id = $1", awa.ID).Scan(&chiffre, &mdp); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(chiffre, secretDe(t, awa.acces)) || strings.Contains(string(chiffre), awa.acces.Secret) || len(chiffre) < 20+12+16 {
		t.Error("secret TOTP en clair")
	}
	if !strings.HasPrefix(mdp, "pbkdf2-sha256$") || strings.Contains(mdp, awa.acces.MotDePasse) {
		t.Errorf("mot de passe stocké : %q", mdp)
	}
	// Une clé différente ne permet pas de se connecter.
	cfg := &config.Config{CleTOTP: bytes.Repeat([]byte{3}, 32)}
	ch, _ := equipe.ConstruireChiffreur(cfg, &integration.Registre{})
	s := equipe.NouveauService(b.base, ch, b.sessions, b.horloge.Maintenant)
	b.horloge.Avancer(totp.Periode)
	if _, err := s.Connecter(context.Background(), "awa", awa.acces.MotDePasse, b.code(t, awa.acces), "x"); err == nil {
		t.Error("connexion avec une autre clé de chiffrement")
	}
}

var (
	reAction      = regexp.MustCompile(`(?s)\{\{.*?\}\}`)
	reCommentaire = regexp.MustCompile(`(?s)<!--.*?-->`)
	reAttribut    = regexp.MustCompile(`(?i)\b(aria-label|title|alt|placeholder|content|value)="([^"]*)"`)
	reBalise      = regexp.MustCompile(`(?s)<[^>]*>`)
	reLettre      = regexp.MustCompile(`\p{L}`)
)

// Comme pour les pages publiques : aucun texte visible en dur dans les
// gabarits de l'espace, tout passe par le catalogue.
func TestAucunTexteEnDurDansLesGabaritsEquipe(t *testing.T) {
	racine := os.DirFS("gabarits")
	err := fs.WalkDir(racine, ".", func(chemin string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := fs.ReadFile(racine, chemin)
		src := reAction.ReplaceAllString(reCommentaire.ReplaceAllString(string(b), ""), "")
		for _, m := range reAttribut.FindAllStringSubmatch(src, -1) {
			if v := m[2]; reLettre.MatchString(v) && !strings.Contains(v, "=") && v != "noindex, nofollow" {
				t.Errorf("%s : attribut %s écrit en dur : %q", chemin, m[1], m[2])
			}
		}
		if reste := reBalise.ReplaceAllString(src, ""); reLettre.MatchString(reste) {
			t.Errorf("%s contient du texte en dur : %q", chemin, strings.Join(strings.Fields(reste), " "))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Toutes les pages de l'espace se rendent sans clé absente du catalogue, sans
// script, et ne sont pas indexées.
func TestPagesEquipeCompletesSansScript(t *testing.T) {
	b := nouveauBanc(t)
	admin := b.creer(t, "admin", equipe.Administrateur, equipe.Redacteur, equipe.Relecteur)
	c := b.client(t)
	pages := map[string]reponse{"connexion": c.get("/equipe/connexion"), "introuvable": c.get("/equipe/rien")}
	pages["refus"] = c.connecter("admin", "x", "1")
	c.connecterCompte(admin)
	pages["accueil"] = c.get("/equipe/")
	pages["membres"] = c.get("/equipe/membres")
	pages["audit"] = c.get("/equipe/audit")
	pages["sms actif"] = c.get("/equipe/sms")
	for b.coupe.Autoriser() {
	}
	pages["sms suspendu"] = c.get("/equipe/sms")
	pages["acces"] = c.post("/equipe/membres", url.Values{"csrf": {c.csrf("/equipe/")}, "identifiant": {"awa"}, "nom": {"Awa"}, "roles": {"redacteur"}})
	pages["interdit"] = c.post("/equipe/membres", url.Values{"csrf": {"x"}})
	for nom, p := range pages {
		if strings.Contains(p.corps, "⟦") || strings.Contains(p.corps, "%!") {
			t.Errorf("%s : clé absente ou mise en forme cassée", nom)
		}
		if strings.Contains(strings.ToLower(p.corps), "<script") || len(p.corps) > 100_000 {
			t.Errorf("%s : script ou page trop lourde", nom)
		}
		if !strings.Contains(p.corps, `content="noindex, nofollow"`) {
			t.Errorf("%s : indexable", nom)
		}
	}
	rec := httptest.NewRecorder()
	b.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/equipe/connexion", nil))
	if h := rec.Header(); h.Get("Cache-Control") != "no-store" || h.Get("X-Robots-Tag") == "" || h.Get("Content-Security-Policy") == "" {
		t.Errorf("en-têtes : %v", h)
	}
	if p := c.get("/equipe"); p.statut != http.StatusSeeOther || p.location != "/equipe/" {
		t.Errorf("/equipe : %d %s", p.statut, p.location)
	}
}

// Le parcours SMS suspendu par le coupe-circuit se réactive à la main, par
// un administrateur seulement, avec une ligne d'audit.
func TestReactivationManuelleDuSMS(t *testing.T) {
	b := nouveauBanc(t)
	admin := b.creer(t, "admin", equipe.Administrateur)
	redac := b.creer(t, "redac", equipe.Redacteur, equipe.Relecteur)
	ca, cr := b.client(t), b.client(t)
	ca.connecterCompte(admin)
	cr.connecterCompte(redac)
	if p := cr.get("/equipe/sms"); p.statut != http.StatusForbidden {
		t.Fatalf("page SMS pour un non-administrateur : %d", p.statut)
	}
	p := ca.get("/equipe/sms")
	if p.statut != http.StatusOK || !strings.Contains(p.corps, "Le parcours SMS est actif.") || strings.Contains(p.corps, "/equipe/sms/reactiver") ||
		!strings.Contains(ca.get("/equipe/").corps, `href="/equipe/sms"`) {
		t.Fatalf("page SMS active : %d", p.statut)
	}
	// Sans suspension, la réactivation ne fait rien et n'inscrit rien.
	if p := ca.post("/equipe/sms/reactiver", url.Values{"csrf": {ca.csrf("/equipe/")}}); p.location != "/equipe/sms?fait=actif" {
		t.Fatalf("réactivation sans suspension : %d %s", p.statut, p.location)
	}
	for b.coupe.Autoriser() {
	}
	p = ca.get("/equipe/sms")
	if !strings.Contains(p.corps, "plafond absolu") || !strings.Contains(p.corps, `action="/equipe/sms/reactiver"`) {
		t.Fatalf("page SMS suspendu : %s", p.corps)
	}
	if p := cr.post("/equipe/sms/reactiver", url.Values{"csrf": {cr.csrf("/equipe/")}}); p.statut != http.StatusForbidden || !b.coupe.Suspendu() {
		t.Fatalf("réactivation par un non-administrateur : %d", p.statut)
	}
	if p := ca.post("/equipe/sms/reactiver", url.Values{"csrf": {"faux"}}); p.statut != http.StatusForbidden || !b.coupe.Suspendu() {
		t.Fatalf("réactivation sans jeton CSRF : %d", p.statut)
	}
	if p := ca.post("/equipe/sms/reactiver", url.Values{"csrf": {ca.csrf("/equipe/")}}); p.location != "/equipe/sms?fait=reactive" || b.coupe.Suspendu() {
		t.Fatalf("réactivation : %d %s", p.statut, p.location)
	}
	if p := ca.get("/equipe/sms?fait=reactive"); !strings.Contains(p.corps, "Le parcours SMS est réactivé.") {
		t.Fatal("confirmation absente")
	}
	as, err := b.base.ListerAudit(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, a := range as {
		if a.Type == equipe.ActionReactivationSMS {
			n++
			if a.Acteur != admin.ID || a.Cible != "" || a.CibleIdentifiant != "" {
				t.Errorf("ligne d'audit : %+v", a)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d lignes d'audit de réactivation", n)
	}
	if p := ca.get("/equipe/audit"); !strings.Contains(p.corps, "Réactivation manuelle du parcours SMS") ||
		!strings.Contains(p.corps, "aucun (action sur la plateforme)") {
		t.Fatal("audit : réactivation absente")
	}
	// En SQL direct, une réactivation doit être sans cible, et toute autre
	// action en a une.
	if _, err := b.base.Pool.Exec(context.Background(), `INSERT INTO audit_equipe (numero, jour, type, acteur, cible)
		VALUES (1000, current_date, 'reactivation_sms', $1, $1)`, admin.ID); err == nil {
		t.Error("une réactivation avec cible doit être refusée")
	}
	if _, err := b.base.Pool.Exec(context.Background(), `INSERT INTO audit_equipe (numero, jour, type, acteur)
		VALUES (1001, current_date, 'connexion', $1)`, admin.ID); err == nil {
		t.Error("une connexion sans cible doit être refusée")
	}
}
