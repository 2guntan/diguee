package web

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/ancrage"
	"github.com/2guntan/diguee/internal/base"
	"github.com/2guntan/diguee/internal/config"
	"github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/publication"
)

// Ticket #17 (spec E9, §11, T13) : un test d'ensemble parcourt toutes les
// pages publiques et toutes celles du parcours de réponse, et vérifie pour
// chacune le poids, l'absence de toute ressource tierce et la liste des
// scripts. Les tests de chaque ticket gardent leurs propres contrôles ; celui-ci
// est le contrôle unique, sur un serveur complet.

const (
	// PoidsMaxPage est le poids maximal d'une page hors polices : HTML,
	// feuille de style, scripts et icône (spec E9).
	PoidsMaxPage = 100 * 1024
	// PoidsMaxStatiques borne la feuille de style et les deux scripts réunis.
	PoidsMaxStatiques = 20 * 1024
	// PoidsMaxPolices borne les quatre fichiers de police réunis.
	PoidsMaxPolices = 100 * 1024
)

// scriptsAutorises sont les seuls scripts du site (spec §11) : le solveur
// du captcha ALTCHA et l'interrogation de la page du jeton.
var scriptsAutorises = []string{scriptAltcha, scriptVerifier}

// pageVue est une page visitée par le test d'ensemble.
type pageVue struct {
	nom    string
	chemin string
	p      page
	statut int    // statut attendu
	script string // script attendu ("" : aucun)
}

// publicationsReglables passe d'une publication à l'autre (sous le seuil,
// au-delà) ou simule une base en panne, pendant le test.
type publicationsReglables struct {
	mu    sync.Mutex
	m     *publicationsMemoire
	panne bool
}

func (p *publicationsReglables) regler(m *publicationsMemoire, panne bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.m, p.panne = m, panne
}

func (p *publicationsReglables) etat() (*publicationsMemoire, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.panne {
		return nil, fmt.Errorf("base en panne (simulée)")
	}
	return p.m, nil
}

func (p *publicationsReglables) DernierResume(ctx context.Context) (*publication.Resume, error) {
	m, err := p.etat()
	if err != nil {
		return nil, err
	}
	return m.DernierResume(ctx)
}

func (p *publicationsReglables) LirePublication(ctx context.Context, j time.Time) (*publication.Publication, error) {
	m, err := p.etat()
	if err != nil {
		return nil, err
	}
	return m.LirePublication(ctx, j)
}

func (p *publicationsReglables) Resumes(ctx context.Context) ([]publication.Resume, error) {
	m, err := p.etat()
	if err != nil {
		return nil, err
	}
	return m.Resumes(ctx)
}

var (
	reBaliseChargee     = regexp.MustCompile(`(?is)<(link|script|img|iframe|frame|embed|object|source|audio|video|track|image|use|input|meta|base|style)\b([^>]*)>`)
	reAttributRessource = regexp.MustCompile(`(?s)([a-zA-Z_:][-a-zA-Z0-9_:.]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))`)
	reStyleBloc         = regexp.MustCompile(`(?is)<style\b[^>]*>(.*?)</style>`)
	reScript            = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`)
	reCSSURL            = regexp.MustCompile(`(?i)url\(\s*(?:"([^"]*)"|'([^']*)'|([^)\s]*))\s*\)`)
	reCSSImport         = regexp.MustCompile(`(?i)@import`)
	reGestionnaire      = regexp.MustCompile(`(?i)<[a-z][^>]*\son[a-z]+\s*=`)
	reLienA             = regexp.MustCompile(`(?is)<a\b[^>]*\shref="([^"]*)"`)
	reIdentifiant       = regexp.MustCompile(`\sid="([^"]*)"`)
	reCodeDev           = regexp.MustCompile(`\(mode de développement\)\x{00a0}: (\d{6})\.`)
)

func attributs(s string) map[string]string {
	a := map[string]string{}
	for _, m := range reAttributRessource.FindAllStringSubmatch(s, -1) {
		a[strings.ToLower(m[1])] = m[2] + m[3] + m[4]
	}
	return a
}

// origine sert à résoudre les adresses relatives : une ressource est locale
// si, résolue depuis la page, elle reste sur ce domaine.
var origine = &url.URL{Scheme: "https", Host: "consultation.diguee.sn"}

// locale résout une adresse de ressource depuis la page ; ok est faux si
// elle sort du domaine (autre hôte, « // », data:, javascript:…).
func locale(depuis, adresse string) (string, bool) {
	adresse = strings.TrimSpace(adresse)
	u, err := url.Parse(adresse)
	if err != nil || adresse == "" {
		return "", false
	}
	base := origine.ResolveReference(&url.URL{Path: depuis})
	r := base.ResolveReference(u)
	if r.Scheme != origine.Scheme || r.Host != origine.Host || u.Scheme != "" || u.Host != "" || strings.HasPrefix(adresse, "//") {
		return "", false
	}
	return r.RequestURI(), true
}

// ressourcesChargees renvoie les adresses que le navigateur charge de
// lui-même en affichant la page (pas les liens ni les formulaires).
func ressourcesChargees(t *testing.T, nom, corps string) []string {
	t.Helper()
	var rs []string
	for _, m := range reBaliseChargee.FindAllStringSubmatch(corps, -1) {
		balise, a := strings.ToLower(m[1]), attributs(m[2])
		switch balise {
		case "base":
			t.Errorf("%s : balise <base> interdite", nom)
		case "meta":
			if strings.EqualFold(a["http-equiv"], "refresh") {
				t.Errorf("%s : <meta http-equiv=refresh> interdite", nom)
			}
		case "link":
			if a["href"] != "" {
				rs = append(rs, a["href"])
			}
		case "object":
			rs = append(rs, a["data"])
		case "image", "use":
			for _, k := range []string{"href", "xlink:href"} {
				if v := a[k]; v != "" && !strings.HasPrefix(v, "#") {
					rs = append(rs, v)
				}
			}
		case "video":
			if a["poster"] != "" {
				rs = append(rs, a["poster"])
			}
			fallthrough
		default:
			if a["src"] != "" {
				rs = append(rs, a["src"])
			}
			for _, c := range strings.Split(a["srcset"], ",") {
				if f := strings.Fields(c); len(f) > 0 {
					rs = append(rs, f[0])
				}
			}
		}
		if st := a["style"]; st != "" {
			rs = append(rs, urlsCSS(t, nom, st)...)
		}
	}
	for _, m := range reStyleBloc.FindAllStringSubmatch(corps, -1) {
		rs = append(rs, urlsCSS(t, nom, m[1])...)
	}
	return rs
}

// urlsCSS renvoie les url() d'une feuille de style ; @import est refusé
// (une seule feuille, sans chargement en cascade).
func urlsCSS(t *testing.T, nom, css string) []string {
	t.Helper()
	if reCSSImport.MatchString(css) {
		t.Errorf("%s : @import interdit", nom)
	}
	var us []string
	for _, m := range reCSSURL.FindAllStringSubmatch(css, -1) {
		us = append(us, m[1]+m[2]+m[3])
	}
	return us
}

// verifierCSP n'accepte que 'none' et 'self' : ni hôte, ni schéma (https:,
// data:…), ni joker, ni 'unsafe-…', ni nonce. script-src n'est présent
// qu'avec un script, connect-src qu'avec l'interrogation de la page du jeton.
func verifierCSP(t *testing.T, nom, csp, script string) {
	t.Helper()
	directives := map[string]bool{}
	for _, d := range strings.Split(csp, ";") {
		f := strings.Fields(d)
		if len(f) == 0 {
			continue
		}
		directives[f[0]] = true
		for _, source := range f[1:] {
			if source != "'none'" && source != "'self'" {
				t.Errorf("%s : CSP %s autorise %s", nom, f[0], source)
			}
		}
	}
	for _, d := range []string{"default-src", "base-uri", "form-action", "frame-ancestors"} {
		if !directives[d] {
			t.Errorf("%s : CSP sans %s (%q)", nom, d, csp)
		}
	}
	if directives["script-src"] != (script != "") {
		t.Errorf("%s : script-src présent = %v, script %q", nom, directives["script-src"], script)
	}
	if directives["connect-src"] != (script == scriptVerifier) {
		t.Errorf("%s : connect-src présent = %v, script %q", nom, directives["connect-src"], script)
	}
}

// controleur vérifie les pages et garde les ressources déjà lues.
type controleur struct {
	t        *testing.T
	srv      http.Handler
	lues     map[string]page // ressources déjà lues, par adresse
	liens    map[string]string
	poids    map[string][3]int // HTML, autres ressources, polices
	statique int               // feuille de style et scripts, lus une fois
}

func (c *controleur) lire(nom, adresse string) page {
	c.t.Helper()
	if p, ok := c.lues[adresse]; ok {
		return p
	}
	p := (&navigateur{t: c.t, h: c.srv}).get(adresse)
	if p.statut != http.StatusOK {
		c.t.Errorf("%s : ressource %s, statut %d", nom, adresse, p.statut)
	}
	c.lues[adresse] = p
	if strings.HasSuffix(adresse, ".css") || strings.HasSuffix(adresse, ".js") {
		c.statique += len(p.corps)
	}
	return p
}

func estPolice(adresse string) bool {
	return strings.HasPrefix(adresse, "/statique/polices/") && strings.HasSuffix(adresse, ".woff2")
}

func (c *controleur) verifier(v pageVue) {
	t := c.t
	t.Helper()
	nom, p := v.nom, v.p
	if p.statut != v.statut {
		t.Errorf("%s (%s) : statut %d, attendu %d", nom, v.chemin, p.statut, v.statut)
	}
	if ct := p.entetes.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("%s : Content-Type %q", nom, ct)
	}
	if !strings.HasPrefix(p.corps, "<!doctype html>") {
		t.Errorf("%s : page incomplète", nom)
	}
	// Ressources chargées : toutes sur le domaine, lues pour en connaître
	// le poids ; la feuille de style est elle-même analysée.
	poids := [3]int{len(p.corps), 0, 0}
	vues := map[string]bool{}
	var suivre func(depuis, adresse string)
	suivre = func(depuis, adresse string) {
		chemin, ok := locale(depuis, adresse)
		if !ok {
			t.Errorf("%s : ressource hors du domaine %q (depuis %s)", nom, adresse, depuis)
			return
		}
		if vues[chemin] {
			return
		}
		vues[chemin] = true
		r := c.lire(nom, chemin)
		if estPolice(chemin) {
			poids[2] += len(r.corps)
			return
		}
		poids[1] += len(r.corps)
		if strings.HasSuffix(chemin, ".css") {
			for _, u := range urlsCSS(t, chemin, r.corps) {
				suivre(chemin, u)
			}
		}
	}
	for _, r := range ressourcesChargees(t, nom, p.corps) {
		suivre(v.chemin, r)
	}
	if total := poids[0] + poids[1]; total >= PoidsMaxPage {
		t.Errorf("%s pèse %d octets hors polices (HTML %d)", nom, total, poids[0])
	}
	c.poids[nom] = poids

	// Scripts : aucun script en ligne, au plus un script, celui attendu.
	var scripts []string
	for _, m := range reScript.FindAllStringSubmatch(p.corps, -1) {
		a := attributs(m[1])
		if strings.TrimSpace(m[2]) != "" || a["src"] == "" {
			t.Errorf("%s : script en ligne interdit", nom)
		}
		if !slices.Contains(scriptsAutorises, a["src"]) {
			t.Errorf("%s : script non autorisé %q", nom, a["src"])
		}
		scripts = append(scripts, a["src"])
	}
	if strings.Count(strings.ToLower(p.corps), "<script") != len(scripts) {
		t.Errorf("%s : balise <script> mal formée", nom)
	}
	if want := []string{v.script}; v.script == "" && len(scripts) != 0 || v.script != "" && !slices.Equal(scripts, want) {
		t.Errorf("%s : scripts %q, attendu %q", nom, scripts, v.script)
	}
	if reGestionnaire.MatchString(p.corps) || strings.Contains(strings.ToLower(p.corps), "javascript:") {
		t.Errorf("%s : gestionnaire d'évènement ou lien javascript:", nom)
	}
	verifierCSP(t, nom, p.entetes.Get("Content-Security-Policy"), v.script)
	for _, h := range []string{"X-Content-Type-Options", "Referrer-Policy", "X-Frame-Options"} {
		if p.entetes.Get(h) == "" {
			t.Errorf("%s : en-tête %s absent", nom, h)
		}
	}

	// Textes : aucune clé absente du catalogue, aucune mise en forme cassée,
	// et toute valeur « À COMPLÉTER » signalée comme telle.
	if strings.Contains(p.corps, "⟦") || strings.Contains(p.corps, "%!") {
		t.Errorf("%s : clé absente du catalogue ou mise en forme cassée", nom)
	}
	if n, signalees := strings.Count(p.corps, config.ACompleter), strings.Count(p.corps, marqueACompleter); n != signalees {
		t.Errorf("%s : %d « %s », dont %d signalés", nom, n, config.ACompleter, signalees)
	}

	// Navigation : même en-tête et même pied sur toutes les pages publiques.
	for _, lien := range []string{"/", "/observatoire", "/resultats", "/methodologie", "/confidentialite", "/journal"} {
		if n := strings.Count(p.corps, `href="`+lien+`"`); n < 2 {
			t.Errorf("%s : lien %s présent %d fois (en-tête et pied attendus)", nom, lien, n)
		}
	}

	// Liens : ceux du domaine sont gardés pour la recherche des liens
	// morts ; les ancres doivent exister dans la page.
	ids := map[string]bool{}
	for _, m := range reIdentifiant.FindAllStringSubmatch(p.corps, -1) {
		ids[m[1]] = true
	}
	for _, m := range reLienA.FindAllStringSubmatch(p.corps, -1) {
		h := strings.ReplaceAll(m[1], "&amp;", "&")
		switch {
		case strings.HasPrefix(h, "#"):
			if !ids[h[1:]] {
				t.Errorf("%s : ancre %s absente", nom, h)
			}
		case strings.HasPrefix(h, "https://"):
			// Lien sortant, suivi seulement si la personne clique : jamais
			// une ressource chargée par la page.
		default:
			if chemin, ok := locale(v.chemin, h); ok {
				if _, deja := c.liens[chemin]; !deja {
					c.liens[chemin] = nom
				}
			} else {
				t.Errorf("%s : lien inattendu %q", nom, h)
			}
		}
	}
}

// suivreLien vérifie que la page contient le lien, puis le suit, comme une
// personne qui clique.
func suivreLien(t *testing.T, n *navigateur, depuis page, chemin string) page {
	t.Helper()
	if !strings.Contains(depuis.corps, `href="`+chemin+`"`) {
		t.Fatalf("lien vers %s absent : %s", chemin, texte(depuis.corps))
	}
	return n.get(chemin)
}

// envoyerFormulaire envoie le formulaire de la page (premier formulaire)
// avec ces champs, comme le ferait un navigateur sans JavaScript.
func envoyerFormulaire(t *testing.T, n *navigateur, depuis page, attendu string, f url.Values) page {
	t.Helper()
	m := reFormAction.FindStringSubmatch(depuis.corps)
	if m == nil || m[1] != attendu {
		t.Fatalf("formulaire vers %s absent : %v", attendu, m)
	}
	return n.post(m[1], f)
}

func TestEnsembleDesPagesPubliques(t *testing.T) {
	pubs := &publicationsReglables{}
	sous := &publicationsMemoire{ps: []*publication.Publication{produire(t, jour1, aleatoires(999, 1), nil)}}
	audela := &publicationsMemoire{ps: []*publication.Publication{
		produire(t, jour2, aleatoires(3000, 2), nil), produire(t, jour1, aleatoires(999, 1), nil)}}
	pubs.regler(sous, false)
	var eng int64
	b := nouveauBanc(t, map[string]string{"DIGUEE_NUMERO_WHATSAPP": "+221 77 000 00 00"}, func(bd *base.Base, o *Options) {
		obs, id := remplirObservatoire(t, bd)
		eng = id
		jp := journal.Nouveau(bd, nil)
		es, err := jp.Toutes(context.Background())
		if err != nil || len(es) == 0 {
			t.Fatalf("journal public : %v, %d entrées", err, len(es))
		}
		j := time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)
		o.Observatoire, o.JournalPublic, o.Publications = obs, jp, pubs
		o.Ancrages = ancragesFixes{[]ancrage.Etat{{Jour: j, Preuve: preuveDeTest(t, j, es[len(es)-1], 870001), Archives: []ancrage.Archive{
			{Jour: j, Cible: ancrage.CibleJournal, Lien: "https://web.archive.org/web/20261130000000/https://consultation.diguee.sn/journal"},
		}}}}
	})
	c := &controleur{t: t, srv: b.srv, lues: map[string]page{}, liens: map[string]string{}, poids: map[string][3]int{}}
	var vues []pageVue
	voir := func(nom, chemin string, p page, statut int, script string) page {
		t.Helper()
		vues = append(vues, pageVue{nom: nom, chemin: chemin, p: p, statut: statut, script: script})
		return p
	}
	anonyme := func(chemin string) page { return (&navigateur{t: t, h: b.srv}).get(chemin) }

	// Pages publiques.
	voir("accueil", "/", anonyme("/"), http.StatusOK, "")
	voir("méthodologie", "/methodologie", anonyme("/methodologie"), http.StatusOK, "")
	voir("confidentialité", "/confidentialite", anonyme("/confidentialite"), http.StatusOK, "")
	voir("résultats sous le seuil", "/resultats", anonyme("/resultats"), http.StatusOK, "")
	pubs.regler(audela, false)
	voir("accueil au-delà du seuil", "/", anonyme("/"), http.StatusOK, "")
	voir("résultats au-delà du seuil", "/resultats", anonyme("/resultats"), http.StatusOK, "")
	voir("résultats d'un jour passé", "/resultats/2026-12-01", anonyme("/resultats/2026-12-01"), http.StatusOK, "")
	voir("liste des publications", "/resultats/publications", anonyme("/resultats/publications"), http.StatusOK, "")
	voir("journal", "/journal", anonyme("/journal"), http.StatusOK, "")
	voir("observatoire", "/observatoire", anonyme("/observatoire"), http.StatusOK, "")
	fiche := "/observatoire/engagements/" + strconv.FormatInt(eng, 10)
	voir("fiche d'engagement", fiche, anonyme(fiche), http.StatusOK, "")
	voir("page introuvable", "/nulle-part", anonyme("/nulle-part"), http.StatusNotFound, "")
	voir("résultats introuvables", "/resultats/2020-01-01", anonyme("/resultats/2020-01-01"), http.StatusNotFound, "")
	voir("fiche introuvable", "/observatoire/engagements/999999", anonyme("/observatoire/engagements/999999"), http.StatusNotFound, "")
	pubs.regler(audela, true)
	voir("base indisponible", "/", anonyme("/"), http.StatusServiceUnavailable, "")
	pubs.regler(audela, false)

	// Vérification inversée par WhatsApp, avec JavaScript (captcha résolu
	// par le test), puis le code par WhatsApp.
	n := &navigateur{t: t, h: b.srv}
	voir("vérification", "/verifier", n.get("/verifier"), http.StatusOK, scriptAltcha)
	expediteur, _ := numeroDeTest(31)
	jeton := demanderJeton(t, n)
	voir("jeton WhatsApp", "/verifier/whatsapp", n.get("/verifier/whatsapp"), http.StatusOK, scriptVerifier)
	voir("simulation WhatsApp", "/dev/whatsapp", n.get("/dev/whatsapp"), http.StatusOK, "")
	b.envoyerWhatsApp(t, expediteur, jeton)
	if p := n.post("/verifier/whatsapp", url.Values{}); p.location != "/questionnaire" {
		t.Fatalf("vérification inversée : %d %q", p.statut, p.location)
	}
	voir("questionnaire", "/questionnaire", n.get("/questionnaire"), http.StatusOK, "")
	voir("questionnaire avec erreurs", "/questionnaire/recapitulatif", n.post("/questionnaire/recapitulatif", url.Values{}), http.StatusUnprocessableEntity, "")
	n4 := &navigateur{t: t, h: b.srv}
	voir("code WhatsApp", "/verifier/code", n4.get("/verifier/code"), http.StatusOK, scriptAltcha)
	_, numeroCode := numeroDeTest(32)
	codeEnvoye(t, b, n4, numeroCode)
	voir("saisie du code WhatsApp", "/verifier/code/saisie", n4.get("/verifier/code/saisie"), http.StatusOK, "")
	if p := parcourir(t, b, expediteur, formulaireVoulu(3)); !strings.Contains(texte(p.corps), "Merci") {
		t.Fatalf("premier parcours : page Merci attendue, %d", p.statut)
	}
	voir("déjà participé", "/verifier/whatsapp", parcourir(t, b, expediteur, formulaireVoulu(3)), http.StatusConflict, "")

	// T13 : le parcours complet sans JavaScript, du lien de l'accueil à la
	// page Merci, par le secours SMS. Le navigateur du test n'exécute aucun
	// script : il suit les liens et envoie les formulaires, rien d'autre.
	s := &navigateur{t: t, h: b.srv, adresse: "198.51.100.40:1000"}
	p := voir("accueil (sans JavaScript)", "/", s.get("/"), http.StatusOK, "")
	p = voir("vérification (sans JavaScript)", "/verifier", suivreLien(t, s, p, "/verifier"), http.StatusOK, scriptAltcha)
	// Le formulaire envoyé sans solution du captcha mène à l'explication ;
	// le bloc <noscript> y mène aussi.
	if r := envoyerFormulaire(t, s, p, "/verifier", url.Values{"altcha": {""}}); r.statut != http.StatusSeeOther || r.location != "/verifier/sans-javascript" {
		t.Fatalf("vérification sans JavaScript : %d %q", r.statut, r.location)
	}
	if !strings.Contains(p.corps, `<noscript><p class="alerte">`) {
		t.Fatal("la page de vérification doit expliquer l'absence de JavaScript")
	}
	p = voir("sans JavaScript", "/verifier/sans-javascript", suivreLien(t, s, p, "/verifier/sans-javascript"), http.StatusOK, "")
	p = voir("SMS (sans JavaScript)", "/verifier/sms", suivreLien(t, s, p, "/verifier/sms"), http.StatusOK, scriptAltcha)
	_, numeroSMS := numeroDeTest(33)
	r := envoyerFormulaire(t, s, p, "/verifier/sms", url.Values{"numero": {numeroSMS}})
	if r.statut != http.StatusSeeOther || r.location != "/verifier/sms/saisie" {
		t.Fatalf("demande du SMS sans JavaScript : %d %q %s", r.statut, r.location, texte(r.corps))
	}
	p = voir("saisie du code SMS", "/verifier/sms/saisie", s.get(r.location), http.StatusOK, "")
	dev := voir("simulation des SMS", "/dev/sms", suivreLien(t, s, p, "/dev/sms"), http.StatusOK, "")
	m := reCodeDev.FindStringSubmatch(texte(dev.corps))
	if m == nil {
		t.Fatalf("code absent de /dev/sms : %s", texte(dev.corps))
	}
	voir("code SMS faux", "/verifier/sms/saisie", envoyerFormulaire(t, s, p, "/verifier/sms/saisie", url.Values{"code": {faux(m[1])}}), http.StatusBadRequest, "")
	r = envoyerFormulaire(t, s, p, "/verifier/sms/saisie", url.Values{"code": {m[1]}})
	if r.statut != http.StatusSeeOther || r.location != "/questionnaire" {
		t.Fatalf("code SMS juste : %d %q", r.statut, r.location)
	}
	p = voir("questionnaire (sans JavaScript)", "/questionnaire", s.get(r.location), http.StatusOK, "")
	p = voir("récapitulatif", "/questionnaire/recapitulatif", envoyerFormulaire(t, s, p, "/questionnaire/recapitulatif", formulaireVoulu(8)), http.StatusOK, "")
	_, caches := champs(p.corps)
	r = envoyerFormulaire(t, s, p, "/envoyer", caches)
	if r.statut != http.StatusSeeOther || r.location != "/merci" || s.cookie != nil {
		t.Fatalf("envoi sans JavaScript : %d %q", r.statut, r.location)
	}
	p = voir("merci", "/merci", s.get(r.location), http.StatusOK, "")
	if !strings.Contains(texte(p.corps), "Merci") {
		t.Fatalf("page Merci : %s", texte(p.corps))
	}
	voir("session expirée", "/envoyer", s.post("/envoyer", caches), http.StatusUnauthorized, "")
	if err := b.tampon.Vider(t.Context()); err != nil {
		t.Fatal(err)
	}
	if parts, reps := b.compter(t); parts != 2 || reps != 2 {
		t.Fatalf("base : %d participations, %d réponses (attendu 2 et 2)", parts, reps)
	}

	// Après la clôture : le parcours affiche « Consultation close ».
	b.horloge.avancer(200 * 24 * time.Hour)
	voir("consultation close", "/verifier", anonyme("/verifier"), http.StatusOK, "")
	voir("accueil après la clôture", "/", anonyme("/"), http.StatusOK, "")

	for _, v := range vues {
		c.verifier(v)
	}
	if !strings.Contains(texte(vues[len(vues)-2].p.corps), "Consultation close") {
		t.Error("page de clôture attendue")
	}

	// Liens morts : chaque lien du domaine trouvé dans ces pages répond, à
	// une personne sans session, autre chose que 404 ou une erreur.
	var liens []string
	for l := range c.liens {
		liens = append(liens, l)
	}
	sort.Strings(liens)
	for _, l := range liens {
		if p := anonyme(l); p.statut == http.StatusNotFound || p.statut == http.StatusMethodNotAllowed || p.statut >= 500 {
			t.Errorf("lien mort %s (trouvé sur « %s ») : statut %d", l, c.liens[l], p.statut)
		}
	}

	// Poids de la feuille de style et des scripts, puis des polices.
	if c.statique > PoidsMaxStatiques {
		t.Errorf("feuille de style et scripts : %d octets (au plus %d)", c.statique, PoidsMaxStatiques)
	}
	polices := 0
	for a, r := range c.lues {
		if estPolice(a) {
			polices += len(r.corps)
		}
	}
	if polices > PoidsMaxPolices || polices == 0 {
		t.Errorf("polices : %d octets (au plus %d)", polices, PoidsMaxPolices)
	}
	noms := make([]string, 0, len(c.poids))
	for nom := range c.poids {
		noms = append(noms, nom)
	}
	sort.Strings(noms)
	for _, nom := range noms {
		w := c.poids[nom]
		t.Logf("%-34s HTML %6d o, autres ressources %6d o, total hors polices %6d o, polices %6d o", nom, w[0], w[1], w[0]+w[1], w[2])
	}
	t.Logf("feuille de style + scripts : %d o ; polices : %d o ; %d pages, %d liens internes vérifiés", c.statique, polices, len(c.poids), len(liens))
}

var reFontFace = regexp.MustCompile(`(?s)@font-face\{([^}]*)\}`)
var reRegle = regexp.MustCompile(`(?s)([^{}]+)\{([^{}]*)\}`)

func declaration(regle, propriete string) string {
	for _, d := range strings.Split(regle, ";") {
		k, v, ok := strings.Cut(d, ":")
		if ok && strings.TrimSpace(k) == propriete {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// Les polices sont limitées aux graisses réellement utilisées, chargées
// sans bloquer l'affichage (font-display: swap), toutes servies depuis le
// domaine ; aucun fichier de police n'est servi sans être déclaré, aucune
// graisse déclarée n'est inutilisée, aucune graisse utilisée n'est absente
// (le navigateur prendrait la plus proche, ou la simulerait).
func TestPolicesLimiteesAuxGraissesUtilisees(t *testing.T) {
	cssOctets, err := fs.ReadFile(fichiersStatiques, "statique/diguee.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(cssOctets)
	declarees := map[string]string{} // famille/graisse → fichier
	for _, m := range reFontFace.FindAllStringSubmatch(css, -1) {
		famille := strings.Trim(declaration(m[1], "font-family"), `"`)
		graisse := declaration(m[1], "font-weight")
		if d := declaration(m[1], "font-display"); d != "swap" {
			t.Errorf("%s %s : font-display %q, attendu swap", famille, graisse, d)
		}
		src := reCSSURL.FindStringSubmatch(declaration(m[1], "src"))
		if src == nil || !strings.HasPrefix(src[3], "/statique/polices/") || !strings.Contains(declaration(m[1], "src"), `format("woff2")`) {
			t.Errorf("%s %s : source %q", famille, graisse, declaration(m[1], "src"))
			continue
		}
		declarees[famille+"/"+graisse] = path.Base(src[3])
	}
	fichiers, _ := fs.Glob(fichiersStatiques, "statique/polices/*")
	for _, f := range fichiers {
		nom := path.Base(f)
		if strings.HasPrefix(nom, "OFL-") && strings.HasSuffix(nom, ".txt") {
			continue // licences
		}
		trouve := false
		for _, d := range declarees {
			trouve = trouve || d == nom
		}
		if !trouve {
			t.Errorf("fichier de police servi sans être utilisé : %s", nom)
		}
	}
	// Graisses utilisées : chaque règle qui donne une graisse ; la famille
	// est celle de la règle, sinon celle du texte courant (Inter).
	utilisees := map[string]string{"Inter/400": "body"}
	sansFontFace := reFontFace.ReplaceAllString(css, "")
	sansFontFace = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(sansFontFace, "")
	for _, m := range reRegle.FindAllStringSubmatch(sansFontFace, -1) {
		selecteur, regle := strings.TrimSpace(m[1]), m[2]
		famille := "Inter"
		if f := declaration(regle, "font-family"); f != "" && f != "inherit" {
			famille = strings.Trim(strings.Split(f, ",")[0], `"`)
		}
		graisse := declaration(regle, "font-weight")
		if graisse == "" {
			if famille == "Bricolage Grotesque" {
				t.Errorf("%s : Bricolage Grotesque sans graisse explicite", selecteur)
			}
			continue
		}
		if famille != "Inter" && famille != "Bricolage Grotesque" {
			continue
		}
		cle := famille + "/" + graisse
		if _, ok := declarees[cle]; !ok {
			t.Errorf("%s : graisse %s de %s non servie", selecteur, graisse, famille)
		}
		utilisees[cle] = selecteur
	}
	for cle, fichier := range declarees {
		if _, ok := utilisees[cle]; !ok {
			t.Errorf("graisse servie mais inutilisée : %s (%s)", cle, fichier)
		}
	}
	if len(declarees) != 4 {
		t.Errorf("polices déclarées : %v (Inter 400 et 600, Bricolage Grotesque 600 et 800 attendues)", declarees)
	}
}
