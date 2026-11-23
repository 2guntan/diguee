// Package config lit la configuration de Diguee dans les variables
// d'environnement. Toutes les variables commencent par DIGUEE_.
//
// Les valeurs que seul le porteur peut fournir (nom de la structure, numéro
// CDP, date d'ouverture…) valent « À COMPLÉTER » par défaut. Elles sont
// affichées telles quelles en développement ; en production, le paquet garde
// refuse le démarrage tant qu'il en reste une.
package config

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ACompleter est la valeur des réglages que le porteur doit encore fournir.
const ACompleter = "À COMPLÉTER"

// Env est l'environnement d'exécution.
type Env string

const (
	Developpement Env = "development"
	Production    Env = "production"
)

// Texte est une valeur textuelle qui peut être provisoire.
type Texte struct {
	Variable string
	Valeur   string
}

// Provisoire indique que la valeur reste à fournir par le porteur.
func (t Texte) Provisoire() bool { return t.Valeur == ACompleter }

func (t Texte) String() string { return t.Valeur }

// Date est un jour (UTC, l'heure de Dakar) qui peut être provisoire.
type Date struct {
	Variable   string
	Jour       time.Time // minuit UTC ; zéro si provisoire
	provisoire bool
}

// Provisoire indique que la date reste à fournir par le porteur.
func (d Date) Provisoire() bool { return d.provisoire }

// Config regroupe tous les réglages. Les noms des variables sont donnés en
// commentaire ; la correspondance avec la spec (§13) est dans le README.
type Config struct {
	Env         Env    // DIGUEE_ENV : development (défaut) ou production
	Adresse     string // DIGUEE_ADRESSE : adresse d'écoute HTTP, « :8080 » par défaut
	PostgresURL string // DIGUEE_POSTGRES_URL : secret, obligatoire

	// Intégrations externes : « fake » désigne une fausse implémentation,
	// interdite en production (paquet garde).
	Empreinteur string // DIGUEE_EMPREINTEUR : fake (défaut, HMAC de développement) ; ovh au ticket #5

	// Responsable du traitement (ADR 0005) et contacts.
	StructureNom     Texte // DIGUEE_STRUCTURE_NOM
	StructureAdresse Texte // DIGUEE_STRUCTURE_ADRESSE
	Contact          Texte // DIGUEE_CONTACT : adresse pour exercer ses droits
	NumeroCDP        Texte // DIGUEE_NUMERO_CDP
	NumeroWhatsApp   Texte // DIGUEE_NUMERO_WHATSAPP : numéro WhatsApp de Diguee

	// Calendrier.
	Ouverture Date // DIGUEE_OUVERTURE : AAAA-MM-JJ
	Cloture   Date // DIGUEE_CLOTURE : AAAA-MM-JJ, ouverture + 6 mois par défaut

	// Vérification et anti-fraude (§7).
	PrefixesAutorises    []string      // DIGUEE_PREFIXES_AUTORISES : 70,75,76,77,78
	JetonDuree           time.Duration // DIGUEE_JETON_DUREE : 10m
	CodeDuree            time.Duration // DIGUEE_CODE_DUREE : 10m
	CodeEssais           int           // DIGUEE_CODE_ESSAIS : 5
	VerifParEmpreinte24h int           // DIGUEE_VERIF_PAR_EMPREINTE_24H : 3
	IPParHeure           int           // DIGUEE_IP_PAR_HEURE : 30
	SMSCoupeCircuit      float64       // DIGUEE_SMS_COUPE_CIRCUIT : 3x
	AltchaDifficulte     int           // DIGUEE_ALTCHA_DIFFICULTE : 100000

	// Tampon et publication (§6.2, §8).
	SeuilPublication int           // DIGUEE_SEUIL_PUBLICATION : 1000
	SeuilCase        int           // DIGUEE_SEUIL_CASE : 30
	TamponTaille     int           // DIGUEE_TAMPON_TAILLE : 20
	TamponDelai      time.Duration // DIGUEE_TAMPON_DELAI : 30m
	HeurePublication time.Duration // DIGUEE_HEURE_PUBLICATION : 06:00Z (décalage depuis minuit UTC)

	// Observatoire (§9).
	PreavisJours int // DIGUEE_PREAVIS_JOURS : 15
}

// Charger lit la configuration avec la fonction getenv (os.Getenv en
// pratique). Toutes les erreurs sont renvoyées ensemble.
func Charger(getenv func(string) string) (*Config, error) {
	l := lecteur{getenv: getenv}
	c := &Config{}

	switch env := l.chaine("DIGUEE_ENV", string(Developpement)); Env(env) {
	case Developpement, Production:
		c.Env = Env(env)
	default:
		l.erreur("DIGUEE_ENV", fmt.Errorf("vaut %q ; attendu development ou production", env))
	}
	c.Adresse = l.chaine("DIGUEE_ADRESSE", ":8080")
	c.PostgresURL = l.chaine("DIGUEE_POSTGRES_URL", "")
	if c.PostgresURL == "" {
		l.erreur("DIGUEE_POSTGRES_URL", errors.New("est obligatoire"))
	}

	c.Empreinteur = l.choix("DIGUEE_EMPREINTEUR", "fake", "fake")

	c.StructureNom = l.texte("DIGUEE_STRUCTURE_NOM")
	c.StructureAdresse = l.texte("DIGUEE_STRUCTURE_ADRESSE")
	c.Contact = l.texte("DIGUEE_CONTACT")
	c.NumeroCDP = l.texte("DIGUEE_NUMERO_CDP")
	c.NumeroWhatsApp = l.texte("DIGUEE_NUMERO_WHATSAPP")

	c.Ouverture = l.date("DIGUEE_OUVERTURE", ACompleter)
	if l.getenv("DIGUEE_CLOTURE") == "" && !c.Ouverture.Provisoire() {
		c.Cloture = Date{Variable: "DIGUEE_CLOTURE", Jour: c.Ouverture.Jour.AddDate(0, 6, 0)}
	} else {
		c.Cloture = l.date("DIGUEE_CLOTURE", ACompleter)
	}
	if !c.Ouverture.Provisoire() && !c.Cloture.Provisoire() && !c.Cloture.Jour.After(c.Ouverture.Jour) {
		l.erreur("DIGUEE_CLOTURE", errors.New("doit être postérieure à DIGUEE_OUVERTURE"))
	}

	c.PrefixesAutorises = l.prefixes("DIGUEE_PREFIXES_AUTORISES", "70,75,76,77,78")
	c.JetonDuree = l.duree("DIGUEE_JETON_DUREE", "10m")
	c.CodeDuree = l.duree("DIGUEE_CODE_DUREE", "10m")
	c.CodeEssais = l.entier("DIGUEE_CODE_ESSAIS", 5)
	c.VerifParEmpreinte24h = l.entier("DIGUEE_VERIF_PAR_EMPREINTE_24H", 3)
	c.IPParHeure = l.entier("DIGUEE_IP_PAR_HEURE", 30)
	c.SMSCoupeCircuit = l.multiplicateur("DIGUEE_SMS_COUPE_CIRCUIT", "3x")
	c.AltchaDifficulte = l.entier("DIGUEE_ALTCHA_DIFFICULTE", 100000)

	c.SeuilPublication = l.entier("DIGUEE_SEUIL_PUBLICATION", 1000)
	c.SeuilCase = l.entier("DIGUEE_SEUIL_CASE", 30)
	c.TamponTaille = l.entier("DIGUEE_TAMPON_TAILLE", 20)
	c.TamponDelai = l.duree("DIGUEE_TAMPON_DELAI", "30m")
	c.HeurePublication = l.heure("DIGUEE_HEURE_PUBLICATION", "06:00Z")

	c.PreavisJours = l.entier("DIGUEE_PREAVIS_JOURS", 15)

	if len(l.erreurs) > 0 {
		return nil, errors.Join(l.erreurs...)
	}
	return c, nil
}

// Provisoires renvoie les noms des variables qui valent encore « À COMPLÉTER ».
func (c *Config) Provisoires() []string {
	var noms []string
	for _, t := range []Texte{c.StructureNom, c.StructureAdresse, c.Contact, c.NumeroCDP, c.NumeroWhatsApp} {
		if t.Provisoire() {
			noms = append(noms, t.Variable)
		}
	}
	for _, d := range []Date{c.Ouverture, c.Cloture} {
		if d.Provisoire() {
			noms = append(noms, d.Variable)
		}
	}
	return noms
}

// Phase est l'état de la consultation à un instant donné.
type Phase string

const (
	PhaseAVenir  Phase = "a_venir"
	PhaseOuverte Phase = "ouverte"
	PhaseClose   Phase = "close"
)

// Phase renvoie l'état de la consultation à l'instant t. Tant que la date
// d'ouverture est provisoire, la consultation est « à venir ».
func (c *Config) Phase(t time.Time) Phase {
	switch {
	case c.Ouverture.Provisoire() || t.Before(c.Ouverture.Jour):
		return PhaseAVenir
	case c.Cloture.Provisoire() || t.Before(c.Cloture.Jour):
		return PhaseOuverte
	default:
		return PhaseClose
	}
}

type lecteur struct {
	getenv  func(string) string
	erreurs []error
}

func (l *lecteur) erreur(nom string, err error) {
	l.erreurs = append(l.erreurs, fmt.Errorf("%s %w", nom, err))
}

func (l *lecteur) chaine(nom, defaut string) string {
	if v := strings.TrimSpace(l.getenv(nom)); v != "" {
		return v
	}
	return defaut
}

// choix lit une valeur parmi une liste fermée.
func (l *lecteur) choix(nom, defaut string, permis ...string) string {
	v := l.chaine(nom, defaut)
	if !slices.Contains(permis, v) {
		l.erreur(nom, fmt.Errorf("vaut %q ; valeurs possibles : %s", v, strings.Join(permis, ", ")))
	}
	return v
}

func (l *lecteur) texte(nom string) Texte {
	return Texte{Variable: nom, Valeur: l.chaine(nom, ACompleter)}
}

func (l *lecteur) date(nom, defaut string) Date {
	v := l.chaine(nom, defaut)
	if v == ACompleter {
		return Date{Variable: nom, provisoire: true}
	}
	j, err := time.ParseInLocation(time.DateOnly, v, time.UTC)
	if err != nil {
		l.erreur(nom, fmt.Errorf("vaut %q ; attendu une date AAAA-MM-JJ", v))
		return Date{Variable: nom, provisoire: true}
	}
	return Date{Variable: nom, Jour: j}
}

func (l *lecteur) entier(nom string, defaut int) int {
	v := l.chaine(nom, strconv.Itoa(defaut))
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		l.erreur(nom, fmt.Errorf("vaut %q ; attendu un entier positif", v))
		return defaut
	}
	return n
}

func (l *lecteur) duree(nom, defaut string) time.Duration {
	v := l.chaine(nom, defaut)
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		l.erreur(nom, fmt.Errorf("vaut %q ; attendu une durée comme 10m ou 1h", v))
	}
	return d
}

// heure lit une heure UTC au format HH:MMZ et la renvoie comme durée depuis minuit.
func (l *lecteur) heure(nom, defaut string) time.Duration {
	v := l.chaine(nom, defaut)
	t, err := time.Parse("15:04Z", v)
	if err != nil {
		l.erreur(nom, fmt.Errorf("vaut %q ; attendu une heure UTC comme 06:00Z", v))
		return 0
	}
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute
}

func (l *lecteur) multiplicateur(nom, defaut string) float64 {
	v := l.chaine(nom, defaut)
	f, err := strconv.ParseFloat(strings.TrimSuffix(v, "x"), 64)
	if err != nil || !strings.HasSuffix(v, "x") || f <= 1 {
		l.erreur(nom, fmt.Errorf("vaut %q ; attendu un multiplicateur supérieur à 1 comme 3x", v))
		return 0
	}
	return f
}

func (l *lecteur) prefixes(nom, defaut string) []string {
	v := l.chaine(nom, defaut)
	var ps []string
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if len(p) != 2 || strings.Trim(p, "0123456789") != "" {
			l.erreur(nom, fmt.Errorf("contient %q ; attendu des préfixes à deux chiffres séparés par des virgules", p))
			continue
		}
		ps = append(ps, p)
	}
	return ps
}
