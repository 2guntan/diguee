#!/usr/bin/env bash
# Essai des scripts de déploiement dans un conteneur Ubuntu 24.04 (Docker,
# privilégié pour cryptsetup et nftables), sans systemd ni KMS : voir
# deploiement/README.md, « Ce qui a été essayé ». À lancer à la racine du
# dépôt : deploiement/essais/conteneur.sh
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 make build >/dev/null
docker run --rm --privileged \
	-v "$PWD:/depot:ro" \
	ubuntu:24.04 bash /depot/deploiement/essais/dans-le-conteneur.sh
