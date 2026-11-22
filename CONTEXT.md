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

**Code** :
Le code à usage unique envoyé au numéro, par WhatsApp en priorité, par SMS en secours, pour vérifier qu'il appartient bien à la personne.
_Avoid_ : OTP (en dehors du code), mot de passe

**Réponse** :
Le contenu d'un questionnaire rempli, avec la région et la tranche d'âge déclarées, sans aucun identifiant de la personne.
_Avoid_ : bulletin, vote

## Relationships

- Une **Participation** et une **Réponse** naissent au même envoi, mais ne sont jamais reliées l'une à l'autre.
- Une **Réponse** couvre tout le **Questionnaire** et est **définitive** : ni modifiable, ni supprimable par la personne.
- Un numéro vérifié produit au plus une **Participation**, repérée par son **Empreinte**.
- À la **Clôture**, les **Participations** et leurs **Empreintes** sont détruites ; seules les **Réponses** restent.

## Example dialogue

> **Dev :** « Awa veut changer sa **Réponse** après un débat télé : on retrouve sa ligne grâce à sa **Participation** ? »
> **Porteur :** « Non. La **Réponse** est définitive, et on ne peut pas la retrouver : c'est justement la garantie d'anonymat. »

## Flagged ambiguities

- « chiffré » était employé pour le numéro : c'est inexact, car on ne peut pas retrouver le numéro à partir de son **Empreinte**. Le terme est corrigé sur le site.

- « vote » désigne parfois la **Participation**, parfois la **Empreinte** :
La valeur calculée à partir d'un numéro vérifié avec une clé secrète, qui permet de repérer un doublon sans conserver le numéro.
_Avoid_ : numéro chiffré, hash

**Clôture** :
La fin de la consultation, six mois après l'ouverture, où la clé et toutes les empreintes sont détruites.
_Avoid_ : fin du vote

**Réponse** : ce sont deux choses distinctes et volontairement déliées. Le mot « vote » est réservé au futur référendum (phase 2).
