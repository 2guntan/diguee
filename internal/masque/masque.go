// Package masque retire des journaux techniques tout ce qui ressemble à une
// adresse IP ou à un numéro de téléphone sénégalais (spec E6, T3, ticket #7).
//
// Le code ne journalise déjà ni IP, ni numéro, ni corps de requête. Ce
// paquet est une défense en profondeur : une erreur réseau d'un service
// extérieur (« dial tcp 203.0.113.7:443: … ») ou un message imprévu ne
// peuvent pas, eux non plus, écrire une adresse ou un numéro. Les motifs
// sont ceux de deploiement/controle-journaux.sh, qui vérifie les journaux
// sur le serveur.
package masque

import (
	"context"
	"log/slog"
	"net/netip"
	"regexp"
	"strings"
)

// Remplacement est le texte qui remplace une valeur masquée.
const Remplacement = "[masqué]"

var (
	// Candidats IPv4 : quatre nombres séparés par des points.
	candidatIPv4 = regexp.MustCompile(`[0-9]{1,3}(?:\.[0-9]{1,3}){3}`)
	// Candidats IPv6 : groupes hexadécimaux et « : », avec « :: » ou huit
	// groupes (une heure « 12:34:56 » n'en est pas un), éventuellement
	// suivis d'une IPv4 (::ffff:192.0.2.1).
	candidatIPv6 = regexp.MustCompile(`[0-9A-Fa-f:]*::[0-9A-Fa-f:]*(?:[0-9]{1,3}(?:\.[0-9]{1,3}){3})?|(?:[0-9A-Fa-f]{1,4}:){7}[0-9A-Fa-f]{1,4}`)
	// Numéros : +221, 00221 ou 221 facultatif, puis 7[05678] et 7 chiffres,
	// avec des espaces, points ou tirets entre les chiffres.
	candidatNumero = regexp.MustCompile(`(?:(?:\+|00)?221[ .\-]?)?7[05678](?:[ .\-]?[0-9]){7}`)
)

func alnum(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// borne indique que s[i:j] n'est pas collé à une lettre ou à un chiffre ;
// pour une adresse, ni à un point qui la prolongerait (« 1.2.3.4.5 »).
func borne(s string, i, j int, adresse bool) bool {
	if i > 0 && (alnum(s[i-1]) || adresse && s[i-1] == '.') {
		return false
	}
	if j < len(s) && (alnum(s[j]) || adresse && s[j] == '.' && j+1 < len(s) && s[j+1] >= '0' && s[j+1] <= '9') {
		return false
	}
	return true
}

// Texte renvoie s sans IP ni numéro.
func Texte(s string) string {
	if !strings.ContainsAny(s, "0123456789:") {
		return s
	}
	s = remplacer(s, candidatIPv6, func(v string) bool {
		for _, essai := range []string{v, strings.TrimSuffix(v, ":")} {
			if a, err := netip.ParseAddr(essai); err == nil && a.Is6() {
				return true
			}
		}
		return false
	}, true)
	s = remplacer(s, candidatIPv4, func(v string) bool {
		a, err := netip.ParseAddr(v)
		return err == nil && a.Is4()
	}, true)
	return remplacer(s, candidatNumero, func(string) bool { return true }, false)
}

// Contient indique si s contient une IP ou un numéro.
func Contient(s string) bool { return Texte(s) != s }

func remplacer(s string, re *regexp.Regexp, valide func(string) bool, adresse bool) string {
	idx := re.FindAllStringIndex(s, -1)
	if idx == nil {
		return s
	}
	var b strings.Builder
	prec := 0
	for _, m := range idx {
		i, j := m[0], m[1]
		if !borne(s, i, j, adresse) || !valide(s[i:j]) {
			continue
		}
		b.WriteString(s[prec:i])
		b.WriteString(Remplacement)
		prec = j
	}
	if prec == 0 {
		return s
	}
	b.WriteString(s[prec:])
	return b.String()
}

// Gestionnaire enveloppe un gestionnaire slog : le message et toutes les
// valeurs textuelles (erreurs comprises, groupes compris) passent par Texte.
type Gestionnaire struct{ h slog.Handler }

// Nouveau renvoie le gestionnaire masquant de h.
func Nouveau(h slog.Handler) *Gestionnaire { return &Gestionnaire{h: h} }

func (g *Gestionnaire) Enabled(ctx context.Context, l slog.Level) bool { return g.h.Enabled(ctx, l) }

func (g *Gestionnaire) Handle(ctx context.Context, r slog.Record) error {
	n := slog.NewRecord(r.Time, r.Level, Texte(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		n.AddAttrs(attr(a))
		return true
	})
	return g.h.Handle(ctx, n)
}

func (g *Gestionnaire) WithAttrs(as []slog.Attr) slog.Handler {
	ms := make([]slog.Attr, len(as))
	for i, a := range as {
		ms[i] = attr(a)
	}
	return &Gestionnaire{h: g.h.WithAttrs(ms)}
}

func (g *Gestionnaire) WithGroup(nom string) slog.Handler {
	return &Gestionnaire{h: g.h.WithGroup(nom)}
}

func attr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, Texte(v.String()))
	case slog.KindGroup:
		as := v.Group()
		ms := make([]any, len(as))
		for i, x := range as {
			ms[i] = attr(x)
		}
		return slog.Group(a.Key, ms...)
	case slog.KindAny:
		// Erreurs, adresses, valeurs quelconques : leur texte seulement.
		if e, ok := v.Any().(error); ok {
			return slog.String(a.Key, Texte(e.Error()))
		}
		if st, ok := v.Any().(interface{ String() string }); ok {
			return slog.String(a.Key, Texte(st.String()))
		}
		return a
	default:
		// Nombres, booléens, durées, dates : rien à masquer (un entier de 9
		// chiffres n'est jamais un numéro dans nos journaux).
		return slog.Attr{Key: a.Key, Value: v}
	}
}
