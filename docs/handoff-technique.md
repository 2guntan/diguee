# Handoff technique : Diguee, plateforme de consultation citoyenne

Date : 22 novembre 2026. Langue de travail avec le porteur : **français**.
Objet de la session suivante : **cadrer et spécifier la plateforme technique** (consultation vérifiée, observatoire des engagements, backend), avant tout code.

## 1. Le projet en bref

- **Diguee** (« Diguee boor la : une promesse est une dette ») est une initiative citoyenne au Sénégal.
- **Phase 1** (en cours) : un observatoire des engagements publics et une consultation pour mesurer le soutien à une loi rendant les élus redevables de leurs promesses, jusqu'à la révocation, **pour les mandats futurs**.
  - La plateforme ne destitue personne.
  - Elle ne vise aucun dirigeant et ne soutient aucun parti. Cette neutralité est une contrainte de conception, pas un slogan.
- **Phase 2** (plus tard, hors périmètre immédiat) : contrat de mandat (la mesure), puis pétition (le signal), puis référendum (la décision).
- Le porteur veut une interface simple et une crédibilité forte. La justesse statistique parfaite n'est pas l'objectif de la phase 1.
- Le porteur est un fondateur pressé : il préfère des recommandations directes à de longues options.

## 2. Ce qui existe déjà (ne pas refaire)

| Élément | Où |
|---|---|
| **Dépôt du projet** (point d'entrée du code, des ADR et de `CONTEXT.md`) | https://github.com/2guntan/diguee |
| Récap complet (nom, message, questionnaire, comptes réseaux, choix) | Google Doc « Diguee - Récap du projet et informations essentielles », dans le Drive du porteur (id `15WKX8vkLOHhCURM7-qO0iuS-t5p1wvq4d6RvdkVqLyQ`) |
| Site vitrine pré-lancement (HTML/CSS statique, sans JS ni backend) | Source dans ce dépôt : `site/index.html`. Déployé dans `public_html/index.html` du projet web PlanetHoster « diguee.sn ». Il sera servi sur https://diguee.sn quand le DNS et le SSL auront fini de se propager (voir §5). |
| Logos finaux (SVG/PNG, favicon, photo de profil) | Dropbox du porteur : `21. diguee/Logos/Logo final/` |
| Maquettes | Fichier Paper « Diguee - Site » (desktop fini, mobile partiel) ; canvas Claude Design (artboards Charte, Accueil, Logo-final) |

Le site actuel montre la question nº 1 avec ses 5 réponses, mais le formulaire est **désactivé** (« La consultation ouvre bientôt »). Il ne publie aucun chiffre : une bande de statut annonce les résultats **à partir de 1 000 réponses, par région et par tranche d'âge**. L'observatoire présente seulement la méthode en quatre champs (Source, Échéance, Statut, Réponse), sans faux engagements. Le futur backend doit tenir ces promesses publiques.

## 3. Décisions déjà prises

- **Vérification** : un numéro sénégalais (+221) avec un code à usage unique, envoyé sur le site **ou via WhatsApp**. L'USSD est écarté pour l'instant. Aucune autre preuve d'identité.
- **Anonymat** affiché sur le site : le numéro est « chiffré, conservé à part de votre réponse, et jamais partagé ». C'est un engagement public à respecter dans l'architecture.
- **Question nº 1** :
  - formulation exacte : « Êtes-vous favorable à une loi qui permettrait, pour les mandats futurs, de révoquer un président qui n'a pas respecté ses engagements de façon grave et mesurée ? » ;
  - échelle : Très favorable / Plutôt favorable / Plutôt opposé / Très opposé / Je ne sais pas ;
  - les autres questions du questionnaire sont dans le Google Doc.
- **Observatoire** : chaque engagement a une source (citée et datée), une échéance, un statut (tenu, partiel, non tenu, en cours) avec ses preuves, et un droit de réponse de l'élu concerné publié tel quel. Il couvre plusieurs mandats, de tous bords.
- **Hébergement actuel** :
  - PlanetHoster, offre World, mutualisée, panneau N0C ;
  - projet web séparé « diguee.sn » ; les ressources sont partagées avec un autre projet du porteur, à garder distinct pour la marque et sur le plan juridique ;
  - DNS sur les serveurs N0C ;
  - **à confirmer : est-ce adapté pour un backend ?**

## 4. Questions ouvertes à trancher

Ce sont les sujets du grilling ; aucun n'est décidé.

1. **Anonymat et doublons** : empêcher qu'un même numéro réponde deux fois sans jamais relier le numéro à la réponse. Pistes : HMAC du numéro avec un secret serveur, table séparée, durée de conservation.
2. **Envoi des codes** :
   - fournisseur SMS au Sénégal, WhatsApp Business (API Cloud de Meta : modèles de message, vérification de l'entreprise), ou les deux ;
   - coût par code, et budget ;
   - lutte contre les fermes de SIM et le spam (limites par IP et par numéro, captcha).
3. **Cadre légal** : déclaration ou autorisation auprès de la CDP (Commission des données personnelles du Sénégal) pour traiter les numéros, la région et l'âge ; mentions légales ; structure qui porte le projet, encore en constitution.
4. **Architecture et hébergement** : rester sur le mutualisé N0C (PHP ou Node selon les limites) ou passer sur un service géré séparé pour l'API et la base ? Contrainte forte : coût faible et maintenance simple.
5. **Publication des résultats** :
   - seuil de 1 000 réponses ;
   - découpage par région et par âge, et seuil minimal par case pour ne pas permettre de ré-identifier quelqu'un ;
   - pondération éventuelle ;
   - affichage en direct ou en lots ;
   - ce qui est visible avant le seuil.
6. **Modèle de données de l'observatoire** : qui saisit et qui valide un engagement, comment versionner les changements de statut, comment recueillir et publier le droit de réponse.
7. **Parcours WhatsApp** : une simple vérification du numéro, ou une consultation complète dans WhatsApp (bot) ?
8. **Crédibilité** : journal d'audit public, code source ouvert ou non, méthodologie publiée, protection contre la manipulation et les attaques.

## 5. Points techniques en suspens (hors spécification)

- diguee.sn résout bien vers le serveur PlanetHoster, mais ne répondait pas encore au moment du handoff : vhost récent et propagation en cours. Vérifier, puis activer le SSL (Let's Encrypt) dans N0C si besoin.
- Rediriger diguee.org vers diguee.sn.
- Éléments encore à remplir sur le site : numéro WhatsApp, structure qui porte le projet, numéro CDP, date de lancement, vrais engagements de l'observatoire.

## 6. Identité, utile pour toute interface

- **Couleurs** :
  - encre `#13233A`, or `#F2B705` ;
  - vert `#0B5D3B` réservé aux boutons et liens, jamais dans le logo ;
  - fond blanc, gris `#5B6472`, filet `#E5E2DA` ;
  - rouge seulement pour le statut « non tenu » ;
  - jamais les couleurs du drapeau ni celles d'un parti.
- **Polices** : Bricolage Grotesque (800 pour les titres, 600 pour le logo texte), Inter pour le texte courant.
- **Style** : sobre, sans effet « IA » ; le porteur le demande explicitement.
- **Contact public** : @digueeboorla sur X, Instagram, TikTok, YouTube et Facebook.

## 7. Règles de travail avec le porteur

- Ne jamais saisir de mot de passe ni de code de vérification : c'est le porteur qui les entre.
- Prévenir avant toute publication sur les comptes Diguee.
- Noms de fichiers lisibles, avec des espaces et sans tirets bas (ex. « Spec plateforme Diguee v1.md »).

## Skills suggérés pour l'agent suivant

1. **`grill-with-docs`** (`mattpocock-skills:grill-with-docs` ou `anthropic-skills:grill-with-docs`) : à lancer en premier, sur les questions du §4. Il produit le glossaire `CONTEXT.md` et une fiche par décision (ADR) au fil de l'eau.
2. **`mattpocock-skills:to-spec`** : une fois les décisions tranchées, en tirer la spécification de la plateforme.
3. **`mattpocock-skills:to-tickets`** : découper la spec en tickets.
4. **`engineering:system-design`** ou **`engineering:architecture`**, en appui sur le choix d'hébergement et l'architecture anonymat/OTP.
5. **`operations:compliance-tracking`**, pour suivre la démarche CDP et les obligations sur les données.
6. Plus tard : **`mattpocock-skills:implement`** ou **`mattpocock-skills:tdd`** pour coder, et **`engineering:deploy-checklist`** avant la mise en production.
