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
	WhatsApp    string        // DIGUEE_WHATSAPP : fake (défaut, page de simulation) ou meta (API Cloud)
	Meta        Meta          // réglages de DIGUEE_WHATSAPP=meta
	SMS         string        // DIGUEE_SMS : fake (défaut, codes écrits dans la console) ; fournisseur à choisir (ADR 0004)

	// CleTOTP chiffre les secrets TOTP de l'espace équipe (AES-256-GCM). Secret,
	// 32 octets. Vide : clé de développement écrite dans le code, déclarée
	// « fake » dans le registre des intégrations, donc refusée en production.
	CleTOTP []byte // DIGUEE_CLE_TOTP : 64 chiffres hexadécimaux

	// CleAltcha signe les défis du captcha ALTCHA (HMAC-SHA-256). Secret, 32
	// octets. Vide : clé de développement écrite dans le code, déclarée
	// « fake » dans le registre des intégrations, donc refusée en production.
	CleAltcha []byte // DIGUEE_CLE_ALTCHA : 64 chiffres hexadécimaux

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
	AltchaDifficulte     int           // DIGUEE_ALTCHA_DIFFICULTE : 100000

	// Code par SMS (§5.3) : limites plus strictes que celles du WhatsApp,
	// et coupe-circuit financier (ticket #10).
	SMSParEmpreinte24h      int           // DIGUEE_SMS_PAR_EMPREINTE_24H : 2 (au plus DIGUEE_VERIF_PAR_EMPREINTE_24H)
	SMSCodeEssais           int           // DIGUEE_SMS_CODE_ESSAIS : 3 (au plus DIGUEE_CODE_ESSAIS)
	SMSIPParHeure           int           // DIGUEE_SMS_IP_PAR_HEURE : 10 (au plus DIGUEE_IP_PAR_HEURE)
	SMSParHeure             int           // DIGUEE_SMS_PAR_HEURE : 100, plafond absolu horaire (coupe toujours)
	SMSCoupeCircuit         float64       // DIGUEE_SMS_COUPE_CIRCUIT : 3x la moyenne horaire sur 7 jours
	SMSCoupeCircuitPlancher int           // DIGUEE_SMS_COUPE_CIRCUIT_PLANCHER : 20 envois dans l'heure au moins
	SMSReactivation         time.Duration // DIGUEE_SMS_REACTIVATION : 1h
	SMSSansJSParHeure       int           // DIGUEE_SMS_SANS_JS_PAR_HEURE : 10 demandes sans captcha par heure, en tout
	SMSSansJSIPParHeure     int           // DIGUEE_SMS_SANS_JS_IP_PAR_HEURE : 2 demandes sans captcha par heure et par adresse

	// Tampon et publication (§6.2, §8).
	SeuilPublication int           // DIGUEE_SEUIL_PUBLICATION : 1000
	SeuilCase        int           // DIGUEE_SEUIL_CASE : 30
	TamponTaille     int           // DIGUEE_TAMPON_TAILLE : 20
	TamponDelai      time.Duration // DIGUEE_TAMPON_DELAI : 30m
	HeurePublication time.Duration // DIGUEE_HEURE_PUBLICATION : 06:00Z (décalage depuis minuit UTC)
	// PopulationANSD est le chemin du fichier JSON de la composition de la
	// population (ANSD), comparée à celle de l'échantillon (spec §8, règle
	// 6). « À COMPLÉTER » : non disponible sur la page des résultats, et
	// refusé en production.
	PopulationANSD Texte // DIGUEE_POPULATION_ANSD

	// Observatoire (§9).
	PreavisJours int // DIGUEE_PREAVIS_JOURS : 15
	// ArchiveTailleMax est la taille maximale, en octets, d'une copie
	// archivée de source (PDF ou image) téléversée par un rédacteur.
	ArchiveTailleMax int64 // DIGUEE_ARCHIVE_TAILLE_MO : 10 (Mo), 100 au plus

	// Stockage d'objets des copies archivées des sources (§6.4, ADR 0006).
	Stockage Stockage

	// URLPublique est l'adresse du site, sans « / » final : pages déposées
	// sur archive.org et liens du texte hebdomadaire pour X.
	URLPublique string // DIGUEE_URL_PUBLIQUE : https://consultation.diguee.sn

	// Ancrage extérieur du journal public (§10, ticket #12).
	Ancrage Ancrage
}

// Ancrage règle l'ancrage quotidien du journal public : OpenTimestamps et
// archive.org, deux services publics sans compte.
type Ancrage struct {
	// Mode : fake (faux calendriers et fausse archive, sans réseau ; défaut
	// en développement, refusé en production) ou reel (défaut en production).
	Mode           string        // DIGUEE_ANCRAGE
	Calendriers    []string      // DIGUEE_ANCRAGE_CALENDRIERS : adresses https:// séparées par des virgules
	MinCalendriers int           // DIGUEE_ANCRAGE_MIN_CALENDRIERS : 2 (au plus le nombre de calendriers)
	Archive        string        // DIGUEE_ANCRAGE_ARCHIVE : https://web.archive.org
	Decalage       time.Duration // DIGUEE_ANCRAGE_DECALAGE : 15m après DIGUEE_HEURE_PUBLICATION
	Delai          time.Duration // DIGUEE_ANCRAGE_DELAI : 60s, délai d'attente d'un appel
}

// Stockage règle le stockage d'objets des copies archivées des sources.
type Stockage struct {
	// Mode : fake (dossier local, défaut, refusé en production) ou ovh
	// (Object Storage d'OVHcloud, API S3, signature AWS SigV4).
	Mode    string // DIGUEE_STOCKAGE
	Dossier string // DIGUEE_STOCKAGE_DOSSIER : dossier du mode fake ; défaut : <dossier temporaire>/diguee-archives

	PointAcces   string        // DIGUEE_OVH_S3_POINT_ACCES : https://s3.gra.io.cloud.ovh.net par exemple
	Region       string        // DIGUEE_OVH_S3_REGION : gra, sbg… (région de la signature)
	Compartiment string        // DIGUEE_OVH_S3_COMPARTIMENT : nom du bucket
	CleAcces     string        // DIGUEE_OVH_S3_CLE_ACCES : identifiant de la clé d'accès S3
	CleSecrete   string        // DIGUEE_OVH_S3_CLE_SECRETE : clé secrète S3 ; secret
	Delai        time.Duration // DIGUEE_OVH_S3_DELAI : délai d'attente d'un appel, 60s
}

// CalendriersParDefaut sont les calendriers publics OpenTimestamps utilisés
// par défaut (ceux du client de référence).
var CalendriersParDefaut = []string{
	"https://a.pool.opentimestamps.org",
	"https://b.pool.opentimestamps.org",
	"https://finney.calendar.eternitywall.com",
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

// Meta décrit l'application WhatsApp de l'API Cloud de Meta (spec §5.1). Le
// secret de l'application et le jeton de vérification du webhook sont des
// secrets : ils ne sont jamais journalisés ni affichés.
type Meta struct {
	IDNumero     string // DIGUEE_META_ID_NUMERO : identifiant du numéro de téléphone (phone_number_id)
	SecretApp    string // DIGUEE_META_SECRET_APP : secret de l'application, vérifie X-Hub-Signature-256 ; secret
	JetonWebhook string // DIGUEE_META_JETON_WEBHOOK : jeton de vérification du webhook (hub.verify_token) ; secret

	// Envoi du code de secours (spec §5.2) par l'API Graph.
	JetonAcces   string        // DIGUEE_META_JETON_ACCES : jeton d'accès permanent (utilisateur système) ; secret
	ModeleCode   string        // DIGUEE_META_MODELE_CODE : nom du modèle de message d'authentification approuvé
	LangueModele string        // DIGUEE_META_LANGUE_MODELE : code de langue du modèle (fr par défaut)
	VersionAPI   string        // DIGUEE_META_VERSION_API : version de l'API Graph (v23.0 par défaut)
	Delai        time.Duration // DIGUEE_META_DELAI : délai d'attente d'un envoi (10s par défaut)
}

var (
	motifIDMeta = regexp.MustCompile(`^[0-9]{5,30}$`)
	// Noms de modèles de Meta : minuscules, chiffres et « _ ».
	motifModeleMeta   = regexp.MustCompile(`^[a-z0-9_]{1,512}$`)
	motifLangueMeta   = regexp.MustCompile(`^[a-z]{2,3}(_[A-Z]{2})?$`)
	motifVersionMeta  = regexp.MustCompile(`^v[0-9]{1,3}\.[0-9]{1,2}$`)
	motifUUID         = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	motifCompartiment = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	motifRegionS3     = regexp.MustCompile(`^[a-z0-9-]{2,40}$`)
	motifCleGCP       = regexp.MustCompile(`^projects/[^/]+/locations/[^/]+/keyRings/[^/]+/cryptoKeys/[^/]+/cryptoKeyVersions/[0-9]+$`)
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
	c.CleAltcha = l.cle("DIGUEE_CLE_ALTCHA", 32)

	c.WhatsApp = l.choix("DIGUEE_WHATSAPP", "fake", "fake", "meta")
	if c.WhatsApp == "meta" {
		c.Meta = Meta{
			IDNumero:     l.motif("DIGUEE_META_ID_NUMERO", motifIDMeta, "l'identifiant numérique du numéro de téléphone (phone_number_id)"),
			SecretApp:    l.secret("DIGUEE_META_SECRET_APP", 16),
			JetonWebhook: l.secret("DIGUEE_META_JETON_WEBHOOK", 16),
			JetonAcces:   l.secret("DIGUEE_META_JETON_ACCES", 32),
			ModeleCode:   l.motif("DIGUEE_META_MODELE_CODE", motifModeleMeta, "le nom du modèle d'authentification (minuscules, chiffres, _)"),
			LangueModele: l.motifDefaut("DIGUEE_META_LANGUE_MODELE", "fr", motifLangueMeta, "un code de langue de Meta comme fr ou en_US"),
			VersionAPI:   l.motifDefaut("DIGUEE_META_VERSION_API", "v23.0", motifVersionMeta, "une version de l'API Graph comme v23.0"),
			Delai:        l.duree("DIGUEE_META_DELAI", "10s"),
		}
	}

	c.StructureNom = l.texte("DIGUEE_STRUCTURE_NOM")
	c.StructureAdresse = l.texte("DIGUEE_STRUCTURE_ADRESSE")
	c.Contact = l.texte("DIGUEE_CONTACT")
	c.NumeroCDP = l.texte("DIGUEE_NUMERO_CDP")
	c.NumeroWhatsApp = l.texte("DIGUEE_NUMERO_WHATSAPP")
	if !c.NumeroWhatsApp.Provisoire() {
		if _, err := c.NumeroWhatsAppChiffres(); err != nil {
			l.erreur("DIGUEE_NUMERO_WHATSAPP", err)
		}
	}

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
	c.AltchaDifficulte = l.entier("DIGUEE_ALTCHA_DIFFICULTE", 100000)

	c.SMS = l.choix("DIGUEE_SMS", "fake", "fake")
	c.SMSParEmpreinte24h = l.auPlus("DIGUEE_SMS_PAR_EMPREINTE_24H", 2, "DIGUEE_VERIF_PAR_EMPREINTE_24H", c.VerifParEmpreinte24h)
	c.SMSCodeEssais = l.auPlus("DIGUEE_SMS_CODE_ESSAIS", 3, "DIGUEE_CODE_ESSAIS", c.CodeEssais)
	c.SMSIPParHeure = l.auPlus("DIGUEE_SMS_IP_PAR_HEURE", 10, "DIGUEE_IP_PAR_HEURE", c.IPParHeure)
	c.SMSParHeure = l.entier("DIGUEE_SMS_PAR_HEURE", 100)
	c.SMSCoupeCircuit = l.multiplicateur("DIGUEE_SMS_COUPE_CIRCUIT", "3x")
	c.SMSCoupeCircuitPlancher = l.entier("DIGUEE_SMS_COUPE_CIRCUIT_PLANCHER", 20)
	c.SMSReactivation = l.duree("DIGUEE_SMS_REACTIVATION", "1h")
	c.SMSSansJSParHeure = l.auPlus("DIGUEE_SMS_SANS_JS_PAR_HEURE", 10, "DIGUEE_SMS_PAR_HEURE", c.SMSParHeure)
	c.SMSSansJSIPParHeure = l.auPlus("DIGUEE_SMS_SANS_JS_IP_PAR_HEURE", 2, "DIGUEE_SMS_IP_PAR_HEURE", c.SMSIPParHeure)

	c.SeuilPublication = l.entier("DIGUEE_SEUIL_PUBLICATION", 1000)
	c.SeuilCase = l.entier("DIGUEE_SEUIL_CASE", 30)
	c.TamponTaille = l.entier("DIGUEE_TAMPON_TAILLE", 20)
	c.TamponDelai = l.duree("DIGUEE_TAMPON_DELAI", "30m")
	c.HeurePublication = l.heure("DIGUEE_HEURE_PUBLICATION", "06:00Z")
	c.PopulationANSD = l.texte("DIGUEE_POPULATION_ANSD")

	c.PreavisJours = l.entier("DIGUEE_PREAVIS_JOURS", 15)
	c.ArchiveTailleMax = int64(l.auPlus("DIGUEE_ARCHIVE_TAILLE_MO", 10, "la limite du code", 100)) << 20

	c.Stockage.Mode = l.choix("DIGUEE_STOCKAGE", "fake", "fake", "ovh")
	c.Stockage.Dossier = l.chaine("DIGUEE_STOCKAGE_DOSSIER", "")
	if c.Stockage.Mode == "ovh" {
		c.Stockage.PointAcces = l.adresseHTTPS("DIGUEE_OVH_S3_POINT_ACCES", "")
		c.Stockage.Region = l.motif("DIGUEE_OVH_S3_REGION", motifRegionS3, "une région en minuscules comme gra")
		c.Stockage.Compartiment = l.motif("DIGUEE_OVH_S3_COMPARTIMENT", motifCompartiment, "un nom de bucket S3 (3 à 63 caractères parmi a-z, 0-9, « . » et « - »)")
		c.Stockage.CleAcces = l.secret("DIGUEE_OVH_S3_CLE_ACCES", 8)
		c.Stockage.CleSecrete = l.secret("DIGUEE_OVH_S3_CLE_SECRETE", 16)
		c.Stockage.Delai = l.duree("DIGUEE_OVH_S3_DELAI", "60s")
	}

	c.URLPublique = l.adresseHTTPS("DIGUEE_URL_PUBLIQUE", "https://consultation.diguee.sn")
	modeAncrage := "fake"
	if c.Env == Production {
		modeAncrage = "reel"
	}
	c.Ancrage.Mode = l.choix("DIGUEE_ANCRAGE", modeAncrage, "fake", "reel")
	for _, u := range strings.Split(l.chaine("DIGUEE_ANCRAGE_CALENDRIERS", strings.Join(CalendriersParDefaut, ",")), ",") {
		if u = strings.TrimSpace(u); u != "" {
			c.Ancrage.Calendriers = append(c.Ancrage.Calendriers, l.adresseHTTPSValeur("DIGUEE_ANCRAGE_CALENDRIERS", u))
		}
	}
	if len(c.Ancrage.Calendriers) == 0 {
		l.erreur("DIGUEE_ANCRAGE_CALENDRIERS", errors.New("ne contient aucun calendrier"))
	}
	c.Ancrage.MinCalendriers = l.auPlus("DIGUEE_ANCRAGE_MIN_CALENDRIERS", min(2, max(1, len(c.Ancrage.Calendriers))),
		"le nombre de DIGUEE_ANCRAGE_CALENDRIERS", len(c.Ancrage.Calendriers))
	c.Ancrage.Archive = l.adresseHTTPS("DIGUEE_ANCRAGE_ARCHIVE", "https://web.archive.org")
	c.Ancrage.Decalage = l.duree("DIGUEE_ANCRAGE_DECALAGE", "15m")
	if c.Ancrage.Decalage >= 24*time.Hour {
		l.erreur("DIGUEE_ANCRAGE_DECALAGE", errors.New("doit être inférieur à 24h"))
	}
	c.Ancrage.Delai = l.duree("DIGUEE_ANCRAGE_DELAI", "60s")

	if len(l.erreurs) > 0 {
		return nil, errors.Join(l.erreurs...)
	}
	return c, nil
}

// Provisoires renvoie les noms des variables qui valent encore « À COMPLÉTER ».
func (c *Config) Provisoires() []string {
	var noms []string
	for _, t := range []Texte{c.StructureNom, c.StructureAdresse, c.Contact, c.NumeroCDP, c.NumeroWhatsApp, c.PopulationANSD} {
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

// NumeroWhatsAppChiffres renvoie le numéro WhatsApp de Diguee en chiffres
// seulement, indicatif compris, comme l'attend wa.me (« 221771234567 »).
func (c *Config) NumeroWhatsAppChiffres() (string, error) {
	if c.NumeroWhatsApp.Provisoire() {
		return "", errors.New("vaut « " + ACompleter + " »")
	}
	var b strings.Builder
	for i, r := range c.NumeroWhatsApp.Valeur {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && i == 0, r == ' ':
		default:
			return "", fmt.Errorf("vaut %q ; attendu un numéro international comme +221 77 123 45 67", c.NumeroWhatsApp.Valeur)
		}
	}
	if n := b.Len(); n < 8 || n > 15 || strings.HasPrefix(b.String(), "0") {
		return "", fmt.Errorf("vaut %q ; attendu un numéro international comme +221 77 123 45 67", c.NumeroWhatsApp.Valeur)
	}
	return b.String(), nil
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
		l.erreur(nom, errors.New("est obligatoire avec ce mode (DIGUEE_EMPREINTEUR, DIGUEE_WHATSAPP ou DIGUEE_STOCKAGE)"))
	}
	return v
}

// secret lit un secret obligatoire d'au moins n caractères. Le message
// d'erreur ne cite jamais la valeur.
func (l *lecteur) secret(nom string, n int) string {
	v := l.obligatoire(nom)
	if v != "" && len(v) < n {
		l.erreur(nom, fmt.Errorf("est trop court (%d caractères au moins)", n))
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

// motifDefaut lit une valeur facultative, avec un défaut, qui doit respecter une forme.
func (l *lecteur) motifDefaut(nom, defaut string, m *regexp.Regexp, attendu string) string {
	v := l.chaine(nom, defaut)
	if !m.MatchString(v) {
		l.erreur(nom, fmt.Errorf("vaut %q ; attendu %s", v, attendu))
	}
	return v
}

// adresseHTTPS lit une adresse https:// sans chemin ni paramètres, sans « / » final.
func (l *lecteur) adresseHTTPS(nom, defaut string) string {
	v := strings.TrimRight(l.chaine(nom, defaut), "/")
	if v == "" {
		l.erreur(nom, errors.New("est obligatoire avec ce mode (DIGUEE_EMPREINTEUR ou DIGUEE_STOCKAGE)"))
		return ""
	}
	return l.adresseHTTPSValeur(nom, v)
}

// adresseHTTPSValeur contrôle une adresse https:// sans chemin ni paramètres.
func (l *lecteur) adresseHTTPSValeur(nom, v string) string {
	v = strings.TrimRight(v, "/")
	u, err := url.Parse(v)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.User != nil {
		l.erreur(nom, fmt.Errorf("vaut %q ; attendu une adresse https:// sans chemin, comme https://exemple.org", v))
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

// auPlus lit un entier positif qui ne doit pas dépasser la valeur d'une
// autre variable : une limite du SMS reste au moins aussi stricte que celle
// du WhatsApp.
func (l *lecteur) auPlus(nom string, defaut int, autre string, plafond int) int {
	n := l.entier(nom, defaut)
	if n > plafond {
		l.erreur(nom, fmt.Errorf("vaut %d ; ne doit pas dépasser %s (%d)", n, autre, plafond))
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
