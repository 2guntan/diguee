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
| `internal/session` | Sessions en mémoire seulement, cookie `HttpOnly`, `Secure`, `SameSite=Strict` ; ouvertes avant la vérification, puis marquées vérifiées avec l'empreinte, identifiant renouvelé au passage au questionnaire |
| `internal/whatsapp` | Vérification inversée (spec §5.1, ADR 0009) : jetons « DIGUEE 4821-KX » en mémoire (aléatoires, 10 minutes, usage unique, liés à la session), webhook de l'API Cloud de Meta (`X-Hub-Signature-256`, poignée de main `hub.*`), métriques agrégées ; intégration `DIGUEE_WHATSAPP` : `fake` (secret de développement, simulation) ou `meta` |
| `internal/qr` | Encodeur de QR code écrit dans le dépôt (ISO/IEC 18004, mode octet, correction M, versions 1 à 10), rendu SVG dans la page : le lien `wa.me` sur ordinateur, sans JavaScript ni service tiers |
| `internal/altcha` | Captcha ALTCHA écrit dans le dépôt (spec §4 étape 2, §7, ADR 0007) : défis SHA-256 signés par HMAC (`DIGUEE_CLE_ALTCHA`), sel avec expiration, vérification de la signature, de l'expiration, de la solution et de l'usage unique (mémoire bornée) ; `Resoudre` fait le calcul du navigateur pour les tests |
| `internal/antifraude` | Limites et surveillance avant la vérification (spec §7), en mémoire seulement : `Fenetre` (fenêtre glissante bornée), `Anonymiseur` (HMAC d'adresse à sel quotidien, IPv6 par /64), `LimiteEmpreinte` (3 vérifications ou codes par empreinte en 24 h), `Ralentisseur` (30 demandes par heure et par adresse, puis ralentissement borné et alerte, sans blocage), `Surveillance` (métriques par préfixe et par région, alerte de pic) |
| `internal/totp` | TOTP (RFC 6238, au-dessus de HOTP, RFC 4226) : SHA-1, 6 chiffres, 30 secondes, tolérance d'un pas, anti-rejeu ; bibliothèque standard seulement |
| `internal/equipe` | Espace équipe `/equipe` (spec §9, ADR 0008) : comptes nominatifs, rôles cumulables, connexion mot de passe + TOTP, sessions de 8 h, CSRF, limitation des échecs, chiffrement des secrets TOTP (AES-256-GCM), audit interne ; `Espace.Gerer` et `MembreDe` pour les pages suivantes (ticket #13) |
| `internal/base` | PostgreSQL : connexion, migrations (`migrations/NNNN_nom.sql`), requêtes (dont les comptes de l'équipe et leur audit) ; `basetest` pour les tests |
| `internal/journal` | Journal public (ADR 0010) : entrées chaînées par SHA-256 sur le JSON canonique (RFC 8785, entiers seulement), API d'écriture `Inscrire` / `InscrireMiseEnProduction`, export JSON et vérification de la chaîne ; `journaltest` : magasin en mémoire pour les tests |
| `internal/i18n` | Catalogues de textes : `fr` livré, `wo` et `ff` prévus |
| `internal/web` | Pages `html/template` sans JavaScript, sauf un petit script d'interrogation sur la page du jeton (accueil, parcours de réponse avec la vérification inversée et le webhook `/webhook/whatsapp`, méthodologie, confidentialité, journal public `/journal` et son export `/journal.json`), feuille de style et polices auto-hébergées ; monte l'espace équipe sous `/equipe` |
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
# DIGUEE_OUVERTURE=2026-11-01 make lancer. Avec DIGUEE_WHATSAPP=fake (défaut), la
# page du jeton propose un lien vers /dev/whatsapp, qui simule le message
# WhatsApp du citoyen : la notification est signée avec le secret de
# développement et remise au vrai webhook. Ce mode est interdit en production.

# Tests (ceux qui demandent PostgreSQL sont sautés sans DIGUEE_TEST_POSTGRES_URL ;
# le test T1/T2 lance pg_dump, version 16, cherché dans le PATH ou dans DIGUEE_TEST_PG_DUMP)
export DIGUEE_TEST_POSTGRES_URL='postgres://localhost/diguee_test?sslmode=disable'
go vet ./... && go test ./...

# Build reproductible : deux builds, même SHA-256
make verifier-build
```

Chaque test de base travaille dans un schéma temporaire, supprimé à la fin. Les tests du parcours (`internal/web/parcours_test.go`) font 200 envois complets (le test résout lui-même le captcha ALTCHA, puis se passe de JavaScript), chacun vérifié par un faux webhook signé, puis vérifient sur un `pg_dump` qu'il n'y a ni numéro, ni IP, ni heure (T1), et que l'ordre du dump ne permet pas d'apparier participations et réponses mieux que le hasard (T2). L'écriture du tampon à l'arrêt est testée par un vrai SIGTERM (`cmd/diguee/main_test.go`), comme l'unique entrée « mise en production » sur plusieurs redémarrages. T11 (modification d'une entrée ancienne détectée) est testé dans `internal/journal` et sur la commande `verifier-journal` ; le refus d'`UPDATE`/`DELETE` en SQL dans `internal/base/journal_test.go`. Les tests de l'espace équipe (`internal/equipe/espace_test.go`) passent par le serveur web complet : connexion refusée sans TOTP valide ou avec un code déjà utilisé, compte désactivé et sessions fermées, pages refusées sans session ou sans le bon rôle, CSRF, échecs limités, ni IP ni secret dans les journaux et la base ; les vecteurs des RFC 4226 et 6238 sont dans `internal/totp`. La vérification inversée (`internal/web/whatsapp_test.go`, `internal/whatsapp`) : signature invalide rejetée (T6), poignée de main, jeton expiré, révoqué ou déjà utilisé sans effet, second expéditeur ignoré et compté, expéditeur hors +221 ou préfixe non autorisé refusé, détection par interrogation et par le bouton, notifications sans rapport acceptées (200), page de simulation absente en mode `meta`, ni numéro ni jeton ni corps dans les journaux. Le QR code (`internal/qr`) est testé sur les tables de la norme (format, version), un vecteur Reed-Solomon connu, deux matrices de référence et une relecture complète (motifs fixes, format, correction) ; les matrices ont été lues par un décodeur indépendant (zxing-cpp) hors du dépôt. En intégration continue (`CI` non vide), l'absence de base fait échouer les tests au lieu de les sauter. La CI (`.github/workflows/ci.yml`) lance `gofmt`, `go vet`, `go test ./...` avec un service `postgres:16`, puis vérifie le build reproductible.

Autres commandes : `diguee migrer` (migrations seules), `diguee verifier-config` (liste ce qui manque pour la production), `diguee version`, `diguee creer-membre` (espace équipe, ci-dessous) et `diguee verifier-journal` (ci-dessous).

### Vérification inversée par WhatsApp

Parcours principal de la vérification (spec §5.1, ADR 0009) ; la vérification factice du ticket #2 est retirée.

1. `GET /verifier` explique le principe et contient un défi ALTCHA, que `/statique/altcha.js` (script maison, autorisé par la CSP de cette seule page, sans aucune connexion) résout dans le navigateur avant d'activer le bouton. `POST /verifier` compte la demande par adresse (ralentissement au-delà de `DIGUEE_IP_PAR_HEURE`, jamais de blocage), puis vérifie la solution (champ `altcha` : signature HMAC, expiration de 20 minutes, solution, usage unique) **avant toute création de jeton** ; sans solution (pas de JavaScript), il renvoie vers `/verifier/sans-javascript`, qui mène au parcours SMS (`/verifier/sms`, page provisoire jusqu'au ticket #10). Ensuite, il ouvre une session non vérifiée et tire un jeton « DIGUEE 4821-KX » (4 chiffres de 2 à 9, 2 lettres sans B, G, I, L, O, Q, S, Z ; `crypto/rand`), valable `DIGUEE_JETON_DUREE`, à usage unique, gardé en mémoire avec l'identifiant de la session. Un nouveau jeton révoque le précédent.
2. `GET /verifier/whatsapp` affiche le jeton, le numéro de Diguee, le bouton `https://wa.me/<numéro>?text=<jeton>` et, sur grand écran avec souris (CSS seulement), le même lien en QR code SVG calculé par le serveur.
3. `POST /webhook/whatsapp` : signature `X-Hub-Signature-256` (HMAC-SHA-256 du corps brut, comparaison à temps constant) vérifiée avant toute lecture, sinon 401 ; seules les notifications `messages` du numéro `DIGUEE_META_ID_NUMERO` et les messages texte sont lus ; jeton retrouvé dans le texte (casse, espaces et tiret tolérés) ; expéditeur (`from`) contrôlé par `empreinte.Normaliser` (+221, préfixes autorisés) ; empreinte calculée, puis comparée à la base et au tampon ; session marquée vérifiée. Le reste (statuts, autres types, jetons inconnus ou expirés) reçoit 200. Si le KMS ou la base échoue, le webhook répond 503, le jeton reste utilisable et Meta réessaie. `GET /webhook/whatsapp` répond à la poignée de main (`hub.mode=subscribe`, `hub.verify_token`, `hub.challenge`).
4. La page constate l'issue par `GET /verifier/etat` toutes les 3 secondes (`/statique/verifier.js`, autorisé par la CSP de cette seule page), ou par le bouton « J'ai envoyé le message » (`POST /verifier/whatsapp`, sans JavaScript). Vérifiée : nouvel identifiant de session, puis questionnaire. Empreinte déjà connue : « Ce numéro a déjà participé. Merci ! » et session détruite. Numéro refusé ou jeton expiré : message, et bouton pour un nouveau jeton.

Un jeton déjà utilisé qui arrive d'un autre expéditeur est ignoré et compté (`Metriques().SecondExpediteur`) ; une nouvelle livraison du même message par Meta ne compte pas. Les compteurs (`whatsapp.Metriques` : jetons créés, vérifications, déjà participé, numéros refusés, jetons inconnus, second expéditeur) sont agrégés, sans donnée individuelle. Ni numéro, ni jeton, ni corps de notification n'est journalisé. Diguee n'envoie jamais rien dans WhatsApp.

Anti-fraude (ticket #8, spec §7) : après le calcul de l'empreinte, `antifraude.LimiteEmpreinte` (interface `whatsapp.Limiteur`) refuse la 4ᵉ vérification d'une même empreinte en 24 heures (statut `Limitee`, « Ce numéro a été vérifié trop de fois aujourd'hui ») ; le compteur est en mémoire, indexé par un HMAC de l'empreinte (clé tirée au démarrage), jamais par le numéro. Le préfixe de chaque numéro accepté est compté par `antifraude.Surveillance`, et la région de chaque réponse l'est à l'écriture du lot par le tampon (jamais à l'envoi) ; un pic (heure en cours au-dessus de 3 fois la moyenne horaire des heures précédentes, jusqu'à 7 jours, avec 24 heures d'historique et 20 événements au moins) donne un avertissement dans le journal, sans donnée individuelle. L'adresse d'une demande est celle de la connexion TCP (aucun proxy devant le serveur, ADR 0007 : `X-Forwarded-For` n'est jamais lu), remplacée aussitôt par un HMAC à sel quotidien.

Points d'accroche des tickets suivants : les codes WhatsApp (ticket #9) et SMS (ticket #10) appellent, avant tout envoi, `Ralentisseur.Freiner(r)` (passé à `web.Options.Ralentisseur`), `Altcha.Verifier(r.PostForm.Get(altcha.Champ))` et, une fois l'empreinte du numéro saisi calculée, `LimiteEmpreinte.Autoriser(empreinte)` sur **la même instance** que la vérification inversée (`cmd/diguee/main.go`) ; ils comptent le préfixe avec `Surveillance.Compter(antifraude.DimensionPrefixe, prefixe)`. Le code de secours (ticket #9) ajoutera son envoi à `whatsapp.Integration` (jeton d'accès Graph, modèle) et marquera la session par `session.Magasin.Verifier` ou `Creer`, avec la même détection des doublons. Le parcours SMS remplacera la page provisoire `/verifier/sms`.

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
| `DIGUEE_WHATSAPP` | `fake` (secret de développement et page `/dev/whatsapp`, interdits en production) ; `meta` (API Cloud de Meta) | |
| `DIGUEE_META_ID_NUMERO` | obligatoire avec `meta` : identifiant du numéro de téléphone (*Phone number ID*), seules ses notifications sont lues | |
| `DIGUEE_META_SECRET_APP` | obligatoire avec `meta` : secret de l'application (*App secret*), vérifie `X-Hub-Signature-256` ; secret | |
| `DIGUEE_META_JETON_WEBHOOK` | obligatoire avec `meta` : jeton de vérification du webhook (*Verify token*), choisi par vous ; secret | |
| `DIGUEE_CLE_ALTCHA` | absente : clé de développement (intégration `captcha_altcha` en mode `fake`, interdite en production) ; secret, 64 chiffres hexadécimaux, signe les défis du captcha | |
| `DIGUEE_CLE_TOTP` | absente : clé de développement (intégration `chiffrement_totp` en mode `fake`, interdite en production) ; secret, 64 chiffres hexadécimaux | |
| `DIGUEE_STRUCTURE_NOM`, `DIGUEE_STRUCTURE_ADRESSE` | À COMPLÉTER | (ADR 0005) |
| `DIGUEE_CONTACT` | À COMPLÉTER | |
| `DIGUEE_NUMERO_CDP` | À COMPLÉTER | `NUMERO_CDP` |
| `DIGUEE_NUMERO_WHATSAPP` | À COMPLÉTER ; numéro international, par exemple `+221 77 123 45 67` (lien `wa.me` et QR code) | `NUMERO_WHATSAPP_DIGUEE` |
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
| `DIGUEE_ALTCHA_DIFFICULTE` | `100000` : `maxnumber` des défis (le navigateur essaie en moyenne la moitié des nombres) ; à ajuster après essai sur téléphone | `ALTCHA_DIFFICULTE` |
| `DIGUEE_PREAVIS_JOURS` | `15` | `PREAVIS_JOURS` |
| `DIGUEE_TEST_POSTGRES_URL` | tests seulement | |
| `DIGUEE_TEST_KMS_REEL` | tests seulement : `ovh` ou `gcp` active le test contre le vrai KMS (`go test ./internal/empreinte -run TestKMSReel -v`) | |

À chaque démarrage, quel que soit `DIGUEE_EMPREINTEUR`, le serveur calcule deux fois l'empreinte d'une valeur de contrôle et refuse de démarrer si les résultats diffèrent (spec T5), si deux valeurs différentes donnent la même empreinte ou si le KMS ne répond pas.

Avec `DIGUEE_ENV=production`, le serveur refuse de démarrer si une valeur vaut « À COMPLÉTER », si une intégration déclarée dans `internal/integration` est en mode `fake`, ou si le questionnaire contient encore des choix provisoires (`Provisoire` dans `internal/questionnaire/v1.go`), et affiche la liste de ce qui manque.

### Règles

Voir `CLAUDE.md` : textes visibles uniquement dans `internal/i18n/catalogues/fr.json` (un test l'impose), aucun numéro, IP ni corps de requête dans les journaux, pages de moins de 100 Ko sans script (`/journal` est paginé par 50 entrées ; seules exceptions, la page du jeton charge `/statique/verifier.js` et fonctionne aussi sans lui, et la page `/verifier` charge `/statique/altcha.js`, le captcha, chacun autorisé par la seule CSP de sa page), Diguee ne répond jamais dans WhatsApp, aucune entrée du journal public déclenchée par un envoi.
