package observatoire

import (
	"strconv"

	"github.com/2guntan/diguee/internal/pdf"
)

// ComposerDossier écrit le dossier de préavis de l'élu, en PDF : l'engagement
// et sa citation, ses sources (lien d'origine et SHA-256 de la copie
// archivée), le statut proposé et son explication avec ses preuves, le
// délai de réponse et l'adresse où répondre. Le dossier ne contient aucune
// date d'impression : les mêmes données donnent le même fichier, dont le
// SHA-256 est conservé à l'enregistrement de l'envoi.
//
// La plateforme ne l'envoie jamais : un membre le télécharge et l'envoie
// lui-même par le canal officiel de l'élu.
func ComposerDossier(e *Engagement, st *Statut, r ReglagesPreavis) []byte {
	t := r.Textes
	valeur := t.T("obs.valeur." + string(st.Valeur))
	d := pdf.Nouveau(t.F("dossier.titre_document", e.ID, st.ID), func(page, pages int) string {
		return t.F("dossier.pied", e.ID, st.ID, page, pages)
	})
	d.Gras(t.T("dossier.emetteur"))
	d.Titre(t.T("dossier.titre"))
	d.Paragraphe(t.F("dossier.destinataire", e.Elu.Nom, e.Elu.Fonction,
		t.F("obs.mandat", t.Date(e.Elu.Mandat.Debut), t.Date(e.Elu.Mandat.Fin))))
	d.Paragraphe(t.F("dossier.objet", e.ID, valeur))
	d.Paragraphe(t.F("dossier.introduction", valeur, r.Jours))

	d.Intertitre(t.T("dossier.engagement"))
	d.Paragraphe(t.T("obs.fiche.citation") + " :")
	d.Citation(t.F("dossier.citation", e.Citation))
	d.Paragraphe(t.F("dossier.declaration", t.Date(e.DateDeclaration)))
	d.Paragraphe(t.F("dossier.echeance", t.Date(e.Echeance)))
	if e.Contexte != "" {
		d.Paragraphe(t.F("dossier.contexte", e.Contexte))
	}

	d.Intertitre(t.T("dossier.sources"))
	sources(d, t, e.Sources)

	d.Intertitre(t.T("dossier.statut"))
	d.Gras(t.F("dossier.statut_propose", valeur))
	d.Paragraphe(t.T("dossier.explication"))
	d.Citation(st.Explication)
	if st.Validation != nil {
		d.Paragraphe(t.F("dossier.redige_valide", st.Redige.ParNom, t.Date(st.Redige.Le), st.Validation.ParNom, t.Date(st.Validation.Le)))
	}
	d.Paragraphe(t.T("dossier.preuves"))
	sources(d, t, st.Preuves)

	d.Intertitre(t.T("dossier.delai"))
	d.Paragraphe(t.F("dossier.delai.texte", r.Jours))
	d.Paragraphe(t.T("dossier.sans_reponse"))

	d.Intertitre(t.T("dossier.repondre"))
	d.Paragraphe(t.T("dossier.repondre.texte"))
	d.Fixe(r.AdresseReponse)
	d.Paragraphe(t.T("dossier.repondre.canaux"))
	d.Paragraphe(t.F("dossier.repondre.reference", e.ID, st.ID))
	d.Paragraphe(t.T("dossier.publication"))
	if !pdf.Representable(e.Citation) || !pdf.Representable(st.Explication) {
		d.Paragraphe(t.T("dossier.caracteres"))
	}
	return d.Octets()
}

func sources(d *pdf.Document, t Textes, ss []Source) {
	for i, s := range ss {
		d.Gras(strconv.Itoa(i+1) + ". " + s.Titre + " (" + t.Date(s.Date) + ")")
		d.Paragraphe(t.T("dossier.source.lien"))
		d.Fixe(s.URL)
		d.Paragraphe(t.T("dossier.source.sha256"))
		d.Fixe(s.Archive.String())
	}
}
