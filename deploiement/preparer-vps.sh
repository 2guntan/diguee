#!/usr/bin/env bash
# Préparation du VPS de Diguee (ticket #7). À lancer en root sur le VPS
# (Ubuntu 24.04 LTS), depuis une copie du dossier deploiement/ :
#
#   sudo ./preparer-vps.sh systeme --admin ubuntu --ssh-depuis "203.0.113.10/32,2001:db8::/64"
#       paquets, mises à jour automatiques, utilisateurs diguee et
#       diguee-migrations (sans shell), SSH par clé seulement, pare-feu
#       nftables, journald 14 jours (rsyslog retiré), unités systemd et
#       scripts de Diguee ;
#   (puis, depuis le poste : deployer.sh --sans-demarrer, et sur le VPS :
#    volume.sh creer — voir deploiement/README.md)
#   sudo ./preparer-vps.sh postgresql
#       cluster PostgreSQL 16 sur le volume chiffré, sans port TCP, rôles et
#       base, fichiers d'environnement.
#
# Chaque étape vérifie avant d'agir et peut être relancée. DIGUEE_A_BLANC=1
# affiche les commandes sans rien changer ; DIGUEE_SANS_SYSTEMD=1 saute ce
# qui demande systemd en marche (essais dans un conteneur).
set -euo pipefail
umask 022
export LC_ALL=C.UTF-8 DEBIAN_FRONTEND=noninteractive

ICI="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
A_BLANC="${DIGUEE_A_BLANC:-0}"
SANS_SYSTEMD="${DIGUEE_SANS_SYSTEMD:-0}"
ETC=/etc/diguee
VOLUME=/srv/diguee-donnees
PGDATA="$VOLUME/postgresql/16/main"

dire() { printf '\n== %s\n' "$*" >&2; }
fais() {
	if [ "$A_BLANC" = 1 ]; then
		echo "+ $*"
	else
		"$@"
	fi
}
# systemd : seulement sur une vraie machine.
sysd() {
	if [ "$SANS_SYSTEMD" = 1 ]; then
		echo "(sans systemd) systemctl $*" >&2
	else
		fais systemctl "$@"
	fi
}
# poser SOURCE DESTINATION MODE [PROPRIÉTAIRE]
poser() {
	fais install -D -m "$3" -o "${4:-root}" -g "${4:-root}" "$1" "$2"
}
# ecrire DESTINATION MODE : copie l'entrée standard.
ecrire() {
	local tmp
	tmp="$(mktemp)"
	cat >"$tmp"
	if [ "$A_BLANC" = 1 ]; then
		echo "+ écrire $1 ($2) :"
		sed 's/^/    /' "$tmp"
	else
		install -D -m "$2" -o root -g root "$tmp" "$1"
	fi
	rm -f "$tmp"
}

verifier_systeme() {
	if [ "$(id -u)" != 0 ] && [ "$A_BLANC" != 1 ]; then
		echo "à lancer en root (sudo)" >&2
		exit 1
	fi
	# shellcheck source=/dev/null
	. /etc/os-release
	if [ "${ID:-}" != ubuntu ] || [ "${VERSION_ID:-}" != 24.04 ]; then
		echo "système ${PRETTY_NAME:-inconnu} : ce script vise Ubuntu 24.04 LTS (PostgreSQL 16 des dépôts)" >&2
		[ "${DIGUEE_FORCER:-0}" = 1 ] || exit 1
	fi
}

# adresses_ssh LISTE FAMILLE : éléments d'un ensemble nftables.
elements_nft() {
	local liste="$1" famille="$2" a sortie=()
	IFS=',' read -r -a adresses <<<"$liste"
	for a in "${adresses[@]}"; do
		a="$(echo "$a" | tr -d '[:space:]')"
		[ -n "$a" ] || continue
		if [ "$famille" = 6 ] && [[ "$a" == *:* ]]; then sortie+=("$a"); fi
		if [ "$famille" = 4 ] && [[ "$a" != *:* ]]; then sortie+=("$a"); fi
	done
	if [ ${#sortie[@]} -gt 0 ]; then
		local IFS=','
		echo "		elements = { ${sortie[*]} }"
	fi
}

etape_systeme() {
	local admin="" ssh_depuis=""
	while [ $# -gt 0 ]; do
		case "$1" in
		--admin) admin="$2"; shift 2 ;;
		--ssh-depuis) ssh_depuis="$2"; shift 2 ;;
		*) echo "argument inconnu : $1" >&2; exit 2 ;;
		esac
	done
	if [ -z "$admin" ] || [ -z "$ssh_depuis" ]; then
		echo "--admin UTILISATEUR et --ssh-depuis \"ADRESSE[/PRÉFIXE],…\" sont obligatoires" >&2
		exit 2
	fi
	verifier_systeme

	dire "Paquets"
	fais apt-get update -q
	fais apt-get -y -q full-upgrade
	fais apt-get -y -q install --no-install-recommends \
		unattended-upgrades nftables cryptsetup postgresql-16 postgresql-client-16 \
		ca-certificates curl openssh-server logrotate e2fsprogs util-linux grep
	# journald est le seul dépôt des journaux (rétention de 14 jours) :
	# rsyslog garderait /var/log/syslog et auth.log plusieurs semaines.
	if dpkg -s rsyslog >/dev/null 2>&1; then
		fais apt-get -y -q purge rsyslog
	fi

	dire "Mises à jour de sécurité automatiques"
	poser "$ICI/systeme/apt-20auto-upgrades" /etc/apt/apt.conf.d/20auto-upgrades 0644
	poser "$ICI/systeme/apt-52diguee-unattended-upgrades" /etc/apt/apt.conf.d/52diguee-unattended-upgrades 0644
	sysd enable --now unattended-upgrades.service

	dire "Utilisateurs sans shell"
	for u in diguee diguee-migrations; do
		if ! id "$u" >/dev/null 2>&1; then
			fais useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin "$u"
		fi
	done
	fais install -d -m 0750 -o root -g root "$ETC"
	fais install -d -m 0755 -o root -g root /opt/diguee /opt/diguee/versions /usr/local/lib/diguee

	dire "SSH par clé seulement"
	local cles
	cles="$(getent passwd "$admin" | cut -d: -f6)/.ssh/authorized_keys"
	if [ "$A_BLANC" != 1 ] && ! grep -qE '^(ssh-|ecdsa-|sk-)' "$cles" 2>/dev/null; then
		echo "aucune clé publique dans $cles : refus de couper les mots de passe (risque de blocage)" >&2
		exit 1
	fi
	sed "s/@ADMIN@/$admin/" "$ICI/systeme/sshd-10-diguee.conf" | ecrire /etc/ssh/sshd_config.d/10-diguee.conf 0644
	if [ "$A_BLANC" != 1 ]; then
		mkdir -p /run/sshd
		sshd -t
	fi
	sysd reload ssh.service

	dire "Pare-feu local nftables"
	local v4 v6 tmp
	v4="$(elements_nft "$ssh_depuis" 4)"
	v6="$(elements_nft "$ssh_depuis" 6)"
	tmp="$(mktemp)"
	awk -v v4="$v4" -v v6="$v6" '
		$0 == "@SSH_V4@" { if (v4 != "") print v4; next }
		$0 == "@SSH_V6@" { if (v6 != "") print v6; next }
		{ print }' "$ICI/systeme/nftables.conf.modele" >"$tmp"
	if [ "$A_BLANC" != 1 ]; then
		nft -c -f "$tmp"
	fi
	ecrire /etc/nftables.conf 0644 <"$tmp"
	rm -f "$tmp"
	sysd enable nftables.service
	sysd restart nftables.service

	dire "Journaux : journald, 14 jours"
	poser "$ICI/systeme/journald-50-diguee.conf" /etc/systemd/journald.conf.d/50-diguee.conf 0644
	poser "$ICI/systeme/logrotate-postgresql-common" /etc/logrotate.d/postgresql-common 0644
	fais install -d -m 2755 -o root -g systemd-journal /var/log/journal
	sysd restart systemd-journald.service
	ecrire "$ETC/adresses-tolerees" 0644 <<-EOF
		# Adresses admises dans le journal système par controle-journaux.sh
		# (T3) : adresses SSH des administrateurs, serveurs NTP… Une adresse
		# ou un préfixe par ligne. Jamais une adresse de participant.
		$(echo "$ssh_depuis" | tr ',' '\n')
	EOF

	dire "Scripts et unités systemd"
	poser "$ICI/volume.sh" /usr/local/lib/diguee/volume.sh 0755
	poser "$ICI/controle-journaux.sh" /usr/local/sbin/diguee-controle-journaux 0755
	poser "$ICI/installer-version.sh" /usr/local/sbin/diguee-installer 0755
	poser "$ICI/commande.sh" /usr/local/sbin/diguee-commande 0755
	for u in diguee.service diguee-migrer.service diguee-volume.service; do
		poser "$ICI/systemd/$u" "/etc/systemd/system/$u" 0644
	done
	poser "$ICI/systemd/postgresql@16-main.service.d/diguee.conf" /etc/systemd/system/postgresql@16-main.service.d/diguee.conf 0644
	sysd daemon-reload

	dire "PostgreSQL : le cluster par défaut (disque non chiffré) est retiré"
	if [ -d /var/lib/postgresql/16/main ] && ! grep -q "$PGDATA" /etc/postgresql/16/main/postgresql.conf 2>/dev/null; then
		fais pg_dropcluster --stop 16 main
	fi

	dire "Fait. Suite : deploiement/README.md, étapes « exécutable », « volume » puis « postgresql »."
}

etape_postgresql() {
	verifier_systeme
	if [ "$A_BLANC" != 1 ] && ! mountpoint -q "$VOLUME"; then
		echo "$VOLUME n'est pas monté : créer ou ouvrir d'abord le volume chiffré (volume.sh creer)" >&2
		exit 1
	fi

	dire "Cluster PostgreSQL 16 sur le volume chiffré"
	if [ ! -f /etc/postgresql/16/main/postgresql.conf ]; then
		fais pg_createcluster 16 main --datadir "$PGDATA" --locale C.UTF-8 --encoding UTF8 -- --data-checksums
	fi
	poser "$ICI/postgresql/diguee.conf" /etc/postgresql/16/main/conf.d/diguee.conf 0644 postgres
	poser "$ICI/postgresql/pg_hba.conf" /etc/postgresql/16/main/pg_hba.conf 0640 postgres
	poser "$ICI/postgresql/pg_ident.conf" /etc/postgresql/16/main/pg_ident.conf 0640 postgres
	if [ "$SANS_SYSTEMD" = 1 ]; then
		fais pg_ctlcluster 16 main restart || fais pg_ctlcluster 16 main start
	else
		sysd enable postgresql@16-main.service
		sysd restart postgresql@16-main.service
	fi

	dire "Rôles et base"
	local existe=""
	if [ "$A_BLANC" != 1 ]; then
		existe="$(runuser -u postgres -- psql -XAtq -c "SELECT 1 FROM pg_roles WHERE rolname = 'diguee_proprietaire'")"
	fi
	if [ "$existe" != 1 ]; then
		fais runuser -u postgres -- psql -X -v ON_ERROR_STOP=1 -q -f "$ICI/postgresql/roles.sql"
	fi

	dire "Fichiers d'environnement (root 0600)"
	if [ ! -e "$ETC/diguee.env" ]; then
		ecrire "$ETC/diguee.env" 0600 <"$ICI/env/diguee.env.exemple"
	fi
	if [ ! -e "$ETC/migrations.env" ]; then
		ecrire "$ETC/migrations.env" 0600 <"$ICI/env/migrations.env.exemple"
	fi
	dire "Fait. Compléter $ETC/diguee.env (valeurs « À COMPLÉTER »), puis deployer.sh."
}

commande="${1:-}"
[ $# -gt 0 ] && shift
case "$commande" in
systeme) etape_systeme "$@" ;;
postgresql) etape_postgresql "$@" ;;
*)
	sed -n '2,21p' "$0" | sed 's/^# \{0,1\}//'
	exit 2
	;;
esac
