#!/usr/bin/env bash
# Restauration d'une sauvegarde chiffrée de Diguee (ticket #15), et contrôle
# de la base restaurée. À lancer sur une machine jetable, jamais sur le
# serveur : l'ordinateur du porteur avec Docker (option --docker, base dans
# un conteneur en mémoire, supprimé à la fin), ou une machine où la clé
# privée age a été apportée pour l'occasion. Procédure complète et test
# mensuel : deploiement/README.md, § 10 « Sauvegardes ».
#
#   restaurer-sauvegarde.sh --identite CLE.txt (--docker | --base URL)
#       [--fichier SAUVEGARDE.pgdump.age] [--comparer-avec JOURNAL]
#       [--diguee CHEMIN] [--garder]
#
#   --identite    clé privée age (sortie d'age-keygen), lue seulement par age
#   --docker      restaure dans un PostgreSQL 16 jetable (image postgres:16,
#                 données en mémoire, port sur 127.0.0.1 seulement)
#   --base URL    ou restaure dans cette base, qui doit être vide
#                 (pg_restore et psql 16 ou plus sur cette machine)
#   --fichier     sauvegarde déjà téléchargée ; sinon la plus récente est
#                 récupérée par « diguee sauvegarde recuperer », avec les
#                 variables DIGUEE_SAUVEGARDE_S3_* d'une clé de lecture
#   --comparer-avec  export du journal public (fichier ou adresse, par
#                 exemple https://consultation.diguee.sn/journal.json) dont
#                 le journal restauré doit être le début exact
#   --diguee      exécutable diguee (défaut : diguee dans le PATH, sinon
#                 bin/diguee du dépôt)
#   --garder      ne supprime pas le conteneur à la fin (--docker)
#
# Étapes : récupérer la sauvegarde chiffrée ; la déchiffrer avec age, en
# flux, directement dans pg_restore (le dump en clair n'est jamais écrit) ;
# compter les lignes des tables ; vérifier la chaîne du journal public
# (diguee sauvegarde controler, puis diguee verifier-journal).
set -euo pipefail
export LC_ALL=C.UTF-8

ICI="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
IDENTITE="" BASE="" DOCKER=0 FICHIER="" COMPARER="" DIGUEE="" GARDER=0
usage() {
	sed -n '2,31p' "$0" | sed 's/^# \{0,1\}//'
	exit 2
}
while [ $# -gt 0 ]; do
	case "$1" in
	--identite) IDENTITE="$2"; shift 2 ;;
	--base) BASE="$2"; shift 2 ;;
	--docker) DOCKER=1; shift ;;
	--fichier) FICHIER="$2"; shift 2 ;;
	--comparer-avec) COMPARER="$2"; shift 2 ;;
	--diguee) DIGUEE="$2"; shift 2 ;;
	--garder) GARDER=1; shift ;;
	-h | --help) usage ;;
	*) echo "argument inconnu : $1" >&2; usage ;;
	esac
done

etape() { printf '\n== %s\n' "$*" >&2; }
echec() {
	printf '\nRESTAURATION EN ÉCHEC : %s\n' "$*" >&2
	exit 1
}

[ -n "$IDENTITE" ] || usage
[ -r "$IDENTITE" ] || echec "clé privée $IDENTITE illisible"
if [ "$DOCKER" = 1 ] && [ -n "$BASE" ]; then
	echec "--docker et --base s'excluent"
fi
[ "$DOCKER" = 1 ] || [ -n "$BASE" ] || usage
if [ -z "$DIGUEE" ]; then
	if command -v diguee >/dev/null 2>&1; then
		DIGUEE="$(command -v diguee)"
	else
		DIGUEE="$ICI/../bin/diguee"
	fi
fi
[ -x "$DIGUEE" ] || echec "exécutable diguee introuvable ($DIGUEE) : make build, ou --diguee CHEMIN"
command -v age >/dev/null || echec "age absent (https://age-encryption.org, paquet « age »)"
if [ "$DOCKER" = 1 ]; then
	command -v docker >/dev/null || echec "docker absent"
else
	for p in pg_restore psql; do
		command -v "$p" >/dev/null || echec "$p absent : pg_restore et psql (PostgreSQL 16 ou plus) sont nécessaires avec --base"
	done
fi

TRAVAIL="$(mktemp -d)"
CONTENEUR=""
nettoyer() {
	rm -rf "$TRAVAIL"
	if [ -n "$CONTENEUR" ] && [ "$GARDER" != 1 ]; then
		docker rm -f -v "$CONTENEUR" >/dev/null 2>&1 || true
	fi
}
trap nettoyer EXIT

# 1. La sauvegarde chiffrée.
if [ -z "$FICHIER" ]; then
	etape "Récupération de la sauvegarde la plus récente"
	FICHIER="$TRAVAIL/sauvegarde.pgdump.age"
	"$DIGUEE" sauvegarde recuperer -sortie "$FICHIER" || echec "récupération"
fi
[ -r "$FICHIER" ] || echec "sauvegarde $FICHIER illisible"
head -c 22 "$FICHIER" | grep -q '^age-encryption.org/v1' || echec "$FICHIER n'est pas un fichier chiffré par age"
echo "sauvegarde : $FICHIER ($(wc -c <"$FICHIER") octets)" >&2

# 2. La base vide.
if [ "$DOCKER" = 1 ]; then
	etape "Base jetable : conteneur postgres:16, données en mémoire"
	MDP="$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"
	CONTENEUR="diguee-restauration-$$"
	docker run -d --name "$CONTENEUR" --tmpfs /var/lib/postgresql/data:rw,size=4g \
		-e POSTGRES_PASSWORD="$MDP" -e POSTGRES_DB=diguee -p 127.0.0.1::5432 postgres:16 >/dev/null || echec "docker run"
	for _ in $(seq 1 60); do
		if docker exec "$CONTENEUR" pg_isready -U postgres -d diguee -h 127.0.0.1 >/dev/null 2>&1; then
			break
		fi
		sleep 1
	done
	docker exec "$CONTENEUR" pg_isready -U postgres -d diguee -h 127.0.0.1 >/dev/null || echec "PostgreSQL ne démarre pas dans le conteneur"
	PORT="$(docker port "$CONTENEUR" 5432/tcp | head -1 | sed 's/.*://')"
	BASE="postgres://postgres:$MDP@127.0.0.1:$PORT/diguee?sslmode=disable"
	psql_cible() { docker exec -i "$CONTENEUR" psql -XAtq -v ON_ERROR_STOP=1 -U postgres -d diguee "$@"; }
	pg_restore_cible() { docker exec -i "$CONTENEUR" pg_restore -U postgres -d diguee "$@"; }
else
	psql_cible() { psql -XAtq -v ON_ERROR_STOP=1 "$BASE" "$@"; }
	pg_restore_cible() { pg_restore --dbname="$BASE" "$@"; }
fi
TABLES="$(psql_cible -c "SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema()")" || echec "connexion à la base cible"
[ "$TABLES" = 0 ] || echec "la base cible n'est pas vide ($TABLES tables)"

# 3. Déchiffrement en flux et restauration.
etape "Déchiffrement (age) et restauration (pg_restore)"
debut=$(date +%s)
age --decrypt --identity "$IDENTITE" "$FICHIER" |
	pg_restore_cible --no-owner --no-acl --exit-on-error --single-transaction ||
	echec "déchiffrement ou restauration (mauvaise clé privée ? sauvegarde altérée ?)"
echo "restaurée en $(($(date +%s) - debut)) s" >&2

# 4. Contrôles.
etape "Contrôle : lignes par table, chaîne du journal public"
EXPORT="$TRAVAIL/journal-restaure.json"
"$DIGUEE" sauvegarde controler -base "$BASE" -journal "$EXPORT" || echec "contrôle de la base restaurée"
etape "Vérification indépendante du journal (diguee verifier-journal)"
"$DIGUEE" verifier-journal "$EXPORT" || echec "journal public restauré non valide"
if [ -n "$COMPARER" ]; then
	etape "Le journal restauré est-il le début exact du journal publié ?"
	"$DIGUEE" verifier-journal -precedent "$EXPORT" "$COMPARER" || echec "journal restauré différent du journal publié"
fi

printf '\nRESTAURATION RÉUSSIE : %s\n' "$(basename "$FICHIER")"
if [ "$DOCKER" = 1 ] && [ "$GARDER" = 1 ]; then
	echo "conteneur gardé : $CONTENEUR (docker rm -f -v $CONTENEUR pour le supprimer)" >&2
fi
