#!/usr/bin/env bash
#
# Bring the demo back after the Codespace has been stopped, without
# reinstalling anything.
#
#   bash deploy/codespaces-resume.sh
#
# A Codespace stops itself after its idle timeout. Its filesystem survives, so
# the k3d cluster, the images, the Helm releases and the model credentials are
# all still there - but nothing is running, and the Cloudflare quick tunnels
# are gone for good, along with their hostnames.
#
# Re-running codespaces-install.sh would also work, but only if the
# credentials are typed in again. Run without them, `helm upgrade` resets the
# chart's glooSecret value to empty, the API pods lose their model
# credentials, and every tenant quietly falls back to stub answers while the
# shops still look healthy. This script never calls Helm, so that cannot
# happen here.
set -euo pipefail

CLUSTER="${CLUSTER:-multitenant}"
BASE_DOMAIN="${BASE_DOMAIN:-localhost}"

say() { printf '\n\033[1;36m=== %s\033[0m\n' "$*"; }
die() { printf '\n\033[1;31mFAILED: %s\033[0m\n' "$*" >&2; exit 1; }

cd "$(dirname "$0")/.."

say "1/5 docker"
for _ in $(seq 1 30); do
  docker info >/dev/null 2>&1 && break
  sleep 2
done
docker info >/dev/null 2>&1 || die "the Docker daemon is not running in this Codespace"

say "2/5 cluster"
k3d cluster list 2>/dev/null | grep -q "^${CLUSTER}[[:space:]]" \
  || die "no cluster named ${CLUSTER} - this Codespace was rebuilt, so run deploy/codespaces-install.sh"
k3d cluster start "$CLUSTER" >/dev/null 2>&1 || true
k3d kubeconfig merge "$CLUSTER" --kubeconfig-switch-context >/dev/null
kubectl wait --for=condition=Ready nodes --all --timeout=300s

say "3/5 tenants"
kubectl -n platform rollout status deploy/postgres --timeout=300s
kubectl -n platform rollout status deploy/redis    --timeout=300s
for t in a b c; do kubectl -n "tenant-$t" rollout status deploy/api --timeout=300s; done

# Still wired to the hosted model? If someone re-ran the installer without
# credentials, the API pods no longer carry them, and this is the place to
# find out rather than in front of an examiner.
for t in a b c; do
  envs="$(kubectl -n "tenant-$t" get deploy api \
            -o jsonpath='{.spec.template.spec.containers[0].env[*].name}')"
  case "$envs" in
    *GLOO_CLIENT_ID*|*GEMINI_API_KEY*) echo "   tenant-$t  model credentials wired" ;;
    *) printf '   tenant-%s  \033[1;33mNO model credentials - answers will be stubs\033[0m\n' "$t" ;;
  esac
done

say "4/5 front doors"
[ -f /etc/nginx/conf.d/tenants.conf ] \
  || die "nginx front-door config missing - run deploy/codespaces-install.sh"
sudo nginx -t >/dev/null 2>&1 || die "nginx rejected its config"
sudo nginx -s reload >/dev/null 2>&1 || sudo nginx
for p in 8081 8082 8083; do
  code=000
  for _ in $(seq 1 30); do
    code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "http://localhost:$p/shop" || true)"
    [ "$code" = "200" ] && break
    sleep 2
  done
  echo "   :$p  HTTP $code"
  [ "$code" = "200" ] || die "front door :$p is not answering"
done

say "5/5 public links"
exec bash deploy/codespaces-tunnel.sh
