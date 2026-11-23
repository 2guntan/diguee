// Package config lit la configuration de Diguee dans les variables
// d'environnement. Toutes les variables commencent par DIGUEE_.
//
// Les valeurs que seul le porteur peut fournir (nom de la structure, numéro
// CDP, date d'ouverture…) valent « À COMPLÉTER » par défaut. Elles sont
// affichées telles quelles en développement ; en production, le paquet garde
// refuse le démarrage tant qu'il en reste une.
package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
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
	Empreinteur string        // DIGUEE_EMPREINTEUR : fake (défaut, HMAC de développement), ovh ou gcp
	OVHKMS      OVHKMS        // réglages de DIGUEE_EMPREINTEUR=ovh
	GCPKMS      GCPKMS        // réglages de DIGUEE_EMPREINTEUR=gcp (repli, ADR 0006)
	KMSDelai    time.Duration // DIGUEE_KMS_DELAI : délai d'attente d'un appel au KMS, 5s

	// CleTOTP chiffre les secrets TOTP de l'espace équipe (AES-256-GCM). Secret,
	// 32 octets. Vide : clé de développement écrite dans le code, déclarée
	// « fake » dans le registre des intégrations, donc refusée en production.
	CleTOTP []byte // DIGUEE_CLE_TOTP : 64 chiffres hexadécimaux

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

// OVHKMS décrit l'accès au KMS d'OVHcloud (spec §5.4). L'authentification se
// fait par certificat client (mTLS). Aucun de ces champs n'est un secret en
// soi : la clé privée du certificat reste dans son fichier, qui n'est lu
// qu'au démarrage et n'est jamais journalisé.
type OVHKMS struct {
	PointAcces string // DIGUEE_OVH_KMS_POINT_ACCES : https://eu-west-par.okms.ovh.net par exemple
	ID         string // DIGUEE_OVH_KMS_ID : identifiant (UUID) du KMS
	CleID      string // DIGUEE_OVH_KMS_CLE_ID : identifiant (UUID) de la clé RSA de signature
	Certificat string // DIGUEE_OVH_KMS_CERTIFICAT : chemin du certificat client (PEM)
	CleClient  string // DIGUEE_OVH_KMS_CLE_CLIENT : chemin de la clé privée du certificat client (PEM), secret
	AC         string // DIGUEE_OVH_KMS_AC : chemin d'autorités de certification (PEM), facultatif
}

// GCPKMS décrit l'accès à Google Cloud KMS (HMAC natif, méthode macSign).
// Le jeton d'accès OAuth 2 est lu dans un fichier à chaque appel : un
// processus extérieur le renouvelle (il expire au bout d'une heure).
type GCPKMS struct {
	PointAcces   string // DIGUEE_GCP_KMS_POINT_ACCES : https://cloudkms.googleapis.com
	Cle          string // DIGUEE_GCP_KMS_CLE : projects/…/locations/…/keyRings/…/cryptoKeys/…/cryptoKeyVersions/N
	JetonFichier string // DIGUEE_GCP_KMS_JETON_FICHIER : chemin du fichier qui contient le jeton d'accès, secret
	AC           string // DIGUEE_GCP_KMS_AC : chemin d'autorités de certification (PEM), facultatif
}

var (
	motifUUID   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	motifCleGCP = regexp.MustCompile(`^projects/[^/]+/locations/[^/]+/keyRings/[^/]+/cryptoKeys/[^/]+/cryptoKeyVersions/[0-9]+$`)
)

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

	c.Empreinteur = l.choix("DIGUEE_EMPREINTEUR", "fake", "fake", "ovh", "gcp")
	c.KMSDelai = l.duree("DIGUEE_KMS_DELAI", "5s")
	switch c.Empreinteur {
	case "ovh":
		c.OVHKMS = OVHKMS{
			PointAcces: l.adresseHTTPS("DIGUEE_OVH_KMS_POINT_ACCES", ""),
			ID:         l.motif("DIGUEE_OVH_KMS_ID", motifUUID, "un UUID"),
			CleID:      l.motif("DIGUEE_OVH_KMS_CLE_ID", motifUUID, "un UUID"),
			Certificat: l.obligatoire("DIGUEE_OVH_KMS_CERTIFICAT"),
			CleClient:  l.obligatoire("DIGUEE_OVH_KMS_CLE_CLIENT"),
			AC:         l.chaine("DIGUEE_OVH_KMS_AC", ""),
		}
	case "gcp":
		c.GCPKMS = GCPKMS{
			PointAcces:   l.adresseHTTPS("DIGUEE_GCP_KMS_POINT_ACCES", "https://cloudkms.googleapis.com"),
			Cle:          l.motif("DIGUEE_GCP_KMS_CLE", motifCleGCP, "projects/…/locations/…/keyRings/…/cryptoKeys/…/cryptoKeyVersions/N"),
			JetonFichier: l.obligatoire("DIGUEE_GCP_KMS_JETON_FICHIER"),
			AC:           l.chaine("DIGUEE_GCP_KMS_AC", ""),
		}
	}
	c.CleTOTP = l.cle("DIGUEE_CLE_TOTP", 32)

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

// obligatoire lit une valeur qui doit être fournie.
func (l *lecteur) obligatoire(nom string) string {
	v := l.chaine(nom, "")
	if v == "" {
		l.erreur(nom, errors.New("est obligatoire avec ce DIGUEE_EMPREINTEUR"))
	}
	return v
}

// motif lit une valeur obligatoire qui doit respecter une forme.
func (l *lecteur) motif(nom string, m *regexp.Regexp, attendu string) string {
	v := l.obligatoire(nom)
	if v != "" && !m.MatchString(v) {
		l.erreur(nom, fmt.Errorf("vaut %q ; attendu %s", v, attendu))
	}
	return v
}

// adresseHTTPS lit une adresse https:// sans chemin ni paramètres, sans « / » final.
func (l *lecteur) adresseHTTPS(nom, defaut string) string {
	v := strings.TrimRight(l.chaine(nom, defaut), "/")
	if v == "" {
		l.erreur(nom, errors.New("est obligatoire avec ce DIGUEE_EMPREINTEUR"))
		return ""
	}
	u, err := url.Parse(v)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.User != nil {
		l.erreur(nom, fmt.Errorf("vaut %q ; attendu une adresse https:// sans chemin, par exemple https://eu-west-par.okms.ovh.net", v))
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

// cle lit un secret de n octets écrit en hexadécimal. Absente, elle vaut nil.
// Le message d'erreur ne cite jamais la valeur.
func (l *lecteur) cle(nom string, n int) []byte {
	v := strings.TrimSpace(l.getenv(nom))
	if v == "" {
		return nil
	}
	b, err := hex.DecodeString(v)
	if err != nil || len(b) != n {
		l.erreur(nom, fmt.Errorf("attendu %d chiffres hexadécimaux (%d octets, par exemple : openssl rand -hex %d)", 2*n, n, n))
		return nil
	}
	return b
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
