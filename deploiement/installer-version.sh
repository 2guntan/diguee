#!/usr/bin/env bash
# Installation d'une version de Diguee sur le VPS (ticket #7). Installé dans
# /usr/local/sbin/diguee-installer ; lancé par deployer.sh (en root) :
#
#   diguee-installer FICHIER SHA256 [--sans-demarrer]
#
#   1. vérifie que le SHA-256 du fichier reçu est celui calculé sur le poste,
#      et que l'exécutable annonce lui-même ce SHA-256 (diguee version) ;
#   2. le range dans /opt/diguee/versions/<sha256>/diguee (root, lecture
#      seule) et fait pointer /opt/diguee/diguee dessus (changement atomique) ;
#   3. sauf --sans-demarrer : migrations (diguee-migrer.service, rôle
#      propriétaire), redémarrage de diguee.service, puis contrôle de
#      https://<hôte>/sante ; en cas d'échec, retour à la version précédente
#      (les migrations, elles, ne sont pas annulées).
#
# L'entrée « mise en production » du journal public est inscrite par le
# serveur lui-même à son démarrage sur une nouvelle version.
set -euo pipefail
umask 022

RACINE="${DIGUEE_RACINE:-/opt/diguee}"
A_BLANC="${DIGUEE_A_BLANC:-0}"
GARDER=5

fais() {
	if [ "$A_BLANC" = 1 ]; then
		echo "+ $*"
	else
		"$@"
	fi
}

if [ $# -lt 2 ]; then
	sed -n '2,18p' "$0" | sed 's/^# \{0,1\}//'
	exit 2
fi
FICHIER="$1" SHA="$2"
shift 2
DEMARRER=1
for a in "$@"; do
	case "$a" in
	--sans-demarrer) DEMARRER=0 ;;
	*) echo "argument inconnu : $a" >&2; exit 2 ;;
	esac
done
if ! [[ "$SHA" =~ ^[0-9a-f]{64}$ ]]; then
	echo "SHA-256 invalide : $SHA" >&2
	exit 2
fi

# 1. Intégrité.
recu="$(sha256sum "$FICHIER" | cut -d' ' -f1)"
if [ "$recu" != "$SHA" ]; then
	echo "SHA-256 différent : reçu $recu, attendu $SHA ; rien n'est installé" >&2
	exit 1
fi
annonce="$("$FICHIER" version | awk '/^sha256 / {print $2}')"
if [ "$annonce" != "$SHA" ]; then
	echo "l'exécutable annonce le SHA-256 $annonce, attendu $SHA" >&2
	exit 1
fi

# 2. Installation.
DEST="$RACINE/versions/$SHA/diguee"
fais install -D -m 0755 -o root -g root "$FICHIER" "$DEST"
fais rm -f "$FICHIER"
PRECEDENTE=""
if [ -L "$RACINE/diguee" ]; then
	PRECEDENTE="$(readlink "$RACINE/diguee")"
fi
basculer() {
	fais ln -sfn "$1" "$RACINE/diguee.nouveau"
	fais mv -T "$RACINE/diguee.nouveau" "$RACINE/diguee"
}
basculer "versions/$SHA/diguee"
echo "version installée : $("$DEST" version | head -1) ($SHA)"

# Les plus anciennes versions sont retirées (la courante et la précédente
# restent toujours).
garder_versions() {
	local v
	# shellcheck disable=SC2012 # noms en hexadécimal, sans espace
	ls -1t "$RACINE/versions" | tail -n +$((GARDER + 1)) | while read -r v; do
		if [ "versions/$v/diguee" != "$PRECEDENTE" ] && [ "$v" != "$SHA" ]; then
			fais rm -rf "${RACINE:?}/versions/$v"
		fi
	done
}
garder_versions

if [ "$DEMARRER" = 0 ]; then
	echo "installée sans démarrer (--sans-demarrer)"
	exit 0
fi

retour() {
	echo "ÉCHEC : $1" >&2
	if [ -n "$PRECEDENTE" ]; then
		echo "retour à $PRECEDENTE (les migrations déjà faites restent)" >&2
		basculer "$PRECEDENTE"
		fais systemctl restart diguee.service || true
	fi
	journalctl -u diguee.service -u diguee-migrer.service -n 30 --no-pager >&2 || true
	exit 1
}

# 3. Migrations, redémarrage, contrôle.
fais systemctl start diguee-migrer.service || retour "migrations"
fais systemctl restart diguee.service || retour "redémarrage"
URL="$(sed -n 's/^DIGUEE_URL_PUBLIQUE=//p' /etc/diguee/diguee.env 2>/dev/null | tail -1)"
URL="${URL:-https://consultation.diguee.sn}"
HOTE="${URL#https://}"
HOTE="${HOTE%%/*}"
CURL=(curl -fsS --max-time 5 --resolve "$HOTE:443:127.0.0.1")
# Annuaire de test de Let's Encrypt : certificat non reconnu, contrôle sans
# vérification du certificat (premier essai seulement).
if grep -q '^DIGUEE_ACME_ANNUAIRE=.*staging' /etc/diguee/diguee.env 2>/dev/null; then
	echo "attention : annuaire ACME de test, certificat non vérifié par ce contrôle" >&2
	CURL+=(--insecure)
fi
if [ "$A_BLANC" != 1 ]; then
	ok=0
	for _ in $(seq 1 30); do
		if "${CURL[@]}" "https://$HOTE/sante" >/dev/null 2>&1; then
			ok=1
			break
		fi
		sleep 2
	done
	[ "$ok" = 1 ] || retour "https://$HOTE/sante ne répond pas"
	pied="$("${CURL[@]}" "https://$HOTE/" | grep -o "$SHA" | head -1 || true)"
	[ "$pied" = "$SHA" ] || retour "le pied de page n'affiche pas le SHA-256 $SHA"
fi
echo "Diguee $SHA en service sur https://$HOTE"
