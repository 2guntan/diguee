// Package ancrage ancre le Journal public hors de nos serveurs (spec §10,
// ADR 0010, ticket #12).
//
// Chaque jour, après la publication quotidienne :
//
//   - l'empreinte de tête du journal est soumise aux calendriers publics
//     OpenTimestamps ; leurs engagements forment une preuve .ots (format du
//     client de référence, voir ots.go), publiée sur
//     /journal/ancrages/AAAA-MM-JJ.ots. Quelques heures plus tard, les
//     calendriers ont inscrit l'engagement dans un bloc Bitcoin : la tâche
//     des jours suivants demande la preuve complète (GET /timestamp/…) et la
//     publie à la place, comme le ferait « ots upgrade » ;
//   - la page /journal et l'export /journal.json sont déposés sur archive.org
//     (« Save Page Now ») ; les liens des copies archivées sont conservés et
//     affichés sur /journal.
//
// Chaque semaine, l'espace équipe montre aux administrateurs un texte prêt à
// publier sur X. La plateforme ne le publie jamais : aucun appel à X n'existe
// dans le code (test TestAucunAppelAX), c'est le porteur qui poste.
//
// Un service injoignable ou une réponse invalide ne fait jamais tomber le
// serveur : la tâche réessaie avec un délai croissant et le signale dans le
// journal technique. L'état des ancrages est en base (tables en ajout seul,
// datées au jour) : au démarrage, l'ancrage du jour qui manque est rattrapé.
package ancrage

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/2guntan/diguee/internal/journal"
	"github.com/2guntan/diguee/internal/publication"
)

// Cible est une page déposée sur archive.org.
type Cible string

// Pages déposées chaque jour, par rapport à l'adresse publique.
const (
	CibleJournal Cible = "journal"
	CibleExport  Cible = "journal.json"
)

// Cibles renvoie les pages déposées chaque jour.
func Cibles() []Cible { return []Cible{CibleJournal, CibleExport} }

// Preuve est une preuve OpenTimestamps de l'empreinte de tête d'un jour. Une
// preuve n'est jamais modifiée : sa mise à niveau (attestation Bitcoin) est
// une révision de plus.
type Preuve struct {
	Jour        time.Time // minuit UTC
	Revision    int       // 0 : engagements des calendriers ; 1… : mises à niveau
	Numero      int64     // numéro de l'entrée de tête ancrée
	Empreinte   journal.Empreinte
	OTS         []byte // fichier .ots
	Calendriers int    // calendriers qui ont répondu
	Bitcoin     bool   // la preuve contient une attestation Bitcoin
}

// Blocs renvoie les hauteurs des blocs Bitcoin attestés par la preuve.
func (p *Preuve) Blocs() []uint64 {
	f, err := LireFichier(p.OTS)
	if err != nil {
		return nil
	}
	return f.Horodatage.Blocs()
}

// Archive est le dépôt d'une page sur archive.org.
type Archive struct {
	Jour  time.Time
	Cible Cible
	Lien  string // https://web.archive.org/web/<horodatage>/<page>
}

// Etat est ce qui a été ancré un jour donné.
type Etat struct {
	Jour     time.Time
	Preuve   *Preuve // dernière révision ; nil si aucune
	Archives []Archive
}

// Archive renvoie le dépôt de la cible, ou nil.
func (e Etat) Archive(c Cible) *Archive {
	for i := range e.Archives {
		if e.Archives[i].Cible == c {
			return &e.Archives[i]
		}
	}
	return nil
}

// Magasin conserve les ancrages, en ajout seul. Implémenté par base.Base.
type Magasin interface {
	// EnregistrerPreuve inscrit une preuve ; sans effet (false) si cette
	// révision existe déjà.
	EnregistrerPreuve(ctx context.Context, p Preuve) (bool, error)
	// EnregistrerArchive inscrit un dépôt ; sans effet (false) s'il existe déjà.
	EnregistrerArchive(ctx context.Context, a Archive) (bool, error)
	// LirePreuve renvoie la dernière révision de la preuve du jour, ou nil.
	LirePreuve(ctx context.Context, jour time.Time) (*Preuve, error)
	// Ancrages renvoie l'état des limite derniers jours ancrés (même en
	// partie), les plus récents d'abord.
	Ancrages(ctx context.Context, limite int) ([]Etat, error)
}

// Echec est le dernier échec d'un service, gardé en mémoire pour l'espace équipe.
type Echec struct {
	Service string // « OpenTimestamps », « archive.org »…
	Quand   time.Time
	Erreur  string
	Essais  int // échecs consécutifs
}

// Options regroupe les dépendances de l'ancreur.
type Options struct {
	Mode    string // fake ou reel, pour l'affichage
	Magasin Magasin
	Journal *journal.Journal
	// Calendriers reçoivent l'empreinte de tête ; il en faut au moins
	// MinCalendriers qui répondent (2 par défaut) pour que la preuve soit
	// enregistrée.
	Calendriers    []Calendrier
	MinCalendriers int
	// Ouvrir renvoie le calendrier à interroger pour une attestation en
	// attente (son URI vient de la réponse du calendrier), ou false s'il
	// n'est pas sur la liste des calendriers admis.
	Ouvrir    func(uri string) (Calendrier, bool)
	Archiveur Archiveur
	// URLPublique est l'adresse du site (https://consultation.diguee.sn).
	URLPublique string
	// Heure est l'instant quotidien de l'ancrage, depuis minuit UTC :
	// l'heure de publication plus un décalage.
	Heure      time.Duration
	ReessaiMin time.Duration // premier délai après un échec ; 5 minutes par défaut
	ReessaiMax time.Duration // délai le plus long ; 6 heures par défaut
	// JoursMiseANiveau est le nombre de jours pendant lesquels une preuve
	// sans attestation Bitcoin est mise à niveau ; 14 par défaut.
	JoursMiseANiveau int
	Maintenant       func() time.Time
	Log              *slog.Logger
}

// Ancreur ancre chaque jour le journal public.
type Ancreur struct {
	opt Options

	mu     sync.Mutex
	echecs map[string]*Echec
}

// Nouveau renvoie l'ancreur.
func Nouveau(o Options) (*Ancreur, error) {
	if o.Magasin == nil || o.Journal == nil || o.Archiveur == nil || len(o.Calendriers) == 0 {
		return nil, errors.New("ancrage : options incomplètes")
	}
	if o.MinCalendriers <= 0 {
		o.MinCalendriers = min(2, len(o.Calendriers))
	}
	if o.MinCalendriers > len(o.Calendriers) {
		return nil, fmt.Errorf("ancrage : %d calendriers exigés pour %d configurés", o.MinCalendriers, len(o.Calendriers))
	}
	if !strings.HasPrefix(o.URLPublique, "https://") && !strings.HasPrefix(o.URLPublique, "http://") {
		return nil, errors.New("ancrage : adresse publique invalide")
	}
	o.URLPublique = strings.TrimRight(o.URLPublique, "/")
	if o.Ouvrir == nil {
		o.Ouvrir = func(string) (Calendrier, bool) { return nil, false }
	}
	if o.ReessaiMin <= 0 {
		o.ReessaiMin = 5 * time.Minute
	}
	if o.ReessaiMax < o.ReessaiMin {
		o.ReessaiMax = max(6*time.Hour, o.ReessaiMin)
	}
	if o.JoursMiseANiveau <= 0 {
		o.JoursMiseANiveau = 14
	}
	if o.Maintenant == nil {
		o.Maintenant = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Ancreur{opt: o, echecs: map[string]*Echec{}}, nil
}

// Services, pour les journaux et l'espace équipe.
const (
	ServiceOTS         = "OpenTimestamps"
	ServiceArchive     = "archive.org"
	ServiceMiseANiveau = "OpenTimestamps (mise à niveau)"
)

// Bilan décrit une exécution.
type Bilan struct {
	Jour         time.Time
	Preuve       *Preuve   // preuve du jour (nouvelle ou déjà là) ; nil si aucune
	Nouvelle     bool      // la preuve a été obtenue par cette exécution
	Archives     []Archive // dépôts faits par cette exécution
	MisesANiveau int       // preuves complétées d'une attestation Bitcoin
}

// JourDu renvoie le jour d'ancrage dû à l'instant t.
func (a *Ancreur) JourDu(t time.Time) time.Time { return publication.JourDu(t, a.opt.Heure) }

// Executer ancre le jour dû, s'il ne l'est pas encore (c'est aussi le
// rattrapage au démarrage), puis met à niveau les preuves des jours
// précédents. Seul le jour dû est rattrapé : l'empreinte de tête d'un jour
// passé n'est plus celle du journal. Une erreur signale un ancrage
// incomplet, à réessayer ; la mise à niveau, elle, est réessayée le
// lendemain sans erreur.
func (a *Ancreur) Executer(ctx context.Context) (Bilan, error) {
	jour := a.JourDu(a.opt.Maintenant())
	bilan := Bilan{Jour: jour}
	var erreurs []error

	p, err := a.opt.Magasin.LirePreuve(ctx, jour)
	if err == nil && p == nil {
		p, err = a.horodater(ctx, jour)
		bilan.Nouvelle = p != nil
	}
	if err != nil {
		erreurs = append(erreurs, a.echouer(ServiceOTS, jour, err))
	} else if bilan.Nouvelle {
		a.reussir(ServiceOTS)
		a.opt.Log.Info("ancrage : preuve OpenTimestamps obtenue", "jour", jour.Format(time.DateOnly),
			"entree", p.Numero, "empreinte", p.Empreinte.String(), "calendriers", p.Calendriers)
	}
	bilan.Preuve = p

	// Les pages sont déposées après l'horodatage, pour que la copie de
	// /journal montre en général la tête ancrée ; mais aussi quand les
	// calendriers ne répondent pas : les deux ancrages sont indépendants.
	if err := a.deposer(ctx, jour, &bilan); err != nil {
		erreurs = append(erreurs, a.echouer(ServiceArchive, jour, err))
	} else {
		a.reussir(ServiceArchive)
	}

	bilan.MisesANiveau = a.mettreANiveau(ctx, jour)
	return bilan, errors.Join(erreurs...)
}

// deposer dépose sur archive.org les pages du jour qui ne le sont pas encore.
func (a *Ancreur) deposer(ctx context.Context, jour time.Time, bilan *Bilan) error {
	etats, err := a.opt.Magasin.Ancrages(ctx, 1)
	if err != nil {
		return err
	}
	var faits []Archive
	if len(etats) > 0 && etats[0].Jour.Equal(jour) {
		faits = etats[0].Archives
	}
	var echecs error
	for _, c := range Cibles() {
		if slices.ContainsFunc(faits, func(x Archive) bool { return x.Cible == c }) {
			continue
		}
		ar, err := a.archiver(ctx, jour, c)
		if err != nil {
			echecs = errors.Join(echecs, err)
			continue
		}
		bilan.Archives = append(bilan.Archives, ar)
		a.opt.Log.Info("ancrage : page déposée sur archive.org", "jour", jour.Format(time.DateOnly),
			"page", string(c), "lien", ar.Lien)
	}
	return echecs
}

// horodater soumet l'empreinte de tête aux calendriers et enregistre la preuve.
func (a *Ancreur) horodater(ctx context.Context, jour time.Time) (*Preuve, error) {
	n, err := a.opt.Journal.Compter(ctx)
	if err != nil {
		return nil, fmt.Errorf("lecture du journal : %w", err)
	}
	if n == 0 {
		return nil, nil // rien à ancrer
	}
	es, err := a.opt.Journal.Lire(ctx, n, n)
	if err != nil || len(es) != 1 {
		return nil, fmt.Errorf("lecture de la tête du journal : %v", err)
	}
	tete := es[0]

	// Comme « ots stamp » : un nonce aléatoire est ajouté au condensat avant
	// de le hacher, pour que les calendriers ne reçoivent pas l'empreinte
	// elle-même.
	f := &Fichier{OpFichier: OpSHA256, Condensat: tete.Empreinte[:],
		Horodatage: &Horodatage{Message: slices.Clone(tete.Empreinte[:])}}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	avecNonce, err := f.Horodatage.Ajouter(Op{Tag: OpAppend, Arg: nonce})
	if err != nil {
		return nil, err
	}
	pointe, err := avecNonce.Ajouter(Op{Tag: OpSHA256})
	if err != nil {
		return nil, err
	}

	type reponse struct {
		h   *Horodatage
		err error
	}
	reponses := make([]reponse, len(a.opt.Calendriers))
	var wg sync.WaitGroup
	for i, c := range a.opt.Calendriers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := c.Soumettre(ctx, pointe.Message)
			reponses[i] = reponse{h, err}
		}()
	}
	wg.Wait()
	var (
		ok      int
		echecs  []error
		absents []string
	)
	for i, r := range reponses {
		if r.err == nil {
			r.err = pointe.Fusionner(r.h)
		}
		if r.err != nil {
			echecs = append(echecs, r.err)
			absents = append(absents, a.opt.Calendriers[i].Adresse())
			continue
		}
		ok++
	}
	if ok < a.opt.MinCalendriers {
		return nil, fmt.Errorf("%d calendrier(s) sur %d ont répondu, %d exigés : %w",
			ok, len(a.opt.Calendriers), a.opt.MinCalendriers, errors.Join(echecs...))
	}
	if len(absents) > 0 {
		a.opt.Log.Warn("ancrage : calendrier OpenTimestamps sans réponse ; preuve obtenue avec les autres",
			"jour", jour.Format(time.DateOnly), "calendriers", strings.Join(absents, ", "),
			"erreur", errors.Join(echecs...).Error())
	}
	ots, err := f.Encoder()
	if err != nil {
		return nil, err
	}
	p := Preuve{Jour: jour, Numero: tete.Numero, Empreinte: tete.Empreinte, OTS: ots, Calendriers: ok}
	if _, err := a.opt.Magasin.EnregistrerPreuve(ctx, p); err != nil {
		return nil, fmt.Errorf("enregistrement de la preuve : %w", err)
	}
	// Une autre instance a pu l'enregistrer avant : la preuve publiée est
	// celle de la base.
	return a.opt.Magasin.LirePreuve(ctx, jour)
}

func (a *Ancreur) archiver(ctx context.Context, jour time.Time, c Cible) (Archive, error) {
	page := a.opt.URLPublique + "/" + string(c)
	lien, err := a.opt.Archiveur.Archiver(ctx, page)
	if err != nil {
		return Archive{}, fmt.Errorf("%s : %w", c, err)
	}
	if !strings.HasPrefix(lien, "https://") || len(lien) > 2048 {
		return Archive{}, fmt.Errorf("%s : lien d'archive invalide", c)
	}
	ar := Archive{Jour: jour, Cible: c, Lien: lien}
	if _, err := a.opt.Magasin.EnregistrerArchive(ctx, ar); err != nil {
		return Archive{}, fmt.Errorf("%s : enregistrement : %w", c, err)
	}
	return ar, nil
}

// mettreANiveau demande aux calendriers l'attestation Bitcoin des preuves des
// jours précédents qui n'en ont pas encore, et renvoie le nombre de preuves
// complétées. Un échec est seulement signalé : la mise à niveau reprend à
// l'exécution suivante, et n'importe qui peut la faire avec « ots upgrade ».
func (a *Ancreur) mettreANiveau(ctx context.Context, aujourdhui time.Time) int {
	etats, err := a.opt.Magasin.Ancrages(ctx, a.opt.JoursMiseANiveau)
	if err != nil {
		a.echouer(ServiceMiseANiveau, aujourdhui, err)
		return 0
	}
	limite := aujourdhui.AddDate(0, 0, -a.opt.JoursMiseANiveau)
	faites := 0
	var echecs error
	for _, e := range etats {
		p := e.Preuve
		if p == nil || p.Bitcoin || !p.Jour.Before(aujourdhui) || p.Jour.Before(limite) {
			continue
		}
		f, err := LireFichier(p.OTS)
		if err != nil {
			echecs = errors.Join(echecs, fmt.Errorf("preuve du %s illisible : %w", p.Jour.Format(time.DateOnly), err))
			continue
		}
		change := false
		for _, fe := range f.Horodatage.Feuilles() {
			uri, ok := fe.Attestation.URI()
			if !ok {
				continue
			}
			c, ok := a.opt.Ouvrir(uri)
			if !ok {
				continue
			}
			h, trouve, err := c.MettreANiveau(ctx, fe.Noeud.Message)
			if err == nil && trouve {
				err = fe.Noeud.Fusionner(h)
				change = err == nil
			}
			if err != nil {
				echecs = errors.Join(echecs, err)
			}
		}
		if !change || len(f.Horodatage.Blocs()) == 0 {
			continue
		}
		ots, err := f.Encoder()
		if err == nil {
			_, err = a.opt.Magasin.EnregistrerPreuve(ctx, Preuve{Jour: p.Jour, Revision: p.Revision + 1,
				Numero: p.Numero, Empreinte: p.Empreinte, OTS: ots, Calendriers: p.Calendriers, Bitcoin: true})
		}
		if err != nil {
			echecs = errors.Join(echecs, err)
			continue
		}
		faites++
		a.opt.Log.Info("ancrage : preuve OpenTimestamps complétée (attestation Bitcoin)",
			"jour", p.Jour.Format(time.DateOnly), "blocs", fmt.Sprint(f.Horodatage.Blocs()))
	}
	if echecs != nil {
		a.echouer(ServiceMiseANiveau, aujourdhui, echecs)
	} else {
		a.reussir(ServiceMiseANiveau)
	}
	return faites
}

func (a *Ancreur) echouer(service string, jour time.Time, err error) error {
	a.mu.Lock()
	e := a.echecs[service]
	if e == nil {
		e = &Echec{Service: service}
		a.echecs[service] = e
	}
	e.Quand, e.Erreur = a.opt.Maintenant().UTC(), tronquer(err.Error(), 500)
	e.Essais++
	essais := e.Essais
	a.mu.Unlock()
	a.opt.Log.Warn("ancrage : échec, nouvel essai plus tard", "service", service,
		"jour", jour.Format(time.DateOnly), "essais", essais, "erreur", err.Error())
	return fmt.Errorf("%s : %w", service, err)
}

func (a *Ancreur) reussir(service string) {
	a.mu.Lock()
	delete(a.echecs, service)
	a.mu.Unlock()
}

// Echecs renvoie les derniers échecs en cours, par service.
func (a *Ancreur) Echecs() []Echec {
	a.mu.Lock()
	defer a.mu.Unlock()
	var es []Echec
	for _, e := range a.echecs {
		es = append(es, *e)
	}
	slices.SortFunc(es, func(x, y Echec) int { return strings.Compare(x.Service, y.Service) })
	return es
}

func tronquer(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s[:n])
	return string(r[:len(r)-1]) + "…"
}

// Lancer ancre aussitôt le jour dû (rattrapage), puis chaque jour à l'heure
// fixée, jusqu'à l'annulation de ctx. Après un échec, il réessaie avec un
// délai qui double à chaque échec (ReessaiMin, puis jusqu'à ReessaiMax),
// sans jamais dépasser l'heure de l'ancrage suivant.
func (a *Ancreur) Lancer(ctx context.Context) {
	delai := a.opt.ReessaiMin
	for {
		_, err := a.Executer(ctx)
		maintenant := a.opt.Maintenant()
		attente := publication.Prochaine(maintenant, a.opt.Heure).Sub(maintenant)
		if err != nil && ctx.Err() == nil {
			attente = min(attente, delai)
			a.opt.Log.Warn("ancrage : nouvel essai programmé", "dans", attente.Round(time.Millisecond).String())
			delai = min(2*delai, a.opt.ReessaiMax)
		} else {
			delai = a.opt.ReessaiMin
		}
		attente = max(attente, min(time.Second, a.opt.ReessaiMin))
		t := time.NewTimer(attente)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// CommandeVerification est la vérification d'une preuve avec le client
// OpenTimestamps (pip install opentimestamps-client) : la mise à niveau
// ajoute l'attestation Bitcoin si la preuve publiée ne l'a pas encore, puis
// la vérification recalcule le chemin jusqu'au bloc.
const CommandeVerification = "ots upgrade diguee-journal-AAAA-MM-JJ.ots\n" +
	"ots verify -d <empreinte de tête> diguee-journal-AAAA-MM-JJ.ots"

// LienPreuve renvoie le chemin de la preuve d'un jour.
func LienPreuve(jour time.Time) string {
	return "/journal/ancrages/" + jour.Format(time.DateOnly) + ".ots"
}

// Semaine est le texte hebdomadaire à publier sur X : l'empreinte ancrée le
// premier jour de la semaine qui en a une.
type Semaine struct {
	Lundi       time.Time
	Jour        time.Time
	Numero      int64
	Empreinte   string
	LienJournal string // adresse publique de /journal
	LienPreuve  string // adresse publique du .ots
}

// Tableau est ce que montre l'espace équipe.
type Tableau struct {
	Mode     string
	Jours    []Etat    // les derniers jours, les plus récents d'abord
	Semaines []Semaine // les dernières semaines, la plus récente d'abord
	Echecs   []Echec
}

// SemainesAffichees est le nombre de textes hebdomadaires montrés.
const SemainesAffichees = 4

// JoursAffiches est le nombre de jours montrés dans l'espace équipe.
const JoursAffiches = 14

// Tableau renvoie l'état des ancrages et les textes hebdomadaires.
func (a *Ancreur) Tableau(ctx context.Context) (*Tableau, error) {
	etats, err := a.opt.Magasin.Ancrages(ctx, 7*SemainesAffichees+7)
	if err != nil {
		return nil, err
	}
	t := &Tableau{Mode: a.opt.Mode, Echecs: a.Echecs(), Semaines: Semaines(etats, a.opt.URLPublique, SemainesAffichees)}
	t.Jours = etats[:min(len(etats), JoursAffiches)]
	return t, nil
}

// Lundi renvoie le lundi de la semaine (ISO) du jour.
func Lundi(jour time.Time) time.Time {
	j := publication.Jour(jour)
	return j.AddDate(0, 0, -((int(j.Weekday()) + 6) % 7))
}

// Semaines regroupe les ancrages par semaine et garde, pour chacune, le
// premier jour qui a une preuve.
func Semaines(etats []Etat, urlPublique string, n int) []Semaine {
	var ss []Semaine
	for _, e := range etats { // les plus récents d'abord
		if e.Preuve == nil {
			continue
		}
		s := Semaine{Lundi: Lundi(e.Jour), Jour: e.Jour, Numero: e.Preuve.Numero,
			Empreinte: e.Preuve.Empreinte.String(), LienJournal: urlPublique + "/journal",
			LienPreuve: urlPublique + LienPreuve(e.Jour)}
		if k := len(ss) - 1; k >= 0 && ss[k].Lundi.Equal(s.Lundi) {
			ss[k] = s // un jour plus ancien de la même semaine
			continue
		}
		if len(ss) == n {
			break
		}
		ss = append(ss, s)
	}
	return ss
}
