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
4. **Architecture et hébergement** : l'orientation est retenue (voir §8). Il reste à choisir l'hébergeur de l'API et de la base, et le pays où les données sont stockées, en lien avec la CDP. Contrainte forte : coût faible et maintenance simple.
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

## 8. Orientation technique retenue par le porteur

Le porteur a validé ces choix le 22/11/2026. Ce sont des hypothèses de départ : le grilling doit les éprouver, puis les consigner en ADR, sans les rouvrir de zéro.

**Menaces prioritaires**, dans l'ordre :

1. bourrage de votes (fermes de SIM, numéros jetables, robots) ;
2. fraude au SMS (envois massifs de codes vers des numéros surtaxés) ;
3. saturation du site et défacement ;
4. fuite ou réquisition de données permettant de savoir qui a répondu quoi. C'est le scénario le plus grave.

**Principes** :

- **Pas de lien entre l'identité et la réponse.** Le numéro n'est jamais stocké en clair : seulement un HMAC calculé avec une clé gardée hors de la base. La table « a déjà voté » et la table des réponses ne partagent aucune clé. Les horodatages sont grossiers et les écritures groupées, pour éviter qu'on les apparie par l'heure. Une fuite de la base ne doit rien révéler sur personne.
- **Minimisation.** Collecter seulement la région et la tranche d'âge déclarées. Pas d'adresse IP complète dans les journaux.
- **Anti-fraude dès l'entrée** :
  - liste blanche des préfixes mobiles sénégalais ;
  - limites par numéro et par IP ;
  - captcha discret avant l'envoi d'un code ;
  - plafond de dépenses chez le fournisseur de codes.
- **Isolation.** Le backend ne va pas sur l'hébergement mutualisé PlanetHoster, qui est partagé avec l'activité d'une autre entreprise du porteur. Prévoir :
  - un pare-feu applicatif et une protection anti-saturation devant le site (Cloudflare ou équivalent) ;
  - une base PostgreSQL gérée et chiffrée ;
  - des sauvegardes chiffrées.
- **Transparence.** Code ouvert, méthodologie publiée, journal public des résultats, test d'intrusion avant le lancement. Double authentification sur tous les comptes, et secrets gérés hors du dépôt.
- **Légal.** Vérifier avec la CDP si un hébergement des données hors du Sénégal demande une autorisation de transfert, avant de choisir l'hébergeur.

**Technologies** :

- **API en Go**, avec la bibliothèque standard en priorité et un minimum de dépendances : moins de risque de bibliothèque compromise, un code facile à auditer, un seul exécutable.
- **Pages HTML générées côté serveur** (`html/template`) avec très peu de JavaScript, pour rester légères sur mobile en 3G.
- **PostgreSQL**.
- La vitrine statique actuelle (`site/index.html`) reste valable : la consultation viendra la compléter.
- **Rust est écarté pour la phase 1.** Il ne répond pas aux risques principaux, peu de développeurs le maîtrisent à Dakar, et il ralentirait le développement. À reconsidérer en phase 2 pour un composant critique isolé, comme un module de vote vérifiable.

## Skills suggérés pour l'agent suivant

1. **`grill-with-docs`** (`mattpocock-skills:grill-with-docs` ou `anthropic-skills:grill-with-docs`) : à lancer en premier, sur les questions du §4. Il produit le glossaire `CONTEXT.md` et une fiche par décision (ADR) au fil de l'eau.
2. **`mattpocock-skills:to-spec`** : une fois les décisions tranchées, en tirer la spécification de la plateforme.
3. **`mattpocock-skills:to-tickets`** : découper la spec en tickets.
4. **`engineering:system-design`** ou **`engineering:architecture`**, en appui sur le choix d'hébergement et l'architecture anonymat/OTP.
5. **`operations:compliance-tracking`**, pour suivre la démarche CDP et les obligations sur les données.
6. Plus tard : **`mattpocock-skills:implement`** ou **`mattpocock-skills:tdd`** pour coder, et **`engineering:deploy-checklist`** avant la mise en production.

## 9. Décisions issues du grilling (22/11/2026)

Les décisions difficiles à défaire sont dans `docs/adr/`, et le vocabulaire dans `CONTEXT.md`. Cette section garde les réglages faciles à ajuster, pour la future spécification.

- **Structure** : une structure juridique distincte de l'autre entreprise du porteur existe déjà, avec ses statuts et son NINEA. Elle porte les comptes Meta et SMS. Une association pourra être créée plus tard pour reprendre le projet ; la mention légale du site devra alors être adaptée. Le §5 (« structure en constitution ») est donc dépassé.
- **Anti-fraude avant l'envoi d'un code** :
  - préfixes acceptés : 70, 75, 76, 77, 78 (liste à confirmer auprès de l'ARTP) ;
  - captcha ALTCHA, auto-hébergé, vérifié côté serveur (voir ADR 0007) ;
  - par numéro : 3 codes par 24 h, un code valable 10 minutes, 5 essais par code ;
  - par IP : 30 demandes par heure, puis ralentissement et alerte, sans blocage (à cause du CGNAT des opérateurs) ;
  - compteurs en mémoire, sans IP dans les journaux ;
  - coupe-circuit : plafond journalier chez les fournisseurs, et suspension automatique du SMS au-delà de 3 fois le volume horaire moyen ;
  - fermes de SIM : risque accepté, avec la règle « une SIM, une participation » publiée et une surveillance des pics par préfixe ou par région.
- **Publication** : en dessous de 1 000 réponses, seul le compteur global est affiché. Au-delà, les résultats sont recalculés une fois par jour sur les réponses jusqu'à la veille, et chaque publication est inscrite au journal public. Le compteur suit le même rythme quotidien. La FAQ du site a été corrigée (« mis à jour chaque jour » au lieu de « en continu »).
