# Diguee

Initiative citoyenne sénégalaise : un observatoire des engagements publics et une consultation vérifiée qui mesure le soutien à une loi rendant les élus redevables de leurs promesses, pour les mandats futurs.

## Language

### Consultation

**Consultation** :
Le dispositif qui recueille, une fois par numéro vérifié, l'avis des citoyens sur le questionnaire.
_Avoid_ : vote, sondage, référendum

**Questionnaire** :
L'ensemble des questions posées dans la consultation, dont la question nº 1 ; il est envoyé en une seule fois.
_Avoid_ : formulaire

**Participation** :
Le fait qu'un numéro vérifié a déjà répondu, enregistré sans aucun lien avec le contenu de la réponse.
_Avoid_ : vote, inscription

**Empreinte** :
La valeur calculée à partir d'un numéro vérifié avec une clé secrète, qui permet de repérer un doublon sans conserver le numéro.
_Avoid_ : numéro chiffré, hash

**Clôture** :
La fin de la consultation, six mois après l'ouverture, où la clé et toutes les empreintes sont détruites.
_Avoid_ : fin du vote

**Vérification inversée** :
La preuve qu'une personne détient un numéro, obtenue lorsqu'elle envoie à Diguee, par WhatsApp, un jeton affiché sur le site.
_Avoid_ : OTP inversé, confirmation

**Code** :
Le code à usage unique envoyé par Diguee au numéro, en secours de la vérification inversée (par WhatsApp, puis par SMS), pour vérifier qu'il appartient bien à la personne.
_Avoid_ : OTP (en dehors du code), mot de passe

**Profil** :
Les quatre critères déclarés à la fin du questionnaire : région ou diaspora, tranche d'âge, sexe, inscription sur les listes électorales.
_Avoid_ : identité, données personnelles

**Thème** :
Un des sujets d'une liste fermée, parmi lesquels le participant choisit ses 3 engagements prioritaires ; il n'y a pas de texte libre.
_Avoid_ : priorité libre, commentaire

**Réponse** :
Le contenu d'un questionnaire rempli, avec la région et la tranche d'âge déclarées, sans aucun identifiant de la personne.
_Avoid_ : bulletin, vote

### Résultats

**Seuil de publication** :
Le nombre de réponses (1 000) en dessous duquel seul le compteur global est affiché, sans aucune répartition.
_Avoid_ : quorum

**Publication quotidienne** :
Le recalcul des résultats, une fois par jour à heure fixe, sur les réponses écrites jusqu'à la veille, consigné dans le journal public.
_Avoid_ : résultats en direct, temps réel

**Journal public** :
La suite chaînée et horodatée hors de nos serveurs de tout ce que Diguee publie ou change : résultats, statuts, versions du code, méthodologie, destruction à la clôture.
_Avoid_ : log, historique

### Observatoire

**Engagement** :
Une promesse publique d'un élu, citée mot pour mot, avec sa source datée et son échéance.
_Avoid_ : promesse (dans les données), mesure

**Source** :
Le document d'origine d'un engagement ou d'une preuve, avec son lien et la copie archivée par Diguee.
_Avoid_ : lien, référence

**Statut** :
L'état d'un engagement à une date donnée : tenu, partiel, non tenu ou en cours, toujours accompagné de ses preuves.
_Avoid_ : note, verdict

**Rédacteur** :
Le membre de l'équipe qui saisit un engagement ou propose un nouveau statut.

**Relecteur** :
Le membre de l'équipe, distinct du rédacteur, qui valide la saisie avant sa publication.
_Avoid_ : modérateur, validateur

**Préavis** :
Le délai de 15 jours laissé à l'élu, après envoi du dossier, avant la publication d'un statut « non tenu » ou « partiel ».

**Droit de réponse** :
Le texte de l'élu concerné, reçu par un canal vérifiable et publié tel quel sous l'engagement.
_Avoid_ : commentaire, réaction

### Cadre

**Responsable du traitement** :
La structure juridique qui porte Diguee et qui répond légalement des données ; elle est distincte de la marque Diguee.
_Avoid_ : propriétaire, éditeur

## Relationships

- Une **Participation** et une **Réponse** naissent au même envoi, mais ne sont jamais reliées l'une à l'autre.
- Une **Réponse** couvre tout le **Questionnaire** et est **définitive** : ni modifiable, ni supprimable par la personne.
- Une **Réponse** contient un **Profil** et 3 **Thèmes** au plus.
- Un numéro vérifié produit au plus une **Participation**, repérée par son **Empreinte**.
- À la **Clôture**, les **Participations** et leurs **Empreintes** sont détruites ; seules les **Réponses** restent.

- Un **Engagement** a une ou plusieurs **Sources** et une suite de **Statuts**, jamais modifiés, seulement ajoutés.
- Chaque **Statut** est saisi par un **Rédacteur** et validé par un **Relecteur** distinct.
- Un **Statut** « non tenu » ou « partiel » n'est publié qu'après un **Préavis**. Il est accompagné, le cas échéant, du **Droit de réponse**.

## Example dialogue

> **Dev :** « Awa veut changer sa **Réponse** après un débat télé : on retrouve sa ligne grâce à sa **Participation** ? »
> **Porteur :** « Non. La **Réponse** est définitive, et on ne peut pas la retrouver : c'est justement la garantie d'anonymat. »

## Flagged ambiguities

- « diaspora » ne désigne que les Sénégalais de l'étranger qui ont un numéro +221 : les autres ne peuvent pas répondre en phase 1.

- « chiffré » était employé pour le numéro : c'est inexact, car on ne peut pas retrouver le numéro à partir de son **Empreinte**. Le terme est corrigé sur le site.

- « vote » désigne parfois la **Participation**, parfois la **Empreinte** :
La valeur calculée à partir d'un numéro vérifié avec une clé secrète, qui permet de repérer un doublon sans conserver le numéro.
_Avoid_ : numéro chiffré, hash

**Clôture** :
La fin de la consultation, six mois après l'ouverture, où la clé et toutes les empreintes sont détruites.
_Avoid_ : fin du vote

**Réponse** : ce sont deux choses distinctes et volontairement déliées. Le mot « vote » est réservé au futur référendum (phase 2).
