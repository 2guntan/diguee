# Déploiement de Diguee chez OVHcloud

Ticket #7, spec §11, ADR 0006 et 0007. La consultation tourne sur un VPS OVHcloud en France, réservé à Diguee : un seul exécutable Go (`diguee`) lancé par systemd, qui sert lui-même le HTTPS (Let's Encrypt), et PostgreSQL 16 en local, dont les données sont sur un volume chiffré (LUKS) déverrouillé au démarrage par le KMS d'OVHcloud. Aucun mandataire devant (pas de Cloudflare).

**État** : tout ce dossier est écrit et essayé localement (voir « Ce qui a été essayé »), mais **n'a jamais tourné sur un vrai VPS**. Le porteur suit l'ordre de `docs/À faire par le porteur.md`, section « Mise en production pas à pas », qui renvoie ici.

## Vue d'ensemble

```
Internet ──► pare-feu réseau OVHcloud (IPv4 : 80, 443, SSH depuis les adresses autorisées)
         ──► nftables sur le VPS (même règles, IPv4 et IPv6)
             ├─ :443  diguee  (TLS Let's Encrypt, HSTS)          utilisateur diguee, sans shell
             ├─ :80   diguee  (défi ACME HTTP-01 + redirection)   CAP_NET_BIND_SERVICE seulement
             └─ :22   sshd    (clé seulement, administrateur nommé)
diguee ──socket Unix, « peer »──► PostgreSQL 16 (aucun port TCP), rôle diguee_app
diguee-migrer ──socket Unix──► PostgreSQL, rôle diguee_proprietaire (utilisateur diguee-migrations)
/srv/diguee-donnees  ◄── volume LUKS2 (fichier /var/lib/diguee-volume/donnees.luks)
                         clé de 64 octets chiffrée par le KMS (/etc/diguee/volume.cle.jwe)
```

Ordre au démarrage de la machine : `network-online.target` → `diguee-volume.service` (clé déchiffrée par le KMS, volume ouvert et monté) → `postgresql@16-main.service` → `diguee.service`. Si le volume ne s'ouvre pas, ni PostgreSQL ni Diguee ne démarrent : voir « Secours ».

## Fichiers

| Fichier | Rôle | Installé dans |
|---|---|---|
| `preparer-vps.sh` | Préparation du VPS, en deux étapes : `systeme`, puis `postgresql` | (lancé depuis une copie du dossier) |
| `deployer.sh` | Déploiement en une commande, depuis le poste | (dépôt) |
| `installer-version.sh` | Partie serveur du déploiement : SHA-256, installation, migrations, redémarrage, contrôle, retour arrière | `/usr/local/sbin/diguee-installer` |
| `volume.sh` | Volume chiffré : `creer`, `ouvrir`, `secours`, `fermer`, `etat` | `/usr/local/lib/diguee/volume.sh` |
| `commande.sh` | Lance une commande de `diguee` (`verifier-config`, `creer-membre`, `publier`, `ancrer`) dans le contexte du service, par `systemd-run` | `/usr/local/sbin/diguee-commande` |
| `controle-journaux.sh` | Contrôle T3 : ni IP ni numéro dans les journaux | `/usr/local/sbin/diguee-controle-journaux` |
| `systemd/diguee.service` | Le service, durci | `/etc/systemd/system/` |
| `systemd/diguee-migrer.service` | Migrations avec le rôle propriétaire | `/etc/systemd/system/` |
| `systemd/diguee-volume.service` | Déverrouillage du volume au démarrage | `/etc/systemd/system/` |
| `systemd/postgresql@16-main.service.d/diguee.conf` | PostgreSQL attend le volume | `/etc/systemd/system/postgresql@16-main.service.d/` |
| `systeme/*` | SSH, nftables, journald, mises à jour automatiques, logrotate | `/etc/…` |
| `postgresql/*` | Réglages, `pg_hba.conf`, `pg_ident.conf`, `roles.sql` | `/etc/postgresql/16/main/` |
| `env/*.exemple` | Modèles des fichiers d'environnement (secrets) | `/etc/diguee/*.env`, root 0600 |
| `essais/` | Essai des scripts dans un conteneur Docker Ubuntu 24.04 | (dépôt) |

Fichiers sur le serveur, jamais dans le dépôt :

| Fichier | Contenu | Droits |
|---|---|---|
| `/etc/diguee/diguee.env` | Variables `DIGUEE_*` du service (secrets compris) | root 0600 (lu par systemd seulement) |
| `/etc/diguee/migrations.env` | `DIGUEE_POSTGRES_URL_MIGRATIONS` (rôle propriétaire) | root 0600 |
| `/etc/diguee/volume.env` | Accès au KMS pour la clé du volume | root 0600 |
| `/etc/diguee/kms-client.pem`, `.key` | Certificat d'accès au KMS de l'empreinte (signature) | root 0600, remis au service par `LoadCredential` |
| `/etc/diguee/kms-volume.pem`, `.key` | Certificat d'accès au KMS de la clé du volume (déchiffrement seulement) | root 0600 |
| `/etc/diguee/volume.cle.jwe` | Clé du volume **chiffrée** par le KMS | root 0600 |
| `/etc/diguee/adresses-tolerees` | Adresses admises par le contrôle T3 dans le journal système | root 0644 |

## 1. Pare-feu réseau d'OVHcloud

Espace client OVHcloud → *Bare Metal Cloud* → *Network* → *IP* → l'adresse IPv4 du VPS → *Edge Network Firewall* (nom actuel du « Network Firewall » ; **à vérifier** : il est proposé pour les adresses des VPS). Le pare-feu d'OVHcloud est **sans état** et ne filtre que l'**IPv4** : il faut autoriser les réponses aux connexions sortantes, et l'IPv6 est couvert par nftables sur le VPS.

| Priorité | Action | Protocole | Source | Port destination | Option | Pourquoi |
|---|---|---|---|---|---|---|
| 0 | Autoriser | TCP | adresse de l'administrateur (`/32`) | 22 | | SSH ; une règle par adresse autorisée (priorités 0 à 3) |
| 4 | Autoriser | TCP | toutes | 443 | | le site |
| 5 | Autoriser | TCP | toutes | 80 | | redirection et défi ACME HTTP-01 |
| 6 | Autoriser | TCP | toutes | | *established* | réponses : KMS, Meta, Object Storage, OpenTimestamps, archive.org, Let's Encrypt, apt |
| 7 | Autoriser | UDP | toutes | | port source 53 | réponses DNS |
| 8 | Autoriser | UDP | toutes | | port source 123 | réponses NTP |
| 9 | Autoriser | ICMP | toutes | | | MTU, diagnostic |
| 19 | Refuser | IPv4 | toutes | | | tout le reste |

Vérification depuis un autre réseau (critère du ticket) : `nmap -Pn -p 1-65535 <IPv4 du VPS>` ne montre que 80 et 443 (et 22 depuis une adresse autorisée seulement) ; idem en IPv6 avec `nmap -6`. L'anti-DDoS d'OVHcloud est inclus et toujours actif.

Le pare-feu local (`systeme/nftables.conf.modele`) applique les mêmes règles en IPv4 et en IPv6 (défense en profondeur), ne journalise aucun paquet refusé et laisse tout sortir.

## 2. Préparer le VPS

VPS : Ubuntu 24.04 LTS (PostgreSQL 16 est dans ses dépôts), commandé avec la clé SSH de l'administrateur. L'utilisateur d'OVHcloud est `ubuntu`, avec `sudo`.

```sh
# Sur le poste : copier le dossier
scp -r deploiement ubuntu@<IPv4>:~/
# Sur le VPS
ssh ubuntu@<IPv4>
sudo ~/deploiement/preparer-vps.sh systeme --admin ubuntu --ssh-depuis "203.0.113.10/32,2001:db8:1234::/64"
```

L'étape `systeme` : mises à jour, paquets (`unattended-upgrades`, `nftables`, `cryptsetup`, `postgresql-16`), retrait de `rsyslog` (journald seul, 14 jours), mises à jour de sécurité automatiques avec redémarrage à 04:30 UTC si nécessaire, utilisateurs `diguee` et `diguee-migrations` sans shell ni dossier personnel, `sshd` par clé seulement (`PermitRootLogin no`, `AllowUsers ubuntu` ; refus de continuer si l'administrateur n'a pas de clé), nftables, scripts et unités systemd, retrait du cluster PostgreSQL par défaut (disque non chiffré). Elle peut être relancée.

**Garder la session SSH ouverte** et en ouvrir une seconde pour vérifier l'accès avant de fermer la première. En cas de blocage : console KVM de l'espace client OVHcloud.

`DIGUEE_A_BLANC=1 sudo -E ./preparer-vps.sh systeme …` affiche ce qui serait fait sans rien changer.

## 3. Installer l'exécutable (sans démarrer)

Sur le poste, à la racine du dépôt (Go de `go.mod`, `make`) :

```sh
deploiement/deployer.sh ubuntu@<IPv4> --sans-demarrer
```

Le build est reproductible (`make build` et `make verifier-build` : deux builds, même SHA-256) ; le serveur vérifie le SHA-256 reçu, puis que l'exécutable l'annonce lui-même (`diguee version`), avant de l'installer dans `/opt/diguee/versions/<sha256>/diguee` (lien `/opt/diguee/diguee`).

## 4. Volume chiffré

### Principe (chiffrement d'enveloppe)

- La clé du volume : 64 octets tirés par `diguee cle-volume creer` (`crypto/rand`), envoyés au KMS d'OVHcloud qui les chiffre avec une **clé symétrique** qui ne sort jamais du KMS (`POST /api/{okmsId}/v1/servicekey/{keyId}/encrypt`, contexte `diguee:volume-luks:v1`). Seul le texte chiffré (JWE) est écrit, dans `/etc/diguee/volume.cle.jwe`. La clé en clair n'est jamais écrite sur un disque.
- Au démarrage, `diguee cle-volume dechiffrer` la fait déchiffrer (`…/decrypt`) et l'écrit dans un tube vers `cryptsetup open --key-file=-` (il refuse d'écrire dans un terminal) ; 10 essais à délai croissant si le réseau n'est pas prêt.
- Une **phrase de secours** occupe un second emplacement de clé LUKS : elle ouvre le volume sans le KMS. Elle est tapée par le porteur à la création et gardée **hors ligne** (gestionnaire de mots de passe et papier sous scellé), jamais sur le serveur.
- Ce que cela protège : un disque ou un instantané du VPS copié hors de la machine ne livre rien sans le KMS (ou la phrase). Ce que cela ne protège pas : une machine en marche, dont le volume est ouvert ; et quelqu'un qui aurait à la fois `/etc/diguee` (certificat d'accès) et le disque, tant que l'accès au KMS n'est pas révoqué.

### Dans l'espace client OVHcloud (KMS)

1. Dans le KMS déjà commandé pour l'empreinte (`docs/À faire par le porteur.md` §1.1), créer une **seconde clé de service** : type symétrique **AES 256 bits** (`oct`), opérations **encrypt** et **decrypt**, nom `diguee-volume`. Noter son UUID → `DIGUEE_OVH_KMS_VOLUME_CLE_ID`.
2. Créer une **seconde identité** (utilisateur local IAM `diguee-volume`) avec son **certificat d'accès**, de préférence à partir d'une demande faite sur le serveur (la clé privée n'en sort pas) :
   ```sh
   sudo openssl req -new -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
     -keyout /etc/diguee/kms-volume.key -out ~/kms-volume.csr -subj "/CN=diguee-volume"
   sudo chmod 600 /etc/diguee/kms-volume.key
   ```
   Certificat délivré → `/etc/diguee/kms-volume.pem` (root 0600).
3. Politique IAM de cette identité : sur la seule clé `diguee-volume`, actions **encrypt et decrypt** le temps de créer le volume, puis **decrypt seulement** (retirer encrypt après l'étape suivante). Noms des actions dans l'IAM d'OVHcloud : `okms:apikms:serviceKey/encrypt` et `okms:apikms:serviceKey/decrypt` (**à vérifier** dans l'espace client, comme `…/sign` pour l'empreinte). Cette identité n'a aucun droit sur la clé de l'empreinte, et inversement.
4. `/etc/diguee/volume.env` d'après `env/volume.env.exemple` (root 0600).

Les chemins de l'API et les champs JSON (`plaintext` en base64, `ciphertext` JWE, `context`) sont ceux du SDK officiel `ovh/okms-sdk-go` v0.5.5 (`EncryptRequest`, `DecryptRequest`) ; le code est testé contre un faux KMS (`internal/empreinte/fauxkms`), **jamais contre le vrai** : faire l'essai ci-dessous avant de créer le volume.

### Créer le volume

```sh
# Essai du KMS : crée un texte chiffré jetable, puis le déchiffre (64 octets attendus)
sudo bash -c 'set -a; . /etc/diguee/volume.env; set +a
  /opt/diguee/diguee cle-volume creer -sortie /root/essai.jwe &&
  /opt/diguee/diguee cle-volume dechiffrer -entree /root/essai.jwe | wc -c; rm -f /root/essai.jwe'

# Création : 20 Go dans un fichier (ou --peripherique /dev/sdX pour un disque additionnel)
sudo bash -c 'set -a; . /etc/diguee/volume.env; set +a; /usr/local/lib/diguee/volume.sh creer --taille 20G'
# → taper deux fois la phrase de secours (6 mots au moins), à garder hors ligne

# Sauvegarde de l'en-tête LUKS (hors ligne, avec la phrase de secours)
sudo cryptsetup luksHeaderBackup /var/lib/diguee-volume/donnees.luks --header-backup-file /root/diguee-luks-entete.img
scp ubuntu@<IPv4>:/root/diguee-luks-entete.img .   # puis l'effacer du serveur
sudo systemctl enable diguee-volume.service
```

Le volume est monté sur `/srv/diguee-donnees` (`nodev,nosuid,noexec`) : `postgresql/` (données de PostgreSQL) et `diguee/acme/` (certificats Let's Encrypt et clé du compte ACME, dossier d'état du service).

### Secours : le KMS ne répond pas

Symptôme : après un redémarrage, `systemctl status diguee-volume` en échec, PostgreSQL et Diguee arrêtés.

```sh
sudo /usr/local/lib/diguee/volume.sh secours      # tape la phrase de secours
sudo systemctl start diguee-volume postgresql@16-main diguee
sudo systemctl status diguee                         # le service repart
```

`diguee-volume.service` accepte un volume déjà ouvert : il ne fait que le monter. Si l'en-tête LUKS est abîmé : `cryptsetup luksHeaderRestore … --header-backup-file diguee-luks-entete.img`. Si le certificat du volume est compromis : le révoquer dans l'IAM, en créer un autre ; la clé du volume ne change pas (`cryptsetup luksChangeKey` si l'on veut la changer : nouvelle clé avec `diguee cle-volume creer`, ancienne retirée).

## 5. PostgreSQL

```sh
sudo ~/deploiement/preparer-vps.sh postgresql
```

- Cluster 16 créé sur le volume (`/srv/diguee-donnees/postgresql/16/main`, sommes de contrôle des pages).
- **Aucun port TCP** (`listen_addresses = ''`) : le serveur et les migrations passent par le socket Unix, avec l'authentification **peer** (`pg_hba.conf`, `pg_ident.conf`) : l'utilisateur système fait foi, il n'y a **aucun mot de passe** à gérer. C'est plus strict qu'une écoute sur la boucle locale.
- Journaux vers journald, sans requête, ni paramètre, ni détail d'erreur (`log_error_verbosity = terse` : le détail d'une violation d'unicité citerait une empreinte).
- Rôles (`postgresql/roles.sql`) :

| Rôle | Connexion | Droits | Utilisé par |
|---|---|---|---|
| `diguee_proprietaire` | utilisateur système `diguee-migrations` | propriétaire de la base, du schéma et des tables | `diguee migrer` (`diguee-migrer.service`) ; plus tard la Clôture (#16) |
| `diguee_application` | aucune (groupe) | ceux de `internal/base/droits.go`, accordés par `diguee migrer` | — |
| `diguee_app` | utilisateur système `diguee` | membre de `diguee_application`, rien d'autre | `diguee.service` |

Droits de l'application : `SELECT` et `INSERT` sur toutes les tables (en ajout seul), `UPDATE` de quatre colonnes de `membre` (rôles, mot de passe, secret TOTP, actif) ; jamais `UPDATE` ni `DELETE` ailleurs, ni `TRUNCATE`, ni `ALTER TABLE` (donc pas de `DISABLE TRIGGER`, réservé au propriétaire), ni création d'objet. `participation` ne peut être vidée que par le propriétaire (Clôture). `diguee migrer` retire puis réaccorde exactement ces droits après chaque migration, et refuse de finir si une table n'est pas dans la politique. En production, `diguee` (servir) **n'applique pas les migrations** et **refuse de démarrer** si son rôle est superutilisateur, propriétaire (même par appartenance) du schéma ou d'une table, peut créer dans le schéma, ou a un droit de trop ; il refuse aussi un schéma en retard sur l'exécutable.

Puis compléter `/etc/diguee/diguee.env` (modèle `env/diguee.env.exemple` déjà copié, root 0600) : toutes les valeurs « À COMPLÉTER », d'après `docs/À faire par le porteur.md`. Copier le certificat de l'empreinte (`kms-client.pem`, `.key`, root 0600). Vérifier :

```sh
sudo diguee-commande verifier-config     # « configuration complète » attendu
```

`diguee-commande` lance `diguee` par `systemd-run`, comme le service : utilisateur `diguee`, `/etc/diguee/diguee.env`, certificat du KMS remis par `LoadCredential`. Même chose pour `creer-membre` (premier compte de l'équipe, `docs/À faire par le porteur.md` §3.1), `publier` et `ancrer`.

## 6. Premier démarrage et déploiements

DNS (zone `diguee.sn` chez N0C, PlanetHoster) : enregistrement `A` de `consultation` vers l'IPv4 du VPS (TTL 300 au début) ; `AAAA` vers son IPv6 seulement après avoir vérifié l'IPv6 du VPS (`curl -6 https://…` depuis l'extérieur), car Let's Encrypt la préfère. Recommandé : `consultation CAA 0 issue "letsencrypt.org"` (sur le sous-domaine seulement, la vitrine n'est pas concernée).

Premier essai conseillé avec l'annuaire de **test** de Let's Encrypt (limites de fréquence larges, certificat non reconnu) :

```sh
# /etc/diguee/diguee.env : DIGUEE_ACME_ANNUAIRE=https://acme-staging-v02.api.letsencrypt.org/directory
deploiement/deployer.sh ubuntu@consultation.diguee.sn   # le contrôle final se fait sans vérifier le certificat de test
# puis retirer DIGUEE_ACME_ANNUAIRE, vider le cache, redémarrer :
sudo rm -rf /srv/diguee-donnees/diguee/acme/* && sudo systemctl restart diguee
```

Ensuite, chaque déploiement :

```sh
deploiement/deployer.sh ubuntu@consultation.diguee.sn
```

1. refuse un arbre git modifié (sauf `--force`) ; 2. construit deux fois et compare les SHA-256 ; 3. copie l'exécutable ; 4. sur le serveur : vérifie le SHA-256, installe, lance `diguee-migrer.service` (rôle propriétaire), redémarre `diguee.service` (SIGTERM : le tampon est écrit), attend `https://consultation.diguee.sn/sante` et vérifie que le pied de page affiche le nouveau SHA-256 ; en cas d'échec, revient à la version précédente (les migrations ne sont pas annulées : elles sont écrites pour être compatibles avec la version précédente). Les 5 dernières versions restent dans `/opt/diguee/versions`.

Le serveur inscrit lui-même l'entrée « mise en production » (version et SHA-256) au journal public à son démarrage sur une nouvelle version.

## 7. Vérifications (critères du ticket)

```sh
curl -sI http://consultation.diguee.sn/ | head -3        # 301 vers https://consultation.diguee.sn/
curl -sI https://consultation.diguee.sn/ | grep -i strict # Strict-Transport-Security: max-age=31536000
openssl s_client -connect consultation.diguee.sn:443 -servername consultation.diguee.sn </dev/null 2>/dev/null | openssl x509 -noout -issuer -dates
nmap -Pn -p- <IPv4>                                       # depuis l'extérieur : 80 et 443 seulement
sudo reboot ; puis : systemctl status diguee-volume postgresql@16-main diguee   # repart seul (KMS)
sudo diguee-controle-journaux                             # T3, après les 200 envois de test
```

**T3 sur le serveur** : `diguee-controle-journaux` cherche, par expression régulière, des adresses IPv4 et IPv6 et des numéros (`7[05678]` + 7 chiffres, avec ou sans `+221`, `00221`, `221`, espaces, points ou tirets) dans le journal de `diguee`, `diguee-migrer` et `diguee-volume` (aucune exception), dans tout le journal système et dans `/var/log/postgresql` ; dans le journal système, les seules adresses admises sont celles de la machine (ses adresses, sa passerelle, ses résolveurs), la boucle locale et `/etc/diguee/adresses-tolerees` (adresses SSH des administrateurs, à compléter par celles des serveurs NTP si `systemd-timesyncd` les cite). Il n'affiche que la moitié de chaque valeur trouvée. `--strict` ajoute toute suite isolée de 9 chiffres ou plus ; `--depuis "2 hours ago"` restreint la période. Faire les 200 envois de test (à la main ou par script), puis lancer le contrôle : résultat attendu « T3 : aucune adresse IP ni aucun numéro non admis ».

Restent hors de journald, sans donnée de participant : `/var/log/wtmp`, `btmp`, `lastlog` (connexions SSH des administrateurs, binaires), `/var/log/apt`, `/var/log/unattended-upgrades`, `/var/log/cloud-init*.log` (adresse du VPS).

## 8. Journaux

- `diguee` écrit sur sa sortie d'erreur, en JSON, reçue par journald : ni IP, ni numéro, ni corps de requête, ni paramètres d'URL ; en plus, un masqueur (`internal/masque`) remplace par `[masqué]` toute IP ou tout numéro qui apparaîtrait dans un message (une erreur réseau d'un service extérieur, par exemple). Les erreurs de `net/http` (qui citent l'adresse du client) et de poignée TLS sont jetées.
- journald : fichiers d'un jour, supprimés au plus tard 14 jours après leur écriture (`MaxFileSec=1day`, `MaxRetentionSec=13day`) ; `rsyslog` retiré ; PostgreSQL envoie ses journaux à journald ; `/var/log/postgresql` tourne chaque jour, 14 jours au plus.
- Lire : `journalctl -u diguee -f`, `journalctl -u diguee --since today -o cat | jq .`

## 9. Ce qui a été essayé, et ce qui ne l'a pas été

Essayé :

- `go test ./...` : TLS (`internal/https` : redirection, HSTS, liste blanche, configuration ; **obtention réelle d'un certificat** auprès de Pebble, le serveur ACME de test de Let's Encrypt, par TLS-ALPN-01 et par HTTP-01, puis relecture du cache), mode ACME de l'exécutable (`cmd/diguee`), rôles PostgreSQL (`internal/base/droits_test.go`, et le parcours T1 du paquet `web` et l'espace équipe avec le rôle de l'application), 200 envois sur l'exécutable lancé avec le rôle de l'application puis contrôle T3 de son journal par `controle-journaux.sh`, clé du volume contre un faux KMS (mTLS, JWE, contexte, texte altéré, KMS indisponible).
- Pebble localement : `go install github.com/letsencrypt/pebble/v2/cmd/pebble@v2.8.0 github.com/letsencrypt/pebble/v2/cmd/pebble-challtestsrv@v2.8.0`, puis `DIGUEE_TEST_PEBBLE=$(go env GOPATH)/bin DIGUEE_TEST_PEBBLE_DOSSIER=$(go env GOMODCACHE)/github.com/letsencrypt/pebble/v2@v2.8.0 go test ./internal/https -run Pebble -v` (la CI le fait).
- `shellcheck` sur tous les scripts (CI).
- `essais/conteneur.sh` (Docker, Ubuntu 24.04, sans systemd) : `preparer-vps.sh systeme` complet (paquets, utilisateurs, `sshd -T`, nftables chargé, journald, rsyslog retiré), `systemd-analyze verify` des unités, `diguee-installer` (SHA-256 juste et altéré), `volume.sh creer` jusqu'au formatage LUKS2 et à la phrase de secours (le bac à sable n'avait pas de device-mapper : ouverture et montage remplacés par un tmpfs), `preparer-vps.sh postgresql` (cluster sur le volume, aucun port TCP), **vrai `diguee migrer` en production** avec le rôle propriétaire par « peer », puis droits du rôle de l'application vérifiés en SQL.

Pas essayé (à faire par le porteur, dans l'ordre de la section « Mise en production pas à pas ») : un vrai VPS et systemd (ordre de démarrage, `LoadCredential`, durcissement de l'unité en marche : `systemd-analyze security diguee`), le pare-feu réseau d'OVHcloud, le vrai KMS (encrypt/decrypt, noms des actions IAM), `cryptsetup open` et le montage au démarrage, Let's Encrypt en vrai, le DNS, le redémarrage automatique des mises à jour.

## 10. Pour les tickets suivants

- **#15 (sauvegardes)** : `pg_dump` se lance par le socket Unix ; prévoir un utilisateur système et un rôle dédiés (par exemple `diguee-sauvegarde` → `diguee_sauvegarde`, membre de `pg_read_all_data`, ligne à ajouter dans `pg_hba.conf` et `pg_ident.conf`), une unité `diguee-sauvegarde.service` et son minuteur sur le modèle de `diguee-migrer.service` (`After=`/`Requires=` du volume et de PostgreSQL), la clé publique `age` dans `/etc/diguee`, et l'envoi vers Object Storage. À sauvegarder aussi hors ligne, une fois : l'en-tête LUKS et la phrase de secours (§4). Le volume chiffré ne remplace pas une sauvegarde chiffrée : les sauvegardes quittent le VPS chiffrées par `age`.
- **#16 (clôture)** : la suppression de `participation` (puis la destruction de la clé de l'empreinte dans le KMS) demande le rôle propriétaire : une commande `diguee cloturer` lancée par une unité `diguee-cloturer.service` sous l'utilisateur `diguee-migrations`, avec `DIGUEE_POSTGRES_URL_MIGRATIONS` (`migrations.env`), comme `diguee-migrer.service`. Le rôle de l'application ne peut ni supprimer ni vider aucune table. Penser aussi à effacer les copies de `participation` hors de la base (sauvegardes du ticket #15 et instantanés du VPS éventuels).
