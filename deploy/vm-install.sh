#!/usr/bin/env bash
#
# One-command deployment onto a fresh Ubuntu VM.
#
#   curl -fsSL https://raw.githubusercontent.com/krizz711/multitenant-saas-on-kubernetes/main/deploy/vm-install.sh | sudo bash
#
# or, from a clone on the VM:
#
#   sudo bash deploy/vm-install.sh
#
# To wire up the hosted model rather than the local stub, pass the credentials
# through the environment. Use `read -s` so they never enter shell history:
#
#   read -rsp 'client id: '     GLOO_CLIENT_ID     && echo
#   read -rsp 'client secret: ' GLOO_CLIENT_SECRET && echo
#   sudo -E bash deploy/vm-install.sh
#
# Works on x86_64 and arm64 without changes: the container images are built ON
# the VM, natively. Cross-compiling from a laptop to an Oracle Ampere instance
# is the usual way an afternoon disappears, and building in place removes the
# problem rather than solving it.
#
# Everything is single-node k3s. That is not a downgrade for this project: the
# claims are about tenant isolation, scale-to-zero and CPU contention between
# neighbours, and all three are sharper on a small node than on a large one.
set -euo pipefail

REPO="${REPO:-https://github.com/krizz711/multitenant-saas-on-kubernetes.git}"
WORKDIR="${WORKDIR:-/opt/multitenant}"

say() { printf '\n\033[1;36m=== %s\033[0m\n' "$*"; }
die() { printf '\n\033[1;31mFAILED: %s\033[0m\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "run with sudo"

# The public address every tenant hostname hangs off. Detected rather than
# asked for, because the address a cloud VM sees on its own interface is
# usually a private one.
PUBLIC_IP="${PUBLIC_IP:-$(curl -fsS --max-time 10 https://api.ipify.org || true)}"
[ -n "$PUBLIC_IP" ] || die "could not determine the public IP; set PUBLIC_IP=... and re-run"
BASE_DOMAIN="${PUBLIC_IP}.nip.io"

say "deploying to ${PUBLIC_IP} (hostnames under ${BASE_DOMAIN})"

say "1/7 packages"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq git curl ca-certificates docker.io >/dev/null
systemctl enable --now docker >/dev/null 2>&1 || true

say "2/7 k3s"
if ! command -v k3s >/dev/null 2>&1; then
  # Traefik ships with k3s and is the ingress controller the chart expects.
  curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="--write-kubeconfig-mode 644" sh -
fi
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
for i in $(seq 1 60); do
  kubectl get nodes >/dev/null 2>&1 && break
  [ "$i" -eq 60 ] && die "k3s API never came up"
  sleep 3
done
kubectl wait --for=condition=Ready nodes --all --timeout=300s

say "3/7 helm"
command -v helm >/dev/null 2>&1 || \
  curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash

say "4/7 source"
if [ -d "$WORKDIR/.git" ]; then
  git -C "$WORKDIR" pull --ff-only
else
  git clone --depth 1 "$REPO" "$WORKDIR"
fi
cd "$WORKDIR"

say "5/7 images (built natively for $(uname -m))"
docker build --build-arg TARGET=api    -t multitenant/api:dev    . >/dev/null
docker build --build-arg TARGET=worker -t multitenant/worker:dev . >/dev/null
# k3s runs containerd, not Docker, so the images have to be handed over
# explicitly. Without this the pods sit in ImagePullBackOff looking for a
# registry that has never heard of them.
docker save multitenant/api:dev multitenant/worker:dev | k3s ctr images import -
docker images --format '   {{.Repository}}:{{.Tag}}  {{.Size}}' | grep multitenant

say "6/7 platform, scheduling, KEDA"
# The platform namespace is declared inside redis.yaml, but `kubectl apply -f`
# on a DIRECTORY applies its files in alphabetical order, so postgres.yaml is
# attempted first and fails on a namespace that does not exist yet. Creating
# it up front removes the dependency on filename ordering; redis.yaml then
# re-declares it harmlessly.
kubectl create namespace platform --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl apply -f deploy/k8s/platform/ >/dev/null
kubectl apply -f deploy/k8s/scheduling/ >/dev/null
kubectl -n platform rollout status deploy/redis    --timeout=300s
kubectl -n platform rollout status deploy/postgres --timeout=600s

helm repo add kedacore https://kedacore.github.io/charts >/dev/null 2>&1 || true
helm repo update >/dev/null 2>&1
# KEDA must precede the tenant charts: they declare ScaledObjects, and Helm
# refuses a manifest whose CRD does not exist yet.
helm upgrade --install keda kedacore/keda -n keda --create-namespace --wait --timeout 10m >/dev/null

say "7/7 merchants"

# The chart wants the NAME of a Secret, not a credential. Naming one here and
# creating it below is deliberate: the secretKeyRef is optional, so if the
# Secret never appears the pod still starts and silently serves stub answers.
# That is the worst failure this project can have, because the demonstration
# still looks like it works. The verify step at the end reports the model each
# tenant actually reached, so the fallback is visible.
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

# After Helm, because Helm owns the namespace and creating it first makes the
# install fail on a resource it does not think it owns. The API tolerates a
# missing Secret, so the ordering costs nothing but a restart.
if [ -n "$GLOO_SECRET_NAME" ] || [ -n "$GEMINI_SECRET_NAME" ]; then
  say "model credentials"
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

say "VERIFY"
for t in a b c; do
  printf '   tenant-%s products -> HTTP ' "$t"
  curl -s --max-time 15 -o /dev/null -w '%{http_code}\n' \
    -H "X-Tenant: tenant-$t" "http://tenant-$t.${BASE_DOMAIN}/products?limit=1"
done

# Which model actually answered. "stub" here means the credentials did not
# reach the pod - the system is up, but claim C2 cannot be demonstrated on it.
for t in a b c; do
  printf '   tenant-%s model    -> ' "$t"
  curl -s --max-time 90 -X POST -H "X-Tenant: tenant-$t" \
    -H "Content-Type: application/json" -d '{"question":"what do you sell?"}' \
    "http://tenant-$t.${BASE_DOMAIN}/ask" \
    | sed -n 's/.*"model":"\([^"]*\)".*/\1/p' | head -1
done

cat <<BANNER

  ============================================================
   OPEN THESE FROM ANY MACHINE WITH INTERNET

     http://tenant-a.${BASE_DOMAIN}/shop     electronics
     http://tenant-b.${BASE_DOMAIN}/shop     outdoor gear
     http://tenant-c.${BASE_DOMAIN}/shop     homeware

   Make sure the provider firewall allows inbound TCP 80.
   That is the single most common reason this appears broken.
  ============================================================

BANNER
