#!/usr/bin/env bash
#
# Public URLs for the three storefronts through Cloudflare quick tunnels, for
# networks that refuse to resolve *.app.github.dev.
#
#   bash deploy/codespaces-tunnel.sh
#
# Some campus resolvers refuse app.github.dev specifically while resolving
# github.dev and trycloudflare.com normally. The Codespace's own forwarded URLs
# then fail with NXDOMAIN even though the ports are public and serving, which
# looks exactly like a broken deployment. A quick tunnel publishes the same
# nginx front doors under trycloudflare.com instead.
#
# Run it INSIDE the Codespace. A tunnel lives as long as the machine running
# it, and one started on a laptop dies the moment the laptop leaves the
# network - which is the situation in the lab.
set -euo pipefail

say() { printf '\n\033[1;36m=== %s\033[0m\n' "$*"; }
die() { printf '\n\033[1;31mFAILED: %s\033[0m\n' "$*" >&2; exit 1; }

if ! cloudflared --version >/dev/null 2>&1; then
  say "installing cloudflared"
  curl -fsSL https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64 -o /tmp/cloudflared
  sudo install -m 0755 /tmp/cloudflared /usr/local/bin/cloudflared
  rm -f /tmp/cloudflared
fi

# Tunnels from an earlier run would otherwise keep their old URLs alive next
# to the new ones, and it is not obvious which set is current.
pkill -f 'cloudflared tunnel' 2>/dev/null || true
sleep 1

say "opening tunnels"
for p in 8081 8082 8083; do
  curl -s -o /dev/null --max-time 5 "http://localhost:$p/shop" \
    || die "nothing answering on :$p - run deploy/codespaces-install.sh first"
  nohup cloudflared tunnel --no-autoupdate --url "http://localhost:$p" \
    > "/tmp/tunnel-$p.log" 2>&1 &
done

declare -A NAME=([8081]="electronics" [8082]="outdoor gear" [8083]="homeware")
declare -A URL=()

say "waiting for Cloudflare to assign hostnames"
for p in 8081 8082 8083; do
  for _ in $(seq 1 30); do
    URL[$p]="$(grep -o 'https://[a-z0-9-]*\.trycloudflare\.com' "/tmp/tunnel-$p.log" | head -1 || true)"
    [ -n "${URL[$p]}" ] && break
    sleep 1
  done
  [ -n "${URL[$p]}" ] || die "no URL for :$p after 30s - see /tmp/tunnel-$p.log"
done

# Checked through Cloudflare and back rather than on localhost, because a
# local check passing while the public path failed is exactly how tonight
# went wrong once already. A new hostname can take a few seconds to resolve.
say "checking each URL from outside"
for p in 8081 8082 8083; do
  code=000
  for _ in $(seq 1 15); do
    code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 "${URL[$p]}/shop" || true)"
    [ "$code" = "200" ] && break
    sleep 2
  done
  printf '   :%s  HTTP %s\n' "$p" "$code"
done

cat <<BANNER

  ============================================================
   OPEN THESE FROM ANY MACHINE WITH INTERNET

     ${URL[8081]}/shop   ${NAME[8081]}
     ${URL[8082]}/shop   ${NAME[8082]}
     ${URL[8083]}/shop   ${NAME[8083]}

   They last as long as this Codespace stays awake, and change
   every time this script runs. Re-run it after any restart.
  ============================================================

BANNER
