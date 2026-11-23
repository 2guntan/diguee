package observatoire

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/i18n"
	"github.com/2guntan/diguee/internal/pdf/pdftest"
	"github.com/2guntan/diguee/internal/stockage"
)

func jourDe(s string) time.Time { j, _ := time.Parse(time.DateOnly, s); return j }

// Le dossier PDF reprend la citation, les sources (lien et SHA-256), le
// statut proposé et son explication, les preuves, le délai et l'adresse
// de réponse.
func TestDossierReprendCitationSourcesEtStatut(t *testing.T) {
	cs, err := i18n.Charger()
	if err != nil {
		t.Fatal(err)
	}
	src := Source{ID: 1, URL: "https://exemple.sn/discours?id=1&lang=fr", Titre: "Discours d'installation", Date: jourDe("2024-04-02"),
		Archive: stockage.Calculer([]byte("discours"))}
	preuve := Source{ID: 2, URL: "https://exemple.sn/rapport", Titre: "Rapport du conseil", Date: jourDe("2026-11-30"),
		Archive: stockage.Calculer([]byte("rapport"))}
	e := &Engagement{ID: 7, Elu: Elu{Nom: "Aminata Ndiaye", Fonction: "Maire de Thiès", ContactOfficiel: "cabinet@thies.example",
		Mandat: Mandat{Debut: jourDe("2022-01-23"), Fin: jourDe("2027-01-23")}},
		Citation: "Nous construirons dix écoles avant la fin de 2026.", DateDeclaration: jourDe("2024-04-02"), Echeance: jourDe("2026-12-31"),
		Sources: []Source{src}}
	st := &Statut{ID: 3, Valeur: NonTenu, Explication: "Aucune école n'a été construite : le chantier n'a pas démarré.",
		Redige: Acte{ParNom: "Awa", Le: jourDe("2026-12-01")}, Validation: &Acte{ParNom: "Moussa", Le: jourDe("2026-12-02")},
		Preuves: []Source{preuve}}
	b := ComposerDossier(e, st, ReglagesPreavis{Jours: 15, AdresseReponse: "reponse@diguee.example", Textes: cs.Choisir(i18n.Reference)})
	pdftest.Verifier(t, b)
	if string(b) != string(ComposerDossier(e, st, ReglagesPreavis{Jours: 15, AdresseReponse: "reponse@diguee.example",
		Textes: cs.Choisir(i18n.Reference)})) {
		t.Error("le dossier n'est pas déterministe")
	}
	texte, ok := pdftest.TexteDe(t, b)
	if !ok {
		t.Skip("pdftotext absent : extraction du texte non vérifiée")
	}
	texte = strings.Join(strings.Fields(texte), " ")
	for _, attendu := range []string{"Aminata Ndiaye", "Maire de Thiès", "« Nous construirons dix écoles avant la fin de 2026. »",
		"https://exemple.sn/discours?id=1&lang=fr", src.Archive.String(), preuve.Archive.String(), "Statut proposé : Non tenu",
		st.Explication, "15 jours", "15e jour", "reponse@diguee.example", "Sollicité le JJ/MM/AAAA, pas de réponse",
		"engagement nº 7, statut nº 3", "Moussa", "31 décembre 2026", "courrier signé", "page 1 sur"} {
		if !strings.Contains(texte, attendu) {
			t.Errorf("%q absent du dossier", attendu)
		}
	}
	if strings.Contains(texte, "cabinet@thies.example") {
		t.Error("le dossier recopie le contact officiel de l'élu")
	}
}

// Aucun envoi à l'élu n'est fait par la plateforme : le paquet de
// l'observatoire n'importe aucun client réseau ni de messagerie.
func TestAucunEnvoiAutomatique(t *testing.T) {
	fichiers, _ := filepath.Glob("*.go")
	interdits := []string{"net", "net/http", "net/smtp", "net/mail", "github.com/2guntan/diguee/internal/whatsapp",
		"github.com/2guntan/diguee/internal/sms", "github.com/2guntan/diguee/internal/codes"}
	for _, f := range fichiers {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range af.Imports {
			chemin, _ := strconv.Unquote(imp.Path.Value)
			for _, x := range interdits {
				if chemin == x {
					t.Errorf("%s importe %s", f, chemin)
				}
			}
		}
	}
	if _, ok := any((*Service)(nil)).(interface{ Envoyer() }); ok {
		t.Error("le service sait envoyer")
	}
}

func TestDelaiMinimal(t *testing.T) {
	s := NouveauService(nil, nil, 0, nil)
	s.ReglerPreavis(ReglagesPreavis{Jours: 3})
	if s.JoursPreavis() != DelaiMinimal {
		t.Errorf("délai %d", s.JoursPreavis())
	}
	s.ReglerPreavis(ReglagesPreavis{Jours: 30})
	if s.JoursPreavis() != 30 {
		t.Errorf("délai %d", s.JoursPreavis())
	}
	for _, v := range Valeurs {
		if v.DemandePreavis() != (v == NonTenu || v == Partiel) {
			t.Errorf("%s", v)
		}
	}
}
