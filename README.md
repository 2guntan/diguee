# Diguee

**Diguee boor la : une promesse est une dette.**

Observatoire citoyen et indépendant des engagements publics au Sénégal, avec une consultation pour mesurer le soutien à une loi qui rendrait les élus redevables de leurs promesses, pour les mandats futurs.

Site : https://diguee.sn · Contact : @digueeboorla

## Contenu du dépôt

| Chemin | Rôle |
|---|---|
| `site/index.html` | Site vitrine pré-lancement : HTML/CSS statique, sans JavaScript, déployé tel quel dans `public_html/` sur l'hébergement PlanetHoster (N0C) |
| `cmd/diguee` | L'exécutable unique de la plateforme (`consultation.diguee.sn`) |
| `internal/config` | Lecture des variables d'environnement `DIGUEE_*`, valeurs « À COMPLÉTER » |
| `internal/integration` | Registre où chaque service externe déclare son mode (`fake` ou réel) |
| `internal/garde` | Refus de démarrer en production s'il reste une valeur provisoire ou une fausse implémentation |
| `internal/empreinte` | Normalisation des numéros +221 et interface `Empreinteur` : `ovh` (KMS d'OVHcloud, signature RS256 puis SHA-256, mTLS), `gcp` (repli Google Cloud KMS, HMAC natif) et `fake` (HMAC à clé locale, développement) ; `fauxkms` : faux serveurs KMS pour les tests |
| `internal/questionnaire` | Questionnaire versionné (Q1 à Q8, profil, « 18 ans ou plus ») : codes stockés et clés du catalogue |
| `internal/tampon` | Tampon mélangé (ADR 0003) : écriture groupée à 20 envois, à 30 minutes et à l'arrêt, doublons refusés |
| `internal/session` | Sessions en mémoire seulement, cookie `HttpOnly`, `Secure`, `SameSite=Strict` |
| `internal/totp` | TOTP (RFC 6238, au-dessus de HOTP, RFC 4226) : SHA-1, 6 chiffres, 30 secondes, tolérance d'un pas, anti-rejeu ; bibliothèque standard seulement |
| `internal/equipe` | Espace équipe `/equipe` (spec §9, ADR 0008) : comptes nominatifs, rôles cumulables, connexion mot de passe + TOTP, sessions de 8 h, CSRF, limitation des échecs, chiffrement des secrets TOTP (AES-256-GCM), audit interne ; `Espace.Gerer` et `MembreDe` pour les pages suivantes (ticket #13) |
| `internal/base` | PostgreSQL : connexion, migrations (`migrations/NNNN_nom.sql`), requêtes (dont les comptes de l'équipe et leur audit) ; `basetest` pour les tests |
| `internal/journal` | Journal public (ADR 0010) : entrées chaînées par SHA-256 sur le JSON canonique (RFC 8785, entiers seulement), API d'écriture `Inscrire` / `InscrireMiseEnProduction`, export JSON et vérification de la chaîne ; `journaltest` : magasin en mémoire pour les tests |
| `internal/i18n` | Catalogues de textes : `fr` livré, `wo` et `ff` prévus |
| `internal/web` | Pages `html/template` sans JavaScript (accueil, parcours de réponse, méthodologie, confidentialité, journal public `/journal` et son export `/journal.json`), feuille de style et polices auto-hébergées ; monte l'espace équipe sous `/equipe` |
| `internal/version` | Version et SHA-256 de l'exécutable, affichés au pied de page et inscrits au journal public à chaque mise en production |
| `docs/` | Spécification, ADR, et [ce que le porteur doit fournir](docs/À%20faire%20par%20le%20porteur.md) |

## Développement

Prérequis : Go 1.24.7 (épinglé dans `go.mod`) et PostgreSQL 16.

```sh
# Une base locale
createdb diguee

# Lancer le serveur (http://localhost:8080) ; les migrations s'appliquent au démarrage
export DIGUEE_POSTGRES_URL='postgres://localhost/diguee?sslmode=disable'
make lancer

# Pour essayer le parcours de réponse en local, la consultation doit être ouverte :
# DIGUEE_OUVERTURE=2026-11-01 make lancer. La vérification du numéro est alors
# factice (aucun message envoyé) ; elle n'existe pas en production.

# Tests (ceux qui demandent PostgreSQL sont sautés sans DIGUEE_TEST_POSTGRES_URL ;
# le test T1/T2 lance pg_dump, version 16, cherché dans le PATH ou dans DIGUEE_TEST_PG_DUMP)
export DIGUEE_TEST_POSTGRES_URL='postgres://localhost/diguee_test?sslmode=disable'
go vet ./... && go test ./...

# Build reproductible : deux builds, même SHA-256
make verifier-build
```

Chaque test de base travaille dans un schéma temporaire, supprimé à la fin. Les tests du parcours (`internal/web/parcours_test.go`) font 200 envois complets sans JavaScript, puis vérifient sur un `pg_dump` qu'il n'y a ni numéro, ni IP, ni heure (T1), et que l'ordre du dump ne permet pas d'apparier participations et réponses mieux que le hasard (T2). L'écriture du tampon à l'arrêt est testée par un vrai SIGTERM (`cmd/diguee/main_test.go`), comme l'unique entrée « mise en production » sur plusieurs redémarrages. T11 (modification d'une entrée ancienne détectée) est testé dans `internal/journal` et sur la commande `verifier-journal` ; le refus d'`UPDATE`/`DELETE` en SQL dans `internal/base/journal_test.go`. Les tests de l'espace équipe (`internal/equipe/espace_test.go`) passent par le serveur web complet : connexion refusée sans TOTP valide ou avec un code déjà utilisé, compte désactivé et sessions fermées, pages refusées sans session ou sans le bon rôle, CSRF, échecs limités, ni IP ni secret dans les journaux et la base ; les vecteurs des RFC 4226 et 6238 sont dans `internal/totp`. En intégration continue (`CI` non vide), l'absence de base fait échouer les tests au lieu de les sauter. La CI (`.github/workflows/ci.yml`) lance `gofmt`, `go vet`, `go test ./...` avec un service `postgres:16`, puis vérifie le build reproductible.

Autres commandes : `diguee migrer` (migrations seules), `diguee verifier-config` (liste ce qui manque pour la production), `diguee version`, `diguee creer-membre` (espace équipe, ci-dessous) et `diguee verifier-journal` (ci-dessous).

### Espace équipe

L'espace `/equipe` (spec §9, ADR 0008) a ses propres routes, gabarits (`internal/equipe/gabarits`, textes dans le catalogue `fr` sous `equipe.*`) et cookies, limités à `/equipe`. Chaque membre a un compte nominatif et un ou plusieurs rôles : **rédacteur**, **relecteur**, **administrateur**. La connexion demande l'identifiant, un mot de passe tiré par la plateforme (20 signes, 100 bits, empreinte PBKDF2-SHA-256) et le code TOTP de l'application d'authentification ; jamais de SMS. Le secret TOTP est chiffré en AES-256-GCM avec `DIGUEE_CLE_TOTP`, lié à l'ID du membre. Un code déjà accepté, ou plus ancien, est refusé ; après un redémarrage, les codes du pas de démarrage aussi.

- Session de 8 heures fixes depuis la connexion, en mémoire seulement (un redémarrage ferme les sessions). Le compte et ses rôles sont relus en base à chaque requête : un changement de rôle vaut tout de suite, et un compte désactivé perd aussitôt ses sessions.
- Seul un administrateur crée un compte, change ses rôles, réinitialise son accès (nouveau mot de passe et nouveau secret, sessions fermées) ou le désactive (définitif, la base refuse la réactivation et la suppression). Il reste toujours au moins un administrateur actif, et personne ne désactive son propre compte.
- Formulaires protégés contre le CSRF : jeton de session dans un champ caché, jeton lié à un cookie pour la connexion, et refus des requêtes venues d'un autre site (`Origin`, `Sec-Fetch-Site`).
- Échecs de connexion limités en mémoire : 5 par identifiant en 15 minutes, 20 par adresse en une heure (l'adresse est remplacée par un HMAC à sel quotidien et n'est jamais conservée ni journalisée).
- Audit interne : la table `audit_equipe`, en ajout seul, garde au jour près chaque création, changement de rôles, réinitialisation, désactivation et connexion, avec son auteur. Ces actions ne sont pas publiques, donc pas au journal public.

Pages suivantes (ticket #13) : `Espace.Gerer("GET /equipe/…", gestionnaire, equipe.Redacteur)` vérifie la session, le compte actif, le rôle (un des rôles donnés) et, hors GET, le jeton CSRF (champ `csrf`, valeur `equipe.JetonCSRFDe(ctx)`) ; `equipe.MembreDe(ctx)` donne le membre connecté (`ID`, `Identifiant`, `Nom`, `Roles`, `A(role)`).

Créer un compte depuis le serveur (le premier administrateur, par exemple) ; le mot de passe, le secret TOTP et l'URI `otpauth://` ne s'affichent qu'une fois :

```sh
diguee creer-membre -identifiant awa.diop -nom "Awa Diop" -roles administrateur,redacteur,relecteur
```

### Journal public

Chaque entrée de `journal_public` contient un numéro, un horodatage à la minute (UTC), un type, un contenu (objet JSON d'agrégats seulement) et l'empreinte de l'entrée précédente ; son empreinte est le SHA-256 du JSON canonique (RFC 8785, entiers seulement) de ces cinq champs, la première ayant une empreinte précédente nulle. La base refuse `UPDATE`, `DELETE` et `TRUNCATE` sur la table, pour tous les rôles (déclencheurs de la migration `0002`), et refuse une insertion qui ne suit pas la dernière entrée. Les écritures sont sérialisées par un verrou consultatif. Au démarrage du serveur, une entrée « mise en production » (version et SHA-256 de l'exécutable) est inscrite si la dernière entrée de ce type ne porte pas déjà ces valeurs. Les autres types (publication quotidienne, statut publié, droit de réponse, méthodologie, ouverture, clôture, procès-verbal) sont écrits par les tickets suivants avec `journal.Journal.Inscrire`. Aucune entrée n'est écrite à cause d'un envoi : l'heure d'une entrée ne doit rien dire de celle d'une réponse.

Vérifier un export, sans configuration ni base :

```sh
go run github.com/2guntan/diguee/cmd/diguee@latest verifier-journal journal.json   # ou https://…/journal.json
# Détecter aussi une réécriture complète de la chaîne : une empreinte publiée
# hors de nos serveurs, ou un export téléchargé plus tôt
diguee verifier-journal -attendu <empreinte> -precedent ancien.json journal.json
```

**Build reproductible** : `make build` compile avec `CGO_ENABLED=0 -trimpath -buildid=`, et fixe la version avec `git describe`. Le même commit, compilé avec la même version de Go pour le même système (`GOOS`/`GOARCH`), donne le même SHA-256, qui est affiché au pied de chaque page.

### Variables d'environnement

Les secrets (URL de la base avec son mot de passe, puis les accès Meta, SMS, KMS…) ne sont jamais dans le dépôt : ils sont passés par l'environnement (fichier `EnvironmentFile` de systemd en production).

| Variable | Défaut | Spec §13 |
|---|---|---|
| `DIGUEE_ENV` | `development` (ou `production`) | |
| `DIGUEE_ADRESSE` | `:8080` | |
| `DIGUEE_POSTGRES_URL` | obligatoire, secret | |
| `DIGUEE_EMPREINTEUR` | `fake` (HMAC de développement, interdit en production) ; `ovh` (KMS d'OVHcloud, RS256 puis SHA-256) ; `gcp` (repli Google Cloud KMS, HMAC natif) | |
| `DIGUEE_OVH_KMS_POINT_ACCES` | obligatoire avec `ovh` : point d'accès régional `https://…okms.ovh.net` | |
| `DIGUEE_OVH_KMS_ID`, `DIGUEE_OVH_KMS_CLE_ID` | obligatoires avec `ovh` : UUID du KMS et de la clé RSA de signature | |
| `DIGUEE_OVH_KMS_CERTIFICAT`, `DIGUEE_OVH_KMS_CLE_CLIENT` | obligatoires avec `ovh` : chemins du certificat client mTLS et de sa clé privée (PEM, secret) | |
| `DIGUEE_OVH_KMS_AC` | facultatif : autorités de certification (PEM) ; défaut : celles du système | |
| `DIGUEE_GCP_KMS_CLE`, `DIGUEE_GCP_KMS_JETON_FICHIER` | obligatoires avec `gcp` : version de clé HMAC et fichier du jeton d'accès (secret) | |
| `DIGUEE_GCP_KMS_POINT_ACCES`, `DIGUEE_GCP_KMS_AC` | `https://cloudkms.googleapis.com` ; facultatif | |
| `DIGUEE_KMS_DELAI` | `5s` (délai d'attente d'un appel au KMS) | |
| `DIGUEE_CLE_TOTP` | absente : clé de développement (intégration `chiffrement_totp` en mode `fake`, interdite en production) ; secret, 64 chiffres hexadécimaux | |
| `DIGUEE_STRUCTURE_NOM`, `DIGUEE_STRUCTURE_ADRESSE` | À COMPLÉTER | (ADR 0005) |
| `DIGUEE_CONTACT` | À COMPLÉTER | |
| `DIGUEE_NUMERO_CDP` | À COMPLÉTER | `NUMERO_CDP` |
| `DIGUEE_NUMERO_WHATSAPP` | À COMPLÉTER | `NUMERO_WHATSAPP_DIGUEE` |
| `DIGUEE_OUVERTURE` | À COMPLÉTER (`AAAA-MM-JJ`) | `OUVERTURE` |
| `DIGUEE_CLOTURE` | ouverture + 6 mois | `CLOTURE` |
| `DIGUEE_PREFIXES_AUTORISES` | `70,75,76,77,78` | `PREFIXES_AUTORISES` |
| `DIGUEE_SEUIL_PUBLICATION` | `1000` | `SEUIL_PUBLICATION` |
| `DIGUEE_SEUIL_CASE` | `30` | `SEUIL_CASE` |
| `DIGUEE_TAMPON_TAILLE` | `20` | `TAMPON_TAILLE` |
| `DIGUEE_TAMPON_DELAI` | `30m` | `TAMPON_DELAI` |
| `DIGUEE_HEURE_PUBLICATION` | `06:00Z` | `HEURE_PUBLICATION` |
| `DIGUEE_JETON_DUREE` | `10m` | `JETON_DUREE` |
| `DIGUEE_CODE_DUREE` | `10m` | `CODE_DUREE` |
| `DIGUEE_CODE_ESSAIS` | `5` | `CODE_ESSAIS` |
| `DIGUEE_VERIF_PAR_EMPREINTE_24H` | `3` | `VERIF_PAR_EMPREINTE_24H` |
| `DIGUEE_IP_PAR_HEURE` | `30` | `IP_PAR_HEURE` |
| `DIGUEE_SMS_COUPE_CIRCUIT` | `3x` | `SMS_COUPE_CIRCUIT` |
| `DIGUEE_ALTCHA_DIFFICULTE` | `100000` (provisoire, réglé au ticket #8) | `ALTCHA_DIFFICULTE` |
| `DIGUEE_PREAVIS_JOURS` | `15` | `PREAVIS_JOURS` |
| `DIGUEE_TEST_POSTGRES_URL` | tests seulement | |
| `DIGUEE_TEST_KMS_REEL` | tests seulement : `ovh` ou `gcp` active le test contre le vrai KMS (`go test ./internal/empreinte -run TestKMSReel -v`) | |

À chaque démarrage, quel que soit `DIGUEE_EMPREINTEUR`, le serveur calcule deux fois l'empreinte d'une valeur de contrôle et refuse de démarrer si les résultats diffèrent (spec T5), si deux valeurs différentes donnent la même empreinte ou si le KMS ne répond pas.

Avec `DIGUEE_ENV=production`, le serveur refuse de démarrer si une valeur vaut « À COMPLÉTER », si une intégration déclarée dans `internal/integration` est en mode `fake`, ou si le questionnaire contient encore des choix provisoires (`Provisoire` dans `internal/questionnaire/v1.go`), et affiche la liste de ce qui manque.

### Règles

Voir `CLAUDE.md` : textes visibles uniquement dans `internal/i18n/catalogues/fr.json` (un test l'impose), aucun numéro, IP ni corps de requête dans les journaux, pages de moins de 100 Ko sans script (`/journal` est paginé par 50 entrées), aucune entrée du journal public déclenchée par un envoi.
