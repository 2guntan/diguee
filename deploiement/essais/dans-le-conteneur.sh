#!/usr/bin/env bash
# Partie intérieure de conteneur.sh : prépare un « VPS » Ubuntu 24.04 avec
# preparer-vps.sh (sans systemd), crée et rouvre le volume chiffré avec
# volume.sh (KMS remplacé par un faux « diguee cle-volume »), crée le
# cluster PostgreSQL, applique les migrations avec le vrai exécutable
# (rôle propriétaire, « peer ») et vérifie les droits du rôle de
# l'application, puis le contrôle T3 du journal.
# shellcheck disable=SC2015 # « A && B || échec » est voulu
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive LC_ALL=C.UTF-8
D=/depot/deploiement
export DIGUEE_SANS_SYSTEMD=1
ok() { printf 'OK  %s\n' "$*"; }
ko() { printf 'KO  %s\n' "$*"; exit 1; }

# Un administrateur avec une clé, comme sur l'image d'OVHcloud.
apt-get update -qq >/dev/null
apt-get install -y -qq sudo openssh-client rsyslog >/dev/null
id ubuntu >/dev/null 2>&1 || useradd -m -s /bin/bash ubuntu
install -d -m 700 -o ubuntu -g ubuntu /home/ubuntu/.ssh
ssh-keygen -q -t ed25519 -N '' -f /tmp/cle
install -m 600 -o ubuntu -g ubuntu /tmp/cle.pub /home/ubuntu/.ssh/authorized_keys

bash "$D/preparer-vps.sh" systeme --admin ubuntu --ssh-depuis "203.0.113.10/32, 2001:db8::/64" >/tmp/systeme.log 2>&1 || { tail -40 /tmp/systeme.log; ko "preparer-vps.sh systeme"; }
ok "preparer-vps.sh systeme"
for u in diguee diguee-migrations; do
	[ "$(getent passwd "$u" | cut -d: -f7)" = /usr/sbin/nologin ] || ko "$u sans shell"
done
ok "utilisateurs diguee et diguee-migrations sans shell"
sshd -T -C user=ubuntu,host=x,addr=203.0.113.10 >/tmp/sshd-T 2>&1 || { cat /tmp/sshd-T; ko "sshd -T"; }
grep -qx 'passwordauthentication no' /tmp/sshd-T || { grep -i 'password\|include' /tmp/sshd-T; ls /etc/ssh/sshd_config.d; ko "sshd : mots de passe"; }
grep -qx 'permitrootlogin no' /tmp/sshd-T || { grep -i 'root\|authenticationm' /tmp/sshd-T; ko "sshd : root"; }
grep -qx 'authenticationmethods publickey' /tmp/sshd-T || ko "sshd : clé"
ok "sshd : clé seulement, root refusé (sshd -T)"
nft -c -f /etc/nftables.conf || ko "nftables.conf"
grep -q '203.0.113.10/32' /etc/nftables.conf && grep -q '2001:db8::/64' /etc/nftables.conf || ko "adresses SSH"
nft -f /etc/nftables.conf && nft list ruleset | grep -q 'tcp dport { 80, 443 } accept' || ko "nft chargé"
nft flush ruleset
ok "nftables : syntaxe vérifiée et règles chargées (80, 443, SSH autorisé)"
! dpkg -s rsyslog >/dev/null 2>&1 || ko "rsyslog encore là"
grep -q 'MaxRetentionSec=13day' /etc/systemd/journald.conf.d/50-diguee.conf || ko journald
grep -q 'Automatic-Reboot "true"' /etc/apt/apt.conf.d/52diguee-unattended-upgrades || ko unattended
ok "journald 14 jours, rsyslog retiré, mises à jour automatiques"

# Exécutable : installation seule.
SHA="$(sha256sum /depot/bin/diguee | cut -d' ' -f1)"
cp /depot/bin/diguee "/tmp/diguee-$SHA"
diguee-installer "/tmp/diguee-$SHA" "$SHA" --sans-demarrer >/dev/null || ko "diguee-installer"
[ "$(readlink /opt/diguee/diguee)" = "versions/$SHA/diguee" ] || ko "lien de version"
cp /depot/bin/diguee /tmp/faux
printf 'x' >>/tmp/faux
diguee-installer /tmp/faux "$SHA" --sans-demarrer >/dev/null 2>&1 && ko "SHA-256 faux accepté"
ok "diguee-installer : SHA-256 vérifié, installé sans démarrer, fichier altéré refusé"
command -v systemd-analyze >/dev/null && for u in diguee diguee-migrer diguee-volume; do
	systemd-analyze verify --man=no "/etc/systemd/system/$u.service" 2>&1 | grep -v -e 'not found' -e 'Failed to prepare' -e "ExecStart=" -e 'Unit .* not found' || true
done
ok "unités installées"

# Volume chiffré, avec un faux « diguee cle-volume » (pas de KMS ici).
cat >/tmp/faux-diguee <<'EOS'
#!/usr/bin/env bash
set -euo pipefail
[ "$1" = cle-volume ] || exit 1
case "$2" in
creer) head -c 64 /dev/urandom >/tmp/cle-en-clair-du-faux-kms; echo "faux.jwe" >"$4" ;;
dechiffrer) [ -t 1 ] && exit 1; cat /tmp/cle-en-clair-du-faux-kms ;;
esac
EOS
chmod +x /tmp/faux-diguee
export DIGUEE_EXECUTABLE=/tmp/faux-diguee DIGUEE_VOLUME_SOURCE=/var/lib/diguee-volume/donnees.luks
if [ -e /dev/mapper/control ] || dmsetup version >/dev/null 2>&1; then
	printf 'phrase de secours de l essai\n' | /usr/local/lib/diguee/volume.sh creer --taille 600M >/tmp/volume.log 2>&1 || { cat /tmp/volume.log; ko "volume.sh creer"; }
	mountpoint -q /srv/diguee-donnees || ko "volume monté"
	[ "$(stat -c %U /srv/diguee-donnees/diguee/acme)" = diguee ] || ko "dossier d'état"
	ok "volume.sh creer : LUKS2, clé du « KMS » + phrase de secours, monté"
	/usr/local/lib/diguee/volume.sh fermer && ! mountpoint -q /srv/diguee-donnees || ko "fermer"
	/usr/local/lib/diguee/volume.sh ouvrir && mountpoint -q /srv/diguee-donnees || ko "ouvrir (clé du KMS)"
	/usr/local/lib/diguee/volume.sh fermer
	printf 'phrase de secours de l essai\n' | /usr/local/lib/diguee/volume.sh secours >/dev/null 2>&1 && mountpoint -q /srv/diguee-donnees || ko "secours"
	ok "volume.sh fermer, ouvrir (KMS) et secours (phrase)"
else
	# Noyau sans device-mapper (cas du bac à sable où ce script a été écrit) :
	# volume.sh creer s'arrête à l'ouverture ; on vérifie l'en-tête LUKS et
	# les deux clés, puis on simule le montage par un tmpfs.
	printf 'phrase de secours de l essai\n' | /usr/local/lib/diguee/volume.sh creer --taille 600M >/tmp/volume.log 2>&1 && ko "device-mapper absent mais ouverture réussie ?"
	grep -q 'Cannot initialize device-mapper' /tmp/volume.log || { cat /tmp/volume.log; ko "volume.sh creer"; }
	echo "(pas de device-mapper : ouverture et montage non essayés)"
	/tmp/faux-diguee cle-volume dechiffrer | cryptsetup open --test-passphrase --key-file=- "$DIGUEE_VOLUME_SOURCE" || ko "clé du « KMS »"
	printf 'phrase de secours de l essai\n' | cryptsetup open --test-passphrase "$DIGUEE_VOLUME_SOURCE" || ko "phrase de secours"
	ok "volume.sh creer : LUKS2 formaté avec la clé du « KMS » (tube), phrase de secours ajoutée ; les deux ouvrent l'en-tête"
	mkdir -p /srv/diguee-donnees
	mount -t tmpfs -o size=700M,mode=0755 tmpfs /srv/diguee-donnees
	install -d -m 0700 -o postgres -g postgres /srv/diguee-donnees/postgresql
	install -d -m 0700 -o diguee -g diguee /srv/diguee-donnees/diguee/acme
fi
[ "$(cryptsetup luksDump "$DIGUEE_VOLUME_SOURCE" | grep -cE '^  [0-9]+: luks2')" = 2 ] || ko "deux emplacements de clé"
[ "$(stat -c %a "$DIGUEE_VOLUME_SOURCE")" = 600 ] || ko "fichier du volume 0600"
printf 'mauvaise phrase\n' | cryptsetup open --test-passphrase "$DIGUEE_VOLUME_SOURCE" 2>/dev/null && ko "mauvaise phrase acceptée"
printf 'faux.jwe\n' | cmp -s - /etc/diguee/volume.cle.jwe || ko "texte chiffré"
ok "deux emplacements de clé, mauvaise phrase refusée, fichier 0600"

# PostgreSQL sur le volume.
bash "$D/preparer-vps.sh" postgresql >/tmp/pg.log 2>&1 || { tail -30 /tmp/pg.log; ko "preparer-vps.sh postgresql"; }
grep -q "/srv/diguee-donnees/postgresql/16/main" /etc/postgresql/16/main/postgresql.conf || ko "données hors du volume"
[ "$(runuser -u postgres -- psql -XAtc 'SHOW listen_addresses')" = "" ] || ko "listen_addresses"
[ "$(stat -c %a /etc/diguee/diguee.env)" = 600 ] || ko "diguee.env 0600"
ok "preparer-vps.sh postgresql : cluster sur le volume, aucun port TCP, fichiers d'environnement 0600"

# Migrations avec le vrai exécutable, rôle propriétaire par « peer ».
APP='postgres://diguee_app@/diguee?host=/var/run/postgresql'
PROPRIO='postgres://diguee_proprietaire@/diguee?host=/var/run/postgresql'
runuser -u diguee-migrations -- env DIGUEE_ENV=production DIGUEE_POSTGRES_URL="$APP" DIGUEE_POSTGRES_URL_MIGRATIONS="$PROPRIO" \
	/opt/diguee/diguee migrer 2>/tmp/migrer.log || { cat /tmp/migrer.log; ko "diguee migrer"; }
grep -q 'droits de l.application accordés' /tmp/migrer.log || ko "droits non accordés"
ok "diguee migrer (production, rôle propriétaire par peer) : $(grep -c 'migration appliquée' /tmp/migrer.log) migrations, droits accordés"
runuser -u diguee -- env DIGUEE_ENV=production DIGUEE_POSTGRES_URL="$APP" /opt/diguee/diguee migrer 2>/dev/null && ko "migrer sans l'adresse du propriétaire"
runuser -u diguee -- psql -XAtq "$PROPRIO" -c 'SELECT 1' 2>/dev/null && ko "diguee devient propriétaire"
runuser -u diguee -- psql -XAtq "postgres://postgres@/diguee?host=/var/run/postgresql" -c 'SELECT 1' 2>/dev/null && ko "diguee devient postgres"
for ordre in "DELETE FROM participation" "UPDATE reponse SET q1 = q1" "TRUNCATE journal_public" \
	"ALTER TABLE journal_public DISABLE TRIGGER ALL" "DELETE FROM membre" "CREATE TABLE intrus (x int)"; do
	runuser -u diguee -- psql -XAtq -v ON_ERROR_STOP=1 "$APP" -c "$ordre" 2>/dev/null && ko "accepté : $ordre"
done
runuser -u diguee -- psql -XAtq -v ON_ERROR_STOP=1 "$APP" -c "INSERT INTO participation VALUES (decode(repeat('ab', 32), 'hex'))" >/dev/null || ko "INSERT refusé"
runuser -u diguee-migrations -- psql -XAtq -v ON_ERROR_STOP=1 "$PROPRIO" -c "DELETE FROM participation" >/dev/null || ko "le propriétaire ne vide pas participation"
ok "rôle de l'application (peer) : INSERT permis ; DELETE, UPDATE, TRUNCATE, DISABLE TRIGGER, CREATE refusés ; ni propriétaire ni postgres"

# Sans intégrations réelles, la production est refusée : verifier-config
# liste ce qui manque.
runuser -u diguee -- env DIGUEE_ENV=production DIGUEE_POSTGRES_URL="$APP" /opt/diguee/diguee verifier-config >/tmp/vc.log 2>&1 && ko "verifier-config complet ?"
grep -q 'empreinteur est en mode fake' /tmp/vc.log || ko "verifier-config"
ok "diguee verifier-config liste les manques"

# T3 : PostgreSQL ne journalise ni requête ni détail.
runuser -u diguee -- psql -XAtq "$APP" -c "INSERT INTO participation VALUES (decode(repeat('cd', 32), 'hex'))" >/dev/null
runuser -u diguee -- psql -XAtq "$APP" -c "INSERT INTO participation VALUES (decode(repeat('cd', 32), 'hex'))" 2>/dev/null || true
for f in /var/log/postgresql/*.log; do
	grep -q 'cdcdcdcd' "$f" && ko "PostgreSQL journalise le détail d'une erreur"
done
/usr/local/sbin/diguee-controle-journaux --fichier /tmp/migrer.log >/dev/null || ko "T3 sur la sortie de migrer"
ok "T3 : aucun détail d'erreur ni IP dans les journaux de PostgreSQL et de diguee migrer"
echo "Tous les essais du conteneur sont passés."
