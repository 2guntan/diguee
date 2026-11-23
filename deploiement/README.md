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
diguee-sauvegarde (02:30 UTC) ──socket Unix──► pg_dump, rôle diguee_sauvegarde (lecture seule)
                  └─ | age (clé publique) ──HTTPS──► Object Storage, compartiment diguee-sauvegardes (30 jours)
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
| `volume.sh` | Volume chiffré : `creer`, `ouvrir`, `secours`, `fermer`, `etat`, `renouveler` (Clôture, §12) | `/usr/local/lib/diguee/volume.sh` |
| `commande.sh` | Lance une commande de `diguee` (`verifier-config`, `creer-membre`, `publier`, `ancrer`) dans le contexte du service, par `systemd-run` | `/usr/local/sbin/diguee-commande` |
| `controle-journaux.sh` | Contrôle T3 : ni IP ni numéro dans les journaux | `/usr/local/sbin/diguee-controle-journaux` |
| `restaurer-sauvegarde.sh` | Restauration d'une sauvegarde et contrôle (ticket #15), **hors du serveur** | (ordinateur du porteur) |
| `systemd/diguee.service` | Le service, durci | `/etc/systemd/system/` |
| `systemd/diguee-migrer.service` | Migrations avec le rôle propriétaire | `/etc/systemd/system/` |
| `systemd/diguee-volume.service` | Déverrouillage du volume au démarrage | `/etc/systemd/system/` |
| `systemd/diguee-sauvegarde.service`, `.timer` | Sauvegarde chiffrée chaque nuit (ticket #15) | `/etc/systemd/system/` |
| `systemd/diguee-cloturer.service` | Procédure de destruction de la Clôture (ticket #16, §12), jamais activée, lancée à la main | `/etc/systemd/system/` |
| `systemd/diguee-alerte@.service` | Alerte au journal quand une unité échoue (`OnFailure=`) | `/etc/systemd/system/` |
| `systemd/postgresql@16-main.service.d/diguee.conf` | PostgreSQL attend le volume | `/etc/systemd/system/postgresql@16-main.service.d/` |
| `systeme/*` | SSH, nftables, journald, mises à jour automatiques, logrotate | `/etc/…` |
| `postgresql/*` | Réglages, `pg_hba.conf`, `pg_ident.conf`, `roles.sql`, `role-sauvegarde.sql` | `/etc/postgresql/16/main/` |
| `env/*.exemple` | Modèles des fichiers d'environnement (secrets) | `/etc/diguee/*.env`, root 0600 |
| `essais/` | Essai des scripts dans un conteneur Docker Ubuntu 24.04 | (dépôt) |

Fichiers sur le serveur, jamais dans le dépôt :

| Fichier | Contenu | Droits |
|---|---|---|
| `/etc/diguee/diguee.env` | Variables `DIGUEE_*` du service (secrets compris) | root 0600 (lu par systemd seulement) |
| `/etc/diguee/migrations.env` | `DIGUEE_POSTGRES_URL_MIGRATIONS` (rôle propriétaire) | root 0600 |
| `/etc/diguee/sauvegarde.env` | Clé d'accès S3 du compartiment des sauvegardes (`DIGUEE_SAUVEGARDE_S3_*`) | root 0600 |
| `/etc/diguee/sauvegarde.age.pub` | Clé **publique** `age` des sauvegardes (la clé privée n'est jamais sur le serveur) | root 0644, remise à l'unité par `LoadCredential` |
| `/var/lib/diguee-sauvegarde/etat.json` | État de la dernière sauvegarde, lu par l'espace équipe (aucun secret) | `diguee-sauvegarde`, 0644 |
| `/etc/diguee/volume.env` | Accès au KMS pour la clé du volume | root 0600 |
| `/etc/diguee/kms-client.pem`, `.key` | Certificat d'accès au KMS de l'empreinte (signature) | root 0600, remis au service par `LoadCredential` |
| `/etc/diguee/kms-volume.pem`, `.key` | Certificat d'accès au KMS de la clé du volume (déchiffrement seulement) | root 0600 |
| `/etc/diguee/volume.cle.jwe` | Clé du volume **chiffrée** par le KMS | root 0600 |
| `/etc/diguee/kms-cloture.pem`, `.key` | À la Clôture seulement : certificat de l'identité de clôture du KMS (désactiver et supprimer la clé de l'empreinte), §12 | root 0600, remis à `diguee-cloturer` par `LoadCredential` ; à supprimer après |
| `/etc/diguee/cloture.env` | Facultatif : réglages de `diguee cloturer` (aucun n'est nécessaire) | root 0600 |
| `/etc/diguee/volume-renouvele.json` | Attestation du renouvellement des clés du volume (Clôture), écrite par `volume.sh renouveler` (aucun secret) | root 0644 |
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
| `diguee_sauvegarde` | utilisateur système `diguee-sauvegarde` | membre de `pg_read_all_data` (lecture de toutes les tables), aucune écriture (`role-sauvegarde.sql`) | `diguee-sauvegarde.service` (`pg_dump`) |

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
- `essais/conteneur.sh` (Docker, Ubuntu 24.04, sans systemd) : `preparer-vps.sh systeme` complet (paquets, utilisateurs, `sshd -T`, nftables chargé, journald, rsyslog retiré), `systemd-analyze verify` des unités, `diguee-installer` (SHA-256 juste et altéré), `volume.sh creer` jusqu'au formatage LUKS2 et à la phrase de secours (le bac à sable n'avait pas de device-mapper : ouverture et montage remplacés par un tmpfs), `preparer-vps.sh postgresql` (cluster sur le volume, aucun port TCP), **vrai `diguee migrer` en production** avec le rôle propriétaire par « peer », puis droits du rôle de l'application vérifiés en SQL ; **sauvegarde (ticket #15)** : `age` installé, rôle `diguee_sauvegarde` par « peer » (lecture de tout, écriture refusée, inaccessible aux autres utilisateurs), vrai `diguee sauvegarde faire` (pg_dump | age, objet chiffré, état 0644 lisible par `diguee`), échec signalé (code 1 et état), puis restauration par `restaurer-sauvegarde.sh` (déchiffrement, `pg_restore`, lignes et journal contrôlés) et « restauration réelle » dans une base `diguee` recréée (propriétaires d'origine gardés, `diguee migrer` réaccorde les droits, données lues par le rôle de l'application) ; `systemd-analyze verify` des nouvelles unités et du minuteur.

- Sauvegardes (ticket #15) : `go test ./internal/sauvegarde ./internal/stockage ./cmd/diguee` (faux S3 qui vérifie la signature SigV4, paire de clés `age` tirée pour le test) ; `restaurer-sauvegarde.sh --docker` essayé à la main (conteneur `postgres:16` en mémoire, supprimé à la fin).
- Clôture (ticket #16) : `go test ./internal/cloture ./internal/web ./internal/empreinte ./internal/base ./cmd/diguee` (simulation de bout en bout sur une vraie base PostgreSQL 16, vraies sauvegardes `pg_dump` + `age` dans un dossier, faux KMS qui imite `deactivate`, `DELETE` et `GET`) ; `volume.sh renouveler` seulement à blanc (`DIGUEE_A_BLANC=1`) et par `shellcheck`. Pas essayé : la destruction d'une clé dans le vrai KMS (codes de réponse, noms des actions IAM), `cryptsetup reencrypt` et l'effacement du WAL sur un vrai volume, `diguee-cloturer.service` sous un vrai systemd (§12).

Pas essayé (à faire par le porteur, dans l'ordre de la section « Mise en production pas à pas ») : un vrai VPS et systemd (ordre de démarrage, `LoadCredential`, durcissement de l'unité en marche : `systemd-analyze security diguee`), le pare-feu réseau d'OVHcloud, le vrai KMS (encrypt/decrypt, noms des actions IAM), `cryptsetup open` et le montage au démarrage, Let's Encrypt en vrai, le DNS, le redémarrage automatique des mises à jour ; pour les sauvegardes : le vrai Object Storage (`ListObjectsV2`, `DeleteObject`, `GetBucketVersioning`, politiques des deux utilisateurs S3), le minuteur et `OnFailure=` sous un vrai systemd.

## 10. Sauvegardes (ticket #15)

Spec §11 et §12, ADR 0002 et 0006. Chaque nuit à 02:30 UTC, `diguee-sauvegarde.timer` lance `diguee-sauvegarde.service` : `diguee sauvegarde faire`, sous l'utilisateur système `diguee-sauvegarde` (rôle `diguee_sauvegarde`, lecture seule).

### Principe et choix

- **Chiffrement en flux, avant de quitter le serveur** : `pg_dump --format=custom` écrit dans un tube lu par `age --encrypt --recipient age1…` (clé publique X25519 du porteur). Le dump en clair n'existe que dans ce tube, jamais sur un disque ; seul le texte chiffré est écrit, dans le `/tmp` privé de l'unité, le temps d'en calculer le SHA-256 (signé avec l'envoi), puis effacé. La commande vérifie l'en-tête `age-encryption.org/v1` / `X25519` avant d'envoyer.
- **`age` en programme, pas en bibliothèque Go** : c'est l'implémentation de référence, du paquet `age` d'Ubuntu 24.04 (mis à jour par les mises à jour de sécurité automatiques), la même que celle du porteur (`age-keygen`, déchiffrement) ; aucune dépendance Go ajoutée (`CLAUDE.md`), aucune cryptographie écrite dans le dépôt. Seules les clés X25519 sont acceptées (ni greffon, ni clé SSH). Le chiffrement `age` est authentifié : une sauvegarde altérée ne se déchiffre pas.
- **Compartiment dédié** `diguee-sauvegardes`, distinct de `diguee-archives` : les archives doivent être en *Object Lock* et ne jamais être supprimées (ADR 0008), les sauvegardes doivent l'être au bout de 30 jours et toutes à la Clôture (ADR 0002). Un préfixe dans le même compartiment rendrait ces deux règles incompatibles, et partagerait la clé d'accès du serveur. **Ni versionnage, ni Object Lock** sur ce compartiment : un objet supprimé y resterait lisible dans ses versions (`supprimer-toutes` le vérifie et échoue si le versionnage est activé ou suspendu).
- **Deux utilisateurs S3** : sur le serveur, `diguee-sauvegarde-ecriture` : déposer, lister, supprimer, lire l'état du versionnage, **pas lire** les objets (inutile : il ne peut pas les déchiffrer, et un intrus n'en ferait rien de plus). Chez le porteur, hors du serveur : `diguee-sauvegarde-lecture` (lire, lister), pour la restauration. (Une clé en écriture seule est impossible : la commande supprime elle-même les sauvegardes de plus de 30 jours.)
- **Nommage** : `sauvegardes/diguee-AAAA-MM-JJTHHMMSSZ.pgdump.age` (UTC). **Conservation** : après chaque envoi réussi (relu dans la liste, à la bonne taille), la commande supprime (`DeleteObject`) les sauvegardes datées d'avant minuit UTC il y a 30 jours : il en reste 31 au plus, la plus ancienne a 30 jours. Le délai n'est pas réglable (Clôture). La suppression se fait par la commande et non par une règle de cycle de vie du compartiment : elle est testée et ne dépend pas de ce que propose OVHcloud ; une règle d'expiration à 32 jours peut s'ajouter en filet si OVHcloud la propose (**à vérifier**), sans rien changer d'autre. Les autres objets rangés sous `sauvegardes/` ne sont pas touchés par la purge des 30 jours (mais le sont par `supprimer-toutes`).
- **Alerte** : en cas d'échec (pg_dump, age, envoi, contrôle, purge, configuration), la commande sort en erreur ; `OnFailure=diguee-alerte@%n.service` écrit au journal une ligne de priorité *alert* (`journalctl -t diguee-alerte`, `journalctl -p alert`). Surtout, l'état (`/var/lib/diguee-sauvegarde/etat.json`, sans secret) est lu par le serveur : l'**accueil de l'espace équipe** montre aux administrateurs la dernière sauvegarde réussie, et une alerte si la dernière a échoué, si aucune n'a réussi depuis 27 heures (minuteur arrêté, commande tuée) ou si l'état est absent ou illisible.

### Dans l'espace client OVHcloud

1. *Public Cloud* → *Object Storage* → créer un conteneur **S3**, classe *Standard*, région **en France** (la même que `diguee-archives`), nom `diguee-sauvegardes`, **privé**, **sans versionnage ni verrouillage des objets**.
2. Créer l'utilisateur S3 `diguee-sauvegarde-ecriture`, et lui donner une politique limitée à ce conteneur (import d'une politique JSON ; noms des actions **à vérifier** dans l'espace client) :
   ```json
   {"Statement": [
     {"Sid": "Objets", "Effect": "Allow", "Action": ["s3:PutObject", "s3:DeleteObject"],
      "Resource": ["arn:aws:s3:::diguee-sauvegardes/sauvegardes/*"]},
     {"Sid": "Compartiment", "Effect": "Allow", "Action": ["s3:ListBucket", "s3:GetBucketVersioning"],
      "Resource": ["arn:aws:s3:::diguee-sauvegardes"]}]}
   ```
   Noter sa clé d'accès et sa clé secrète → `/etc/diguee/sauvegarde.env`.
3. Créer l'utilisateur `diguee-sauvegarde-lecture` (actions `s3:GetObject` et `s3:ListBucket` sur ce conteneur) ; ses clés restent **chez le porteur** (gestionnaire de mots de passe), jamais sur le serveur.

### Sur le poste du porteur : la paire de clés `age`

```sh
age-keygen -o diguee-sauvegardes.age.txt   # clé privée : ne la donnez à personne, ne la copiez jamais sur le serveur
age-keygen -y diguee-sauvegardes.age.txt   # clé publique : age1… (une ligne), seule à aller sur le serveur
```

Garder la clé privée **hors ligne** : gestionnaire de mots de passe et copie de secours (papier sous scellé, clé USB chiffrée). Sans elle, aucune sauvegarde ne peut être relue ; si elle fuit, changer de paire (nouvelle clé publique sur le serveur) : les sauvegardes déjà faites restent lisibles avec l'ancienne jusqu'à leur suppression, 30 jours plus tard.

### Sur le serveur

```sh
# Clé publique (une ligne age1…), et clé d'accès S3 d'écriture
echo 'age1…' | sudo tee /etc/diguee/sauvegarde.age.pub && sudo chmod 644 /etc/diguee/sauvegarde.age.pub
sudo nano /etc/diguee/sauvegarde.env            # DIGUEE_SAUVEGARDE_S3_CLE_ACCES et _CLE_SECRETE (root 0600)
# Première sauvegarde, à la main, puis le minuteur
sudo systemctl start diguee-sauvegarde.service && journalctl -u diguee-sauvegarde -n 20 -o cat
systemctl list-timers diguee-sauvegarde.timer   # prochaine exécution (activé par preparer-vps.sh postgresql)
# Lister les sauvegardes (avec la clé du serveur)
sudo systemd-run --quiet --pty --wait --collect -p User=diguee-sauvegarde -p EnvironmentFile=/etc/diguee/sauvegarde.env \
  -E DIGUEE_ENV=production /opt/diguee/diguee sauvegarde lister
```

L'unité : `After=`/`Requires=` du volume et de PostgreSQL, base `postgres://diguee_sauvegarde@/diguee?host=/var/run/postgresql` (« peer »), `/usr/lib/postgresql/16/bin/pg_dump`, `/usr/bin/age`, clé publique remise par `LoadCredential`, `PrivateTmp`, `ProtectSystem=strict`, réseau limité à IP et socket Unix, `Nice=10`, entrées-sorties au repos. L'utilisateur `diguee-sauvegarde` ne lit ni `diguee.env` ni les certificats du KMS, et ne peut rien écrire dans la base.

### Restauration (test mensuel, sur une machine jetable)

Le test se fait **chaque mois** (spec §11, ADR 0006), par le porteur (seul à avoir la clé privée), sur **son ordinateur avec Docker** : la base restaurée vit dans un conteneur `postgres:16` dont les données sont en mémoire (`tmpfs`), supprimé à la fin. La clé privée ne quitte pas l'ordinateur.

```sh
# Dans le dépôt, à jour : make build (ou --diguee CHEMIN)
export DIGUEE_SAUVEGARDE_STOCKAGE=ovh DIGUEE_SAUVEGARDE_S3_POINT_ACCES=https://s3.gra.io.cloud.ovh.net \
  DIGUEE_SAUVEGARDE_S3_REGION=gra DIGUEE_SAUVEGARDE_S3_COMPARTIMENT=diguee-sauvegardes \
  DIGUEE_SAUVEGARDE_S3_CLE_ACCES=<clé de LECTURE> DIGUEE_SAUVEGARDE_S3_CLE_SECRETE=<secret de lecture>
deploiement/restaurer-sauvegarde.sh --identite ~/chemin/diguee-sauvegardes.age.txt --docker \
  --comparer-avec https://consultation.diguee.sn/journal.json
```

Le script : récupère la sauvegarde la plus récente (`diguee sauvegarde recuperer`, ou `--fichier` pour une sauvegarde téléchargée à la main depuis l'espace client) ; démarre la base jetable ; déchiffre avec `age --decrypt` **directement dans** `pg_restore --no-owner --no-acl --exit-on-error --single-transaction` ; compte les lignes de chaque table et vérifie la chaîne du journal public (`diguee sauvegarde controler`, qui en écrit l'export), revérifie l'export avec `diguee verifier-journal`, puis vérifie que le journal restauré est **le début exact** du journal publié en ligne (`-precedent`). Il affiche `RESTAURATION RÉUSSIE` ou `RESTAURATION EN ÉCHEC : …`. Sans Docker : `--base postgres://…` vers une base vide (`pg_restore` et `psql` 16 sur la machine).

Résultat attendu : la sauvegarde date de la nuit, les nombres de lignes sont plausibles (participations, réponses, entrées du journal), le journal est valide et identique au début du journal en ligne. Noter la date du test et le résultat (par exemple dans le gestionnaire de tâches de l'équipe). En cas d'échec : refaire avec la sauvegarde précédente (`diguee sauvegarde lister`, puis `recuperer -cle …` et `--fichier`), et prévenir l'administrateur.

### Restauration réelle (perte du serveur)

Essayée dans le conteneur du ticket #7 (`essais/`, sans SSH ni vrai VPS) ; le test mensuel, lui, restaure dans une base jetable, sans les rôles d'origine. Nouveau VPS préparé jusqu'à `preparer-vps.sh postgresql` (D §2 à §5 : les rôles existent, la base `diguee` est vide), service arrêté. La sauvegarde est déchiffrée **sur le poste du porteur** et le flux en clair passe par SSH jusqu'à `pg_restore`, qui écrit sur le volume chiffré :

```sh
age --decrypt --identity diguee-sauvegardes.age.txt diguee-AAAA-MM-JJT….pgdump.age |
  ssh ubuntu@<IPv4> 'sudo -u postgres pg_restore --dbname=diguee --exit-on-error --single-transaction'
ssh ubuntu@<IPv4> 'sudo systemctl start diguee-migrer && sudo systemctl start diguee'   # droits réaccordés, schéma à jour
```

Les propriétaires d'origine (`diguee_proprietaire`) sont gardés ; `diguee-migrer` réaccorde les droits exacts du rôle de l'application. Les réponses reçues depuis la dernière sauvegarde sont perdues (au plus un jour) ; le tampon en mémoire aussi.

### Clôture (#16)

Toutes les sauvegardes contiennent la table `participation`, donc les empreintes (ADR 0002) : à la Clôture, après la suppression de la table, `diguee cloturer` supprime tout ce qui est sous `sauvegardes/` (`sauvegarde.SupprimerToutes`, qui vérifie que la liste est vide et que le compartiment n'est pas versionné), puis fait elle-même une nouvelle sauvegarde sans la table : voir §12. `diguee sauvegarde supprimer-toutes -confirmer` reste disponible à la main. Les copies que la procédure ne voit pas restent à traiter à part : sauvegardes téléchargées pour un test de restauration (à effacer après chaque test), instantanés du VPS.

## 11. Pour les tickets suivants

- **#15 (sauvegardes)** : fait, voir §10. À sauvegarder aussi hors ligne, une fois : l'en-tête LUKS et la phrase de secours (§4). Le volume chiffré ne remplace pas une sauvegarde chiffrée : les sauvegardes quittent le VPS chiffrées par `age`.
- **#16 (clôture)** : fait, voir §12.

## 12. Clôture (ticket #16)

Spec §12, ADR 0002. À `DIGUEE_CLOTURE` (minuit UTC, ouverture + 6 mois par défaut), la consultation ferme ; ensuite, une procédure détruit les empreintes, toutes les sauvegardes qui les contiennent et la clé qui les calculait, renouvelle les clés du volume chiffré, et publie un procès-verbal. Chaque étape est inscrite au journal public (entrée `cloture`, champ `etape`), le procès-verbal aussi (entrée `proces_verbal`). Les réponses et les publications sont conservées.

### Ce qui se fait tout seul (serveur)

À la date de clôture, sans rien lancer : les pages du parcours affichent « Consultation close », aucun jeton ni code n'est plus délivré, le webhook WhatsApp ignore les messages (200 pour Meta) ; le tampon est écrit une dernière fois puis fermé. La **dernière publication quotidienne** est produite à l'heure habituelle (06:00 UTC) du **lendemain** du dernier lot (le plus souvent : le lendemain de la date de clôture), donc sur toutes les réponses ; aussitôt après, l'entrée `cloture` de l'étape `fermeture` est inscrite au journal. Le serveur doit tourner (s'il est arrêté, il la fait à son redémarrage, avec la publication du jour). Vérifier :

```sh
curl -s https://consultation.diguee.sn/journal.json | grep -o '"type":"cloture"[^}]*' | head
```

Pourquoi pas tout de suite à minuit : une publication par jour, toujours à la même heure, et aucune entrée du journal écrite à un instant lié à un envoi (choix prudent, au prix d'environ 30 heures).

### Préparer la destruction (porteur, espace client OVHcloud)

1. **Identité de clôture du KMS** : un nouveau certificat d'accès (identité IAM `diguee-cloture`), avec une politique qui n'autorise, sur la seule clé `diguee-empreinte`, que la lecture, la désactivation et la suppression (noms d'actions **à vérifier** dans l'IAM d'OVHcloud, par exemple `okms:apikms:serviceKey/get`, `…/deactivate`, `…/delete`). Le certificat du serveur ne peut que signer : il ne peut pas détruire la clé, et ne doit pas pouvoir le faire. Comme pour le ticket #5, de préférence à partir d'une demande créée sur le serveur :
   ```sh
   sudo openssl req -new -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
     -keyout /etc/diguee/kms-cloture.key -out /root/kms-cloture.csr -subj "/CN=diguee-cloture"
   sudo chmod 600 /etc/diguee/kms-cloture.key   # certificat signé → /etc/diguee/kms-cloture.pem (0600)
   ```
2. **Identité du volume** : lui redonner, le temps du renouvellement, le droit de **chiffrer** avec la clé `diguee-volume` (`volume.sh renouveler` fait chiffrer une nouvelle clé du volume), puis le lui retirer.
3. Sur le serveur, une fois (installations antérieures à ce ticket ; `roles.sql` le fait désormais) : `sudo -u postgres psql -c "GRANT pg_checkpoint TO diguee_proprietaire"` (le `CHECKPOINT` de la procédure).
4. Arrêter la sauvegarde de nuit pendant la procédure : `sudo systemctl stop diguee-sauvegarde.timer` (la procédure supprime de toute façon tout objet apparu avant sa propre sauvegarde, et le procès-verbal exige que la plus ancienne sauvegarde restante soit la sienne).

### Lancer la destruction

```sh
sudo systemctl start diguee-cloturer ; journalctl -u diguee-cloturer -o cat -n 100
```

`diguee cloturer` (utilisateur `diguee-migrations`, rôle propriétaire ; `diguee.env`, `migrations.env`, `sauvegarde.env`) fait, dans l'ordre, en inscrivant chaque étape au journal :

| Étape | Ce qui est fait | Inscrit au journal |
|---|---|---|
| `verification` | consultation close ; fermeture inscrite par le serveur ; dernière publication présente, identique, et postérieure à toutes les réponses | jour et SHA-256 de la dernière publication, nombre de lignes de `reponse` et de `publication` |
| (contrôle préalable) | le KMS accepte l'identité de clôture, le compartiment répond, `pg_dump`, `age` et la clé publique sont là : rien n'est supprimé sinon | — |
| `suppression_participations` | `LOCK`, `TRUNCATE participation`, `DROP TABLE participation`, **dans la même transaction** que l'entrée du journal | nombre de participations supprimées, SHA-256 des ordres |
| `liberation_espace` | `VACUUM (FULL, ANALYZE)` de chaque table, `CHECKPOINT` | SHA-256 des ordres |
| `suppression_sauvegardes` | toutes les sauvegardes (`sauvegardes/…`), liste vide vérifiée, compartiment non versionné | nombre, SHA-256 de la liste des clés |
| `nouvelle_sauvegarde` | supprime ce qui serait apparu entre-temps, puis sauvegarde (`pg_dump` \| `age`) sans la table ; elle doit être seule dans le compartiment | clé, taille, SHA-256 du fichier chiffré |
| `destruction_cle_empreinte` | KMS : `GET` (état), `POST …/deactivate` (`cessation_of_operation`), `DELETE`, puis `GET` : introuvable | état final, SHA-256 des requêtes |
| `renouvellement_volume` | **étape manuelle**, ci-dessous : la procédure s'arrête `EN ATTENTE` (sortie 0) | attestation : SHA-256 de la nouvelle clé chiffrée et de l'en-tête LUKS |
| procès-verbal | contrôle final (plus de `participation`, réponses et dernière publication inchangées, chaîne du journal valide) | entrée `proces_verbal` : date, entrée et empreinte de chaque étape, nombres, tables et leurs lignes |

**Choix : la nouvelle sauvegarde est faite par la procédure elle-même**, et non en lançant `diguee-sauvegarde.service` : un utilisateur sans privilège ne peut pas démarrer une unité, et les étapes doivent s'enchaîner sans trou. Même code que la sauvegarde de nuit (`sauvegarde.Faire`), avec la clé d'écriture S3 de `sauvegarde.env` (lue par systemd), le rôle propriétaire (lecture de toute la base) et la clé publique `age` (`LoadCredential`). Le fichier d'état de l'espace équipe n'est pas mis à jour (il appartient à `diguee-sauvegarde`) : la sauvegarde de nuit suivante le fera.

**Échec** : la commande s'arrête à l'étape en échec, sort avec le code 1, écrit `ALERTE clôture` au journal et déclenche `diguee-alerte@` ; aucune étape suivante n'est faite. Corriger, puis relancer `systemctl start diguee-cloturer` : la commande relit le journal public et **reprend à la première étape qui manque** (une étape n'est inscrite que si la précédente est la dernière inscrite, et deux procédures ne tournent jamais ensemble). Si une étape a été faite mais que son inscription a échoué (panne au mauvais moment), elle est refaite : toutes sont sans effet la deuxième fois (table déjà absente, aucune sauvegarde à supprimer, clé déjà introuvable) ; le nombre inscrit peut alors être 0 ou -1 (table déjà absente), ce qui se lit dans le journal.

### Renouveler les clés du volume (root, étape manuelle)

Quand `journalctl -u diguee-cloturer` affiche `EN ATTENTE : … renouvellement des clés du volume chiffré` :

```sh
sudo /usr/local/lib/diguee/volume.sh renouveler      # DIGUEE_A_BLANC=1 pour voir les commandes sans rien faire
sudo systemctl start diguee-cloturer                 # reprend : renouvellement inscrit, puis procès-verbal
sudo systemctl start diguee-sauvegarde.timer         # sauvegardes de nuit (sans participation)
```

`volume.sh renouveler` : (1) WAL de PostgreSQL : `wal_recycle = off`, plusieurs `CHECKPOINT` et `pg_switch_wal()`, pour que les segments écrits avant la suppression de la table soient supprimés et non renommés pour resservir ; (2) arrêt de `diguee` et de PostgreSQL, et **réécriture de l'espace libre** du système de fichiers (fichier de zéros jusqu'au disque plein, puis supprimé) : les blocs de l'ancienne table et des anciens segments ; (3) **rechiffrement du volume avec une nouvelle clé maîtresse** (`cryptsetup reencrypt`, en ligne, en ne gardant que l'emplacement de la clé du KMS) : une ancienne copie de l'en-tête LUKS, avec l'ancienne clé ou l'ancienne phrase de secours, ne déchiffre plus rien ; `cryptsetup luksChangeKey` seul ne suffirait pas, car il ne change pas la clé maîtresse ; (4) nouvelle clé du volume chiffrée par le KMS (`diguee cle-volume creer`), ajoutée, essayée, l'ancienne retirée ; (5) **nouvelle phrase de secours** au clavier ; (6) redémarrage, nouvel en-tête dans `/root/diguee-luks-entete-….img`, attestation `/etc/diguee/volume-renouvele.json`. Ensuite, le porteur copie le nouvel en-tête et la nouvelle phrase hors ligne, et **détruit l'ancienne copie de l'en-tête et l'ancienne phrase**.

**À vérifier sur un vrai VPS** (rien de cette étape n'a pu être essayé ici, faute de device-mapper) : `cryptsetup reencrypt` en ligne sur un volume monté (durée : quelques minutes pour 20 Go), le comportement de `--key-slot` (les autres emplacements retirés), la suppression effective des segments WAL avec `wal_recycle = off`. En cas de doute, faire l'étape volume éteint : `volume.sh secours` n'est pas nécessaire, `cryptsetup reencrypt` fonctionne aussi hors ligne.

### Après

- Supprimer `/etc/diguee/kms-cloture.*` et l'identité `diguee-cloture` dans l'espace client ; retirer à l'identité du volume le droit de chiffrer.
- Effacer à la main ce que la procédure ne voit pas (rappel dans `docs/À faire par le porteur.md`) : **instantanés (snapshots) et sauvegardes automatiques du VPS** dans l'espace client, copies locales de sauvegardes téléchargées pour les tests de restauration, ancienne copie hors ligne de l'en-tête LUKS.
- Vérifier le procès-verbal publié : `diguee verifier-journal https://consultation.diguee.sn/journal.json`, puis lire l'entrée `proces_verbal`.

### Simulation

`go test ./internal/cloture -run TestSimulationDeBoutEnBout -v` (avec `DIGUEE_TEST_POSTGRES_URL`, `pg_dump`, `pg_restore`, `age`) joue toute la Clôture sur une base de test, avec de vraies sauvegardes chiffrées dans un dossier et le faux KMS : fermeture, panne du KMS au milieu (alerte, arrêt), reprise, attente de l'étape du volume, procès-verbal ; puis il vérifie qu'aucune empreinte ne reste, ni dans la base (`pg_dump` complet), ni dans la sauvegarde restante (déchiffrée, convertie en SQL, restaurée et contrôlée), que réponses et publications sont intactes et que la chaîne du journal est valide (spec T12). En développement, `diguee cloturer -sans-volume` fait la procédure sans l'étape du volume (refusé en production).
