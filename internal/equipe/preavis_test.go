package equipe_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/equipe"
	"github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/pdf/pdftest"
)

// Préavis et droit de réponse par les formulaires de l'espace équipe : le
// dossier PDF, l'enregistrement de l'envoi (aucun envoi par la plateforme),
// la publication bloquée jusqu'au 15e jour (T9 par l'API web), le droit de
// réponse vérifié par un autre membre et publié tel quel, la mention
// « Sollicité le …, pas de réponse » sans réponse.
func TestPreavisEtDroitDeReponseDeBoutEnBout(t *testing.T) {
	b := nouveauBanc(t) // horloge : 1er décembre 2026
	ctx := context.Background()
	awa := b.creer(t, "awa", equipe.Redacteur, equipe.Relecteur)
	moussa := b.creer(t, "moussa", equipe.Relecteur, equipe.Redacteur)
	ca, cm := b.client(t), b.client(t)
	var jeton, jetonM string
	connecter := func() {
		t.Helper()
		ca.connecterCompte(awa)
		b.horloge.Avancer(30 * time.Second)
		cm.connecterCompte(moussa)
		jeton, jetonM = ca.csrf("/equipe/observatoire/elus"), cm.csrf("/equipe/observatoire/elus")
	}
	// Avance l'horloge au jour donné (10 h), puis reconnecte : une session
	// dure 8 heures.
	allerAu := func(jour string) {
		t.Helper()
		j, _ := time.Parse(time.DateOnly, jour)
		b.horloge.Avancer(j.Add(10 * time.Hour).Sub(b.horloge.Maintenant()))
		connecter()
	}
	connecter()

	attendreNB(t, "élu", ca.post("/equipe/observatoire/elus", url.Values{"csrf": {jeton}, "nom": {"Aminata Ndiaye"},
		"fonction": {"Maire de Thiès"}, "mandat_debut": {"2022-01-23"}, "mandat_fin": {"2027-01-23"},
		"contact": {"cabinet@thies.example (courriel officiel)"}}), http.StatusSeeOther)
	attendreNB(t, "source", ca.televerser(map[string]string{"csrf": jeton, "titre": "Discours", "url": "https://exemple.sn/discours",
		"date": "2024-04-02"}, []byte("%PDF-1.4\n% discours\n")), http.StatusSeeOther)
	citation := "Nous construirons dix écoles avant la fin de 2026."
	attendreNB(t, "engagement", ca.post("/equipe/observatoire/engagements", url.Values{"csrf": {jeton}, "elu": {"1"},
		"citation": {citation}, "date_declaration": {"2024-04-02"}, "echeance": {"2026-11-30"}, "sources": {"1"}}), http.StatusSeeOther)
	attendreNB(t, "validation", cm.post("/equipe/observatoire/engagements/1/valider", url.Values{"csrf": {jetonM}}), http.StatusSeeOther)
	for i, v := range []string{"non_tenu", "partiel"} {
		chemin := "/equipe/observatoire/statuts/" + string(rune('1'+i))
		attendreNB(t, "brouillon", ca.post("/equipe/observatoire/engagements/1/statuts", url.Values{"csrf": {jeton}, "valeur": {v},
			"explication": {"Aucune école n'est construite à l'échéance."}, "preuves": {"1"}}), http.StatusSeeOther)
		attendreNB(t, "proposition", ca.post(chemin+"/proposer", url.Values{"csrf": {jeton}}), http.StatusSeeOther)
		attendreNB(t, "dossier avant validation", ca.get(chemin+"/dossier.pdf"), http.StatusBadRequest)
		attendreNB(t, "validation", cm.post(chemin+"/valider", url.Values{"csrf": {jetonM}}), http.StatusSeeOther)
	}
	attendreNB(t, "tableau : préavis à envoyer", ca.get("/equipe/observatoire"), http.StatusOK, "Préavis à envoyer", "Statut nº 1", "Statut nº 2")

	// Le dossier : un PDF à télécharger, envoyé par un membre lui-même.
	attendreNB(t, "page du statut", ca.get("/equipe/observatoire/statuts/1"), http.StatusOK,
		"Télécharger le dossier de l'élu (PDF)", "cabinet@thies.example (courriel officiel)", "La plateforme n'envoie rien")
	d := ca.get("/equipe/observatoire/statuts/1/dossier.pdf")
	if d.statut != http.StatusOK || d.entetes.Get("Content-Type") != "application/pdf" ||
		!strings.HasPrefix(d.entetes.Get("Content-Disposition"), "attachment;") || d.entetes.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("dossier : %d %v", d.statut, d.entetes)
	}
	pdftest.Verifier(t, []byte(d.corps))
	if texte, ok := pdftest.TexteDe(t, []byte(d.corps)); ok {
		texte = strings.Join(strings.Fields(texte), " ")
		for _, attendu := range []string{citation, "https://exemple.sn/discours", "Statut proposé : Non tenu", "15 jours"} {
			if !strings.Contains(texte, attendu) {
				t.Errorf("dossier : %q absent", attendu)
			}
		}
	}
	if n, _ := journal.Nouveau(b.base, nil).Compter(ctx); n != 0 {
		t.Fatalf("journal public : %d entrée(s) avant toute publication", n)
	}

	// L'envoi enregistré : CSRF, date contrôlée.
	attendreNB(t, "préavis sans CSRF", ca.post("/equipe/observatoire/statuts/1/preavis", url.Values{"envoye_le": {"2026-12-01"},
		"moyen": {"x"}}), http.StatusForbidden)
	attendreNB(t, "préavis dans le futur", ca.post("/equipe/observatoire/statuts/1/preavis", url.Values{"csrf": {jeton},
		"envoye_le": {"2026-12-02"}, "moyen": {"Courriel au cabinet"}}), http.StatusBadRequest, "ni dans le futur")
	for _, s := range []string{"1", "2"} {
		attendreNB(t, "préavis", ca.post("/equipe/observatoire/statuts/"+s+"/preavis", url.Values{"csrf": {jeton},
			"envoye_le": {"2026-12-01"}, "moyen": {"Courriel au cabinet, accusé de réception"}}), http.StatusSeeOther)
	}
	attendreNB(t, "publication pendant le préavis", cm.post("/equipe/observatoire/statuts/1/publier", url.Values{"csrf": {jetonM}}),
		http.StatusBadRequest, "pas échu")
	attendreNB(t, "page du statut préavisé", ca.get("/equipe/observatoire/statuts/1"), http.StatusOK,
		"publiable à partir du 16 décembre 2026", "Courriel au cabinet")
	attendreNB(t, "tableau : préavis en cours", ca.get("/equipe/observatoire"), http.StatusOK, "Préavis en cours",
		"envoyé le 1 décembre 2026, publiable à partir du 16 décembre 2026")

	// Un droit de réponse reçu le 3 décembre, saisi par awa, vérifié par moussa.
	allerAu("2026-12-03")
	texteElu := "Monsieur le rédacteur,\n\nLes travaux ont commencé ; <b>trois</b> écoles ouvriront en janvier.\n\nLe maire"
	attendreNB(t, "réponse sans canal", ca.post("/equipe/observatoire/statuts/1/reponses", url.Values{"csrf": {jeton},
		"texte": {texteElu}, "canal_detail": {"x"}, "recu_le": {"2026-12-03"}}), http.StatusBadRequest, "Choisissez le canal")
	p := attendreNB(t, "réponse", ca.post("/equipe/observatoire/statuts/1/reponses", url.Values{"csrf": {jeton}, "texte": {texteElu},
		"canal": {"courrier_signe"}, "canal_detail": {"Lettre signée du maire, référence 2026-118"}, "recu_le": {"2026-12-03"}}), http.StatusSeeOther)
	if p.location != "/equipe/observatoire/reponses/1?fait=reponse" {
		t.Fatalf("redirection %s", p.location)
	}
	attendreNB(t, "tableau : réponse à vérifier", cm.get("/equipe/observatoire"), http.StatusOK, "Droits de réponse à vérifier", "Droit de réponse nº 1")
	attendreNB(t, "page de la réponse", ca.get("/equipe/observatoire/reponses/1"), http.StatusOK, "<b>trois</b>", "un autre membre doit la relire")
	attendreNB(t, "vérification par awa", ca.post("/equipe/observatoire/reponses/1/verifier", url.Values{"csrf": {jeton}}),
		http.StatusBadRequest, "une autre personne que le rédacteur")
	attendreNB(t, "vérification", cm.post("/equipe/observatoire/reponses/1/verifier", url.Values{"csrf": {jetonM}}), http.StatusSeeOther)
	attendreNB(t, "réponse vérifiée", cm.get("/equipe/observatoire/reponses/1?fait=reponse_verifiee"), http.StatusOK, "il sera publié avec le statut")

	// Le 15 décembre (J+14) : toujours refusé ; le 16 (J+15) : publié.
	allerAu("2026-12-15")
	attendreNB(t, "publication à J+14", cm.post("/equipe/observatoire/statuts/1/publier", url.Values{"csrf": {jetonM}}),
		http.StatusBadRequest, "pas échu")
	allerAu("2026-12-16")
	attendreNB(t, "tableau : à publier", cm.get("/equipe/observatoire"), http.StatusOK, "Statuts validés, à publier")
	for _, s := range []string{"1", "2"} {
		attendreNB(t, "publication à J+15", cm.post("/equipe/observatoire/statuts/"+s+"/publier", url.Values{"csrf": {jetonM}}), http.StatusSeeOther)
	}
	es, err := journal.Nouveau(b.base, nil).Toutes(ctx)
	if err != nil || len(es) != 3 || es[0].Type != journal.StatutPublie || es[1].Type != journal.DroitDeReponse || es[2].Type != journal.StatutPublie {
		t.Fatalf("journal : %+v %v", es, err)
	}

	public := b.client(t)
	fiche := attendreNB(t, "fiche publique", public.get("/observatoire/engagements/1"), http.StatusOK, citation,
		"Droit de réponse de l'élu", "Reçu le 3 décembre 2026 par courrier signé · publié le 16 décembre 2026",
		"Sollicité le 01/12/2026.", "Sollicité le 01/12/2026, pas de réponse", "entrée nº 2")
	if !strings.Contains(fiche.corps, `<div class="reponse">`+strings.ReplaceAll(strings.ReplaceAll(texteElu, "<", "&lt;"), ">", "&gt;")+`</div>`) {
		t.Error("le droit de réponse n'est pas publié mot pour mot")
	}
	for _, interdit := range []string{"Lettre signée du maire", "Courriel au cabinet", "cabinet@thies.example"} {
		if strings.Contains(fiche.corps, interdit) {
			t.Errorf("la fiche publique montre %q", interdit)
		}
	}
	if len(fiche.corps) >= 100*1024 || strings.Contains(strings.ToLower(fiche.corps), "<script") {
		t.Errorf("fiche : %d octets ou script", len(fiche.corps))
	}

	// Après la publication : un droit de réponse au partiel, publié dès qu'il
	// est vérifié ; la mention « pas de réponse » disparaît.
	allerAu("2026-12-20")
	attendreNB(t, "réponse après publication", cm.post("/equipe/observatoire/statuts/2/reponses", url.Values{"csrf": {jetonM},
		"texte": {"Nous contestons ce statut."}, "canal": {"compte_officiel"}, "canal_detail": {"compte officiel de la mairie"},
		"recu_le": {"2026-12-19"}}), http.StatusSeeOther)
	p = attendreNB(t, "vérification après publication", ca.post("/equipe/observatoire/reponses/2/verifier", url.Values{"csrf": {jeton}}), http.StatusSeeOther)
	if !strings.HasSuffix(p.location, "fait=reponse_publiee") {
		t.Errorf("redirection %s", p.location)
	}
	fiche = attendreNB(t, "fiche après la seconde réponse", public.get("/observatoire/engagements/1"), http.StatusOK,
		"Nous contestons ce statut.", "Reçu le 19 décembre 2026 par compte officiel de l'élu · publié le 20 décembre 2026")
	if strings.Contains(fiche.corps, "pas de réponse") {
		t.Error("mention « pas de réponse » malgré un droit de réponse publié")
	}
	if es, _ := journal.Nouveau(b.base, nil).Toutes(ctx); len(es) != 4 || es[3].Type != journal.DroitDeReponse {
		t.Errorf("journal : %d entrée(s)", len(es))
	}
	attendreNB(t, "journal public", public.get("/journal"), http.StatusOK)

	for _, chemin := range []string{"/equipe/observatoire", "/equipe/observatoire/statuts/1", "/equipe/observatoire/statuts/2",
		"/equipe/observatoire/reponses/1", "/equipe/observatoire/reponses/2", "/equipe/observatoire/engagements/1"} {
		p := attendreNB(t, chemin, ca.get(chemin), http.StatusOK)
		if strings.Contains(strings.ToLower(p.corps), "<script") || len(p.corps) > 100_000 {
			t.Errorf("%s : script ou page trop lourde", chemin)
		}
	}
	attendreNB(t, "réponse inconnue", ca.get("/equipe/observatoire/reponses/99"), http.StatusNotFound)
	if j := b.journal.String(); strings.Contains(j, "travaux") || strings.Contains(j, "Lettre signée") {
		t.Error("le journal applicatif contient une saisie")
	}
}

// espacesFines met dans un texte attendu les espaces insécables du
// catalogue (avant « : », dans les guillemets, après « nº »).
var espacesFines = strings.NewReplacer("nº ", "nº\u00a0", " :", "\u00a0:", "« ", "«\u00a0", " »", "\u00a0»")

func attendreNB(t *testing.T, quoi string, p reponse, statut int, contient ...string) reponse {
	t.Helper()
	for i := range contient {
		contient[i] = espacesFines.Replace(contient[i])
	}
	return attendre(t, quoi, p, statut, contient...)
}
