#!/usr/bin/env bash
# Volume chiffré (LUKS2) des données de Diguee : PostgreSQL et dossier
# d'état du service (certificats Let's Encrypt). Installé dans
# /usr/local/lib/diguee/volume.sh ; root seulement. Voir deploiement/README.md.
#
#   volume.sh creer [--taille 20G | --peripherique /dev/sdb]
#       première installation : tire la clé du volume, la fait chiffrer par
#       le KMS (diguee cle-volume creer), formate le volume avec cette clé,
#       ajoute la phrase de secours (saisie au clavier), crée le système de
#       fichiers et les dossiers ;
#   volume.sh ouvrir      (diguee-volume.service, au démarrage)
#       clé déchiffrée par le KMS, passée à cryptsetup par un tube, puis
#       montage sur /srv/diguee-donnees ;
#   volume.sh secours
#       ouverture avec la phrase de secours (KMS indisponible), au clavier ;
#       ensuite : systemctl start diguee-volume postgresql@16-main diguee ;
#   volume.sh fermer      (arrêt)
#   volume.sh etat
#
# Variables (fichier /etc/diguee/volume.env, root 0600) : celles de
# « diguee cle-volume » (DIGUEE_OVH_KMS_*) ; facultatives :
# DIGUEE_VOLUME_SOURCE (fichier ou périphérique, défaut
# /var/lib/diguee-volume/donnees.luks), DIGUEE_VOLUME_CLE_CHIFFREE (défaut
# /etc/diguee/volume.cle.jwe), DIGUEE_EXECUTABLE (défaut /opt/diguee/diguee).
# DIGUEE_A_BLANC=1 affiche les commandes au lieu de les exécuter.
set -euo pipefail
umask 077

SOURCE="${DIGUEE_VOLUME_SOURCE:-/var/lib/diguee-volume/donnees.luks}"
CLE_CHIFFREE="${DIGUEE_VOLUME_CLE_CHIFFREE:-/etc/diguee/volume.cle.jwe}"
DIGUEE="${DIGUEE_EXECUTABLE:-/opt/diguee/diguee}"
NOM="diguee-donnees"
POINT="/srv/diguee-donnees"
A_BLANC="${DIGUEE_A_BLANC:-0}"

fais() {
	if [ "$A_BLANC" = 1 ]; then
		echo "+ $*"
	else
		"$@"
	fi
}

# cle : écrit la clé du volume sur la sortie standard (un tube, jamais un
# terminal ni un fichier).
cle() {
	"$DIGUEE" cle-volume dechiffrer -entree "$CLE_CHIFFREE"
}

ouvert() { [ -e "/dev/mapper/$NOM" ]; }

monter() {
	fais mkdir -p "$POINT"
	if ! mountpoint -q "$POINT"; then
		fais mount -o nodev,nosuid,noexec "/dev/mapper/$NOM" "$POINT"
	fi
}

commande="${1:-}"
[ $# -gt 0 ] && shift
case "$commande" in
creer)
	TAILLE="20G"
	PERIPHERIQUE=""
	while [ $# -gt 0 ]; do
		case "$1" in
		--taille) TAILLE="$2"; shift 2 ;;
		--peripherique) PERIPHERIQUE="$2"; shift 2 ;;
		*) echo "argument inconnu : $1" >&2; exit 2 ;;
		esac
	done
	[ -n "$PERIPHERIQUE" ] && SOURCE="$PERIPHERIQUE"
	if [ -e "$CLE_CHIFFREE" ]; then
		echo "$CLE_CHIFFREE existe déjà : volume déjà créé ? (refus de l'écraser)" >&2
		exit 1
	fi
	if [ -z "$PERIPHERIQUE" ]; then
		if [ -e "$SOURCE" ]; then
			echo "$SOURCE existe déjà : refus de l'écraser" >&2
			exit 1
		fi
		fais mkdir -p "$(dirname "$SOURCE")"
		fais chmod 700 "$(dirname "$SOURCE")"
		fais fallocate -l "$TAILLE" "$SOURCE"
		fais chmod 600 "$SOURCE"
	fi
	# 1. Clé tirée et chiffrée par le KMS ; seul le texte chiffré est écrit.
	fais "$DIGUEE" cle-volume creer -sortie "$CLE_CHIFFREE"
	# 2. Formatage avec la clé, reçue par un tube.
	if [ "$A_BLANC" = 1 ]; then
		echo "+ $DIGUEE cle-volume dechiffrer -entree $CLE_CHIFFREE | cryptsetup luksFormat --type luks2 --batch-mode --key-file=- $SOURCE"
	else
		cle | cryptsetup luksFormat --type luks2 --batch-mode --pbkdf argon2id --key-file=- "$SOURCE"
	fi
	# 3. Phrase de secours, dans un second emplacement de clé : tapée au
	#    clavier par le porteur, gardée hors ligne (gestionnaire de mots de
	#    passe et papier sous scellé). Elle permet d'ouvrir le volume sans le
	#    KMS (volume.sh secours).
	echo "Phrase de secours du volume : 6 mots au moins, à conserver hors ligne." >&2
	if [ "$A_BLANC" = 1 ]; then
		echo "+ cryptsetup luksAddKey --key-file=<(clé du KMS) $SOURCE"
	else
		cryptsetup luksAddKey --pbkdf argon2id --key-file=<(cle) "$SOURCE"
	fi
	# 4. Ouverture, système de fichiers, dossiers.
	if [ "$A_BLANC" = 1 ]; then
		echo "+ $DIGUEE cle-volume dechiffrer … | cryptsetup open --type luks2 --key-file=- $SOURCE $NOM"
	else
		cle | cryptsetup open --type luks2 --key-file=- "$SOURCE" "$NOM"
	fi
	fais mkfs.ext4 -q -L diguee-donnees "/dev/mapper/$NOM"
	monter
	fais install -d -m 0700 -o postgres -g postgres "$POINT/postgresql"
	fais install -d -m 0700 -o diguee -g diguee "$POINT/diguee"
	fais install -d -m 0700 -o diguee -g diguee "$POINT/diguee/acme"
	echo "Volume créé, ouvert et monté sur $POINT. En-tête LUKS à sauvegarder hors ligne :" >&2
	echo "  cryptsetup luksHeaderBackup $SOURCE --header-backup-file /root/diguee-luks-entete.img" >&2
	;;
ouvrir)
	if ! ouvert; then
		if [ "$A_BLANC" = 1 ]; then
			echo "+ $DIGUEE cle-volume dechiffrer -entree $CLE_CHIFFREE | cryptsetup open --type luks2 --key-file=- $SOURCE $NOM"
		else
			cle | cryptsetup open --type luks2 --key-file=- "$SOURCE" "$NOM"
		fi
	fi
	monter
	;;
secours)
	if ! ouvert; then
		echo "Ouverture avec la phrase de secours (KMS non utilisé)." >&2
		fais cryptsetup open --type luks2 "$SOURCE" "$NOM"
	fi
	monter
	echo "Volume ouvert. Ensuite : systemctl start diguee-volume postgresql@16-main diguee" >&2
	;;
fermer)
	if mountpoint -q "$POINT"; then
		fais umount "$POINT"
	fi
	if ouvert; then
		fais cryptsetup close "$NOM"
	fi
	;;
etat)
	cryptsetup status "$NOM" || true
	mountpoint "$POINT" || true
	;;
*)
	sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//'
	exit 2
	;;
esac
