# À faire par le porteur

Ce document liste tout ce que **seul le porteur** peut fournir : comptes, accès, contenus et choix. En attendant, le code tourne avec des **fausses implémentations** et des **valeurs provisoires**. Quand l'élément est disponible, il suffit de changer la configuration, **sans modifier le code**.

**Règle de sécurité** : avec `DIGUEE_ENV=production`, le serveur **refuse de démarrer** s'il reste une fausse implémentation ou une valeur « À COMPLÉTER ». Au démarrage, il affiche la liste exacte de ce qui manque.

Chaque section indique : ce qui tourne en attendant, ce qu'il faut faire, et le réglage à changer. Toutes les variables commencent par `DIGUEE_` (liste complète dans le `README`). La commande `diguee verifier-config` affiche ce qui manque encore. Ce document est mis à jour au fil des tickets (voir « État d'avancement » en bas).

---

## 1. Comptes et services externes

### 1.1 OVHcloud : KMS pour l'empreinte (tickets #5, #7, #15)

- **En attendant** (en place depuis le ticket #2) : `DIGUEE_EMPREINTEUR=fake` (valeur par défaut), un HMAC-SHA-256 calculé avec une clé de développement écrite dans le code (`internal/empreinte`, type `HMACLocal`). Il est déclaré `fake` dans le registre des intégrations : le serveur refuse donc de démarrer en production. La vérification du numéro est elle aussi factice : en développement seulement, la page `/verifier` accepte tout numéro +221 valide sans rien envoyer (les vrais parcours arrivent aux tickets #6, #9 et #10).
- **À faire** :
  1. Créer le compte OVHcloud **réservé à Diguee**, avec la double authentification.
  2. Commander un KMS en région France, puis créer une clé RSA non exportable, utilisable pour signer avec l'algorithme **RS256**.
  3. Créer un accès (certificat ou jeton) qui n'a que la permission `kms:serviceKey/sign` sur cette clé.
- **Réglage** : `DIGUEE_EMPREINTEUR=ovh`, avec l'identifiant de la clé, le point d'accès régional et l'accès créé.
- **Vérification** : au démarrage, le serveur contrôle que la signature est déterministe. S'il refuse de démarrer, il faut basculer sur Google Cloud KMS (`DIGUEE_EMPREINTEUR=gcp`, ADR 0006).

### 1.2 OVHcloud : VPS et DNS (ticket #7)

- **En attendant** : le développement se fait en local. Les scripts de déploiement sont écrits, mais n'ont pas tourné sur un vrai serveur.
- **À faire** :
  1. Commander un VPS-1 en région France, réservé à Diguee.
  2. Faire pointer `consultation.diguee.sn` vers son adresse IP. La zone DNS est chez N0C, sur PlanetHoster.
  3. Exécuter la procédure de déploiement documentée par le ticket #7.

### 1.3 OVHcloud : stockage d'objets (tickets #13, #15)

- **En attendant** : `DIGUEE_STOCKAGE=fake`, un dossier local.
- **À faire** : créer un bucket S3 en France et une clé d'accès limitée à ce bucket.
- **Réglage** : `DIGUEE_STOCKAGE=ovh`, avec le point d'accès, le bucket et la clé.

### 1.4 Clé de chiffrement des sauvegardes (ticket #15)

- **En attendant** : une paire de clés `age` de test, générée pour les tests.
- **À faire** : générer **vous-même** une paire de clés `age` sur votre ordinateur (`age-keygen`). Gardez la clé privée **hors ligne**, dans un gestionnaire de mots de passe et sur un support de secours, et donnez seulement la clé publique.
- **Réglage** : la clé publique des sauvegardes.

### 1.5 Meta : WhatsApp Business (tickets #6, #9)

- **En attendant** : `DIGUEE_WHATSAPP=fake`. Une page de développement simule l'envoi du message par le citoyen (vérification inversée), et les codes de secours sont affichés dans la console. Ce mode est interdit en production.
- **À faire** :
  1. Créer le portefeuille business Meta et l'application WhatsApp, avec la vérification de l'entreprise.
  2. Obtenir le **numéro WhatsApp de Diguee**.
  3. Faire approuver un modèle de message d'**authentification** pour le code de secours.
  4. Déclarer l'adresse du webhook (`https://consultation.diguee.sn/webhook/whatsapp`) avec un jeton de vérification.
- **Réglage** : `DIGUEE_WHATSAPP=meta`, avec l'identifiant du numéro, le jeton d'accès, le secret de l'application (qui sert à vérifier les signatures), le jeton de vérification du webhook et le nom du modèle.
- **À vérifier** : le tarif réel pour le Sénégal dans votre gestionnaire WhatsApp.

### 1.6 Fournisseur SMS (ticket #10)

- **En attendant** : `DIGUEE_SMS=fake`, avec les codes affichés dans la console.
- **À faire** : choisir un fournisseur qui a un **accord direct avec les opérateurs sénégalais** (ADR 0004), ouvrir le compte et régler un **plafond de dépenses journalier** chez lui.
- **Réglage** : `DIGUEE_SMS=<nom du fournisseur>`, avec ses accès. Un petit adaptateur sera à écrire pour ce fournisseur : il implémente l'interface `Envoyeur`.

### 1.7 Clé de chiffrement des secrets TOTP de l'espace équipe (ticket #4)

- **En attendant** : sans `DIGUEE_CLE_TOTP`, les secrets TOTP sont chiffrés avec une clé de développement écrite dans le code (`internal/equipe/chiffrement.go`). Elle est déclarée `fake` dans le registre des intégrations (`chiffrement_totp`) : le serveur, et la commande `diguee creer-membre`, refusent donc de démarrer en production.
- **À faire** : générer **vous-même**, sur le serveur, une clé de 32 octets : `openssl rand -hex 32`. La mettre dans le fichier d'environnement de systemd (jamais dans le dépôt), et en garder une copie hors ligne, avec la clé privée des sauvegardes : une sauvegarde de la base ne sert à rien pour l'espace équipe sans elle.
- **Réglage** : `DIGUEE_CLE_TOTP=<64 chiffres hexadécimaux>`.
- **Attention** : changer ou perdre cette clé rend illisibles tous les secrets TOTP déjà enregistrés. Il faut alors recréer l'accès de chacun : un administrateur créé par `diguee creer-membre` (avec un nouvel identifiant), puis « Réinitialiser l'accès » pour les autres comptes dans `/equipe/membres`.

## 2. Contenus et valeurs à fournir

Ces valeurs sont « À COMPLÉTER » dans la configuration. Elles s'affichent comme telles sur les pages de développement.

| Élément | Réglage | Où il apparaît | Ticket |
|---|---|---|---|
| Nom et adresse de la structure (responsable du traitement) | `DIGUEE_STRUCTURE_NOM`, `DIGUEE_STRUCTURE_ADRESSE` | Politique de confidentialité, premier message WhatsApp | #1, #6 |
| Numéro CDP | `DIGUEE_NUMERO_CDP` | Politique de confidentialité, pied de page | #1 |
| Adresse de contact (droits d'accès et d'opposition) | `DIGUEE_CONTACT` | Politique de confidentialité | #1 |
| Date d'ouverture (la clôture tombe 6 mois plus tard) | `DIGUEE_OUVERTURE` (format `AAAA-MM-JJ` ; `DIGUEE_CLOTURE` est calculée, mais peut être fixée) | Accueil, méthodologie, clôture | #1, #16 |
| Échelles de réponse de Q1 et Q2. **Provisoire** : les propositions de la spec §3, Q1 « Toujours / Souvent / Rarement / Jamais / Je ne sais pas », Q2 « Oui, toujours / Oui, pour les engagements graves / Non / Je ne sais pas » | libellés : clés `questionnaire.q1.choix.*` et `questionnaire.q2.choix.*` de `internal/i18n/catalogues/fr.json` ; nombre de choix et marque `Provisoire` : `internal/questionnaire/v1.go` | Questionnaire, récapitulatif | #2 |
| Choix de réponse de Q5 (« qui devrait évaluer »). **Provisoire**, proposé par le développement : « Une autorité indépendante / Le Conseil constitutionnel / L'Assemblée nationale / Un panel de citoyens tirés au sort / Je ne sais pas » | clés `questionnaire.q5.choix.*` du catalogue `fr` ; nombre de choix et marque `Provisoire` : `internal/questionnaire/v1.go` | Questionnaire, récapitulatif | #2 |
| Choix du critère « sexe » du profil (la spec ne les fixe pas) : « Femme / Homme / Je préfère ne pas répondre », à confirmer | clés `profil.sexe.choix.*` du catalogue `fr` | Questionnaire | #2 |
| Numéro WhatsApp de Diguee | `DIGUEE_NUMERO_WHATSAPP` | Vérification inversée (lien `wa.me`) | #1, #6 |
| Liste définitive des préfixes mobiles (ARTP) | `DIGUEE_PREFIXES_AUTORISES` (défaut provisoire `70,75,76,77,78`) | Vérification | #6 |
| Traductions en wolof et en pulaar | `internal/i18n/catalogues/wo.json` et `ff.json` ; une langue n'est proposée qu'une fois son catalogue complet | Catalogues `wo` et `ff` | #1 |
| Composition de la population (ANSD) par région, âge et sexe | à définir (ticket #11) | Page des résultats | #11 |
| Premiers élus et engagements de l'observatoire | espace équipe (ticket #13) | Observatoire | #13 |

**Questionnaire** : tant que Q1, Q2 ou Q5 sont marquées `Provisoire` dans `internal/questionnaire/v1.go`, le serveur refuse de démarrer en production (`diguee verifier-config` les liste). Une fois les choix validés : ajuster les libellés du catalogue, le nombre de choix dans `v1.go` si besoin, puis retirer la marque. Les codes d'une version publiée ne doivent plus changer ensuite (spec §3.2 : une nouvelle version ne peut être publiée qu'avant l'ouverture).

Valeurs qui ont un défaut tiré de la spec, à confirmer si besoin : `DIGUEE_ALTCHA_DIFFICULTE` (100000, provisoire jusqu'au ticket #8) et les seuils du §13 (`DIGUEE_SEUIL_PUBLICATION`, `DIGUEE_SEUIL_CASE`, etc.).

Secrets à fournir en production : `DIGUEE_POSTGRES_URL`, l'adresse de la base avec son mot de passe (ticket #7), et `DIGUEE_CLE_TOTP` (§1.7), à mettre dans le fichier d'environnement de systemd, jamais dans le dépôt.

### 2.1 Journal public (ticket #3) : points à valider

- **Rôle SQL en production (ticket #7)** : la base refuse `UPDATE`, `DELETE` et `TRUNCATE` sur le journal pour tous les rôles, propriétaire compris. Mais le propriétaire de la table peut désactiver ces déclencheurs. Aujourd'hui, le serveur applique lui-même les migrations, donc il est propriétaire. Recommandation pour le ticket #7 : un rôle propriétaire réservé à `diguee migrer`, et un rôle d'application qui n'a que `SELECT` et `INSERT` sur `journal_public` (le verrou d'écriture est consultatif et ne demande aucun autre droit). Cela suppose que `diguee` (servir) n'applique plus les migrations en production.
- **Exception à T1** : la spec §10 exige un horodatage à la minute dans le journal, alors que T1 interdit tout horodatage plus fin que le jour dans un `pg_dump`. Le test T1 exclut donc la seule table `journal_public`, et vérifie que les envois n'y écrivent rien. À confirmer.
- **Ancrage extérieur** : OpenTimestamps, archive.org et le texte hebdomadaire pour X arrivent au ticket #12. D'ici là, seule la chaîne interne et la comparaison avec un export ou une empreinte gardés à part détectent une réécriture.
- La commande publiée `go run github.com/2guntan/diguee/cmd/diguee@latest verifier-journal …` suppose que le dépôt soit public (ADR 0010).

### 2.2 Espace équipe (ticket #4) : points à valider

- **Mot de passe en plus du TOTP** : le ticket demande un TOTP « au minimum ». Un code de 6 chiffres seul resterait devinable à la longue, même avec la limitation des échecs : la connexion demande donc aussi un mot de passe. Il est **tiré par la plateforme** (20 signes, 100 bits), affiché une seule fois avec le secret TOTP, et seule son empreinte PBKDF2-SHA-256 est gardée. Aucun membre ne choisit ni ne change son mot de passe : en cas d'oubli ou de perte du téléphone, un administrateur « réinitialise l'accès ».
- **Pas de QR code** : il faudrait du JavaScript ou un générateur de QR code écrit dans le dépôt. Le secret est affiché en base32 (saisie manuelle dans l'application) et l'URI `otpauth://` peut être transformée en QR code **hors ligne** (jamais par un site web, qui verrait le secret).
- **WebAuthn (clé de sécurité)** : hors périmètre de ce ticket, comme le permet la spec (« TOTP au minimum »). À ajouter plus tard si vous le souhaitez (une bibliothèque WebAuthn est autorisée par la spec §11).
- **Sessions et anti-rejeu en mémoire** : aucune colonne plus fine que le jour en base (même règle que T1). Un redémarrage du serveur ferme donc toutes les sessions et, juste après, il faut attendre le code TOTP suivant (30 secondes au plus) pour se connecter.
- **Limites des échecs** (constantes du code, `internal/equipe/service.go`) : 5 échecs par identifiant en 15 minutes, 20 échecs par adresse en une heure, comptés en mémoire. Un tiers qui connaît un identifiant peut donc bloquer ce compte pendant 15 minutes.
- **Audit interne plutôt que journal public** : la spec §9 dit « chaque action inscrite au journal ». Les actions sur les comptes (création, rôles, réinitialisation, désactivation, connexion) ne sont pas des publications et le journal public ne prévoit pas ces types (spec §10) ; elles vont dans la table `audit_equipe`, en ajout seul comme le journal, datées au jour près, lisible par les administrateurs sur `/equipe/audit`. Les actions éditoriales du ticket #13 (statut publié, droit de réponse) iront, elles, au journal public.
- **Désactivation définitive** : un compte désactivé ne peut pas être réactivé (la base le refuse) ni supprimé (les statuts citeront leurs auteurs). Pour revenir, une personne reçoit un nouveau compte. Il reste toujours au moins un administrateur actif, et un administrateur ne peut pas désactiver son propre compte.
- **Rôle SQL en production (ticket #7)** : comme pour le journal public, le rôle de l'application ne devrait avoir que `SELECT` et `INSERT` sur `audit_equipe`, et pas `DELETE` sur `membre`.

## 3. Actions humaines prévues

- Recopier `site/index.html` (FAQ corrigée) dans `public_html/` chez PlanetHoster.
- Créer les comptes de l'équipe (deux personnes) dans l'espace équipe, avec un TOTP chacun : procédure ci-dessous.

### 3.1 Créer le premier compte de l'espace équipe (ticket #4)

1. Régler `DIGUEE_CLE_TOTP` (§1.7) **avant** de créer le moindre compte.
2. Sur le serveur, avec le même fichier d'environnement que le service, lancer :
   `diguee creer-membre -identifiant <votre.identifiant> -nom "<Prénom Nom>" -roles administrateur,redacteur,relecteur`
   (identifiant : 2 à 40 caractères, minuscules sans accent, chiffres, `.`, `_`, `-` ; `-roles` vaut `administrateur` par défaut).
3. La commande affiche **une seule fois** l'identifiant, le mot de passe, le secret TOTP et l'URI `otpauth://`. Rien n'est écrit dans les journaux. Enregistrez le secret dans une application d'authentification (TOTP, SHA-1, 6 chiffres, 30 secondes ; saisie manuelle du secret, ou QR code fait hors ligne à partir de l'URI) et le mot de passe dans un gestionnaire de mots de passe. Fermez ensuite le terminal (ne laissez pas la sortie dans un fichier ni dans un historique partagé).
4. Connectez-vous sur `https://consultation.diguee.sn/equipe/`, puis créez le compte de la deuxième personne dans « Comptes » : ses accès s'affichent une seule fois, à lui remettre en main propre.
5. La création par la commande est inscrite à l'audit interne (« Création du compte en ligne de commande »). Si besoin plus tard, la même commande crée un nouvel administrateur (par exemple si plus personne ne peut se connecter).
- Publier chaque semaine sur X l'empreinte du journal préparée par la plateforme.
- Ticket #18 : audit en préproduction et test d'intrusion avant le lancement.
- Fusionner la branche de travail dans `main` quand vous le jugerez bon.

---

## État d'avancement du développement

Mis à jour à la fin de chaque ticket : ce qui est fait, et les éventuelles valeurs provisoires ajoutées.

| Ticket | État | Notes |
|---|---|---|
| #1 | fait | Squelette Go : accueil (question clé, compteur lu en base), Méthodologie et Politique de confidentialité avec la configuration réelle, catalogues `fr` (livré), `wo` et `ff` (prévus), migrations, garde de production et registre des intégrations, build reproductible (version et SHA-256 au pied de page), CI GitHub Actions. Valeurs provisoires ajoutées : `DIGUEE_STRUCTURE_NOM`, `DIGUEE_STRUCTURE_ADRESSE`, `DIGUEE_CONTACT`, `DIGUEE_NUMERO_CDP`, `DIGUEE_NUMERO_WHATSAPP`, `DIGUEE_OUVERTURE` ; défaut provisoire `DIGUEE_ALTCHA_DIFFICULTE`. Aucune fausse implémentation pour l'instant. |
| #2 | fait | Parcours de réponse de bout en bout, sans JavaScript : accueil (bouton « Répondre à la consultation » pendant l'ouverture), vérification factice (développement seulement), questionnaire v1 (Q1 à Q8, profil, « 18 ans ou plus » obligatoire), récapitulatif « définitive », envoi, merci. Tampon mélangé en mémoire (`DIGUEE_TAMPON_TAILLE`, `DIGUEE_TAMPON_DELAI`, écriture à l'arrêt), deux tables sans lien, identifiants aléatoires, jour seulement, doublon refusé en base et dans le tampon. Sessions en mémoire. Fausse implémentation ajoutée : `DIGUEE_EMPREINTEUR=fake` (remplacée par `ovh` au ticket #5). Valeurs provisoires ajoutées : échelles de Q1 et Q2, choix de Q5 (marque `Provisoire`, bloquante en production), choix du critère « sexe » à confirmer. |
| #3 | fait | Journal public chaîné (SHA-256 du JSON canonique RFC 8785), en ajout seul jusque dans la base (déclencheurs : `UPDATE`, `DELETE`, `TRUNCATE` refusés, insertion hors chaîne refusée), page `/journal` paginée et export `/journal.json`, commande `diguee verifier-journal <fichier|url>` (options `-attendu` et `-precedent`), API d'écriture pour les tickets suivants, entrée « mise en production » automatique au démarrage sur une nouvelle version. Aucune fausse implémentation ni valeur provisoire ajoutée ; points à valider en §2.1. |
| #4 | fait | Espace équipe `/equipe` séparé des pages publiques : comptes nominatifs, rôles cumulables rédacteur / relecteur / administrateur, connexion par identifiant + mot de passe tiré par la plateforme + TOTP (RFC 6238 écrit dans le dépôt, anti-rejeu ; jamais de SMS ; WebAuthn hors périmètre), secret TOTP chiffré (AES-256-GCM), sessions de 8 h en mémoire, CSRF, échecs limités, aucune IP journalisée, administration des comptes réservée aux administrateurs (création, rôles, réinitialisation, désactivation qui ferme les sessions), audit interne `audit_equipe` en ajout seul, commande `diguee creer-membre` (§3.1). Fausse implémentation ajoutée : clé de développement des secrets TOTP (`DIGUEE_CLE_TOTP` absente, §1.7). Points à valider en §2.2. |
