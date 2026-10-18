#!/usr/bin/env bash
#
# Runs once, when the Codespace is created. Installs the tools only; the
# cluster is built by deploy/codespaces-install.sh, which is a separate and
# explicit step because it needs credentials handed to it.
set -euo pipefail

say() { printf '\n\033[1;36m=== %s\033[0m\n' "$*"; }

say "kubectl"
if ! command -v kubectl >/dev/null 2>&1; then
  KVER="$(curl -fsSL https://dl.k8s.io/release/stable.txt)"
  curl -fsSLo /tmp/kubectl "https://dl.k8s.io/release/${KVER}/bin/linux/amd64/kubectl"
  sudo install -m 0755 /tmp/kubectl /usr/local/bin/kubectl && rm -f /tmp/kubectl
fi

say "helm"
command -v helm >/dev/null 2>&1 || \
  curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash

say "k3d"
command -v k3d >/dev/null 2>&1 || \
  curl -fsSL https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh | bash

# nginx is the Host-rewriting front door. Installed here rather than in the
# deploy script so a rerun of the deploy does not wait on apt.
say "nginx"
sudo apt-get update -qq
sudo apt-get install -y -qq nginx >/dev/null
# The stock site answers on :80 and only gets in the way here.
sudo rm -f /etc/nginx/sites-enabled/default

cat <<'BANNER'

  ============================================================
   Tools ready. Now deploy:

     read -rsp 'gloo client id: '     GLOO_CLIENT_ID     && echo
     read -rsp 'gloo client secret: ' GLOO_CLIENT_SECRET && echo
     export GLOO_CLIENT_ID GLOO_CLIENT_SECRET
     bash deploy/codespaces-install.sh

   Omit the credentials and every tenant serves stub answers,
   which looks healthy and demonstrates nothing.
  ============================================================

BANNER
