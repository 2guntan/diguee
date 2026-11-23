package ancrage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
)

// Fichiers de référence du client OpenTimestamps (opentimestamps-client,
// dossier examples/), avec le SHA-256 de leur fichier d'origine.
var references = []struct {
	fichier     string
	condensat   string
	blocs       []uint64
	calendriers []string
}{
	{"hello-world.txt.ots", "03ba204e50d126e4674c005e04d82e84c21366780af1f43bd54a37816b6ab340", []uint64{358391}, nil},
	{"incomplete.txt.ots", "05c4f616a8e5310d19d938cfd769864d7f4ccdc2ca8b479b10af83564b097af9", nil,
		[]string{"https://alice.btc.calendar.opentimestamps.org"}},
	{"two-calendars.txt.ots", "efaa174f68e59705757460f4f7d204bd2b535cfd194d9d945418732129404ddb", nil,
		[]string{"https://alice.btc.calendar.opentimestamps.org", "https://bob.btc.calendar.opentimestamps.org"}},
}

// Les fichiers .ots de référence se lisent, et se réécrivent octet pour
// octet : le format écrit ici est celui du client de référence.
func TestFichiersDeReference(t *testing.T) {
	for _, r := range references {
		t.Run(r.fichier, func(t *testing.T) {
			brut, err := os.ReadFile("testdata/" + r.fichier)
			if err != nil {
				t.Fatal(err)
			}
			f, err := LireFichier(brut)
			if err != nil {
				t.Fatal(err)
			}
			if f.OpFichier != OpSHA256 || hex.EncodeToString(f.Condensat) != r.condensat {
				t.Errorf("condensat : %x", f.Condensat)
			}
			if got := f.Horodatage.Blocs(); !egaux(got, r.blocs) {
				t.Errorf("blocs : %v, attendu %v", got, r.blocs)
			}
			cs := f.Horodatage.Calendriers()
			if len(cs) != len(r.calendriers) {
				t.Errorf("calendriers : %v", cs)
			}
			for _, c := range r.calendriers {
				if !contient(cs, c) {
					t.Errorf("calendrier %s absent de %v", c, cs)
				}
			}
			re, err := f.Encoder()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(re, brut) {
				t.Errorf("réécriture différente :\n%x\n%x", re, brut)
			}
		})
	}
}

// Une preuve construite ici a la structure d'un fichier fait par « ots
// stamp » : en-tête, version, SHA-256, condensat, nonce ajouté puis SHA-256,
// et une branche par calendrier.
func TestPreuveConstruiteCommeLeClientDeReference(t *testing.T) {
	condensat := sha256.Sum256([]byte("tête du journal"))
	f := &Fichier{OpFichier: OpSHA256, Condensat: condensat[:], Horodatage: &Horodatage{Message: condensat[:]}}
	nonce := bytes.Repeat([]byte{0x42}, 16)
	n, _ := f.Horodatage.Ajouter(Op{Tag: OpAppend, Arg: nonce})
	pointe, _ := n.Ajouter(Op{Tag: OpSHA256})
	for _, nom := range []string{"a", "b"} {
		h, err := FauxCalendrier{Nom: nom}.Soumettre(t.Context(), pointe.Message)
		if err != nil {
			t.Fatal(err)
		}
		if err := pointe.Fusionner(h); err != nil {
			t.Fatal(err)
		}
	}
	brut, err := f.Encoder()
	if err != nil {
		t.Fatal(err)
	}
	attendu := append(append([]byte{}, EnTeteOTS...), 0x01, 0x08)
	attendu = append(attendu, condensat[:]...)
	attendu = append(append(attendu, 0xf0, 0x10), nonce...)
	attendu = append(attendu, 0x08, 0xff, 0xf0, 0x10) // fourche : deux calendriers
	if !bytes.HasPrefix(brut, attendu) {
		t.Fatalf("structure :\n%x\nattendu le début\n%x", brut, attendu)
	}
	g, err := LireFichier(brut)
	if err != nil {
		t.Fatal(err)
	}
	if cs := g.Horodatage.Calendriers(); len(cs) != 2 || !contient(cs, "https://a.diguee.invalid") {
		t.Errorf("calendriers : %v", cs)
	}
	// Chaque attestation porte sur un message obtenu à partir du condensat.
	for _, fe := range g.Horodatage.Feuilles() {
		if len(fe.Noeud.Message) != 32 {
			t.Errorf("message attesté : %x", fe.Noeud.Message)
		}
	}
	if s := sha256.Sum256(append(condensat[:], nonce...)); !bytes.Equal(g.Horodatage.Branches[0].Suite.Branches[0].Suite.Message, s[:]) {
		t.Error("le nonce n'est pas appliqué comme par ots stamp")
	}
}

// Une donnée extérieure invalide est refusée, sans jamais faire paniquer.
func TestFichiersInvalides(t *testing.T) {
	brut, _ := os.ReadFile("testdata/two-calendars.txt.ots")
	cas := map[string][]byte{
		"vide":           nil,
		"en-tête":        append([]byte("x"), brut[1:]...),
		"tronqué":        brut[:len(brut)-3],
		"octets en trop": append(append([]byte{}, brut...), 0x00),
		"version":        append(append(append([]byte{}, EnTeteOTS...), 0x02), brut[len(EnTeteOTS)+1:]...),
	}
	for nom, b := range cas {
		if _, err := LireFichier(b); err == nil {
			t.Errorf("%s : accepté", nom)
		}
	}
	for i := range brut {
		_, _ = LireFichier(brut[:i])
	}
	// Horodatage trop profond.
	profond := bytes.Repeat([]byte{OpSHA256}, maxProfondeur+10)
	if _, err := LireHorodatage(append(profond, 0x00), make([]byte, 32)); err == nil {
		t.Error("horodatage trop profond accepté")
	}
	// URI de calendrier invalide.
	a := Attestation{Tag: TagEnAttente, Charge: varbytes(nil, []byte("https://exemple.org/?x=<script>"))}
	b := append([]byte{0x00}, a.Tag[:]...)
	if _, err := LireHorodatage(varbytes(b, a.Charge), make([]byte, 32)); err == nil {
		t.Error("URI invalide acceptée")
	}
}

func FuzzLireFichier(f *testing.F) {
	for _, r := range references {
		b, _ := os.ReadFile("testdata/" + r.fichier)
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		g, err := LireFichier(b)
		if err != nil {
			return
		}
		if _, err := g.Encoder(); err != nil {
			t.Fatalf("lu mais pas réécrit : %v", err)
		}
	})
}

func egaux(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contient(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
