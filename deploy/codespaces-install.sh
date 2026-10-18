#!/usr/bin/env bash
#
# Deploy the whole stack inside a GitHub Codespace and expose the three
# storefronts on public URLs.
#
#   read -rsp 'gloo client id: '     GLOO_CLIENT_ID     && echo
#   read -rsp 'gloo client secret: ' GLOO_CLIENT_SECRET && echo
#   export GLOO_CLIENT_ID GLOO_CLIENT_SECRET
#   bash deploy/codespaces-install.sh
#
# Sibling of deploy/vm-install.sh and deliberately the same shape. The only
# real difference is the last mile, and it is worth stating plainly.
#
# Codespaces forwards PORTS. This architecture routes on HOSTNAMES: one
# ingress controller, one address, and the Host header decides whose
# namespace a request lands in. Those two models do not meet on their own.
#
# The obvious workaround, a kubectl port-forward straight to each tenant's
# Service, is the wrong one, and the chart already says why in
# charts/tenant/templates/ingress.yaml: a port-forward bypasses the ingress,
# and the ingress is where a request for a scaled-to-zero tenant is HELD
# while its pods start. Bypass it and claim C1 has nowhere left to happen,
# while the shop still loads and everything still looks fine.
#
# So instead one nginx front door per tenant listens on its own port, pins
# that tenant's Host header onto the request, and forwards to the real
# Traefik. The ingress and its host rules are untouched; only the way the
# hostname arrives changes.
set -euo pipefail

CLUSTER="${CLUSTER:-multitenant}"
BASE_DOMAIN="${BASE_DOMAIN:-localhost}"

say() { printf '\n\033[1;36m=== %s\033[0m\n' "$*"; }
die() { printf '\n\033[1;31mFAILED: %s\033[0m\n' "$*" >&2; exit 1; }

cd "$(dirname "$0")/.."
command -v docker >/dev/null 2>&1 || die "no docker; is this a Codespace with the docker-in-docker feature?"
command -v k3d    >/dev/null 2>&1 || die "no k3d; run .devcontainer/post-create.sh first"

say "1/8 cluster"
if k3d cluster list 2>/dev/null | grep -q "^${CLUSTER}[[:space:]]"; then
  echo "   ${CLUSTER} exists, reusing it"
  k3d cluster start "$CLUSTER" >/dev/null 2>&1 || true
else
  # Single node, unlike the two agents in `make up`. A Codespace pays for
  # every node as another container, and the claims are all node-local
  # anyway: CPU shares are a cgroup weight enforced on ONE node, so
  # contention split across nodes is contention that never happens.
  k3d cluster create "$CLUSTER" -p "8080:80@loadbalancer" --wait
fi
kubectl config use-context "k3d-${CLUSTER}" >/dev/null
kubectl wait --for=condition=Ready nodes --all --timeout=300s

say "2/8 images"
docker build --build-arg TARGET=api    -t multitenant/api:dev    . >/dev/null
docker build --build-arg TARGET=worker -t multitenant/worker:dev . >/dev/null
k3d image import multitenant/api:dev multitenant/worker:dev -c "$CLUSTER" >/dev/null
docker images --format '   {{.Repository}}:{{.Tag}}  {{.Size}}' | grep multitenant

say "3/8 platform"
# The platform namespace is declared inside redis.yaml, but `kubectl apply -f`
# on a DIRECTORY applies its files in alphabetical order, so postgres.yaml is
# attempted first and fails on a namespace that does not exist yet. Creating
# it up front removes the dependency on filename ordering; redis.yaml then
# re-declares it harmlessly.
kubectl create namespace platform --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl apply -f deploy/k8s/platform/   >/dev/null
kubectl apply -f deploy/k8s/scheduling/ >/dev/null
kubectl -n platform rollout status deploy/redis    --timeout=300s
kubectl -n platform rollout status deploy/postgres --timeout=600s

say "4/8 KEDA"
# stdout stays quiet, but stderr is deliberately NOT swallowed here. Hiding it
# meant a failed `helm repo update` printed nothing whatsoever and set -e ended
# the whole run on a bare prompt, with the last line on screen being the step
# banner. An installer that dies silently is worse than one that is noisy.
helm repo add kedacore https://kedacore.github.io/charts >/dev/null || true
helm repo update >/dev/null || die "helm repo update failed - its error is above"
# Before the tenants: they declare ScaledObjects, and Helm refuses a manifest
# whose CRD does not exist yet.
helm upgrade --install keda kedacore/keda -n keda --create-namespace \
  --wait --timeout 10m >/dev/null || die "KEDA install failed - its error is above"

say "5/8 merchants"
GLOO_SECRET_NAME=""
if [ -n "${GLOO_CLIENT_ID:-}" ] && [ -n "${GLOO_CLIENT_SECRET:-}" ]; then
  GLOO_SECRET_NAME="gloo-credentials"
fi
GEMINI_SECRET_NAME=""
if [ -n "${GEMINI_API_KEY:-}" ]; then
  GEMINI_SECRET_NAME="gemini-credentials"
fi

for t in a b c; do
  tier=standard; [ "$t" = "b" ] && tier=premium
  helm upgrade --install "tenant-$t" deploy/charts/tenant \
    --set "tenant=tenant-$t" --set "tier=$tier" \
    --set "baseDomain=${BASE_DOMAIN}" \
    ${GLOO_SECRET_NAME:+--set glooSecret=$GLOO_SECRET_NAME} \
    ${GEMINI_SECRET_NAME:+--set geminiSecret=$GEMINI_SECRET_NAME} >/dev/null
  echo "   tenant-$t ($tier)"
done

say "6/8 model credentials"
if [ -n "$GLOO_SECRET_NAME" ] || [ -n "$GEMINI_SECRET_NAME" ]; then
  # After Helm, because Helm owns the namespace and creating it first makes
  # the install fail on a resource it does not think it owns. The API
  # tolerates a missing Secret, so the ordering costs a restart and nothing
  # else.
  for t in a b c; do
    ns="tenant-$t"
    if [ -n "$GLOO_SECRET_NAME" ]; then
      kubectl -n "$ns" create secret generic "$GLOO_SECRET_NAME" \
        --from-literal=clientId="$GLOO_CLIENT_ID" \
        --from-literal=clientSecret="$GLOO_CLIENT_SECRET" \
        --dry-run=client -o yaml | kubectl apply -f - >/dev/null
    fi
    if [ -n "$GEMINI_SECRET_NAME" ]; then
      kubectl -n "$ns" create secret generic "$GEMINI_SECRET_NAME" \
        --from-literal=apiKey="$GEMINI_API_KEY" \
        --dry-run=client -o yaml | kubectl apply -f - >/dev/null
    fi
    kubectl -n "$ns" rollout restart deploy/api >/dev/null
    echo "   $ns credentials installed"
  done
else
  printf '   \033[1;33mNONE SUPPLIED - every tenant will serve stub answers.\033[0m\n'
  printf '   The shops will look healthy and claim C2 will measure nothing.\n'
fi

for t in a b c; do kubectl -n "tenant-$t" rollout status deploy/api --timeout=300s; done

say "waiting for catalogues"
for t in a b c; do
  for i in $(seq 1 40); do
    kubectl -n "tenant-$t" logs deploy/api --tail=40 2>/dev/null | grep -q "catalogue ready" && break
    sleep 6
  done
  echo "   tenant-$t $(kubectl -n "tenant-$t" logs deploy/api --tail=40 2>/dev/null | grep -q 'catalogue ready' && echo ready || echo 'NOT READY')"
done

say "7/8 per-tenant front doors"
# One listener per tenant, each pinning its own Host header onto the request
# before handing it to the real ingress on :8080. The read timeout is long
# because a cold model call can legitimately take a minute.
CONF="$(mktemp)"
PORT=8081
for t in a b c; do
  cat >> "$CONF" <<NGINX
server {
    listen ${PORT};
    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host tenant-${t}.${BASE_DOMAIN};
        proxy_set_header X-Tenant tenant-${t};
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_http_version 1.1;
        proxy_read_timeout 300s;
        proxy_send_timeout 300s;
    }
}
NGINX
  echo "   :${PORT}  ->  tenant-${t}.${BASE_DOMAIN}"
  PORT=$((PORT + 1))
done
sudo cp "$CONF" /etc/nginx/conf.d/tenants.conf
rm -f "$CONF"
sudo nginx -t >/dev/null 2>&1 || die "nginx rejected the generated config"
sudo nginx -s reload >/dev/null 2>&1 || sudo nginx

say "8/8 publishing ports"
# Codespaces forwards ports privately by default, which means the lab PC
# meets a GitHub login wall instead of a shop. Public is what makes the URL
# openable from any machine. If the CLI token lacks the scope, the Ports
# panel does the same job by hand.
PUBLISHED=no
if [ -z "${CODESPACE_NAME:-}" ]; then
  echo "   not inside a Codespace, nothing to publish"
  PUBLISHED=n/a
elif ! command -v gh >/dev/null 2>&1; then
  # This branch used to be folded into the condition, so a missing gh skipped
  # the whole step in silence and the banner below still printed URLs that
  # could not work. Say so instead.
  printf '   \033[1;33mthe gh CLI is not installed, so the ports are still PRIVATE\033[0m\n'
elif gh codespace ports visibility 8081:public 8082:public 8083:public \
       -c "$CODESPACE_NAME" >/dev/null 2>&1; then
  echo "   8081, 8082, 8083 are public"
  PUBLISHED=yes
else
  printf '   \033[1;33mthe gh CLI could not set visibility\033[0m\n'
fi

if [ "$PUBLISHED" = "no" ]; then
  printf '   Open the PORTS panel, right-click each of 8081-8083, then\n'
  printf '   Port Visibility -> Public. Until you do, the URLs below will\n'
  printf '   not resolve from another machine.\n'
fi

say "VERIFY"
for t in a b c; do
  printf '   tenant-%s products -> HTTP ' "$t"
  curl -s --max-time 15 -o /dev/null -w '%{http_code}\n' \
    -H "Host: tenant-$t.${BASE_DOMAIN}" -H "X-Tenant: tenant-$t" \
    "http://localhost:8080/products?limit=1"
done

# "stub" here means the credentials never reached the pod. The system is up,
# but C2 cannot be demonstrated on it.
for t in a b c; do
  printf '   tenant-%s model    -> ' "$t"
  curl -s --max-time 90 -X POST \
    -H "Host: tenant-$t.${BASE_DOMAIN}" -H "X-Tenant: tenant-$t" \
    -H "Content-Type: application/json" -d '{"question":"what do you sell?"}' \
    "http://localhost:8080/ask" \
    | sed -n 's/.*"model":"\([^"]*\)".*/\1/p' | head -1
done

DOM="${GITHUB_CODESPACES_PORT_FORWARDING_DOMAIN:-app.github.dev}"
if [ -n "${CODESPACE_NAME:-}" ]; then
  cat <<BANNER

  ============================================================
   OPEN THESE FROM ANY MACHINE WITH INTERNET

     https://${CODESPACE_NAME}-8081.${DOM}/shop   electronics
     https://${CODESPACE_NAME}-8082.${DOM}/shop   outdoor gear
     https://${CODESPACE_NAME}-8083.${DOM}/shop   homeware

   If a URL shows a GitHub sign-in page, that port is still
   private. PORTS panel, right-click, Visibility, Public.

   The Codespace stops after 30 idle minutes and the shops go
   with it. Restart it and re-run this script before a demo.
  ============================================================

BANNER
fi
