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

# The test is `helm version`, not `command -v helm`. Piping the upstream
# installer to bash as a non-root user leaves the binary in place but without
# the execute bit, and `command -v` happily finds a file it cannot run. That
# reported the tool as present and then failed four steps later inside the
# deploy, with a permission error nobody was looking for.
say "helm"
if ! helm version >/dev/null 2>&1; then
  curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | sudo bash
  sudo chmod 755 /usr/local/bin/helm 2>/dev/null || true
  helm version >/dev/null 2>&1 || { echo "helm still not runnable" >&2; exit 1; }
fi

say "k3d"
if ! k3d version >/dev/null 2>&1; then
  curl -fsSL https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh | sudo bash
  sudo chmod 755 /usr/local/bin/k3d 2>/dev/null || true
  k3d version >/dev/null 2>&1 || { echo "k3d still not runnable" >&2; exit 1; }
fi

# nginx is the Host-rewriting front door. Installed here rather than in the
# deploy script so a rerun of the deploy does not wait on apt.
say "nginx"
sudo apt-get update -qq
sudo apt-get install -y -qq nginx >/dev/null
# The stock site answers on :80 and only gets in the way here.
sudo rm -f /etc/nginx/sites-enabled/default

# Prove every tool RUNS, rather than merely existing on PATH.
say "verifying"
for t in docker kubectl helm k3d; do
  printf '   %-8s ' "$t"
  if "$t" version >/dev/null 2>&1; then echo ok; else echo "NOT RUNNABLE"; fi
done
printf '   %-8s ' nginx
nginx -v >/dev/null 2>&1 && echo ok || echo "NOT RUNNABLE"

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
