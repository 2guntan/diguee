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
| `internal/whatsapp` | Vérification inversée (spec §5.1, ADR 0009) : jetons « DIGUEE 4821-KX » en mémoire (aléatoires, 10 minutes, usage unique, liés à la session), webhook de l'API Cloud de Meta (`X-Hub-Signature-256`, poignée de main `hub.*`), métriques agrégées ; envoi du code de secours par un modèle d'authentification (`EnvoyeurMeta`, API Graph `POST /{version}/{id-numéro}/messages`) ; intégration `DIGUEE_WHATSAPP` : `fake` (secret de développement, simulation, codes dans la console) ou `meta` |
| `internal/codes` | Code envoyé au numéro, en secours de la vérification inversée (spec §5.2, §5.3) : interface `Envoyeur` (WhatsApp et SMS), `Verificateur` (un par canal : numéro contrôlé, empreinte, limite par empreinte partagée plus une limite propre au canal, coupe-circuit facultatif (`Disjoncteur`), code à 6 chiffres `crypto/rand`, gardé en HMAC lié à la session, `DIGUEE_CODE_DUREE`, `DIGUEE_CODE_ESSAIS`, comparaison à temps constant), `Console` (fausse implémentation : code écrit dans la console, jamais en production) |
| `internal/sms` | Envoi des codes par SMS, dernier recours (spec §5.3, ADR 0004) : intégration `DIGUEE_SMS`, seulement `fake` (`codes.Console`) tant que le fournisseur n'est pas choisi ; la documentation du paquet dit où brancher son adaptateur |
| `internal/qr` | Encodeur de QR code écrit dans le dépôt (ISO/IEC 18004, mode octet, correction M, versions 1 à 10), rendu SVG dans la page : le lien `wa.me` sur ordinateur, sans JavaScript ni service tiers |
| `internal/altcha` | Captcha ALTCHA écrit dans le dépôt (spec §4 étape 2, §7, ADR 0007) : défis SHA-256 signés par HMAC (`DIGUEE_CLE_ALTCHA`), sel avec expiration, vérification de la signature, de l'expiration, de la solution et de l'usage unique (mémoire bornée) ; `Resoudre` fait le calcul du navigateur pour les tests |
| `internal/antifraude` | Limites et surveillance avant la vérification (spec §7), en mémoire seulement : `Fenetre` (fenêtre glissante bornée), `Anonymiseur` (HMAC d'adresse à sel quotidien, IPv6 par /64), `LimiteEmpreinte` (3 vérifications ou codes par empreinte en 24 h), `Ralentisseur` (30 demandes par heure et par adresse, puis ralentissement borné et alerte, sans blocage), `Surveillance` (métriques par préfixe et par région, alerte de pic), `CoupeCircuit` (coupe-circuit financier du SMS : pic au-delà de 3 fois la moyenne horaire sur 7 jours ou plafond horaire absolu, réactivation automatique ou manuelle) |
| `internal/totp` | TOTP (RFC 6238, au-dessus de HOTP, RFC 4226) : SHA-1, 6 chiffres, 30 secondes, tolérance d'un pas, anti-rejeu ; bibliothèque standard seulement |
| `internal/equipe` | Espace équipe `/equipe` (spec §9, ADR 0008) : comptes nominatifs, rôles cumulables, connexion mot de passe + TOTP, sessions de 8 h, CSRF, limitation des échecs, chiffrement des secrets TOTP (AES-256-GCM), audit interne, page `/equipe/sms` (état du coupe-circuit SMS et réactivation manuelle, administrateurs) ; pages de l'observatoire `/equipe/observatoire` (ticket #13) |
| `internal/base` | PostgreSQL : connexion, migrations (`migrations/NNNN_nom.sql`), requêtes (dont les comptes de l'équipe et leur audit, et l'observatoire) ; `basetest` pour les tests |
| `internal/observatoire` | Observatoire (spec §6.4, §9, ADR 0008, tickets #13 et #14) : élus, sources, engagements, statuts et leur circuit (brouillon, proposé, validé ou rejeté, préavis, publié), droits de réponse (saisi, vérifié ou rejeté, publié), règles vérifiées par le `Service` (quatre yeux, « non tenu » et « partiel » publiables seulement à l'échéance du préavis) et revérifiées par la base ; dossier de préavis de l'élu en PDF ; contenu des entrées « statut publié » et « droit de réponse » du journal public |
| `internal/pdf` | PDF 1.4 minimal en bibliothèque standard (ticket #14) : texte seul, pages A4, polices standard Helvetica, Helvetica-Bold et Courier en encodage WinAnsi (accents français), sortie déterministe ; `pdftest` vérifie la structure (xref, trailer, longueurs) et extrait le texte avec `pdftotext` s'il est installé |
| `internal/stockage` | Interface `Stockage` des copies archivées des sources, rangées sous leur SHA-256 et vérifiées à chaque lecture : `Dossier` (mode `fake`, dossier local) et `S3` (mode `ovh`, Object Storage d'OVHcloud, signature AWS SigV4 en bibliothèque standard) ; intégration `DIGUEE_STOCKAGE` |
| `internal/publication` | Publication quotidienne (spec §8, ticket #11) : comptage des réponses des jours antérieurs (`Agregateur`, sans jamais garder une réponse), règles de masquage et d'arrondi, instantané figé (`Instantane`, JSON d'entiers), `Publieur` (une publication par jour sous verrou, enregistrée avec son entrée au journal public dans la même transaction), `Planificateur` (tâche quotidienne et rattrapage au démarrage), population de l'ANSD (`DIGUEE_POPULATION_ANSD`) |
| `internal/journal` | Journal public (ADR 0010) : entrées chaînées par SHA-256 sur le JSON canonique (RFC 8785, entiers seulement), API d'écriture `Inscrire` / `InscrireMiseEnProduction`, export JSON et vérification de la chaîne ; `journaltest` : magasin en mémoire pour les tests |
| `internal/ancrage` | Ancrage extérieur du journal public (spec §10, ADR 0010, ticket #12) : format OpenTimestamps (`.ots`) lu et écrit dans le dépôt, clients des calendriers (`POST /digest`, mise à niveau `GET /timestamp/…`) et de « Save Page Now » d'archive.org, tâche quotidienne avec nouveaux essais à délai croissant, texte hebdomadaire pour X ; intégration `DIGUEE_ANCRAGE` (`fake` ou `reel`) |
| `internal/i18n` | Catalogues de textes : `fr` livré, `wo` et `ff` prévus |
| `internal/web` | Pages `html/template` sans JavaScript, sauf un petit script d'interrogation sur la page du jeton (accueil, parcours de réponse avec la vérification inversée et le webhook `/webhook/whatsapp`, résultats `/resultats` et leurs exports JSON, méthodologie, confidentialité, journal public `/journal` et son export `/journal.json`, observatoire `/observatoire`), feuille de style et polices auto-hébergées ; monte l'espace équipe sous `/equipe` |
| `internal/https` | HTTPS servi par l'exécutable (ticket #7, ADR 0007) : certificat Let's Encrypt par ACME (`golang.org/x/crypto/acme/autocert`, défis TLS-ALPN-01 et HTTP-01), cache sur disque, seul le nom de `DIGUEE_URL_PUBLIQUE` accepté, redirection de :80 vers HTTPS, HSTS |
| `internal/masque` | Défense en profondeur des journaux (E6, T3) : gestionnaire `slog` qui remplace toute adresse IP ou tout numéro par `[masqué]` |
| `internal/version` | Version et SHA-256 de l'exécutable, affichés au pied de page et inscrits au journal public à chaque mise en production |
| `deploiement/` | Mise en production chez OVHcloud (ticket #7) : préparation du VPS, unités systemd durcies, pare-feu, volume chiffré, PostgreSQL, déploiement en une commande, contrôle T3 des journaux ; procédure dans [`deploiement/README.md`](deploiement/README.md) |
| `docs/` | Spécification, ADR, et [ce que le porteur doit fournir](docs/À%20faire%20par%20le%20porteur.md) |

## Développement

Prérequis : Go 1.24.7 (épinglé dans `go.mod`) et PostgreSQL 16.

```sh
# Une base locale
createdb diguee

# Lancer le serveur (http://localhost:8080) ; en développement, les migrations s'appliquent au démarrage
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

Chaque test de base travaille dans un schéma temporaire, supprimé à la fin. Les tests du parcours (`internal/web/parcours_test.go`) font 200 envois complets (le test résout lui-même le captcha ALTCHA, puis se passe de JavaScript), chacun vérifié par un faux webhook signé, puis vérifient sur un `pg_dump` qu'il n'y a ni numéro, ni IP, ni heure (T1), et que l'ordre du dump ne permet pas d'apparier participations et réponses mieux que le hasard (T2). L'écriture du tampon à l'arrêt est testée par un vrai SIGTERM (`cmd/diguee/main_test.go`), comme l'unique entrée « mise en production » sur plusieurs redémarrages. T11 (modification d'une entrée ancienne détectée) est testé dans `internal/journal` et sur la commande `verifier-journal` ; le refus d'`UPDATE`/`DELETE` en SQL dans `internal/base/journal_test.go`. Les tests de l'espace équipe (`internal/equipe/espace_test.go`) passent par le serveur web complet : connexion refusée sans TOTP valide ou avec un code déjà utilisé, compte désactivé et sessions fermées, pages refusées sans session ou sans le bon rôle, CSRF, échecs limités, ni IP ni secret dans les journaux et la base ; les vecteurs des RFC 4226 et 6238 sont dans `internal/totp`. La vérification inversée (`internal/web/whatsapp_test.go`, `internal/whatsapp`) : signature invalide rejetée (T6), poignée de main, jeton expiré, révoqué ou déjà utilisé sans effet, second expéditeur ignoré et compté, expéditeur hors +221 ou préfixe non autorisé refusé, détection par interrogation et par le bouton, notifications sans rapport acceptées (200), page de simulation absente en mode `meta`, ni numéro ni jeton ni corps dans les journaux. Le QR code (`internal/qr`) est testé sur les tables de la norme (format, version), un vecteur Reed-Solomon connu, deux matrices de référence et une relecture complète (motifs fixes, format, correction) ; les matrices ont été lues par un décodeur indépendant (zxing-cpp) hors du dépôt. L'ancrage (`internal/ancrage`) est testé sans réseau, contre des imitations `httptest` des calendriers OpenTimestamps et d'archive.org : preuve obtenue et publiée chaque jour, fichiers `.ots` de référence relus et réécrits à l'identique, pannes et réponses invalides réessayées à délai croissant puis signalées, mise à niveau Bitcoin, et absence de tout hôte de X dans le code. En intégration continue (`CI` non vide), l'absence de base fait échouer les tests au lieu de les sauter. La CI (`.github/workflows/ci.yml`) lance `gofmt`, `go vet`, installe Pebble, lance `shellcheck` sur `deploiement/`, `go test ./...` avec un service `postgres:16`, puis vérifie le build reproductible.

Autres commandes : `diguee migrer` (migrations seules, avec le rôle propriétaire, puis droits du rôle de l'application, ci-dessous), `diguee cle-volume` (clé du volume chiffré, ci-dessous), `diguee verifier-config` (liste ce qui manque pour la production), `diguee version`, `diguee creer-membre` (espace équipe, ci-dessous), `diguee publier` (publication quotidienne à la main, ci-dessous), `diguee ancrer` (ancrage du jour à la main, ci-dessous) et `diguee verifier-journal` (ci-dessous).

### Mise en production (ticket #7)

Procédure complète, fichiers et ce qui a été essayé : [`deploiement/README.md`](deploiement/README.md) ; ordre des actions du porteur : `docs/À faire par le porteur.md`, « Mise en production pas à pas ».

- **HTTPS** : avec `DIGUEE_TLS=acme` (défaut en production ; `non` y est refusé par la garde), l'exécutable sert le site sur `DIGUEE_ADRESSE_HTTPS` (:443) avec un certificat Let's Encrypt obtenu et renouvelé par `autocert` (cache `DIGUEE_ACME_CACHE`, sur le volume chiffré), pour le seul nom d'hôte de `DIGUEE_URL_PUBLIQUE` (autre nom ou connexion sans SNI : refus sans bruit, sans appel à Let's Encrypt), et `DIGUEE_ADRESSE_HTTP` (:80) ne sert que le défi HTTP-01 et la redirection 301 vers `DIGUEE_URL_PUBLIQUE` (l'en-tête `Host` reçu n'est jamais repris ; hors GET et HEAD : 400). Chaque réponse HTTPS porte `Strict-Transport-Security: max-age=31536000`. TLS 1.2 au moins. En développement, `DIGUEE_TLS=non` : HTTP simple sur `DIGUEE_ADRESSE`.
- **Rôles PostgreSQL** : `diguee migrer` se connecte avec `DIGUEE_POSTGRES_URL_MIGRATIONS` (rôle propriétaire ; obligatoire en production, absente de l'environnement du serveur), applique les migrations puis donne au rôle de groupe `diguee_application` exactement les droits de `internal/base/droits.go` : `SELECT`, `INSERT` sur toutes les tables, `UPDATE` de quatre colonnes de `membre`, rien d'autre (ni `UPDATE`/`DELETE` ailleurs, ni `TRUNCATE`, ni `ALTER TABLE … DISABLE TRIGGER`, ni création) ; une table absente de la politique fait échouer la commande. En production, le serveur n'applique pas les migrations : il refuse un schéma en retard et refuse de démarrer si son rôle (`DIGUEE_POSTGRES_URL`) est superutilisateur, propriétaire du schéma ou d'une table, peut créer dans le schéma ou a un droit de trop. En développement, `diguee` migre lui-même (avec `DIGUEE_POSTGRES_URL_MIGRATIONS` si elle est donnée) et accorde les droits si le rôle `diguee_application` existe. `participation` ne peut être vidée que par le propriétaire (Clôture, #16).
- **Volume chiffré** : `diguee cle-volume creer -sortie F` tire une clé de 64 octets, la fait chiffrer par une clé symétrique du KMS d'OVHcloud (`POST /api/{okmsId}/v1/servicekey/{keyId}/encrypt`, même client mTLS que l'empreinte, contexte `diguee:volume-luks:v1`), vérifie le déchiffrement et n'écrit que le texte chiffré ; `diguee cle-volume dechiffrer -entree F` écrit la clé dans un tube vers `cryptsetup` (jamais dans un terminal), avec 10 essais à délai croissant. Variables : `DIGUEE_OVH_KMS_POINT_ACCES`, `DIGUEE_OVH_KMS_ID`, `DIGUEE_OVH_KMS_VOLUME_*`, lues seules (sans le reste de la configuration).
- **Journaux** : le journal JSON passe par `internal/masque` (toute IP ou tout numéro qui apparaîtrait, par exemple dans une erreur réseau d'un service extérieur, devient `[masqué]`) ; journald, 14 jours ; `deploiement/controle-journaux.sh` fait le contrôle T3 sur le serveur.

Tests : `internal/https` (redirection, HSTS, liste blanche, configuration ; obtention d'un certificat auprès de Pebble par TLS-ALPN-01 et HTTP-01 si `DIGUEE_TEST_PEBBLE` est donnée, ce que fait la CI), `internal/base/droits_test.go` (avec le rôle de l'application : `UPDATE`, `DELETE`, `TRUNCATE`, `ALTER TABLE … DISABLE TRIGGER`, `OWNER TO`, `DROP`, `CREATE`, `session_replication_role` refusés sur toutes les tables, `UPDATE` de `membre` limité à ses colonnes ; contrôle du rôle ; politique complète), `cmd/diguee/production_test.go` (200 envois sur l'exécutable lancé avec le rôle de l'application, puis journal contrôlé par `controle-journaux.sh` : ni IP ni numéro ; refus du rôle propriétaire en production ; mode ACME), `cmd/diguee/volume_test.go` (clé du volume contre le faux KMS), `internal/masque` ; le test T1/T2 du paquet `web` et les tests de l'espace équipe tournent avec le rôle de l'application (`basetest.OuvrirApplication`, qui crée au besoin les rôles `diguee_application` et `diguee_test_app` : le rôle de `DIGUEE_TEST_POSTGRES_URL` doit pouvoir créer des rôles). `deploiement/essais/conteneur.sh` essaie les scripts dans Docker.

### Publication quotidienne des résultats

Spec §8, ticket #11. Le serveur publie lui-même chaque jour à `DIGUEE_HEURE_PUBLICATION` (06:00 GMT) ; au démarrage, il rattrape la dernière publication due si elle manque (avant 06:00, celle de la veille), avant d'ouvrir ses portes. `diguee publier` fait la même chose à la main. Une seule publication par jour : verrou consultatif de la base (serveur et commande peuvent tourner en même temps), clé primaire sur le jour, et une publication existante n'est jamais refaite, même si des réponses arrivent ensuite. Une publication manquée plus ancienne n'est pas rattrapée.

- **Portée** : la publication du jour J porte sur les réponses dont `jour` < J (écrites jusqu'à la veille) et de la version courante du questionnaire. Une réponse écrite aujourd'hui n'apparaît que demain.
- **Compteur** : l'accueil affiche le compteur de la dernière publication, jamais un comptage en direct, arrondi à la **dizaine inférieure** (le même arrondi pour tous les effectifs : deux arrondis d'un même nombre en diraient plus long ; vers le bas, il n'annonce jamais 1 000 avant le seuil).
- **Sous le Seuil de publication** (`DIGUEE_SEUIL_PUBLICATION`, 1 000) : l'instantané ne contient que ce compteur, sans aucune répartition.
- **Au-delà** : chaque question seule, chaque question × un critère du profil, et les critères du profil deux à deux (46 tableaux), plus la composition de l'échantillon ; jamais deux questions ni trois critères. Chaque case est un pourcentage de sa rangée, arrondi à l'unité ; une case de moins de `DIGUEE_SEUIL_CASE` (30) réponses affiche « moins de 30 réponses » ; si c'est la seule de sa rangée, la plus petite des autres est « masquée » ; l'effectif d'une rangée de moins de 30 réponses n'est pas donné. Q7 et les thèmes (Q8) donnent la part des répondants qui ont choisi chaque option ; une option est masquée si ceux qui l'ont choisie **ou** ceux qui ne l'ont pas choisie sont moins de 30. Les régions (et la diaspora) de moins de 30 réponses sont regroupées dans « Autres régions ». Pas de pondération : « résultats non pondérés », et composition de l'échantillon à côté de celle de la population (ANSD, `DIGUEE_POPULATION_ANSD` ; « non disponible » tant qu'elle n'est pas fournie).
- **Instantané figé** : table `publication` (migration `0005`), en ajout seul comme le journal (`UPDATE`, `DELETE`, `TRUNCATE` refusés) : le JSON (servi octet pour octet) et sa mise en page HTML (fragment en français, produit au moment de la publication par `web.RendreInstantane`, sans heure), datés au jour. Dans la même transaction, une entrée « publication quotidienne » au journal public : jour, compteur, résultats publiés ou non, seuils, nombre de tableaux et SHA-256 du JSON. Aucune réponse individuelle n'est exportée.
- **Pages** : `/resultats` (dernière publication), `/resultats/AAAA-MM-JJ` (publications passées), `/resultats/publications` (liste), `/resultats.json` et `/resultats/AAAA-MM-JJ.json` (export). Sans JavaScript, sous 100 Ko (les croisements sont repliés dans des `<details>`, les balises de fin facultatives des tableaux sont omises).

Pour la clôture (ticket #16) : après l'écriture du dernier tampon, `publication.Publieur.Publier(ctx, lendemain du dernier jour d'écriture)` produit la dernière publication sur toutes les réponses (sans effet si elle existe déjà) ; `Planificateur` continue sinon chaque jour.

Tests : `internal/publication` (T10 : case de 29 et sa complémentaire masquées, arrondis ; seuil 999/1 000 ; choix multiples ; regroupement des régions ; aucune combinaison de trois critères ; avec PostgreSQL : réponse du jour publiée le lendemain, une seule publication et une seule entrée au journal sous 8 appels simultanés, instantané immuable, tout ou rien, rattrapage), `internal/web/resultats_test.go` (pages sous 100 Ko sans script de 999 à 60 000 réponses, export JSON = SHA-256 du journal, archives), `cmd/diguee` (commande `publier`, une seule publication sur trois démarrages) ; le test T1/T2 (`parcours_test.go`) publie aussi les 200 réponses et vérifie que la table `publication` ne contient que des agrégats datés au jour.

### Vérification inversée par WhatsApp

Parcours principal de la vérification (spec §5.1, ADR 0009) ; la vérification factice du ticket #2 est retirée.

1. `GET /verifier` explique le principe et contient un défi ALTCHA, que `/statique/altcha.js` (script maison, autorisé par la CSP de cette seule page, sans aucune connexion) résout dans le navigateur avant d'activer le bouton. `POST /verifier` compte la demande par adresse (ralentissement au-delà de `DIGUEE_IP_PAR_HEURE`, jamais de blocage), puis vérifie la solution (champ `altcha` : signature HMAC, expiration de 20 minutes, solution, usage unique) **avant toute création de jeton** ; sans solution (pas de JavaScript), il renvoie vers `/verifier/sans-javascript`, qui mène au code par SMS (`/verifier/sms`, possible sans JavaScript sous un quota séparé, ci-dessous). Ensuite, il ouvre une session non vérifiée et tire un jeton « DIGUEE 4821-KX » (4 chiffres de 2 à 9, 2 lettres sans B, G, I, L, O, Q, S, Z ; `crypto/rand`), valable `DIGUEE_JETON_DUREE`, à usage unique, gardé en mémoire avec l'identifiant de la session. Un nouveau jeton révoque le précédent.
2. `GET /verifier/whatsapp` affiche le jeton, le numéro de Diguee, le bouton `https://wa.me/<numéro>?text=<jeton>` et, sur grand écran avec souris (CSS seulement), le même lien en QR code SVG calculé par le serveur.
3. `POST /webhook/whatsapp` : signature `X-Hub-Signature-256` (HMAC-SHA-256 du corps brut, comparaison à temps constant) vérifiée avant toute lecture, sinon 401 ; seules les notifications `messages` du numéro `DIGUEE_META_ID_NUMERO` et les messages texte sont lus ; jeton retrouvé dans le texte (casse, espaces et tiret tolérés) ; expéditeur (`from`) contrôlé par `empreinte.Normaliser` (+221, préfixes autorisés) ; empreinte calculée, puis comparée à la base et au tampon ; session marquée vérifiée. Le reste (statuts, autres types, jetons inconnus ou expirés) reçoit 200. Si le KMS ou la base échoue, le webhook répond 503, le jeton reste utilisable et Meta réessaie. `GET /webhook/whatsapp` répond à la poignée de main (`hub.mode=subscribe`, `hub.verify_token`, `hub.challenge`).
4. La page constate l'issue par `GET /verifier/etat` toutes les 3 secondes (`/statique/verifier.js`, autorisé par la CSP de cette seule page), ou par le bouton « J'ai envoyé le message » (`POST /verifier/whatsapp`, sans JavaScript). Vérifiée : nouvel identifiant de session, puis questionnaire. Empreinte déjà connue : « Ce numéro a déjà participé. Merci ! » et session détruite. Numéro refusé ou jeton expiré : message, et bouton pour un nouveau jeton.

Un jeton déjà utilisé qui arrive d'un autre expéditeur est ignoré et compté (`Metriques().SecondExpediteur`) ; une nouvelle livraison du même message par Meta ne compte pas. Les compteurs (`whatsapp.Metriques` : jetons créés, vérifications, déjà participé, numéros refusés, jetons inconnus, second expéditeur) sont agrégés, sans donnée individuelle. Ni numéro, ni jeton, ni corps de notification n'est journalisé. Diguee n'envoie jamais rien dans WhatsApp.

Anti-fraude (ticket #8, spec §7) : après le calcul de l'empreinte, `antifraude.LimiteEmpreinte` (interface `whatsapp.Limiteur`) refuse la 4ᵉ vérification d'une même empreinte en 24 heures (statut `Limitee`, « Ce numéro a été vérifié trop de fois aujourd'hui ») ; le compteur est en mémoire, indexé par un HMAC de l'empreinte (clé tirée au démarrage), jamais par le numéro. Le préfixe de chaque numéro accepté est compté par `antifraude.Surveillance`, et la région de chaque réponse l'est à l'écriture du lot par le tampon (jamais à l'envoi) ; un pic (heure en cours au-dessus de 3 fois la moyenne horaire des heures précédentes, jusqu'à 7 jours, avec 24 heures d'historique et 20 événements au moins) donne un avertissement dans le journal, sans donnée individuelle. L'adresse d'une demande est celle de la connexion TCP (aucun proxy devant le serveur, ADR 0007 : `X-Forwarded-For` n'est jamais lu), remplacée aussitôt par un HMAC à sel quotidien.

Les codes WhatsApp et SMS (ci-dessous) appliquent, avant tout envoi, la limite par adresse (`Ralentisseur.Freiner(r)` ; un ralentisseur plus strict pour le SMS), le captcha (`Altcha.Verifier` ; pour le SMS sans JavaScript, un quota séparé) et, une fois l'empreinte du numéro saisi calculée, `LimiteEmpreinte.Autoriser(empreinte)` sur **la même instance** que la vérification inversée (`cmd/diguee/main.go`) ; ils comptent le préfixe avec `Surveillance.Compter(antifraude.DimensionPrefixe, prefixe)`.

### Code envoyé par WhatsApp, en secours

Pour qui a son WhatsApp sur un autre téléphone (spec §5.2, ADR 0004 et 0009) : lien « Mon WhatsApp est sur un autre téléphone » sur `/verifier` et sur la page du jeton.

1. `GET /verifier/code` : saisie du numéro et défi ALTCHA (`/statique/altcha.js`, seul script de la page). `POST /verifier/code` : ralentisseur par adresse, captcha (sans solution : `/verifier/sans-javascript`), puis `codes.Verificateur.Demander` : format et préfixe (`empreinte.Normaliser`), empreinte, préfixe compté par la surveillance, limite par empreinte (**la même instance** `antifraude.LimiteEmpreinte` que la vérification inversée, créée dans `cmd/diguee/main.go` : 3 vérifications ou codes par 24 h, tous parcours confondus), tirage d'un code à 6 chiffres (`crypto/rand`) et envoi par `whatsapp.Integration.Envoyeur`. Le numéro est retiré du formulaire dès sa lecture et n'est plus référencé après l'envoi ; la mémoire ne garde que l'empreinte et un HMAC du code (clé aléatoire tirée au démarrage, lié à l'identifiant de session), jamais le code en clair. Une nouvelle demande révoque le code précédent, et le jeton de la vérification inversée est révoqué (un seul parcours à la fois).
2. `GET /verifier/code/saisie` : saisie du code (`autocomplete="one-time-code"`), essais restants. `POST` : comparaison à temps constant (`hmac.Equal`). Code juste avant `DIGUEE_CODE_DUREE` : détection des doublons (base et tampon), puis session vérifiée, nouvel identifiant de session et questionnaire ; empreinte connue : « Ce numéro a déjà participé. Merci ! » et session détruite. Toute saisie fausse (même mal formée) consomme un essai ; au `DIGUEE_CODE_ESSAIS`ᵉ, ou après l'expiration, le code est invalidé et doit être redemandé.

La détection « déjà participé » n'a lieu qu'après un code juste : faite avant l'envoi, elle dirait à n'importe qui si un numéro saisi a participé. En mode `meta`, `whatsapp.EnvoyeurMeta` envoie le modèle d'authentification `DIGUEE_META_MODELE_CODE` (langue `DIGUEE_META_LANGUE_MODELE`), avec le code en paramètre du corps et du bouton « Copier le code » (bouton d'URL d'indice 0, format de Meta), jeton `DIGUEE_META_JETON_ACCES` en `Authorization: Bearer`, délai `DIGUEE_META_DELAI` ; ses erreurs ne gardent que le statut HTTP et le code d'erreur numérique de Meta. En mode `fake`, `codes.Console` écrit le code (jamais le numéro) sur la sortie d'erreur du serveur, hors du journal structuré, et la page `/dev/whatsapp` montre le dernier code : c'est la seule exception à « aucune donnée dans les journaux », impossible en production (la console refuse `DIGUEE_ENV=production`, et le mode `fake` y est refusé par la garde). Les tests sont dans `internal/codes`, `internal/web/code_test.go` et `internal/whatsapp/envoi_test.go` (faux serveur `httptest` de l'API Graph qui vérifie la requête).

### Code par SMS, en dernier recours

Pour qui n'a pas WhatsApp (spec §5.3, ADR 0004, ticket #10) : lien « Je n'ai pas WhatsApp » sur `/verifier`, sur la page du jeton et sur `/verifier/code`. Les mêmes gestionnaires que le code WhatsApp (`internal/web/code.go`, paramétrés par canal) servent `GET`/`POST /verifier/sms` et `/verifier/sms/saisie`, avec un second `codes.Verificateur` (`cmd/diguee/main.go`) :

- **limites plus strictes**, toutes configurables, et refusées au démarrage si elles sont plus lâches que celles du WhatsApp : la limite par empreinte **partagée** par tous les canaux (`DIGUEE_VERIF_PAR_EMPREINTE_24H`, la même instance) plus une limite propre au SMS (`DIGUEE_SMS_PAR_EMPREINTE_24H`, 2 par 24 h), `DIGUEE_SMS_CODE_ESSAIS` (3 essais), ralentissement par adresse dès `DIGUEE_SMS_IP_PAR_HEURE` (10 demandes par heure ; jamais de blocage) ;
- **coupe-circuit** (`antifraude.CoupeCircuit`, T14) : un envoi qui porterait le volume de l'heure au-delà de `DIGUEE_SMS_COUPE_CIRCUIT` (3) fois la moyenne horaire des heures pleines précédentes (7 jours au plus, 24 heures d'historique au moins, `DIGUEE_SMS_COUPE_CIRCUIT_PLANCHER` = 20 envois dans l'heure au moins), ou au-delà du plafond absolu `DIGUEE_SMS_PAR_HEURE` (100, même sans historique), est refusé et suspend le parcours. Alerte : avertissement du journal « alerte : coupe-circuit SMS déclenché », avec des nombres seulement. Pendant la suspension, `/verifier/sms` répond 503 avec une page qui explique que le secours SMS est momentanément indisponible et renvoie vers WhatsApp ; aucun SMS ne part (ni empreinte calculée). Réactivation automatique après `DIGUEE_SMS_REACTIVATION` (1 h), ou manuelle par un administrateur sur `/equipe/sms` (inscrite à `audit_equipe`, type `reactivation_sms`, sans cible). Compteurs en mémoire : un redémarrage les efface et réactive le parcours ;
- **sans JavaScript** (choix à valider par le porteur, docs §1.9) : le captcha ALTCHA demande JavaScript. Avec JavaScript, le SMS exige le captcha comme les autres canaux (solution fausse ou expirée : refus). Sans solution, le SMS reste possible sans captcha, mais sous un quota séparé, refusé au-delà (429) : `DIGUEE_SMS_SANS_JS_IP_PAR_HEURE` (2 par heure et par adresse) et `DIGUEE_SMS_SANS_JS_PAR_HEURE` (10 par heure pour tout le site, alerte au plus une fois par heure), en plus de toutes les limites ci-dessus ; ces SMS comptent dans le coupe-circuit. `/verifier/sans-javascript` l'explique.

`DIGUEE_SMS=fake` (défaut, interdit en production) : `codes.Console` écrit le code (jamais le numéro) sur la sortie d'erreur et la page `/dev/sms` le montre. Aucun fournisseur n'est encore choisi : son adaptateur s'ajoute dans `internal/sms` (mode de plus, type qui implémente `codes.Envoyeur`), voir la documentation de ce paquet et `docs/À faire par le porteur.md` §1.6. Tests : `internal/web/sms_test.go` (parcours complet avec et sans JavaScript jusqu'à la page Merci, pic simulé, plafond, réactivations, limites, limite partagée entre les trois parcours, ni numéro ni code dans le journal), `internal/antifraude`, `internal/codes`, `internal/equipe/espace_test.go`.

### Espace équipe

L'espace `/equipe` (spec §9, ADR 0008) a ses propres routes, gabarits (`internal/equipe/gabarits`, textes dans le catalogue `fr` sous `equipe.*`) et cookies, limités à `/equipe`. Chaque membre a un compte nominatif et un ou plusieurs rôles : **rédacteur**, **relecteur**, **administrateur**. La connexion demande l'identifiant, un mot de passe tiré par la plateforme (20 signes, 100 bits, empreinte PBKDF2-SHA-256) et le code TOTP de l'application d'authentification ; jamais de SMS. Le secret TOTP est chiffré en AES-256-GCM avec `DIGUEE_CLE_TOTP`, lié à l'ID du membre. Un code déjà accepté, ou plus ancien, est refusé ; après un redémarrage, les codes du pas de démarrage aussi.

- Session de 8 heures fixes depuis la connexion, en mémoire seulement (un redémarrage ferme les sessions). Le compte et ses rôles sont relus en base à chaque requête : un changement de rôle vaut tout de suite, et un compte désactivé perd aussitôt ses sessions.
- Seul un administrateur crée un compte, change ses rôles, réinitialise son accès (nouveau mot de passe et nouveau secret, sessions fermées) ou le désactive (définitif, la base refuse la réactivation et la suppression). Il reste toujours au moins un administrateur actif, et personne ne désactive son propre compte.
- Formulaires protégés contre le CSRF : jeton de session dans un champ caché, jeton lié à un cookie pour la connexion, et refus des requêtes venues d'un autre site (`Origin`, `Sec-Fetch-Site`).
- Échecs de connexion limités en mémoire : 5 par identifiant en 15 minutes, 20 par adresse en une heure (l'adresse est remplacée par un HMAC à sel quotidien et n'est jamais conservée ni journalisée).
- Audit interne : la table `audit_equipe`, en ajout seul, garde au jour près chaque création, changement de rôles, réinitialisation, désactivation et connexion, avec son auteur. Ces actions ne sont pas publiques, donc pas au journal public.

Pour ajouter une page : `Espace.Gerer("GET /equipe/…", gestionnaire, equipe.Redacteur)` vérifie la session, le compte actif, le rôle (un des rôles donnés) et, hors GET, le jeton CSRF (champ `csrf`, valeur `equipe.JetonCSRFDe(ctx)`) ; `equipe.MembreDe(ctx)` donne le membre connecté (`ID`, `Identifiant`, `Nom`, `Roles`, `A(role)`).

Créer un compte depuis le serveur (le premier administrateur, par exemple) ; le mot de passe, le secret TOTP et l'URI `otpauth://` ne s'affichent qu'une fois :

```sh
diguee creer-membre -identifiant awa.diop -nom "Awa Diop" -roles administrateur,redacteur,relecteur
```

### Observatoire

Ticket #13, spec §6.4 et §9, ADR 0008. Tout est en **ajout seul jusque dans la base** (migration `0007`) : `UPDATE`, `DELETE` et `TRUNCATE` sont refusés sur les onze tables, pour tous les rôles, propriétaire compris (déclencheurs, comme le journal). Les états intermédiaires ne sont jamais des modifications : chaque étape est une ligne ajoutée dans sa propre table, qui référence l'étape précédente.

```
elu ─┐
source ─┬─ engagement_source ─ engagement ─► engagement_validation (relecteur ≠ rédacteur)
        │                                        │
        └─ statut_preuve ─ statut (brouillon) ◄──┘
                             ─► statut_proposition (par son rédacteur, une preuve au moins)
                                  ─► statut_validation (relecteur ≠ rédacteur) ─► statut_publication (+ journal public)
                                  └► statut_rejet (relecteur ≠ rédacteur, avec un motif)
```

- **Quatre yeux (T7)** : `statut_validation` recopie `redige_par`, garanti exact par une clé étrangère composée `(statut_id, redige_par)` vers `statut`, et porte `CHECK (valide_par <> redige_par)` ; même règle pour `engagement_validation` et `statut_rejet`. Le `Service` refuse aussi (`ErrQuatreYeux`) avant d'écrire.
- **Ordre du circuit** : clés étrangères vers l'étape précédente, clés primaires (une étape n'a lieu qu'une fois), déclencheurs (validation et rejet exclusifs, sources d'un engagement figées à sa validation, preuves figées à la proposition, publication liée à une entrée `statut_publie` du journal).
- **Préavis** : `statut_publication` recopie la valeur du statut (clé étrangère composée) ; le ticket #13 y portait `CHECK (valeur IN ('tenu', 'en_cours'))`, remplacé au ticket #14 par le déclencheur `statut_preavis_echu` (ci-dessous).
- **Dates** : au jour (`*_le`), jamais d'heure ; l'ordre d'un même jour est celui des identifiants, attribués par l'application (max + 1 sous verrou consultatif), sans séquence. Aucune exception de schéma à T1.
- **Sources** : lien d'origine (`http://` ou `https://`) et copie archivée téléversée par le rédacteur (PDF, PNG ou JPEG reconnus à leur signature, `DIGUEE_ARCHIVE_TAILLE_MO`, 10 Mo), déposée dans le `Stockage` sous son SHA-256 puis relue avant d'enregistrer la source. Servie sous `/observatoire/archives/<sha256>.<extension>` (et `/equipe/observatoire/archives/…` pour relire avant validation) : type fixé par Diguee, `Content-Disposition: attachment`, `X-Content-Type-Options: nosniff`, `Content-Security-Policy: default-src 'none'; sandbox` ; publique seulement si elle appartient à un engagement public ou à une preuve publiée ; jamais servie si son contenu ne correspond plus à son SHA-256.
- **Espace équipe** `/equipe/observatoire` (rédacteurs et relecteurs ; chaque action demande son rôle, tous les formulaires passent par `Gerer`, donc par le CSRF) : tableau de bord (engagements à valider, statuts proposés, statuts validés à publier, mes brouillons), élus, sources (téléversement, seule route dont le corps peut dépasser 64 Ko), saisie d'un engagement, fiche d'un engagement (sources, validation, rédaction d'un statut), fiche d'un statut (preuves, proposition, validation ou rejet, publication).
- **Pages publiques** : `/observatoire`, liste des engagements publics (validés, avec au moins un statut publié), filtrable par élu, mandat et statut (le dernier publié), 30 par page ; `/observatoire/engagements/{id}`, fiche avec la citation, le contexte, l'échéance, les sources (lien d'origine, copie archivée et son SHA-256) et l'historique complet des statuts publiés, dans l'ordre de publication, chacun avec ses preuves, son rédacteur, son relecteur et son entrée du journal. Sans JavaScript, sous 100 Ko. Le contact officiel de l'élu n'est jamais publié.
- **Journal public** : la publication et son entrée `statut_publie` sont écrites dans la même transaction (sous le verrou de l'observatoire, puis celui du journal) : numéros de l'engagement et du statut, valeur, élu, noms du rédacteur et du relecteur, SHA-256 des copies des sources et des preuves, et `texte_sha256`, le SHA-256 du JSON canonique `{"citation","date_declaration","echeance","explication","valeur"}` (dates `AAAA-MM-JJ`), que chacun peut recalculer depuis la fiche.

#### Préavis et droit de réponse (ticket #14)

Migration `0008`, toujours en ajout seul (déclencheurs sur les cinq nouvelles tables). La contrainte `statut_publiable_sans_preavis` du ticket #13 est remplacée par un déclencheur.

```
statut_validation (non tenu, partiel) ─► statut_preavis (date d'envoi, échéance ≥ envoi + 15 j, SHA-256 du dossier)
                                            ├─► statut_publication, à partir de l'échéance (+ journal public)
                                            └─► droit_reponse (texte tel quel, canal, référence du canal, reçu le)
                                                  ├─► droit_reponse_verification (autre membre) ─► droit_reponse_publication (+ journal public)
                                                  └─► droit_reponse_rejet (autre membre, motif)
```

- **Dossier de l'élu** (`/equipe/observatoire/statuts/{id}/dossier.pdf`, pour un « non tenu » ou un « partiel » validé) : PDF 1.4 écrit par `internal/pdf`, textes du catalogue (`dossier.*`) : élu, citation, date et échéance de l'engagement, sources (lien d'origine et SHA-256 de la copie), statut proposé, explication, rédacteur et relecteur, preuves, délai de réponse (`DIGUEE_PREAVIS_JOURS`), adresse de réponse (`DIGUEE_ADRESSE_REPONSE`) et canaux acceptés. Sans date d'impression : déterministe. Le contact officiel de l'élu n'y est pas.
- **Aucun envoi par la plateforme** : un membre (rédacteur ou relecteur) télécharge le dossier, l'envoie lui-même par le canal officiel de l'élu (affiché sur la page du statut), puis enregistre la date d'envoi (entre la validation et aujourd'hui) et le moyen d'envoi (jamais publié). Le dossier est alors déposé dans le `Stockage` sous son SHA-256 (conservé dans `statut_preavis`), et c'est cette copie qui est servie ensuite. Un test vérifie que le paquet `observatoire` n'importe aucun client réseau ni de messagerie.
- **T9** : l'échéance est `envoi + DIGUEE_PREAVIS_JOURS` (15 au moins : la configuration refuse moins, la base aussi par `CHECK (echeance >= envoye_le + 15)`). Le `Service` refuse la publication sans préavis (`ErrSansPreavis`) ou avant l'échéance (`ErrPreavisEnCours`) ; en base, le déclencheur `statut_preavis_echu` refuse une publication de « non tenu » ou « partiel » sans préavis, avant l'échéance, ou datée d'un autre jour que son entrée du journal public (chaînée, ancrée chaque jour). L'horloge du `Service` est injectable (tests à J+14 et J+15).
- **Droit de réponse** : saisi par un rédacteur, mot pour mot (10 000 caractères), avec son canal (`adresse_officielle`, `courrier_signe`, `compte_officiel`), la référence vérifiable du canal (jamais publiée) et sa date de réception (après l'envoi du préavis) ; vérifié ou rejeté par un relecteur qui ne l'a pas saisi (`droit_reponse_quatre_yeux`, clé étrangère composée, comme les statuts). Tant qu'un droit de réponse attend sa vérification, le statut n'est pas publié (`droit_reponse_en_attente`). Vérifié avant la publication, il est publié avec le statut ; après, dès sa vérification. Chaque publication écrit son entrée `droit_de_reponse` dans la même transaction : numéros, élu, canal, date de réception, noms des deux membres, et `texte_sha256` (SHA-256 du JSON canonique `{"canal","recu_le","texte"}`). L'entrée `statut_publie` porte aussi `preavis_envoye_le`, `preavis_echeance` et `dossier_sha256`.
- **Fiche publique** : sous l'engagement, la section « Droit de réponse de l'élu » donne chaque texte tel quel (retours à la ligne gardés), sa date de réception, son canal, sa date de publication et son entrée du journal, sans commentaire ; dans l'historique, chaque statut préavisé porte « Sollicité le JJ/MM/AAAA, pas de réponse », ou « Sollicité le JJ/MM/AAAA. » avec un lien vers la réponse.
- **Espace équipe** : page du statut (dossier, canal officiel de l'élu, enregistrement de l'envoi, échéance, publication à partir de l'échéance, droits de réponse et leur saisie), page d'un droit de réponse (`/equipe/observatoire/reponses/{id}` : vérification ou rejet), tableau de bord (préavis à envoyer, préavis en cours avec leur échéance, droits de réponse à vérifier). Tous les formulaires passent par `Gerer` (CSRF).

Tests du ticket #14 : `internal/base/preavis_test.go` (T9 par l'API, par le magasin, journal compris, et en SQL direct, à J+14 et J+15, date de publication falsifiée ; échéance à 14 jours et envoi avant validation refusés en base ; quatre yeux du droit de réponse par l'API et en SQL ; réponse en attente bloquante ; publication avec le statut ou après ; rejet ; T8 sur les nouvelles tables ; dossier conservé identique à son SHA-256), `internal/equipe/preavis_test.go` (de bout en bout par les formulaires : dossier PDF, CSRF, dates, échéance au tableau de bord, quatre yeux, publication à J+15, réponse mot pour mot avec date et canal, « Sollicité le 01/12/2026, pas de réponse », réponse après publication, pages sous 100 Ko sans script), `internal/observatoire/dossier_test.go` (contenu du PDF par `pdftotext`, aucun envoi automatique), `internal/pdf` (structure, déterminisme, accents) et T1 avec un préavis et un droit de réponse publiés.

Pour le ticket #15 (sauvegardes) : l'interface `stockage.Stockage` (`Deposer`, `Lire`), et la liste des objets dans `source.archive_sha256` et `statut_preavis.dossier_sha256`.

Tests : `internal/base/observatoire_test.go` (T7 par l'API, par le magasin et en SQL direct, rédacteur falsifié ; T8 sur toutes les tables ; ordre du circuit), `internal/equipe/observatoire_test.go` (circuit complet par les formulaires : rôles, CSRF, types et tailles de fichier, quatre yeux, publication inscrite au journal, filtres, fiche dans l'ordre, lien d'origine et copie identique à son SHA-256, en-têtes, copie altérée non servie, pages sous 100 Ko sans script), `internal/stockage` (vecteurs SigV4 de la documentation d'AWS, faux serveur S3 qui recalcule la signature, dossier), et T1 (`parcours_test.go`) avec un statut publié.

### Journal public

Chaque entrée de `journal_public` contient un numéro, un horodatage à la minute (UTC), un type, un contenu (objet JSON d'agrégats seulement) et l'empreinte de l'entrée précédente ; son empreinte est le SHA-256 du JSON canonique (RFC 8785, entiers seulement) de ces cinq champs, la première ayant une empreinte précédente nulle. La base refuse `UPDATE`, `DELETE` et `TRUNCATE` sur la table, pour tous les rôles (déclencheurs de la migration `0002`), et refuse une insertion qui ne suit pas la dernière entrée. Les écritures sont sérialisées par un verrou consultatif. Au démarrage du serveur, une entrée « mise en production » (version et SHA-256 de l'exécutable) est inscrite si la dernière entrée de ce type ne porte pas déjà ces valeurs. La publication quotidienne écrit la sienne, la publication d'un statut de l'observatoire et celle d'un droit de réponse aussi (ci-dessus). Les autres types (méthodologie, ouverture, clôture, procès-verbal) sont écrits par les tickets suivants avec `journal.Journal.Inscrire`. Aucune entrée n'est écrite à cause d'un envoi : l'heure d'une entrée ne doit rien dire de celle d'une réponse.

Vérifier un export, sans configuration ni base :

```sh
go run github.com/2guntan/diguee/cmd/diguee@latest verifier-journal journal.json   # ou https://…/journal.json
# Détecter aussi une réécriture complète de la chaîne : une empreinte publiée
# hors de nos serveurs, ou un export téléchargé plus tôt
diguee verifier-journal -attendu <empreinte> -precedent ancien.json journal.json
```

### Ancrage du journal hors de nos serveurs

Ticket #12, ADR 0010. Chaque jour, `DIGUEE_ANCRAGE_DECALAGE` (15 minutes) après `DIGUEE_HEURE_PUBLICATION`, le serveur :

1. soumet l'empreinte de tête du journal aux calendriers publics OpenTimestamps (`DIGUEE_ANCRAGE_CALENDRIERS` : `a.pool.opentimestamps.org`, `b.pool.opentimestamps.org`, `finney.calendar.eternitywall.com`), comme `ots stamp` : nonce de 16 octets ajouté, SHA-256, puis `POST /digest` du condensat brut à chaque calendrier ; leurs engagements partiels forment un fichier `.ots` au format du client de référence (relu et réécrit octet pour octet sur les fichiers d'exemple d'`opentimestamps-client`, `internal/ancrage/testdata`). Il faut au moins `DIGUEE_ANCRAGE_MIN_CALENDRIERS` (2) réponses valides ; la preuve est servie sous `/journal/ancrages/AAAA-MM-JJ.ots` ;
2. dépose `/journal` et `/journal.json` sur archive.org (`GET https://web.archive.org/save/<adresse>`, sans compte) et garde les liens des copies ; les 7 derniers jours sont affichés sur `/journal`, la liste complète dans `/journal/ancrages.json` ;
3. met à niveau les preuves des 14 jours précédents encore en attente (`GET <calendrier>/timestamp/<engagement>`, calendriers connus seulement, comme la liste blanche d'`ots upgrade`) : dès qu'un calendrier donne l'attestation Bitcoin, la preuve complétée est enregistrée comme nouvelle révision et servie à la place.

Un calendrier ou archive.org injoignable, ou une réponse invalide, n'arrête rien : avertissement dans le journal technique, nouvel essai après 5 minutes, puis 10, 20… jusqu'à 6 heures, et échec en cours affiché aux administrateurs (`/equipe/ancrage`). L'état est en base (tables `ancrage_preuve` et `ancrage_archive`, migration `0006`, en ajout seul, datées au jour) : au démarrage, l'ancrage du jour dû qui manque est rattrapé en tâche de fond. Seul le jour dû est rattrapé (l'empreinte de tête d'un jour passé n'est plus celle du journal).

Chaque semaine, `/equipe/ancrage` (administrateurs) affiche un texte prêt à publier sur X : date, numéro et empreinte de la tête ancrée le premier jour de la semaine, lien vers `/journal` et vers la preuve. **La plateforme ne publie jamais sur X** : aucun appel à X n'existe dans le code (test `TestAucunAppelAX`) ; le porteur copie et poste lui-même.

Vérifier une preuve, avec le client OpenTimestamps (`pip install opentimestamps-client`) :

```sh
ots upgrade diguee-journal-AAAA-MM-JJ.ots          # si l'attestation Bitcoin manque encore
ots verify -d <empreinte de tête> diguee-journal-AAAA-MM-JJ.ots   # demande un nœud Bitcoin
ots info diguee-journal-AAAA-MM-JJ.ots             # chemin jusqu'au bloc, sans nœud
```

L'empreinte vérifiée se retrouve dans le journal téléchargé : `diguee verifier-journal -attendu <empreinte> journal.json`. En développement, `DIGUEE_ANCRAGE=fake` (défaut) remplace les calendriers et archive.org par des imitations sans réseau (adresses en `.invalid`) ; ce mode est refusé en production, où `reel` est le défaut. `diguee ancrer` lance l'ancrage du jour à la main (utile pour un premier essai réel).

**Build reproductible** : `make build` compile avec `CGO_ENABLED=0 -trimpath -buildid=`, et fixe la version avec `git describe`. Le même commit, compilé avec la même version de Go pour le même système (`GOOS`/`GOARCH`), donne le même SHA-256, qui est affiché au pied de chaque page.

### Variables d'environnement

Les secrets (URL de la base avec son mot de passe, puis les accès Meta, SMS, KMS…) ne sont jamais dans le dépôt : ils sont passés par l'environnement (fichier `EnvironmentFile` de systemd en production).

| Variable | Défaut | Spec §13 |
|---|---|---|
| `DIGUEE_ENV` | `development` (ou `production`) | |
| `DIGUEE_ADRESSE` | `:8080` : adresse d'écoute en HTTP simple (`DIGUEE_TLS=non`) | |
| `DIGUEE_POSTGRES_URL` | obligatoire, secret ; en production, rôle de l'application `diguee_app` (`postgres://diguee_app@/diguee?host=/var/run/postgresql`) | |
| `DIGUEE_POSTGRES_URL_MIGRATIONS` | vide : `DIGUEE_POSTGRES_URL` (développement) ; obligatoire pour `diguee migrer` en production : rôle propriétaire ; secret, jamais dans l'environnement du serveur | |
| `DIGUEE_TLS` | `non` en développement (HTTP simple, refusé en production) ; `acme` en production (HTTPS, Let's Encrypt) | |
| `DIGUEE_ADRESSE_HTTPS`, `DIGUEE_ADRESSE_HTTP` | `:443`, `:80` (mode `acme`) | |
| `DIGUEE_ACME_CACHE` | `/srv/diguee-donnees/diguee/acme` : certificats et clé du compte ACME (mode `acme`) | |
| `DIGUEE_ACME_ANNUAIRE` | `https://acme-v02.api.letsencrypt.org/directory` ; annuaire de test : `https://acme-staging-v02.api.letsencrypt.org/directory` | |
| `DIGUEE_ACME_COURRIEL` | facultatif : contact du compte ACME | |
| `DIGUEE_EMPREINTEUR` | `fake` (HMAC de développement, interdit en production) ; `ovh` (KMS d'OVHcloud, RS256 puis SHA-256) ; `gcp` (repli Google Cloud KMS, HMAC natif) | |
| `DIGUEE_OVH_KMS_POINT_ACCES` | obligatoire avec `ovh` : point d'accès régional `https://…okms.ovh.net` | |
| `DIGUEE_OVH_KMS_ID`, `DIGUEE_OVH_KMS_CLE_ID` | obligatoires avec `ovh` : UUID du KMS et de la clé RSA de signature | |
| `DIGUEE_OVH_KMS_CERTIFICAT`, `DIGUEE_OVH_KMS_CLE_CLIENT` | obligatoires avec `ovh` : chemins du certificat client mTLS et de sa clé privée (PEM, secret) | |
| `DIGUEE_OVH_KMS_AC` | facultatif : autorités de certification (PEM) ; défaut : celles du système | |
| `DIGUEE_OVH_KMS_VOLUME_CLE_ID`, `DIGUEE_OVH_KMS_VOLUME_CERTIFICAT`, `DIGUEE_OVH_KMS_VOLUME_CLE_CLIENT` | `diguee cle-volume` seulement : UUID de la clé symétrique du volume, certificat client et clé privée de l'identité qui la déchiffre (secret) | |
| `DIGUEE_GCP_KMS_CLE`, `DIGUEE_GCP_KMS_JETON_FICHIER` | obligatoires avec `gcp` : version de clé HMAC et fichier du jeton d'accès (secret) | |
| `DIGUEE_GCP_KMS_POINT_ACCES`, `DIGUEE_GCP_KMS_AC` | `https://cloudkms.googleapis.com` ; facultatif | |
| `DIGUEE_KMS_DELAI` | `5s` (délai d'attente d'un appel au KMS) | |
| `DIGUEE_WHATSAPP` | `fake` (secret de développement et page `/dev/whatsapp`, interdits en production) ; `meta` (API Cloud de Meta) | |
| `DIGUEE_META_ID_NUMERO` | obligatoire avec `meta` : identifiant du numéro de téléphone (*Phone number ID*), seules ses notifications sont lues | |
| `DIGUEE_META_SECRET_APP` | obligatoire avec `meta` : secret de l'application (*App secret*), vérifie `X-Hub-Signature-256` ; secret | |
| `DIGUEE_META_JETON_WEBHOOK` | obligatoire avec `meta` : jeton de vérification du webhook (*Verify token*), choisi par vous ; secret | |
| `DIGUEE_META_JETON_ACCES` | obligatoire avec `meta` : jeton d'accès permanent d'un utilisateur système (API Graph), envoie le code de secours ; secret, 32 caractères au moins | |
| `DIGUEE_META_MODELE_CODE` | obligatoire avec `meta` : nom du modèle de message d'authentification approuvé (minuscules, chiffres, `_`) | |
| `DIGUEE_META_LANGUE_MODELE` | `fr` : langue du modèle, telle que Meta l'a approuvée (`fr`, `en_US`…) | |
| `DIGUEE_META_VERSION_API` | `v23.0` : version de l'API Graph | |
| `DIGUEE_META_DELAI` | `10s` : délai d'attente d'un envoi de code | |
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
| `DIGUEE_POPULATION_ANSD` | À COMPLÉTER ; chemin du fichier JSON de la population de référence (ANSD), format dans `docs/À faire par le porteur.md` §2.3 | |
| `DIGUEE_JETON_DUREE` | `10m` | `JETON_DUREE` |
| `DIGUEE_CODE_DUREE` | `10m` | `CODE_DUREE` |
| `DIGUEE_CODE_ESSAIS` | `5` | `CODE_ESSAIS` |
| `DIGUEE_VERIF_PAR_EMPREINTE_24H` | `3` | `VERIF_PAR_EMPREINTE_24H` |
| `DIGUEE_IP_PAR_HEURE` | `30` | `IP_PAR_HEURE` |
| `DIGUEE_SMS` | `fake` (codes dans la console et page `/dev/sms`, interdits en production) ; fournisseur à brancher (`internal/sms`) | |
| `DIGUEE_SMS_PAR_EMPREINTE_24H` | `2` (au plus `DIGUEE_VERIF_PAR_EMPREINTE_24H`) : codes SMS par empreinte en 24 h, en plus de la limite partagée | |
| `DIGUEE_SMS_CODE_ESSAIS` | `3` (au plus `DIGUEE_CODE_ESSAIS`) | |
| `DIGUEE_SMS_IP_PAR_HEURE` | `10` (au plus `DIGUEE_IP_PAR_HEURE`) : demandes de SMS par adresse et par heure avant ralentissement | |
| `DIGUEE_SMS_PAR_HEURE` | `100` : plafond absolu de SMS par heure, au-delà le coupe-circuit coupe | |
| `DIGUEE_SMS_COUPE_CIRCUIT` | `3x` : multiple de la moyenne horaire sur 7 jours qui coupe le parcours SMS | `SMS_COUPE_CIRCUIT` |
| `DIGUEE_SMS_COUPE_CIRCUIT_PLANCHER` | `20` : envois dans l'heure avant que la règle du multiple s'applique | |
| `DIGUEE_SMS_REACTIVATION` | `1h` : réactivation automatique après une coupure | |
| `DIGUEE_SMS_SANS_JS_PAR_HEURE` | `10` (au plus `DIGUEE_SMS_PAR_HEURE`) : demandes de SMS sans captcha par heure, pour tout le site | |
| `DIGUEE_SMS_SANS_JS_IP_PAR_HEURE` | `2` (au plus `DIGUEE_SMS_IP_PAR_HEURE`) : demandes de SMS sans captcha par heure et par adresse | |
| `DIGUEE_ALTCHA_DIFFICULTE` | `100000` : `maxnumber` des défis (le navigateur essaie en moyenne la moitié des nombres) ; à ajuster après essai sur téléphone | `ALTCHA_DIFFICULTE` |
| `DIGUEE_PREAVIS_JOURS` | `15` ; 15 au moins (ADR 0008), délai du préavis à l'élu | `PREAVIS_JOURS` |
| `DIGUEE_ADRESSE_REPONSE` | À COMPLÉTER ; adresse où l'élu envoie son droit de réponse, écrite dans le dossier de préavis (ticket #14) ; refusée en production tant qu'elle vaut « À COMPLÉTER » | |
| `DIGUEE_ARCHIVE_TAILLE_MO` | `10` (100 au plus) : taille maximale d'une copie archivée de source, en Mo | |
| `DIGUEE_STOCKAGE` | `fake` (dossier local, interdit en production) ; `ovh` (Object Storage S3 d'OVHcloud) | |
| `DIGUEE_STOCKAGE_DOSSIER` | `<dossier temporaire>/diguee-archives` : dossier du mode `fake` | |
| `DIGUEE_OVH_S3_POINT_ACCES` | obligatoire avec `ovh` : `https://s3.<région>.io.cloud.ovh.net` | |
| `DIGUEE_OVH_S3_REGION`, `DIGUEE_OVH_S3_COMPARTIMENT` | obligatoires avec `ovh` : région de la signature (`gra`, `sbg`) et nom du conteneur | |
| `DIGUEE_OVH_S3_CLE_ACCES`, `DIGUEE_OVH_S3_CLE_SECRETE` | obligatoires avec `ovh` : clé d'accès S3 et clé secrète (secret) | |
| `DIGUEE_OVH_S3_DELAI` | `60s` : délai d'attente d'un appel au stockage | |
| `DIGUEE_URL_PUBLIQUE` | `https://consultation.diguee.sn` : adresse du site, pour archive.org et le texte hebdomadaire pour X | |
| `DIGUEE_ANCRAGE` | `fake` en développement (faux calendriers et fausse archive, interdits en production) ; `reel` en production (OpenTimestamps et archive.org, sans compte) | |
| `DIGUEE_ANCRAGE_CALENDRIERS` | `https://a.pool.opentimestamps.org,https://b.pool.opentimestamps.org,https://finney.calendar.eternitywall.com` | |
| `DIGUEE_ANCRAGE_MIN_CALENDRIERS` | `2` (au plus le nombre de calendriers) : réponses exigées pour garder une preuve | |
| `DIGUEE_ANCRAGE_ARCHIVE` | `https://web.archive.org` | |
| `DIGUEE_ANCRAGE_DECALAGE` | `15m` : délai entre la publication quotidienne et l'ancrage | |
| `DIGUEE_ANCRAGE_DELAI` | `60s` : délai d'attente d'un appel à un calendrier ou à archive.org | |
| `DIGUEE_TEST_POSTGRES_URL` | tests seulement | |
| `DIGUEE_TEST_PEBBLE`, `DIGUEE_TEST_PEBBLE_DOSSIER` | tests seulement : dossier des exécutables `pebble` et `pebble-challtestsrv`, et dossier des sources de Pebble (certificats de test) ; active `TestPebble` | |
| `DIGUEE_TEST_KMS_REEL` | tests seulement : `ovh` ou `gcp` active le test contre le vrai KMS (`go test ./internal/empreinte -run TestKMSReel -v`) | |

À chaque démarrage, quel que soit `DIGUEE_EMPREINTEUR`, le serveur calcule deux fois l'empreinte d'une valeur de contrôle et refuse de démarrer si les résultats diffèrent (spec T5), si deux valeurs différentes donnent la même empreinte ou si le KMS ne répond pas.

Avec `DIGUEE_ENV=production`, le serveur refuse de démarrer si une valeur vaut « À COMPLÉTER », si `DIGUEE_TLS` n'est pas `acme`, si une intégration déclarée dans `internal/integration` est en mode `fake`, si son rôle PostgreSQL a plus que les droits de l'application ou si le schéma n'est pas à jour, ou si le questionnaire contient encore des choix provisoires (`Provisoire` dans `internal/questionnaire/v1.go`), et affiche la liste de ce qui manque.

### Règles

Voir `CLAUDE.md` : textes visibles uniquement dans `internal/i18n/catalogues/fr.json` (un test l'impose), aucun numéro, IP ni corps de requête dans les journaux, pages de moins de 100 Ko sans script (`/observatoire` est paginé par 30 engagements, `/journal` est paginé par 50 entrées, `/resultats` replie ses croisements ; seules exceptions, la page du jeton charge `/statique/verifier.js` et fonctionne aussi sans lui, et les pages `/verifier`, `/verifier/code` et `/verifier/sms` chargent `/statique/altcha.js`, le captcha, chacun autorisé par la seule CSP de sa page), Diguee ne répond jamais dans WhatsApp, aucune entrée du journal public déclenchée par un envoi.
