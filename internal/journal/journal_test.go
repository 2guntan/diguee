package journal_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/journal/journaltest"
)

func TestCanonique(t *testing.T) {
	cas := map[string]string{
		`{ "b" : 2, "a" : [1, true, null, "x"] }`: `{"a":[1,true,null,"x"],"b":2}`,
		`{"texte":"<é>&\u2028/\n\u0001\""}`:       "{\"texte\":\"<é>&\u2028/\\n\\u0001\\\"\"}",
		`-0`:                                      `0`,
		`{"z":{"b":{},"a":[]}}`:                   `{"z":{"a":[],"b":{}}}`,
		// RFC 8785 §3.2.3 : tri par unités UTF-16 (l'émoji passe avant U+FB33).
		`{"\u20ac":1,"\r":2,"\ufb33":3,"1":4,"\ud83d\ude00":5,"\u0080":6,"\u00f6":7}`: "{\"\\r\":2,\"1\":4,\"\u0080\":6,\"ö\":7,\"€\":1,\"😀\":5,\"\ufb33\":3}",
	}
	for entree, attendu := range cas {
		got, err := journal.Canonique([]byte(entree))
		if err != nil || string(got) != attendu {
			t.Errorf("Canonique(%s) = %s, %v ; attendu %s", entree, got, err, attendu)
		}
	}
	for _, refuse := range []string{`{"a":1.5}`, `{"a":1e3}`, `{"a":9007199254740992}`, `{"a":1,"a":2}`,
		`{"a":1} {}`, "{\"a\":\"\xff\"}", `{"a":`} {
		if got, err := journal.Canonique([]byte(refuse)); err == nil {
			t.Errorf("Canonique(%s) aurait dû échouer : %s", refuse, got)
		}
	}
}

// Une empreinte calculée à la main, pour que toute autre implémentation du
// script de vérification puisse se comparer à celle-ci.
func TestEmpreinteDeReference(t *testing.T) {
	e, err := journal.Suivante(nil, time.Date(2026, 11, 23, 6, 0, 42, 0, time.UTC), journal.MiseEnProduction,
		json.RawMessage(`{"version":"v1.0.0","sha256_executable":"`+strings.Repeat("ab", 32)+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	attenduJSON := `{"contenu":{"sha256_executable":"` + strings.Repeat("ab", 32) + `","version":"v1.0.0"},` +
		`"empreinte_precedente":"` + strings.Repeat("0", 64) + `","horodatage":"2026-11-23T06:00:00Z",` +
		`"numero":1,"type":"mise_en_production"}`
	c, _ := e.JSONCanonique()
	if string(c) != attenduJSON {
		t.Fatalf("JSON canonique :\n%s\nattendu :\n%s", c, attenduJSON)
	}
	if e.Empreinte != sha256.Sum256([]byte(attenduJSON)) {
		t.Fatal("l'empreinte n'est pas le SHA-256 du JSON canonique")
	}
}

type horloge struct {
	mu sync.Mutex
	t  time.Time
}

func (h *horloge) maintenant() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.t = h.t.Add(37 * time.Second)
	return h.t
}

func remplir(t *testing.T, j *journal.Journal, n int) {
	t.Helper()
	ctx := context.Background()
	for i := range n {
		ty := journal.Types()[i%len(journal.Types())]
		if _, err := j.Inscrire(ctx, ty, map[string]any{"rang": i, "texte": fmt.Sprintf("entrée %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
}

func exporter(t *testing.T, j *journal.Journal) []byte {
	t.Helper()
	es, err := j.Toutes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := journal.Exporter(&buf, es); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func nouveau() *journal.Journal {
	h := &horloge{t: time.Date(2026, 11, 23, 5, 59, 0, 0, time.UTC)}
	return journal.Nouveau(&journaltest.Memoire{}, h.maintenant)
}

func TestChaineIntacte(t *testing.T) {
	j := nouveau()
	remplir(t, j, 12)
	es, rap, err := journal.LireExport(bytes.NewReader(exporter(t, j)))
	if err != nil {
		t.Fatal(err)
	}
	if rap.Entrees != 12 || len(es) != 12 || rap.Tete != es[11].Empreinte {
		t.Fatalf("rapport %+v", rap)
	}
	for i, e := range es {
		if e.Numero != int64(i+1) || e.Horodatage.Second() != 0 || e.Horodatage.Location() != time.UTC {
			t.Errorf("entrée %d : %+v", i, e)
		}
	}
	// Journal vide : valide.
	if _, rap, err := journal.LireExport(bytes.NewReader(exporter(t, nouveau()))); err != nil || rap.Entrees != 0 {
		t.Errorf("journal vide : %v, %+v", err, rap)
	}
}

// modifier décode l'export, applique f, puis le réencode.
func modifier(t *testing.T, brut []byte, f func(entrees []map[string]any) []map[string]any) []byte {
	t.Helper()
	var x map[string]any
	dec := json.NewDecoder(bytes.NewReader(brut))
	dec.UseNumber()
	if err := dec.Decode(&x); err != nil {
		t.Fatal(err)
	}
	var es []map[string]any
	for _, e := range x["entrees"].([]any) {
		es = append(es, e.(map[string]any))
	}
	es = f(es)
	x["entrees"] = es
	x["nombre_entrees"] = len(es)
	if len(es) > 0 {
		x["tete"] = es[len(es)-1]["empreinte"]
	}
	b, _ := json.Marshal(x)
	return b
}

// rechainer recalcule toutes les empreintes, comme le ferait un faussaire.
func rechainer(t *testing.T, es []map[string]any) []map[string]any {
	t.Helper()
	prec := strings.Repeat("0", 64)
	for _, e := range es {
		e["empreinte_precedente"] = prec
		c, err := journal.CanoniqueDe(map[string]any{"numero": e["numero"], "horodatage": e["horodatage"],
			"type": e["type"], "contenu": e["contenu"], "empreinte_precedente": prec})
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(c)
		prec = journal.Empreinte(h).String()
		e["empreinte"] = prec
	}
	return es
}

// T11 (spec §14) : toute modification d'une entrée ancienne est détectée.
func TestT11ModificationDetectee(t *testing.T) {
	j := nouveau()
	remplir(t, j, 10)
	brut := exporter(t, j)
	cas := map[string]func([]map[string]any) []map[string]any{
		"contenu": func(es []map[string]any) []map[string]any {
			es[2]["contenu"].(map[string]any)["texte"] = "réécrite"
			return es
		},
		"nombre ajouté au contenu": func(es []map[string]any) []map[string]any {
			es[0]["contenu"].(map[string]any)["total"] = 1
			return es
		},
		"horodatage": func(es []map[string]any) []map[string]any {
			es[3]["horodatage"] = "2026-11-23T05:00:00Z"
			return es
		},
		"type": func(es []map[string]any) []map[string]any {
			es[1]["type"] = string(journal.Cloture)
			return es
		},
		"numéro": func(es []map[string]any) []map[string]any {
			es[4]["numero"] = 40
			return es
		},
		"empreinte remplacée": func(es []map[string]any) []map[string]any {
			es[2]["empreinte"] = strings.Repeat("1", 64)
			return es
		},
		"entrée retirée": func(es []map[string]any) []map[string]any {
			return append(es[:5], es[6:]...)
		},
		"première entrée retirée": func(es []map[string]any) []map[string]any {
			return es[1:]
		},
		"entrées inversées": func(es []map[string]any) []map[string]any {
			es[6], es[7] = es[7], es[6]
			return es
		},
		"champ non haché ajouté": func(es []map[string]any) []map[string]any {
			es[2]["note"] = "ajoutée"
			return es
		},
		"champ retiré": func(es []map[string]any) []map[string]any {
			delete(es[2], "empreinte_precedente")
			return es
		},
		"horodatage à la seconde": func(es []map[string]any) []map[string]any {
			es[0]["horodatage"] = strings.Replace(es[0]["horodatage"].(string), ":00Z", ":07Z", 1)
			return rechainer(t, es)
		},
	}
	for nom, f := range cas {
		x := modifier(t, brut, f)
		if _, _, err := journal.LireExport(bytes.NewReader(x)); err == nil {
			t.Errorf("%s : modification non détectée", nom)
		}
	}

	// Un faussaire qui recalcule toute la chaîne obtient une chaîne cohérente,
	// mais l'empreinte de tête publiée hors de nos serveurs, ou un export
	// téléchargé plus tôt, le trahit.
	ancien, rapAncien, err := journal.LireExport(bytes.NewReader(brut))
	if err != nil {
		t.Fatal(err)
	}
	faux := modifier(t, brut, func(es []map[string]any) []map[string]any {
		es[2]["contenu"].(map[string]any)["texte"] = "réécrite"
		return rechainer(t, es)
	})
	esFaux, _, err := journal.LireExport(bytes.NewReader(faux))
	if err != nil {
		t.Fatalf("la chaîne recalculée devrait être cohérente : %v", err)
	}
	if journal.Contient(esFaux, rapAncien.Tete) {
		t.Error("l'empreinte de tête publiée ne devrait plus apparaître")
	}
	if err := journal.VerifierPrefixe(ancien, esFaux); err == nil {
		t.Error("la comparaison avec l'export ancien devrait échouer")
	}
	var ec *journal.ErreurChaine
	if err := journal.VerifierPrefixe(ancien, esFaux); !errors.As(err, &ec) || ec.Position != 3 {
		t.Errorf("l'entrée 3 devrait être désignée : %v", err)
	}

	// Le journal qui grandit honnêtement garde l'ancien comme début exact.
	remplir(t, j, 3)
	actuel, _, err := journal.LireExport(bytes.NewReader(exporter(t, j)))
	if err != nil || journal.VerifierPrefixe(ancien, actuel) != nil || !journal.Contient(actuel, rapAncien.Tete) {
		t.Errorf("croissance honnête refusée : %v", err)
	}
	if err := journal.VerifierPrefixe(actuel, ancien); err == nil {
		t.Error("un journal raccourci doit être refusé")
	}
}

func TestEcrituresSimultanees(t *testing.T) {
	j := nouveau()
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := j.Inscrire(context.Background(), journal.Methodologie, map[string]int{"i": i}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	es, _ := j.Toutes(context.Background())
	if rap, err := journal.VerifierChaine(es); err != nil || rap.Entrees != 50 {
		t.Fatalf("%v, %+v", err, rap)
	}
}

func TestMiseEnProductionSansDoublon(t *testing.T) {
	j := nouveau()
	ctx := context.Background()
	shaA, shaB := strings.Repeat("a", 64), strings.Repeat("b", 64)
	etapes := []struct {
		version, sha string
		inscrite     bool
	}{
		{"v1", shaA, true},
		{"v1", shaA, false}, // redémarrage
		{"v1", shaA, false},
		{"v2", shaB, true}, // nouvelle version
		{"v2", shaB, false},
		{"v1", shaA, true}, // retour en arrière : nouvelle mise en production
	}
	for i, e := range etapes {
		if _, err := j.Inscrire(ctx, journal.Methodologie, map[string]int{"i": i}); err != nil {
			t.Fatal(err)
		}
		got, ins, err := j.InscrireMiseEnProduction(ctx, e.version, e.sha)
		if err != nil || ins != e.inscrite {
			t.Fatalf("étape %d : inscrite=%v, %v", i, ins, err)
		}
		var c journal.ContenuMiseEnProduction
		_ = json.Unmarshal(got.Contenu, &c)
		if got.Type != journal.MiseEnProduction || c.Version != e.version || c.SHA256Executable != e.sha {
			t.Errorf("étape %d : %+v", i, got)
		}
	}
	es, _ := j.Toutes(ctx)
	n := 0
	for _, e := range es {
		if e.Type == journal.MiseEnProduction {
			n++
		}
	}
	if n != 3 {
		t.Errorf("%d mises en production, 3 attendues", n)
	}
	// Démarrages simultanés sur une nouvelle version : une seule entrée.
	var wg sync.WaitGroup
	compte := make(chan bool, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ins, err := j.InscrireMiseEnProduction(ctx, "v3", strings.Repeat("c", 64))
			if err != nil {
				t.Error(err)
			}
			compte <- ins
		}()
	}
	wg.Wait()
	close(compte)
	k := 0
	for ins := range compte {
		if ins {
			k++
		}
	}
	if k != 1 {
		t.Errorf("%d entrées pour des démarrages simultanés, 1 attendue", k)
	}
	if _, _, err := j.InscrireMiseEnProduction(ctx, "v4", "pas-un-sha"); err == nil {
		t.Error("SHA-256 invalide accepté")
	}
}

func TestContenusRefuses(t *testing.T) {
	j := nouveau()
	ctx := context.Background()
	for nom, c := range map[string]any{
		"type inconnu":     nil,
		"tableau":          []int{1},
		"chaîne":           "x",
		"virgule":          map[string]float64{"part": 0.5},
		"numéro":           map[string]string{"x": "+221 77 123 45 67"},
		"numéro sans +221": map[string]any{"x": []string{"771234567"}},
		"adresse IP":       map[string]string{"source": "192.0.2.10"},
	} {
		ty := journal.PublicationQuotidienne
		if nom == "type inconnu" {
			ty, c = "inconnu", map[string]int{}
		}
		if _, err := j.Inscrire(ctx, ty, c); err == nil {
			t.Errorf("%s : contenu accepté", nom)
		}
	}
	// Des agrégats et des empreintes passent.
	if _, err := j.Inscrire(ctx, journal.PublicationQuotidienne, map[string]any{
		"jour": "2027-01-16", "total": 1234, "instantane_sha256": strings.Repeat("7", 64), "heure": "06:00Z",
	}); err != nil {
		t.Error(err)
	}
}

// L'horloge qui recule ne casse pas l'ordre du journal.
func TestHorlogeQuiRecule(t *testing.T) {
	heures := []time.Time{
		time.Date(2026, 11, 23, 6, 1, 0, 0, time.UTC),
		time.Date(2026, 11, 23, 6, 0, 30, 0, time.UTC),
	}
	i := 0
	j := journal.Nouveau(&journaltest.Memoire{}, func() time.Time { i++; return heures[i-1] })
	remplir(t, j, 2)
	es, _ := j.Toutes(context.Background())
	if _, err := journal.VerifierChaine(es); err != nil || !es[1].Horodatage.Equal(es[0].Horodatage) {
		t.Errorf("%v, %v", err, es[1].Horodatage)
	}
}
