package antifraude

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"sync/atomic"
	"time"

	"github.com/2guntan/diguee/internal/empreinte"
)

// LimiteEmpreinte borne le nombre de vérifications ou de codes par empreinte
// (spec §7 : DIGUEE_VERIF_PAR_EMPREINTE_24H, 3 par défaut) dans une fenêtre
// glissante de 24 heures. Le compteur est en mémoire et indexé par
// l'empreinte, jamais par le numéro. Par prudence, la clé du compteur n'est
// pas l'empreinte elle-même mais son HMAC avec une clé tirée au démarrage et
// jamais écrite : la table ne peut pas être rapprochée de la table des
// participations.
//
// Elle satisfait whatsapp.Limiteur (vérification inversée) et sert telle
// quelle aux codes WhatsApp et SMS (tickets #9 et #10) : une même instance
// partagée entre les parcours compte toutes les tentatives d'une empreinte.
type LimiteEmpreinte struct {
	fenetre    *Fenetre
	cle        [32]byte
	maintenant func() time.Time
	refusees   atomic.Int64
}

// DureeLimiteEmpreinte est la fenêtre de la limite par empreinte.
const DureeLimiteEmpreinte = 24 * time.Hour

// NouvelleLimiteEmpreinte renvoie une limite de max tentatives par 24 heures.
// maintenant vaut time.Now si nil.
func NouvelleLimiteEmpreinte(max int, maintenant func() time.Time) *LimiteEmpreinte {
	if maintenant == nil {
		maintenant = time.Now
	}
	l := &LimiteEmpreinte{fenetre: NouvelleFenetre(max, max, DureeLimiteEmpreinte), maintenant: maintenant}
	_, _ = rand.Read(l.cle[:])
	return l
}

// Autoriser compte une tentative pour l'empreinte et dit si elle est
// permise : la (max+1)-ième tentative dans les 24 heures est refusée, et
// une tentative refusée ne prolonge pas le blocage.
func (l *LimiteEmpreinte) Autoriser(e empreinte.Empreinte) bool {
	m := hmac.New(sha256.New, l.cle[:])
	m.Write(e[:])
	if l.fenetre.AjouterSiPossible(string(m.Sum(nil)), l.maintenant()) {
		return true
	}
	l.refusees.Add(1)
	return false
}

// Refusees renvoie le nombre de tentatives refusées depuis le démarrage.
func (l *LimiteEmpreinte) Refusees() int64 { return l.refusees.Load() }
