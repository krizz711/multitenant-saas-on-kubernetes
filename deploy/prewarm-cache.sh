#!/usr/bin/env bash
#
# Pre-warm the semantic cache with real model answers.
#
#   bash deploy/prewarm-cache.sh
#
# Run this WITH internet, before a demonstration that will not have any.
#
# Why this is legitimate rather than a cheat: claim C2 says a repeated
# question costs nothing, and that is exactly what is being shown. The answers
# are genuine model output, bought once and paid for in tokens. Serving them
# from cache afterwards at zero cost IS the claim. The only thing pre-warming
# changes is which side of the demonstration the purchase happened on, and the
# per-tenant cache in Redis now survives a restart so it is still there later.
set -u

INGRESS="${INGRESS:-http://localhost:8080}"
TENANTS="${TENANTS:-tenant-a tenant-b}"

# The questions to warm. Keep these identical to the ones used live - the
# cache matches on the whole grounded prompt, so a different wording is a
# different entry and will miss.
QUESTIONS_A=(
  "Which headphones are best for travel?"
  "What is the cheapest keyboard you have?"
  "Do you sell anything with fast charging?"
)
QUESTIONS_B=(
  "Which jacket works in heavy rain?"
  "What should I wear for a cold hike?"
  "Do you have waterproof boots?"
)

ask() { # tenant, question -> prints cached flag
  curl -s --max-time 60 -X POST \
    -H "Host: $1.localhost" -H "X-Tenant: $1" -H "Content-Type: application/json" \
    -d "{\"question\":$(printf '%s' "$2" | python -c 'import json,sys; print(json.dumps(sys.stdin.read()))')}" \
    "$INGRESS/ask"
}

warm_one() {
  local tenant="$1" q="$2"
  printf '  %-44.44s ' "$q"

  local first
  first=$(ask "$tenant" "$q")
  local model
  model=$(printf '%s' "$first" | python -c 'import json,sys; d=json.load(sys.stdin); print(d.get("model","?"))' 2>/dev/null)

  if printf '%s' "$model" | grep -qi "stub"; then
    echo "STUB - no real answer (check credentials / network)"
    return 1
  fi

  # Second ask must hit, or the entry did not store and the lab will call out.
  local second cached
  second=$(ask "$tenant" "$q")
  cached=$(printf '%s' "$second" | python -c 'import json,sys; print(json.load(sys.stdin).get("cached"))' 2>/dev/null)

  if [ "$cached" = "True" ] || [ "$cached" = "true" ]; then
    echo "warmed  ($model)"
    return 0
  fi
  echo "NOT CACHED - would re-call the model live"
  return 1
}

fail=0
for t in $TENANTS; do
  echo ""
  echo "=== $t ==="
  if [ "$t" = "tenant-a" ]; then qs=("${QUESTIONS_A[@]}"); else qs=("${QUESTIONS_B[@]}"); fi
  for q in "${qs[@]}"; do warm_one "$t" "$q" || fail=1; done
done

echo ""
if [ "$fail" -eq 0 ]; then
  echo "All questions warmed with real model answers."
  echo "They will serve from cache offline, at zero tokens."
else
  echo "SOME QUESTIONS DID NOT WARM - see above before relying on this."
fi
exit $fail
