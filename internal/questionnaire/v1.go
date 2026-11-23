package questionnaire

import "strconv"

// Codes communs.
const (
	CodeNSP   int16 = 0 // « Je ne sais pas » (questions)
	CodeRefus int16 = 0 // « Je préfère ne pas répondre » (profil)
)

// Clés communes du catalogue.
const (
	CleNSP   = "choix.nsp"
	CleRefus = "choix.refus"
)

// choixNumerotes renvoie les choix 1 à n, de clés « prefixe.choix.1 » à
// « prefixe.choix.n », suivis du choix exclusif (code 0) de clé fin.
func choixNumerotes(prefixe string, n int, fin string) []Choix {
	cs := make([]Choix, 0, n+1)
	for i := 1; i <= n; i++ {
		cs = append(cs, Choix{Code: int16(i), Cle: prefixe + ".choix." + strconv.Itoa(i)})
	}
	return append(cs, Choix{Code: 0, Cle: fin, Exclusif: true})
}

func question(nom string, t Type, n int, provisoire bool) Question {
	cle := "questionnaire." + nom
	return Question{Nom: nom, Cle: cle, Type: t, Partie: PartieQuestions,
		Choix: choixNumerotes(cle, n, CleNSP), Provisoire: provisoire}
}

func critere(nom string, n int) Question {
	cle := "profil." + nom
	return Question{Nom: nom, Cle: cle, Type: Unique, Partie: PartieProfil,
		Choix: choixNumerotes(cle, n, CleRefus)}
}

// V1 est la version 1 du questionnaire (spec §3). Une nouvelle version ne
// peut être publiée qu'avant l'ouverture ; la version est stockée avec chaque
// réponse, et les codes d'une version publiée ne changent jamais.
//
// Valeurs provisoires (spec §15.1) : les échelles de Q1 et Q2 reprennent les
// propositions de la spec, les choix de Q5 sont une proposition du
// développement. Leurs libellés sont dans le catalogue ; pour les valider,
// ajuster les libellés et le nombre de choix ici, puis retirer Provisoire.
func V1() *Questionnaire {
	q7 := question("q7", Multiple, 4, false)
	q7.Aide = "questionnaire.q7.aide"
	themes := question("themes", Themes, 11, false) // les 11 thèmes de la spec §3.1
	themes.Aide = "questionnaire.themes.aide"

	inscrit := critere("inscrit", 2) // 1 Oui, 2 Non
	inscrit.Choix = append(inscrit.Choix[:2],
		Choix{Code: 3, Cle: CleNSP},
		Choix{Code: CodeRefus, Cle: CleRefus, Exclusif: true})

	return &Questionnaire{
		Version: 1,
		Questions: []Question{
			question("q1", Unique, 4, true),  // Toujours / Souvent / Rarement / Jamais
			question("q2", Unique, 3, true),  // Oui, toujours / Oui, si graves / Non
			question("q3", Unique, 4, false), // Très favorable … Très opposé
			question("q4", Unique, 3, false), // Référendum / Assemblée / Les deux
			question("q5", Unique, 4, true),  // proposition provisoire
			question("q6", Unique, 3, false), // Mi-mandat / Chaque année / À tout moment
			q7,                               // Président / Ministres / Députés / Maires
			themes,
			critere("region", 15),     // 14 régions (ordre alphabétique) puis 15 = diaspora
			critere("tranche_age", 5), // 18-24 / 25-34 / 35-49 / 50-64 / 65 et plus
			critere("sexe", 2),        // 1 Femme, 2 Homme
			inscrit,
		},
		CleMajeur: "questionnaire.majeur",
	}
}

// Courant est la version servie aux participants.
var Courant = V1
