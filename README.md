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
| `internal/empreinte` | Normalisation des numéros +221 et interface `Empreinteur` (implémentation de développement `fake` : HMAC à clé locale) |
| `internal/questionnaire` | Questionnaire versionné (Q1 à Q8, profil, « 18 ans ou plus ») : codes stockés et clés du catalogue |
| `internal/tampon` | Tampon mélangé (ADR 0003) : écriture groupée à 20 envois, à 30 minutes et à l'arrêt, doublons refusés |
| `internal/session` | Sessions en mémoire seulement, cookie `HttpOnly`, `Secure`, `SameSite=Strict` |
| `internal/base` | PostgreSQL : connexion, migrations (`migrations/NNNN_nom.sql`), requêtes ; `basetest` pour les tests |
| `internal/i18n` | Catalogues de textes : `fr` livré, `wo` et `ff` prévus |
| `internal/web` | Pages `html/template` sans JavaScript (accueil, parcours de réponse, méthodologie, confidentialité), feuille de style et polices auto-hébergées |
| `internal/version` | Version et SHA-256 de l'exécutable, affichés au pied de page |
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

Chaque test de base travaille dans un schéma temporaire, supprimé à la fin. Les tests du parcours (`internal/web/parcours_test.go`) font 200 envois complets sans JavaScript, puis vérifient sur un `pg_dump` qu'il n'y a ni numéro, ni IP, ni heure (T1), et que l'ordre du dump ne permet pas d'apparier participations et réponses mieux que le hasard (T2). L'écriture du tampon à l'arrêt est testée par un vrai SIGTERM (`cmd/diguee/main_test.go`). En intégration continue (`CI` non vide), l'absence de base fait échouer les tests au lieu de les sauter. La CI (`.github/workflows/ci.yml`) lance `gofmt`, `go vet`, `go test ./...` avec un service `postgres:16`, puis vérifie le build reproductible.

Autres commandes : `diguee migrer` (migrations seules), `diguee verifier-config` (liste ce qui manque pour la production), `diguee version`.

**Build reproductible** : `make build` compile avec `CGO_ENABLED=0 -trimpath -buildid=`, et fixe la version avec `git describe`. Le même commit, compilé avec la même version de Go pour le même système (`GOOS`/`GOARCH`), donne le même SHA-256, qui est affiché au pied de chaque page.

### Variables d'environnement

Les secrets (URL de la base avec son mot de passe, puis les accès Meta, SMS, KMS…) ne sont jamais dans le dépôt : ils sont passés par l'environnement (fichier `EnvironmentFile` de systemd en production).

| Variable | Défaut | Spec §13 |
|---|---|---|
| `DIGUEE_ENV` | `development` (ou `production`) | |
| `DIGUEE_ADRESSE` | `:8080` | |
| `DIGUEE_POSTGRES_URL` | obligatoire, secret | |
| `DIGUEE_EMPREINTEUR` | `fake` (HMAC de développement, interdit en production) ; `ovh` au ticket #5 | |
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

Avec `DIGUEE_ENV=production`, le serveur refuse de démarrer si une valeur vaut « À COMPLÉTER », si une intégration déclarée dans `internal/integration` est en mode `fake`, ou si le questionnaire contient encore des choix provisoires (`Provisoire` dans `internal/questionnaire/v1.go`), et affiche la liste de ce qui manque.

### Règles

Voir `CLAUDE.md` : textes visibles uniquement dans `internal/i18n/catalogues/fr.json` (un test l'impose), aucun numéro, IP ni corps de requête dans les journaux, pages de moins de 100 Ko sans script.
