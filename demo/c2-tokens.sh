#!/usr/bin/env bash
# C2 - what a repeated question costs.
#
# Model tokens dominate operating cost. The gateway is the only route to the
# model, so it is the only place a token can be refused. This asks one question
# three times and reports what each one bought.
#
# The tenant caches are cleared first so the run starts from a known state and
# is reproducible; otherwise the first ask would already be a hit.
set -u
Q="${1:-Which jacket works in heavy rain?}"
# Both overridable so the same script runs against a public VM, where Traefik
# listens on port 80 and the tenant hostnames hang off <IP>.nip.io rather than
# .localhost. Routing is by Host header either way, so ENDPOINT can stay on the
# loopback address and nothing has to resolve in DNS for this to work:
#
#   DOMAIN=1.2.3.4.nip.io ENDPOINT=http://localhost/ask bash demo/c2-tokens.sh
DOMAIN="${DOMAIN:-localhost}"
ENDPOINT="${ENDPOINT:-http://localhost:8080/ask}"

ask() { # tenant -> "cached prompt output seconds"
  local t="$1"
  curl -s --max-time 120 -w ' %{time_total}' \
    -H "Host: $t.$DOMAIN" -H "X-Tenant: $t" -H "Content-Type: application/json" \
    -d "$(python -c 'import json,sys; print(json.dumps({"question": sys.argv[1]}))' "$Q")" \
    "$ENDPOINT" | python -c '
import json, sys
body, _, secs = sys.stdin.read().rpartition(" ")
d = json.loads(body)
print(d.get("cached"), d.get("prompt_tokens", 0), d.get("output_tokens", 0), float(secs))
'
}

row() { # label tenant cached prompt output seconds
  python - "$@" <<'PYROW'
import sys
label, tenant, cached, pt, ot, secs = sys.argv[1:7]
pt, ot, secs = int(pt), int(ot), float(secs)
hit = cached == "True"
cost = "0 tokens bought" if hit else f"{pt}+{ot} = {pt+ot} tokens"
print(f"  {label:<18s} {tenant:<8s}  cached {'YES' if hit else 'no '}  {cost:<22s} {secs:5.2f} s")
PYROW
}

kubectl -n platform exec deploy/redis -- redis-cli DEL cache:tenant-b cache:tenant-a >/dev/null 2>&1
echo "# question: \"$Q\""
echo

A=($(ask tenant-b)); B=($(ask tenant-b)); C=($(ask tenant-a))
row "first ask"          tenant-b "${A[@]}"
row "the same question"  tenant-b "${B[@]}"
row "another merchant"   tenant-a "${C[@]}"

python - "${A[1]}" "${A[2]}" "${A[3]}" "${B[3]}" <<'PYSUM'
import sys
pt, ot, t1, t2 = int(sys.argv[1]), int(sys.argv[2]), float(sys.argv[3]), float(sys.argv[4])
print()
print(f"  the repeat bought 0 of {pt+ot} tokens and returned in {t2:.2f} s,")
print(f"  against {t1:.2f} s paid for - {t1/max(t2,1e-6):.0f}x faster, for no money.")
print()
print("  the third row is a MISS on purpose: a cached answer carries one")
print("  merchant's catalogue, so the cache key includes the tenant.")
PYSUM
