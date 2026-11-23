#!/usr/bin/env bash
# Contrôle T3 (spec §14, E2, E6, ticket #7) : les journaux applicatifs et
# système ne contiennent aucune adresse IP ni aucun numéro de téléphone.
#
# Sur le serveur (root, ou membre du groupe systemd-journal et adm) :
#   controle-journaux.sh                      # 14 derniers jours
#   controle-journaux.sh --depuis "2 hours ago"
# Sur des fichiers (tests, copie de journaux) :
#   controle-journaux.sh --fichier journal.json [--fichier autre.log]
#
# Règles :
#   - journal de diguee.service, diguee-migrer.service et fichiers donnés par
#     --fichier : aucune IP, aucun numéro, sans exception ;
#   - reste du journal système et /var/log/postgresql : aucun numéro ; les
#     seules IP admises sont celles de la machine elle-même (ses adresses, sa
#     passerelle, ses résolveurs DNS), la boucle locale, et la liste
#     --tolerer (par défaut /etc/diguee/adresses-tolerees : adresses SSH des
#     administrateurs, serveurs NTP), une adresse ou un préfixe CIDR par
#     ligne. Aucune adresse d'un participant ne peut entrer par ces voies :
#     seul diguee reçoit les ports 80 et 443.
#
# Les motifs sont ceux d'internal/masque (adresses IPv4 et IPv6 bornées,
# numéros sénégalais 7[05678]XXXXXXX avec ou sans +221/00221/221 et
# séparateurs) ; --strict ajoute toute suite isolée de 9 chiffres ou plus.
# Sortie : chaque correspondance non admise (valeur masquée à moitié) et sa
# source ; code 0 si rien, 1 si quelque chose, 2 en cas d'erreur d'usage.
set -euo pipefail
export LC_ALL=C.UTF-8

DEPUIS="14 days ago"
TOLERER="/etc/diguee/adresses-tolerees"
STRICT=0
FICHIERS=()

usage() { sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }

while [ $# -gt 0 ]; do
	case "$1" in
	--depuis) DEPUIS="$2"; shift 2 ;;
	--fichier) FICHIERS+=("$2"); shift 2 ;;
	--tolerer) TOLERER="$2"; shift 2 ;;
	--strict) STRICT=1; shift ;;
	-h | --help) usage ;;
	*) echo "argument inconnu : $1" >&2; usage ;;
	esac
done

if ! echo 1 | grep -qP '1' 2>/dev/null; then
	echo "grep -P (PCRE) est nécessaire" >&2
	exit 2
fi

# Motifs (PCRE). Bornes : pas de lettre ni de chiffre de part et d'autre ;
# pour une adresse, pas de point qui la prolongerait.
OCTET='(?:25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])'
IPV4="(?<![0-9A-Za-z.])${OCTET}(?:\\.${OCTET}){3}(?![0-9A-Za-z]|\\.[0-9])"
H='[0-9A-Fa-f]{1,4}'
IPV6="(?<![0-9A-Za-z:])(?:(?:${H}:){7}${H}|(?:${H}:){1,7}:(?:${H}(?::${H}){0,6})?|::${H}(?::${H}){0,6})(?![0-9A-Za-z:])"
NUMERO='(?<![0-9A-Za-z])(?:(?:\+|00)?221[ .\-]?)?7[05678](?:[ .\-]?[0-9]){7}(?![0-9A-Za-z])'
LONG='(?<![0-9A-Za-z.])[0-9]{9,}(?![0-9A-Za-z]|\.[0-9])'

# Adresses admises hors de diguee.
declare -A ADMISES=(["127.0.0.1"]=1 ["0.0.0.0"]=1 ["::1"]=1 ["::"]=1)
PREFIXES=()
ajouter_admise() {
	local a="${1%%/*}"
	[ -n "$a" ] || return 0
	if [[ "$1" == */* ]]; then PREFIXES+=("$1"); else ADMISES["$a"]=1; fi
}
if [ ${#FICHIERS[@]} -eq 0 ]; then
	if command -v ip >/dev/null 2>&1; then
		while read -r a; do ajouter_admise "$a"; done < <(ip -o addr show 2>/dev/null | awk '{print $4}' | sed 's|/.*||')
		while read -r a; do ajouter_admise "$a"; done < <(ip route show default 2>/dev/null | awk '{print $3}'; ip -6 route show default 2>/dev/null | awk '{print $3}')
	fi
	for f in /etc/resolv.conf /run/systemd/resolve/resolv.conf; do
		[ -r "$f" ] && while read -r a; do ajouter_admise "$a"; done < <(awk '/^nameserver/ {print $2}' "$f")
	done
	if [ -r "$TOLERER" ]; then
		while read -r a; do ajouter_admise "$a"; done < <(sed 's/#.*//' "$TOLERER" | tr -s ' \t' '\n' | grep -v '^$' || true)
	fi
fi

# dans_prefixe ADRESSE : vrai si l'adresse IPv4 est dans un préfixe admis.
dans_prefixe() {
	local a="$1" p reseau bits
	[[ "$a" == *.*.*.* ]] || return 1
	for p in "${PREFIXES[@]+"${PREFIXES[@]}"}"; do
		reseau="${p%/*}" bits="${p#*/}"
		[[ "$reseau" == *.*.*.* ]] || continue
		local IFS=.
		# shellcheck disable=SC2086
		set -- $a; local x=$(( ($1 << 24) | ($2 << 16) | ($3 << 8) | $4 ))
		# shellcheck disable=SC2086
		set -- $reseau; local y=$(( ($1 << 24) | ($2 << 16) | ($3 << 8) | $4 ))
		local m=$(( bits == 0 ? 0 : (0xFFFFFFFF << (32 - bits)) & 0xFFFFFFFF ))
		(( (x & m) == (y & m) )) && return 0
	done
	return 1
}

admise() {
	local a="$1"
	[[ "$a" == 127.* ]] && return 0
	[ -n "${ADMISES[$a]+x}" ] && return 0
	dans_prefixe "$a"
}

# Affiche une valeur à moitié masquée : le contrôle ne doit pas recopier en
# clair ce qu'il trouve.
voiler() {
	local v="$1" n=${#1}
	echo "${v:0:$(( n / 2 ))}…"
}

TROUVE=0
signaler() { # source genre valeur
	echo "INTERDIT [$1] $2 : $(voiler "$3")"
	TROUVE=1
}

# examiner SOURCE STRICT_IP : lit l'entrée standard.
examiner() {
	local source="$1" sans_exception="$2" contenu v
	contenu="$(cat)"
	[ -n "$contenu" ] || return 0
	while read -r v; do
		[ -n "$v" ] || continue
		if [ "$sans_exception" = 1 ] || ! admise "$v"; then signaler "$source" "adresse IP" "$v"; fi
	done < <(printf '%s\n' "$contenu" | grep -oP "$IPV4|$IPV6" | sort -u || true)
	while read -r v; do
		[ -n "$v" ] && signaler "$source" "numéro" "$v"
	done < <(printf '%s\n' "$contenu" | grep -oP "$NUMERO" | sort -u || true)
	if [ "$STRICT" = 1 ]; then
		while read -r v; do
			[ -n "$v" ] && signaler "$source" "suite de chiffres" "$v"
		done < <(printf '%s\n' "$contenu" | grep -oP "$LONG" | sort -u || true)
	fi
	return 0
}

if [ ${#FICHIERS[@]} -gt 0 ]; then
	for f in "${FICHIERS[@]}"; do
		if [ ! -r "$f" ]; then echo "fichier illisible : $f" >&2; exit 2; fi
		# shellcheck disable=SC2094 # examiner écrit sur la sortie standard, pas dans $f
		examiner "$f" 1 <"$f"
	done
else
	command -v journalctl >/dev/null || { echo "journalctl introuvable" >&2; exit 2; }
	# Pas de tube vers examiner : il tournerait dans un sous-shell et TROUVE
	# serait perdu.
	for u in diguee.service diguee-migrer.service diguee-volume.service; do
		examiner "journal $u" 1 < <(journalctl --no-pager --quiet -o cat --since "$DEPUIS" -u "$u")
	done
	examiner "journal système" 0 < <(journalctl --no-pager --quiet -o short-iso --since "$DEPUIS")
	if [ -d /var/log/postgresql ]; then
		while IFS= read -r -d '' f; do
			# shellcheck disable=SC2094
			case "$f" in
			*.gz) examiner "$f" 0 < <(zcat "$f") ;;
			*) examiner "$f" 0 <"$f" ;;
			esac
		done < <(find /var/log/postgresql -type f -mtime -15 -print0)
	fi
fi

if [ "$TROUVE" = 0 ]; then
	echo "T3 : aucune adresse IP ni aucun numéro non admis${FICHIERS[*]:+ dans ${FICHIERS[*]}}."
	exit 0
fi
exit 1
