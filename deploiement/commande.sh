#!/usr/bin/env bash
# Lance une commande de diguee sur le serveur, dans le même contexte que le
# service (utilisateur diguee, /etc/diguee/diguee.env, certificat du KMS
# remis par systemd), sans jamais écrire les secrets ailleurs. Installé dans
# /usr/local/sbin/diguee-commande (ticket #7). Exemples :
#
#   sudo diguee-commande verifier-config
#   sudo diguee-commande creer-membre -identifiant awa.diop -nom "Awa Diop" -roles administrateur,redacteur,relecteur
#   sudo diguee-commande publier
#   sudo diguee-commande ancrer
#
# Les migrations passent par « systemctl start diguee-migrer » (rôle
# propriétaire), jamais par ce script.
set -euo pipefail
if [ $# -eq 0 ] || [ "$1" = migrer ] || [ "$1" = servir ]; then
	sed -n '2,13p' "$0" | sed 's/^# \{0,1\}//'
	exit 2
fi
NOM="diguee-commande-$$"
CRED="/run/credentials/$NOM.service"
exec systemd-run --quiet --pty --wait --collect --unit="$NOM" \
	--property=User=diguee --property=Group=diguee \
	--property=EnvironmentFile=/etc/diguee/diguee.env \
	--property=LoadCredential=kms-client.pem:/etc/diguee/kms-client.pem \
	--property=LoadCredential=kms-client.key:/etc/diguee/kms-client.key \
	--property=NoNewPrivileges=yes --property=ProtectSystem=strict --property=ProtectHome=yes \
	--property=PrivateTmp=yes \
	--setenv=DIGUEE_ENV=production --setenv=DIGUEE_TLS=acme \
	--setenv=DIGUEE_ACME_CACHE=/srv/diguee-donnees/diguee/acme \
	--setenv=DIGUEE_OVH_KMS_CERTIFICAT="$CRED/kms-client.pem" \
	--setenv=DIGUEE_OVH_KMS_CLE_CLIENT="$CRED/kms-client.key" \
	/opt/diguee/diguee "$@"
