# Spécification de la plateforme Diguee, v1 (phase 1)

Date : 23 novembre 2026. Statut : **brouillon à relire par le porteur**.

Sources : `CONTEXT.md` (vocabulaire), `docs/adr/0001` à `0010` (décisions), `docs/handoff-technique.md` §8 et §9 (principes et réglages). En cas de désaccord, les ADR priment. Les termes en **gras** sont définis dans `CONTEXT.md`.

---

## 1. Objet et périmètre

La plateforme réalise trois choses en phase 1 :

1. **La consultation.** Elle recueille au plus une **Réponse** par numéro mobile sénégalais vérifié, sans jamais relier le numéro à la réponse.
2. **Les résultats.** Une **Publication quotidienne** des agrégats commence au-delà du **Seuil de publication**, avec des règles anti-réidentification.
3. **L'observatoire.** Il présente les **Engagements** publics, leurs **Statuts** sourcés et le **Droit de réponse** des élus.

Un **Journal public** chaîné et ancré à l'extérieur rend l'ensemble vérifiable.

**Hors périmètre en phase 1** :
- USSD ;
- bot de consultation dans WhatsApp (ADR 0009) ;
- pondération des résultats ;
- plusieurs instances du serveur (ADR 0003) ;
- Rust ;
- tout ce qui relève de la phase 2 (contrat de mandat, pétition, référendum) ;
- les démarches CDP, gérées par le porteur.

## 2. Exigences non négociables

Chacune de ces exigences correspond à un test d'acceptation (§14).

| # | Exigence | Origine |
|---|---|---|
| E1 | Aucune donnée stockée ne permet de relier une **Participation** à une **Réponse** : ni clé commune, ni ordre d'écriture, ni horodatage fin. | ADR 0001, 0003 |
| E2 | Le numéro n'est jamais écrit sur disque : ni en base, ni dans les journaux, ni dans les sauvegardes, ni dans les traces d'erreur. Seule son **Empreinte** est conservée. | ADR 0002 |
| E3 | L'empreinte est calculée par le KMS d'OVHcloud avec une clé non exportable. Le serveur ne voit jamais cette clé. | ADR 0002, 0006 |
| E4 | À la **Clôture**, la clé, la table des participations et toute sauvegarde qui la contient sont détruites, puis un procès-verbal est inscrit au journal. | ADR 0002 |
| E5 | Aucun tiers ne voit à la fois le numéro et les réponses : pas de proxy qui déchiffre le trafic, pas de questionnaire dans WhatsApp. | ADR 0007, 0009 |
| E6 | Aucune adresse IP n'est écrite dans les journaux. | §8 |
| E7 | Une publication ou un changement de statut dans l'observatoire exige deux personnes distinctes, et la plateforme l'impose techniquement. | ADR 0008 |
| E8 | Le journal public est chaîné (SHA-256), horodaté chaque jour avec OpenTimestamps et archivé sur archive.org. | ADR 0010 |
| E9 | Les pages restent utilisables sans JavaScript, et légères en 3G : moins de 100 Ko par page, hors polices. | §8 |

## 3. Le questionnaire

Il se remplit en 2 minutes environ. Chaque question propose « Je ne sais pas ». Il est envoyé en une seule fois, et la réponse est **définitive** (ADR 0001).

| # | Question | Type | Choix |
|---|---|---|---|
| Q1 | Les promesses de campagne sont-elles généralement tenues ? | choix unique | *échelle à fixer* (proposition : Toujours / Souvent / Rarement / Jamais / Je ne sais pas) |
| Q2 | Un élu doit-il rendre des comptes sur ses engagements ? | choix unique | *échelle à fixer* (proposition : Oui, toujours / Oui, pour les engagements graves / Non / Je ne sais pas) |
| Q3 | **Question clé**, formulation exacte : « Êtes-vous favorable à une loi qui permettrait, pour les mandats futurs, de révoquer un président qui n'a pas respecté ses engagements de façon grave et mesurée ? » | choix unique | Très favorable / Plutôt favorable / Plutôt opposé / Très opposé / Je ne sais pas |
| Q4 | Qui devrait décider ? | choix unique | Le peuple par référendum / L'Assemblée nationale / Les deux / Je ne sais pas |
| Q5 | Qui devrait évaluer le respect des engagements ? | choix unique | *choix à fixer* |
| Q6 | Quand l'évaluation devrait-elle avoir lieu ? | choix unique | À mi-mandat / Chaque année / À tout moment / Je ne sais pas |
| Q7 | À qui la règle devrait-elle s'appliquer ? | choix multiple | Président / Ministres / Députés / Maires / Je ne sais pas |
| Q8 | Vos 3 engagements prioritaires | 3 choix exactement, ou « Je ne sais pas » | Les 11 **Thèmes** (§3.1) |

**Profil**, demandé à la fin :
- région, parmi les 14 régions, ou diaspora ;
- tranche d'âge : 18-24 / 25-34 / 35-49 / 50-64 / 65 et plus ;
- sexe ;
- inscription sur les listes électorales : Oui / Non / Je ne sais pas.

Chaque critère propose aussi « Je préfère ne pas répondre ». La personne doit déclarer avoir 18 ans ou plus, sinon le questionnaire est refusé.

### 3.1 Thèmes, liste fermée validée le 23/11/2026

1. emploi des jeunes ;
2. coût de la vie ;
3. santé ;
4. éducation ;
5. justice et lutte contre la corruption ;
6. agriculture, élevage et pêche ;
7. eau, électricité et assainissement ;
8. logement ;
9. sécurité ;
10. ressources naturelles (pétrole, gaz, mines) ;
11. transports et routes.

Il n'y a pas de champ libre.

### 3.2 Langues

L'interface est en français au lancement. Tous les textes visibles sont externalisés dans des catalogues par langue (`fr`, `wo`, `ff`), pour ajouter le wolof et le pulaar sans toucher au code. La langue choisie n'est **pas** enregistrée dans la réponse, car cela ajouterait un critère de réidentification.

Le questionnaire est **versionné**. Chaque réponse porte l'identifiant de la version, et une nouvelle version ne peut être publiée qu'avant l'ouverture.

## 4. Parcours du citoyen

Le site public est à l'adresse `consultation.diguee.sn`. La vitrine reste sur `diguee.sn`, chez PlanetHoster, avec un bouton « Répondre » vers la consultation.

```
Accueil ──► Vérifier mon numéro ──► Questionnaire ──► Récapitulatif + « réponse définitive » ──► Merci
               │ (A) WhatsApp inversé (principal)
               │ (B) Code par WhatsApp (secours)
               └ (C) Code par SMS (dernier recours)
```

1. **Accueil.** On y trouve la question clé, le compteur, et des liens vers la méthodologie et la politique de confidentialité. Une mention indique que la réponse est anonyme et définitive.
2. **Captcha ALTCHA**, une preuve de travail calculée dans le navigateur. Il est résolu avant la création d'un jeton ou l'envoi d'un code. Sans JavaScript, la personne voit une page d'explication et passe par le parcours C.
3. **Vérification** (§5). Si l'empreinte est déjà dans la table des participations ou dans le tampon, le message est : « Ce numéro a déjà participé. Merci ! ». Rien d'autre n'est révélé.
4. **Questionnaire.** Toutes les questions tiennent sur une seule page, sans aucune sauvegarde intermédiaire en base. Les réponses en cours ne vivent que dans le formulaire du navigateur.
5. **Récapitulatif.** On y voit toutes les réponses et un avertissement : « Votre réponse est définitive et ne pourra pas être modifiée, car nous ne pouvons pas la retrouver. » La personne confirme.
6. **Envoi.** La participation et la réponse sont placées dans le **tampon** (§6.2). La session est détruite.
7. **Merci.** Un lien de partage est proposé, sans aucun identifiant.

**Session** : un cookie aléatoire, `HttpOnly`, `Secure`, `SameSite=Strict`, qui renvoie à un état **gardé en mémoire uniquement**. Cet état contient l'avancement de la vérification, puis l'empreinte une fois le numéro vérifié. Il expire au bout de 30 minutes d'inactivité.

## 5. Vérification du numéro

Règle commune aux trois parcours : **dès que le numéro est connu et vérifié, on calcule son empreinte, puis on efface le numéro de la mémoire.** La session ne garde que l'empreinte.

### 5.1 (A) Vérification inversée par WhatsApp, le parcours principal (ADR 0009)

1. Le serveur crée un **jeton** court et lisible, par exemple `DIGUEE 4821-KX`. Il est aléatoire, valable 10 minutes, à usage unique et lié à la session.
2. La page affiche :
   - sur mobile, un bouton « Vérifier avec WhatsApp » qui ouvre `https://wa.me/<numéro Diguee>?text=<jeton>` ;
   - sur ordinateur, le même lien sous forme de QR code.
3. Le citoyen envoie le message. Meta l'adresse au **webhook** de Diguee.
4. Le webhook :
   - vérifie la signature `X-Hub-Signature-256` avec le secret de l'application Meta ;
   - extrait l'expéditeur et le texte ;
   - retrouve le jeton ;
   - vérifie que l'expéditeur est en +221 et que son préfixe figure dans la liste autorisée ;
   - fait calculer l'empreinte par le KMS ;
   - marque la session comme vérifiée.
   Le corps de la requête n'est jamais écrit dans un journal.
5. La page détecte la vérification de deux façons : par une interrogation légère toutes les 3 secondes, qui demande un peu de JavaScript, ou par un bouton « J'ai envoyé le message ». Diguee **ne répond pas** dans WhatsApp. C'est gratuit, et Meta ne voit rien de plus.

Si un même jeton arrive depuis deux expéditeurs différents, seul le premier compte, et l'incident est signalé dans les métriques.

### 5.2 (B) Code envoyé par WhatsApp, en secours

Le lien « Mon WhatsApp est sur un autre téléphone » mène à ce parcours :
1. Le citoyen saisit son numéro.
2. Le serveur contrôle le format et le préfixe.
3. Le serveur envoie un **Code** à 6 chiffres par le modèle de message d'authentification de Meta.
4. Le code est valable 10 minutes, avec 5 essais au plus, puis il est invalidé.

### 5.3 (C) Code par SMS, en dernier recours (ADR 0004)

Le lien « Je n'ai pas WhatsApp » mène à ce parcours. Il est identique au parcours B, mais passe par un fournisseur SMS qui a un accord direct avec les opérateurs sénégalais. Le fournisseur reste à choisir, derrière une interface `Envoyeur` interchangeable. Les limites sont plus strictes (§7), et le parcours se coupe automatiquement en cas d'emballement.

### 5.4 Calcul de l'empreinte (ADR 0002 et 0006)

```
numéro_normalisé = "+221" + 9 chiffres       (sans espaces, sans 00)
signature        = KMS_OVH.sign(clé_empreinte, alg = RS256, message = numéro_normalisé)
empreinte        = SHA-256(signature)          (32 octets)
```

- **RS256 (PKCS#1 v1.5) est obligatoire.** L'algorithme PS256 (PSS) est aléatoire et casserait la détection des doublons.
- **Test au démarrage** : le serveur signe deux fois une valeur de contrôle et refuse de démarrer si les deux résultats diffèrent.
- **Solution de repli** si le test échoue sur OVH : Google Cloud KMS, région Paris, avec un HMAC natif. Le calcul est isolé derrière une interface `Empreinteur`.
- La clé n'est utilisée que pour cet usage, avec une permission limitée à `kms:serviceKey/sign`.

## 6. Données

### 6.1 Schéma PostgreSQL de la consultation

```sql
-- Aucun horodatage, aucune séquence. Détruite à la Clôture.
CREATE TABLE participation (
  empreinte bytea PRIMARY KEY            -- 32 octets
);

-- Aucun identifiant de personne. Conservée après la Clôture.
CREATE TABLE reponse (
  id                  uuid PRIMARY KEY,  -- aléatoire (v4), jamais séquentiel
  jour                date NOT NULL,     -- jour d'écriture du lot, pas l'heure
  version_questionnaire smallint NOT NULL,
  q1 smallint, q2 smallint, q3 smallint NOT NULL, q4 smallint, q5 smallint, q6 smallint,
  q7 smallint[],                         -- choix multiples
  themes smallint[],                     -- 0 ou 3 éléments
  region smallint, tranche_age smallint, sexe smallint, inscrit smallint
);
```

Pour limiter ce qu'on peut déduire de l'ordre physique des lignes, les deux tables reçoivent leurs lignes par **lots mélangés** (§6.2). Un `CLUSTER` hebdomadaire sur `reponse(id)` détruit en plus l'ordre d'insertion.

### 6.2 Tampon mélangé (ADR 0003)

- Le tampon est en mémoire et protégé par un verrou. Il contient deux listes : les empreintes et les réponses.
- Il est vidé en **une seule transaction** quand il atteint **20 envois**, ou au plus tard **30 minutes** après le premier envoi. Les deux listes sont mélangées séparément avec un générateur aléatoire cryptographique. La colonne `jour` reçoit la date de l'écriture.
- À l'arrêt propre du service (SIGTERM), le tampon est écrit, même incomplet.
- En cas de plantage, le tampon est perdu, sans conséquence grave : la participation est perdue en même temps que la réponse, donc la personne peut répondre à nouveau.
- La détection des doublons consulte la base, puis le tampon.
- Deux envois avec la même empreinte dans le même tampon : le second est refusé.

### 6.3 Ce qui n'est jamais stocké

Les numéros, les adresses IP, l'agent utilisateur, la langue, l'heure d'une réponse, les jetons et les codes. Les jetons et les codes vivent en mémoire et expirent.

### 6.4 Schéma de l'observatoire (ADR 0008)

```sql
CREATE TABLE membre    (id, nom, role /* redacteur|relecteur|admin */, totp_secret_chiffre, webauthn jsonb, actif);
CREATE TABLE elu       (id, nom, fonction, mandat_debut, mandat_fin, contact_officiel);
CREATE TABLE engagement(id, elu_id, citation, contexte, date_declaration, echeance, cree_par, valide_par, publie_le);
CREATE TABLE source    (id, url, titre, date_source, archive_chemin, archive_sha256, ajoute_par, ajoute_le);
CREATE TABLE engagement_source(engagement_id, source_id);

-- Ajout seul : aucun UPDATE ni DELETE autorisé (rôle SQL + trigger).
CREATE TABLE statut (
  id, engagement_id, valeur /* tenu|partiel|non_tenu|en_cours */,
  explication, redige_par, redige_le, valide_par, valide_le,
  preavis_envoye_le, preavis_echeance, publie_le,
  CHECK (valide_par IS NULL OR valide_par <> redige_par)
);
CREATE TABLE statut_preuve(statut_id, source_id);
CREATE TABLE droit_reponse(id, engagement_id, statut_id, texte, canal, recu_le, verifie_par, publie_le);
```

- La contrainte « relecteur différent du rédacteur » est imposée **en base**, par une contrainte CHECK, et **dans l'API**.
- Les archives de sources (PDF et captures) sont stockées dans le stockage d'objets OVH, sous leur SHA-256.

## 7. Anti-fraude

Tous les seuils sont des paramètres de configuration (§13).

| Contrôle | Valeur par défaut |
|---|---|
| Liste des préfixes autorisés | 70, 75, 76, 77, 78. Le 72 et les autres sont à confirmer auprès de l'ARTP. |
| Captcha ALTCHA | Obligatoire avant chaque jeton ou code. Difficulté réglable. |
| Par empreinte | 3 vérifications ou codes par 24 h. Compteur en mémoire, indexé par l'empreinte et non par le numéro. |
| Par code | Valable 10 min, 5 essais au plus. |
| Par jeton | Valable 10 min, à usage unique. |
| Par IP | 30 demandes par heure. Au-delà, ralentissement progressif et alerte, sans blocage, à cause du CGNAT des opérateurs. L'IP est remplacée par un HMAC avec un sel qui change chaque jour, et le compteur reste en mémoire. |
| Coupe-circuit SMS | Si le volume horaire dépasse 3 fois la moyenne glissante sur 7 jours, le parcours C est suspendu automatiquement et le porteur est alerté. |
| Plafonds fournisseurs | Plafond journalier configuré chez Meta et chez le fournisseur SMS. |
| Surveillance | Métriques agrégées par préfixe et par région, avec alerte en cas de pic anormal. Aucune donnée individuelle. |

## 8. Publication des résultats

- **Avant 1 000 réponses**, seul le compteur global est affiché, mis à jour chaque jour.
- **Au-delà**, une tâche tourne chaque jour à **06:00 GMT**, l'heure de Dakar. Elle calcule les agrégats sur les réponses dont `jour < aujourd'hui`, applique les règles ci-dessous, produit un instantané figé (JSON et pages HTML), puis inscrit au journal ses totaux et son empreinte.

**Règles appliquées automatiquement** :
1. On croise au plus **deux** critères à la fois : une question × un critère de profil, ou deux critères de profil entre eux.
2. Une case qui compte moins de **30** réponses affiche « moins de 30 réponses ».
3. **Masquage complémentaire** : si une seule case d'une ligne est masquée, la plus petite des autres l'est aussi.
4. Les résultats sont publiés en **pourcentages arrondis à l'unité**. Seuls les effectifs par ligne ou au total sont donnés, **arrondis à la dizaine**.
5. Une région qui compte moins de 30 réponses au total est regroupée dans « Autres régions ».
6. **Pas de pondération.** Les chiffres sont présentés comme des « résultats non pondérés », à côté de la **composition de l'échantillon** comparée aux chiffres de l'ANSD.

Les réponses individuelles ne sont **jamais** exportées ni publiées (ADR 0010).

## 9. Observatoire

**Pages publiques** :
- la liste des engagements, filtrable par élu, mandat et statut ;
- la fiche de chaque engagement, avec :
  - la citation et ses sources (lien d'origine et copie archivée) ;
  - l'échéance ;
  - l'**historique complet des statuts** et les preuves de chacun ;
  - le droit de réponse, publié tel quel.

**Espace d'administration**, à l'adresse `consultation.diguee.sn/equipe`, sur une partie séparée du serveur :
- connexion par **TOTP** au minimum, ou par clé de sécurité WebAuthn ; jamais par SMS ;
- session de 8 h ;
- chaque action inscrite au journal.

**Circuit d'un statut** :

```
Brouillon (rédacteur) ──► Proposé ──► Validé (relecteur ≠ rédacteur)
   ├─ tenu / en cours ─────────────────────────────► Publié
   └─ non tenu / partiel ─► Préavis envoyé (J) ─► J+15 ─► Publié
                                  └─ droit de réponse reçu ─► vérifié ─► publié avec le statut
```

- Le préavis est préparé par la plateforme, qui fournit le dossier en PDF. Un membre l'envoie lui-même par le canal officiel de l'élu, puis enregistre la date d'envoi. Le statut ne peut être publié qu'à partir de J+15.
- Sans réponse, la fiche affiche : « Sollicité le JJ/MM/AAAA, pas de réponse ».
- Un droit de réponse reçu après la publication est publié dès qu'il a été vérifié.
- Une erreur se corrige en ajoutant un nouveau statut qui l'explique, jamais en effaçant.

## 10. Journal public (ADR 0010)

```
entrée_n = { n, horodatage (minute), type, contenu, empreinte_précédente = H(entrée_{n-1}) }
H(entrée_n) = SHA-256(JSON canonique de entrée_n)
```

- **Types d'entrées** :
  - publication quotidienne ;
  - statut publié ;
  - droit de réponse publié ;
  - mise en production (empreinte de version du code et SHA-256 de l'exécutable) ;
  - modification de la méthodologie ou de ses paramètres ;
  - ouverture, clôture, procès-verbal de destruction.
- Les entrées de la consultation ne contiennent **que des agrégats**.
- **Chaque jour**, l'empreinte de tête est soumise aux calendriers OpenTimestamps (preuve `.ots` conservée et publiée), et la page du journal est envoyée à archive.org.
- **Chaque semaine**, la plateforme prépare un texte prêt à publier sur X (empreinte et lien). **Le porteur le publie lui-même.**
- La page `/journal` est publique et lisible, avec un export JSON. Un script de vérification de la chaîne est publié dans le dépôt.

## 11. Architecture et déploiement (ADR 0006 et 0007)

```
Citoyen ──HTTPS──► VPS OVHcloud France (anti-DDoS OVH inclus)
                    ├─ diguee (exécutable Go unique) : HTTP/TLS, pages html/template, tampon, tâches planifiées
                    ├─ PostgreSQL 16 (local, port fermé à l'extérieur, données sur volume chiffré)
                    └─► KMS OVHcloud (empreinte)      └─► Object Storage OVHcloud (sauvegardes, archives de sources)
Meta (webhook WhatsApp) ──HTTPS──► /webhook/whatsapp (signature vérifiée)
```

- **Un seul exécutable Go**, lancé par systemd, qui sert aussi le TLS (Let's Encrypt via ACME). Pas de Cloudflare.
- **Dépendances autorisées**, à limiter et à justifier une par une :
  - `pgx` (pilote PostgreSQL) ;
  - `ovh/okms-sdk-go` ;
  - `golang.org/x/crypto/acme/autocert` ;
  - éventuellement une bibliothèque WebAuthn.
  ALTCHA et TOTP sont implémentés dans le dépôt, car leur code est court et facile à auditer.
- **JavaScript** : uniquement le solveur ALTCHA et l'interrogation de la page de vérification. Aucun script tiers, aucune mesure d'audience.
- **Construction reproductible** : `go build -trimpath` avec des versions épinglées. Le pied de page affiche la version et le SHA-256 de l'exécutable. Le dépôt est **public avant le lancement**.
- **Système** :
  - mises à jour de sécurité automatiques ;
  - SSH par clé uniquement ;
  - pare-feu réseau OVH qui n'ouvre que les ports 443 et 80 (pour la redirection et ACME), plus SSH depuis une liste d'adresses autorisées ;
  - compte OVH réservé à Diguee, protégé par une double authentification.
- **Volume chiffré** : les données PostgreSQL sont sur un volume LUKS. Sa clé est elle-même chiffrée par le KMS et déchiffrée au démarrage (chiffrement d'enveloppe). La procédure exacte sera détaillée dans le ticket d'infrastructure.
- **Sauvegardes** :
  - un `pg_dump` chaque nuit, chiffré avec `age` : la clé publique est sur le serveur, la clé privée reste hors ligne chez le porteur ;
  - les sauvegardes sont envoyées sur Object Storage et conservées **30 jours** ;
  - la restauration est testée chaque mois.
- **Journaux techniques** : sans IP, sans numéro, sans corps de requête. Conservation de 14 jours.

## 12. Clôture (ADR 0002)

À la date `CLOTURE` (ouverture + 6 mois), une procédure en plusieurs étapes, chacune inscrite au journal :

1. Les formulaires sont fermés. Les pages de vérification affichent « Consultation close ».
2. Le dernier tampon est écrit, puis la dernière publication quotidienne est produite.
3. `DROP TABLE participation`, puis `VACUUM FULL` et un nouveau `CHECKPOINT`.
4. **Toutes les sauvegardes antérieures**, qui contiennent les empreintes, sont supprimées de l'Object Storage. On fait ensuite une nouvelle sauvegarde sans la table.
5. La clé d'empreinte est détruite dans le KMS, avec une demande de suppression définitive. Les clés du volume chiffré sont tournées.
6. Un procès-verbal est publié : date, empreintes des opérations, état final des tables.

Les **Réponses** et les résultats sont conservés.

## 13. Paramètres de configuration

`OUVERTURE`, `CLOTURE`, `PREFIXES_AUTORISES`, `SEUIL_PUBLICATION=1000`, `SEUIL_CASE=30`, `TAMPON_TAILLE=20`, `TAMPON_DELAI=30m`, `HEURE_PUBLICATION=06:00Z`, `JETON_DUREE=10m`, `CODE_DUREE=10m`, `CODE_ESSAIS=5`, `VERIF_PAR_EMPREINTE_24H=3`, `IP_PAR_HEURE=30`, `SMS_COUPE_CIRCUIT=3x`, `PREAVIS_JOURS=15`, `ALTCHA_DIFFICULTE`, `NUMERO_WHATSAPP_DIGUEE`, `NUMERO_CDP`. Les secrets (Meta, SMS, KMS, ACME) sont gérés hors du dépôt.

## 14. Critères d'acceptation

| # | Test | Exigence |
|---|---|---|
| T1 | Après 200 envois de test, un `pg_dump` complet ne contient aucun numéro, aucune IP et aucun horodatage plus fin que le jour. | E1, E2, E6 |
| T2 | Sur ce même dump, un attaquant qui connaît l'ordre d'arrivée des 200 envois ne fait pas mieux que le hasard pour apparier participations et réponses. | E1 |
| T3 | Les journaux applicatifs et système, après ces 200 envois, ne contiennent aucun numéro ni aucune IP (recherche par expression régulière). | E2, E6 |
| T4 | Un même numéro est refusé la deuxième fois, que la première réponse soit en base ou encore dans le tampon. | ADR 0003 |
| T5 | Au démarrage, le serveur refuse de tourner si l'algorithme de signature n'est pas déterministe. | E3 |
| T6 | Un webhook à la signature invalide est rejeté. | §5.1 |
| T7 | Une tentative de valider sa propre saisie échoue, dans l'API comme en SQL direct. | E7 |
| T8 | Un `UPDATE` ou un `DELETE` sur `statut` échoue. | ADR 0008 |
| T9 | Un statut « non tenu » ne peut pas être publié avant J+15. | §9 |
| T10 | Avec un jeu de données où une case compte 29 réponses, cette case et la case complémentaire sont masquées. Les pourcentages et les effectifs sont arrondis. | §8 |
| T11 | Toute modification d'une entrée ancienne du journal est détectée par le script de vérification. | E8 |
| T12 | En simulation, la procédure de clôture laisse zéro empreinte, en base comme dans les sauvegardes. | E4 |
| T13 | Les pages d'accueil, de questionnaire et de résultats pèsent moins de 100 Ko et fonctionnent sans JavaScript, avec le parcours C pour la vérification. | E9 |
| T14 | Le coupe-circuit SMS se déclenche sur un pic simulé. | §7 |
| T15 | Un test d'intrusion externe est réalisé avant le lancement, et ses points critiques sont corrigés. | §8 |

## 15. Questions ouvertes

1. Les échelles de réponse de Q1 et Q2, et les choix de Q5 (« qui devrait évaluer »), à fixer avec le porteur ou l'institut de sondage.
2. La liste définitive des préfixes, avec l'ARTP.
3. Le test réel du KMS OVH (RS256 déterministe) et son tarif.
4. Le choix du fournisseur SMS local (parcours C).
5. La traduction en wolof et en pulaar, à l'écrit et en audio, et la date à laquelle ces langues entrent dans le périmètre.
6. La date d'ouverture, le numéro WhatsApp de Diguee et le numéro CDP.
7. La première liste d'élus et d'engagements de l'observatoire, à préparer par l'équipe éditoriale.
