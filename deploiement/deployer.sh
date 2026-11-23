#!/usr/bin/env bash
# Déploiement de Diguee en une commande (ticket #7), depuis le poste du
# porteur, à la racine du dépôt :
#
#   deploiement/deployer.sh admin@consultation.diguee.sn [--sans-demarrer] [--force]
#
#   1. vérifie que l'arbre git est propre (la version affichée doit
#      correspondre à un commit publié ; --force pour passer outre) ;
#   2. construit l'exécutable de façon reproductible (make build : Go de
#      go.mod, CGO_ENABLED=0, -trimpath, -buildid=) pour linux/amd64, deux
#      fois, et vérifie que les deux SHA-256 sont identiques ;
#   3. le copie sur le VPS (scp), puis lance diguee-installer en root
#      (sudo) : vérification du SHA-256, installation, migrations,
#      redémarrage, contrôle de /sante et du pied de page ; retour à la
#      version précédente en cas d'échec.
#
# --sans-demarrer : installe seulement l'exécutable (première installation,
# avant la création du volume chiffré).
# DIGUEE_A_BLANC=1 : affiche les commandes distantes sans les lancer.
set -euo pipefail

if [ $# -lt 1 ] || [[ "$1" == -* ]]; then
	sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
	exit 2
fi
CIBLE="$1"
shift
OPTIONS=()
FORCE=0
for a in "$@"; do
	case "$a" in
	--sans-demarrer) OPTIONS+=("$a") ;;
	--force) FORCE=1 ;;
	*) echo "argument inconnu : $a" >&2; exit 2 ;;
	esac
done
A_BLANC="${DIGUEE_A_BLANC:-0}"

cd "$(git rev-parse --show-toplevel)"
if [ -n "$(git status --porcelain)" ]; then
	if [ "$FORCE" = 1 ]; then
		echo "attention : arbre git modifié, version « -dirty »" >&2
	else
		echo "arbre git modifié : valider ou mettre de côté les changements (ou --force)" >&2
		exit 1
	fi
fi

export GOOS=linux GOARCH=amd64 CGO_ENABLED=0
make build >/dev/null
make verifier-build >/dev/null
SHA="$(sha256sum bin/diguee | cut -d' ' -f1)"
for f in bin/repro/a bin/repro/b; do
	if [ "$(sha256sum "$f" | cut -d' ' -f1)" != "$SHA" ]; then
		echo "build non reproductible : $f diffère de bin/diguee" >&2
		exit 1
	fi
done
echo "exécutable $(git describe --tags --always --dirty) : sha256 $SHA"

DISTANT="/tmp/diguee-$SHA"
if [ "$A_BLANC" = 1 ]; then
	echo "+ scp bin/diguee $CIBLE:$DISTANT"
	echo "+ ssh -t $CIBLE sudo /usr/local/sbin/diguee-installer $DISTANT $SHA ${OPTIONS[*]:-}"
	exit 0
fi
scp -q bin/diguee "$CIBLE:$DISTANT"
# shellcheck disable=SC2029 # variables développées sur le poste, voulu
ssh -t "$CIBLE" sudo /usr/local/sbin/diguee-installer "$DISTANT" "$SHA" "${OPTIONS[@]}"
