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
#   volume.sh renouveler  (Clôture, ticket #16, après la destruction de la
#       clé de l'empreinte) : WAL de PostgreSQL non recyclé, espace libre
#       réécrit, rechiffrement du volume avec une nouvelle clé maîtresse
#       (cryptsetup reencrypt), nouvelle clé chiffrée par le KMS, nouvelle
#       phrase de secours (au clavier), puis attestation lue par
#       « diguee cloturer » ; voir deploiement/README.md §12 ;
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
ATTESTATION="${DIGUEE_CLOTURE_ATTESTATION_VOLUME:-/etc/diguee/volume-renouvele.json}"
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

# cle_nouvelle : comme cle, pour la clé chiffrée en cours de renouvellement.
cle_nouvelle() {
	"$DIGUEE" cle-volume dechiffrer -entree "$CLE_CHIFFREE.nouvelle"
}

# sql : une commande SQL en superutilisateur, sur la base diguee.
sql() {
	fais runuser -u postgres -- psql -X -q -v ON_ERROR_STOP=1 -d diguee -c "$1"
}

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
renouveler)
	# Clôture (ticket #16, spec §12 étape 5). À lancer après « diguee
	# cloturer » arrêté EN ATTENTE sur le renouvellement du volume : la table
	# participation est supprimée, l'espace libéré (VACUUM FULL, CHECKPOINT),
	# les sauvegardes supprimées et la clé de l'empreinte détruite.
	# NON ESSAYÉ SUR UN VRAI VPS (voir README §12, « à vérifier »).
	ouvert || [ "$A_BLANC" = 1 ] || { echo "volume fermé : volume.sh ouvrir d'abord" >&2; exit 1; }
	# Lancé à la main : l'accès au KMS du volume vient de volume.env.
	if [ -z "${DIGUEE_OVH_KMS_ID:-}" ] && [ -r /etc/diguee/volume.env ]; then
		set -a
		# shellcheck source=/dev/null
		. /etc/diguee/volume.env
		set +a
	fi
	[ -e "$CLE_CHIFFREE.nouvelle" ] && { echo "$CLE_CHIFFREE.nouvelle existe (essai interrompu ?) : la vérifier, puis la supprimer" >&2; exit 1; }
	# 1. WAL : les segments écrits avant la suppression de la table
	#    contiennent les empreintes. Sans recyclage, les segments dont
	#    PostgreSQL n'a plus besoin sont supprimés (et non renommés pour
	#    resservir) ; plusieurs bascules et points de contrôle les rendent
	#    inutiles.
	sql "ALTER SYSTEM SET wal_recycle = off"
	sql "SELECT pg_reload_conf()"
	for _ in 1 2 3 4; do
		sql "CHECKPOINT"
		sql "SELECT pg_switch_wal()"
	done
	sql "CHECKPOINT"
	sql "ALTER SYSTEM RESET wal_recycle"
	sql "SELECT pg_reload_conf()"
	# 2. Espace libre du système de fichiers réécrit (les blocs des fichiers
	#    supprimés : ancienne table, anciens segments WAL), services arrêtés
	#    pour que le disque plein ne gêne personne.
	fais systemctl stop diguee.service postgresql@16-main.service
	if [ "$A_BLANC" = 1 ]; then
		echo "+ dd if=/dev/zero of=$POINT/.effacement bs=1M (jusqu'au disque plein) ; sync ; rm"
	else
		dd if=/dev/zero of="$POINT/.effacement" bs=1M status=none 2>/dev/null || true
		sync
		rm -f "$POINT/.effacement"
		sync
	fi
	# 3. Nouvelle clé maîtresse du volume (rechiffrement complet, en ligne) :
	#    une ancienne copie de l'en-tête LUKS, avec l'ancienne clé ou
	#    l'ancienne phrase de secours, ne déchiffre plus le volume. Seul
	#    l'emplacement de la clé du KMS est gardé (les autres sont retirés).
	if [ "$A_BLANC" = 1 ]; then
		echo "+ cryptsetup reencrypt --key-file=<(clé du KMS) --key-slot <emplacement de la clé du KMS> $SOURCE"
	else
		# L'emplacement de la clé du KMS : celui qu'elle ouvre.
		SLOT=""
		for n in $(seq 0 31); do
			if cryptsetup open --test-passphrase --key-slot "$n" --key-file=<(cle) "$SOURCE" 2>/dev/null; then
				SLOT="$n"
				break
			fi
		done
		[ -n "$SLOT" ] || { echo "aucun emplacement LUKS ne s'ouvre avec la clé du KMS" >&2; exit 1; }
		cryptsetup reencrypt --batch-mode --key-file=<(cle) --key-slot "$SLOT" "$SOURCE"
	fi
	# 4. Nouvelle clé du volume, chiffrée par le KMS (l'identité du volume
	#    doit avoir de nouveau le droit encrypt, README §12), ajoutée, puis
	#    l'ancienne retirée.
	fais "$DIGUEE" cle-volume creer -sortie "$CLE_CHIFFREE.nouvelle"
	if [ "$A_BLANC" = 1 ]; then
		echo "+ cryptsetup luksAddKey --key-file=<(ancienne clé) $SOURCE <(nouvelle clé)"
		echo "+ cryptsetup open --test-passphrase --key-file=<(nouvelle clé) $SOURCE"
		echo "+ cryptsetup luksRemoveKey --key-file=<(ancienne clé) $SOURCE"
	else
		cryptsetup luksAddKey --pbkdf argon2id --key-file=<(cle) "$SOURCE" <(cle_nouvelle)
		cryptsetup open --test-passphrase --key-file=<(cle_nouvelle) "$SOURCE"
		cryptsetup luksRemoveKey --key-file=<(cle) "$SOURCE"
	fi
	fais mv "$CLE_CHIFFREE.nouvelle" "$CLE_CHIFFREE"
	# 5. Nouvelle phrase de secours, au clavier (l'ancienne a été retirée
	#    avec les autres emplacements) : la noter hors ligne, détruire
	#    l'ancienne.
	echo "Nouvelle phrase de secours du volume : 6 mots au moins, à conserver hors ligne ; l'ancienne ne sert plus." >&2
	if [ "$A_BLANC" = 1 ]; then
		echo "+ cryptsetup luksAddKey --key-file=<(clé du KMS) $SOURCE"
	else
		cryptsetup luksAddKey --pbkdf argon2id --key-file=<(cle) "$SOURCE"
	fi
	# 6. Services redémarrés, nouvel en-tête à sauvegarder hors ligne,
	#    attestation.
	fais systemctl start postgresql@16-main.service diguee.service
	ENTETE="/root/diguee-luks-entete-$(date -u +%Y%m%dT%H%M%SZ).img"
	fais cryptsetup luksHeaderBackup "$SOURCE" --header-backup-file "$ENTETE"
	if [ "$A_BLANC" = 1 ]; then
		echo "+ attestation écrite dans $ATTESTATION"
	else
		sha_cle="$(sha256sum "$CLE_CHIFFREE" | cut -d' ' -f1)"
		sha_entete="$(sha256sum "$ENTETE" | cut -d' ' -f1)"
		umask 022
		printf '{"date":"%s","sha256_cle_chiffree":"%s","sha256_entete_luks":"%s","rechiffrement":true,"espace_libre_efface":true}\n' \
			"$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$sha_cle" "$sha_entete" >"$ATTESTATION.tmp"
		mv "$ATTESTATION.tmp" "$ATTESTATION"
		chmod 0644 "$ATTESTATION"
	fi
	echo "Volume renouvelé. Copier $ENTETE hors ligne (et détruire l'ancienne copie de l'en-tête et l'ancienne phrase de secours), puis : systemctl start diguee-cloturer" >&2
	;;
*)
	sed -n '2,31p' "$0" | sed 's/^# \{0,1\}//'
	exit 2
	;;
esac
