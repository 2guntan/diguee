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
for u in diguee diguee-migrations diguee-sauvegarde; do
	[ "$(getent passwd "$u" | cut -d: -f7)" = /usr/sbin/nologin ] || ko "$u sans shell"
done
ok "utilisateurs diguee, diguee-migrations et diguee-sauvegarde sans shell"
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
if command -v systemd-analyze >/dev/null; then
	for u in diguee.service diguee-migrer.service diguee-volume.service diguee-sauvegarde.service diguee-sauvegarde.timer diguee-alerte@essai.service; do
		f="/etc/systemd/system/$u"
		[ -e "$f" ] || f="/etc/systemd/system/${u%%@*}@.service"
		systemd-analyze verify --man=no "$f" 2>&1 | grep -v -e 'not found' -e 'Failed to prepare' -e "ExecStart=" -e 'Unit .* not found' || true
	done
	systemd-analyze calendar '*-*-* 02:30:00 UTC' >/dev/null || ko "OnCalendar du minuteur"
fi
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
# Sauvegarde (ticket #15) : vrai exécutable, rôle diguee_sauvegarde par
# « peer », pg_dump | age ; compartiment remplacé par un dossier (pas d'Object
# Storage ici). La clé publique est remise par LoadCredential en production
# (/etc/diguee est fermé aux utilisateurs du service) : ici, une copie lisible.
command -v age >/dev/null || ko "age non installé par preparer-vps.sh"
grep -q 'diguee-sauvegarde.*diguee_sauvegarde' /etc/postgresql/16/main/pg_ident.conf || ko "pg_ident.conf"
[ "$(stat -c %a /etc/diguee/sauvegarde.env)" = 600 ] || ko "sauvegarde.env 0600"
age-keygen -o /root/cle-sauvegarde.txt 2>/dev/null
install -d -m 0755 /srv/essai-sauvegarde
age-keygen -y /root/cle-sauvegarde.txt >/srv/essai-sauvegarde/sauvegarde.age.pub
install -d -m 0755 -o diguee-sauvegarde -g diguee-sauvegarde /var/lib/diguee-sauvegarde /srv/essai-sauvegarde/compartiment
SVG='postgres://diguee_sauvegarde@/diguee?host=/var/run/postgresql'
runuser -u diguee-sauvegarde -- psql -XAtq -v ON_ERROR_STOP=1 "$SVG" -c "SELECT count(*) FROM participation" >/dev/null || ko "diguee_sauvegarde ne lit pas"
for ordre in "INSERT INTO participation VALUES (decode(repeat('ef', 32), 'hex'))" "DELETE FROM journal_public" "CREATE TABLE intrus (x int)"; do
	runuser -u diguee-sauvegarde -- psql -XAtq -v ON_ERROR_STOP=1 "$SVG" -c "$ordre" 2>/dev/null && ko "diguee_sauvegarde accepté : $ordre"
done
runuser -u diguee-sauvegarde -- psql -XAtq "$PROPRIO" -c 'SELECT 1' 2>/dev/null && ko "diguee-sauvegarde devient propriétaire"
runuser -u diguee -- psql -XAtq "$SVG" -c 'SELECT 1' 2>/dev/null && ko "diguee devient diguee_sauvegarde"
ok "rôle diguee_sauvegarde (peer) : lecture de tout, aucune écriture ; personne d'autre ne le prend"
sauvegarder() {
	runuser -u diguee-sauvegarde -- env DIGUEE_ENV=development DIGUEE_SAUVEGARDE_STOCKAGE=fake \
		DIGUEE_SAUVEGARDE_DOSSIER=/srv/essai-sauvegarde/compartiment DIGUEE_SAUVEGARDE_POSTGRES_URL="$1" \
		DIGUEE_SAUVEGARDE_CLE_PUBLIQUE=/srv/essai-sauvegarde/sauvegarde.age.pub \
		DIGUEE_SAUVEGARDE_PG_DUMP=/usr/lib/postgresql/16/bin/pg_dump DIGUEE_SAUVEGARDE_AGE=/usr/bin/age \
		DIGUEE_SAUVEGARDE_ETAT=/var/lib/diguee-sauvegarde/etat.json /opt/diguee/diguee sauvegarde faire
}
sauvegarder "$SVG" >/tmp/sauvegarde.log 2>&1 || { cat /tmp/sauvegarde.log; ko "diguee sauvegarde faire"; }
OBJ="$(find /srv/essai-sauvegarde/compartiment -name 'diguee-*.pgdump.age')"
[ -n "$OBJ" ] && head -c 21 "$OBJ" | grep -q 'age-encryption.org/v1' || ko "sauvegarde non chiffrée"
grep -q 'PGDMP' "$OBJ" && ko "en-tête de pg_dump en clair"
[ "$(stat -c %a /var/lib/diguee-sauvegarde/etat.json)" = 644 ] || ko "état 0644"
runuser -u diguee -- grep -q '"reussie": true' /var/lib/diguee-sauvegarde/etat.json || ko "état illisible par le service ou en échec"
/usr/local/sbin/diguee-controle-journaux --fichier /tmp/sauvegarde.log >/dev/null || ko "T3 sur la sortie de la sauvegarde"
ok "diguee sauvegarde faire : pg_dump | age, objet chiffré, état lisible par le service"
sauvegarder 'postgres://diguee_proprietaire@/diguee?host=/var/run/postgresql' >/tmp/echec.log 2>&1 && ko "sauvegarde avec un rôle refusé : pas d'échec"
grep -q '"reussie": false' /var/lib/diguee-sauvegarde/etat.json || ko "échec non inscrit dans l'état"
ok "échec de sauvegarde (rôle refusé par peer) : code de sortie 1, inscrit dans l'état"

# Restauration sur une « machine jetable » (ici, le même conteneur, rôle
# postgres), par le script documenté.
install -d -m 0700 -o postgres -g postgres /srv/essai-sauvegarde/restauration
install -m 0600 -o postgres -g postgres /root/cle-sauvegarde.txt /srv/essai-sauvegarde/restauration/cle.txt
install -m 0600 -o postgres -g postgres "$OBJ" /srv/essai-sauvegarde/restauration/sauvegarde.pgdump.age
runuser -u postgres -- createdb restauree
runuser -u postgres -- bash "$D/restaurer-sauvegarde.sh" --identite /srv/essai-sauvegarde/restauration/cle.txt \
	--base 'postgres://postgres@/restauree?host=/var/run/postgresql' --diguee /opt/diguee/diguee \
	--fichier /srv/essai-sauvegarde/restauration/sauvegarde.pgdump.age >/tmp/restauration.log 2>&1 || { cat /tmp/restauration.log; ko "restaurer-sauvegarde.sh"; }
grep -q 'RESTAURATION RÉUSSIE' /tmp/restauration.log || ko "restauration"
grep -qE '^participation +1$' /tmp/restauration.log || { cat /tmp/restauration.log; ko "lignes de participation"; }
ok "restaurer-sauvegarde.sh : déchiffrée par age, restaurée, lignes et journal contrôlés"
# Restauration réelle (deploiement/README.md §10), en dernier : la base
# diguee est recréée vide comme par roles.sql, restaurée avec les
# propriétaires d'origine, puis diguee migrer réaccorde les droits.
runuser -u postgres -- psql -X -v ON_ERROR_STOP=1 -q -c "DROP DATABASE diguee WITH (FORCE)" \
	-c "CREATE DATABASE diguee OWNER diguee_proprietaire ENCODING 'UTF8' LOCALE 'C.UTF-8' TEMPLATE template0" \
	-c "REVOKE ALL ON DATABASE diguee FROM PUBLIC" -c "GRANT CONNECT ON DATABASE diguee TO diguee_app, diguee_sauvegarde" \
	-c "GRANT CONNECT, CREATE, TEMPORARY ON DATABASE diguee TO diguee_proprietaire" >/dev/null
runuser -u postgres -- psql -X -v ON_ERROR_STOP=1 -q -d diguee -c "ALTER SCHEMA public OWNER TO diguee_proprietaire" \
	-c "REVOKE ALL ON SCHEMA public FROM PUBLIC" -c "GRANT USAGE ON SCHEMA public TO diguee_application" >/dev/null
age --decrypt --identity /root/cle-sauvegarde.txt "$OBJ" |
	runuser -u postgres -- pg_restore --dbname=diguee --exit-on-error --single-transaction 2>/tmp/reelle.log || { cat /tmp/reelle.log; ko "restauration réelle"; }
[ "$(runuser -u postgres -- psql -XAtq -d diguee -c "SELECT tableowner FROM pg_tables WHERE tablename = 'participation'")" = diguee_proprietaire ] || ko "propriétaire d'origine"
runuser -u diguee-migrations -- env DIGUEE_ENV=production DIGUEE_POSTGRES_URL="$APP" DIGUEE_POSTGRES_URL_MIGRATIONS="$PROPRIO" /opt/diguee/diguee migrer 2>/tmp/migrer2.log || { cat /tmp/migrer2.log; ko "migrer après restauration"; }
[ "$(runuser -u diguee -- psql -XAtq "$APP" -c "SELECT count(*) FROM participation")" = 1 ] || ko "données restaurées, lues par le rôle de l'application"
ok "restauration réelle : propriétaires d'origine gardés, diguee migrer réaccorde les droits"
echo "Tous les essais du conteneur sont passés."
