# Deploying to GitHub Codespaces

A public URL for the three storefronts with **no credit card and no student
verification** — just the GitHub account that already owns this repository.

This is the path that cannot get blocked on paperwork. Oracle wants a card;
Azure for Students wants academic verification. Codespaces wants neither.

Budget about 15 minutes, almost all of it the two Go images building.

---

## What it costs

| | Core-hours / month | On a 4-core machine |
|---|---|---|
| Any GitHub account | 120 | 30 hours |
| Verified student | 180 | 45 hours |

Storage is 15 GB free. Nothing is charged without a payment method on file —
when the quota is gone the Codespace simply will not start until the month
rolls over, so the failure mode is an inconvenience, not a bill.

**Four cores is not comfort.** On the 2-core default, the pod requests for
three tenants plus Postgres, Redis, KEDA and the k3s system pods add up to
more than the node can allocate, and pods sit `Pending` forever while looking
exactly like a deployment bug. `.devcontainer/devcontainer.json` asks for 4
via `hostRequirements`, so the machine picker defaults correctly.

---

## 1. Create the Codespace

On the repository page: **Code → Codespaces → Create codespace on main**.

It reads `.devcontainer/devcontainer.json`, which brings up a Debian container
with Docker inside it, then runs `post-create.sh` to install kubectl, Helm,
k3d and nginx. Two or three minutes, once.

## 2. Deploy

In the Codespace terminal:

```bash
read -rsp 'gloo client id: '     GLOO_CLIENT_ID     && echo
read -rsp 'gloo client secret: ' GLOO_CLIENT_SECRET && echo
export GLOO_CLIENT_ID GLOO_CLIENT_SECRET
bash deploy/codespaces-install.sh
```

`read -rsp` keeps the credentials out of shell history and off the process
list. Omit them and every tenant quietly serves stub answers — the shops will
look completely healthy and claim C2 will be measuring nothing.

## 3. Check the model line

The script ends by printing which model actually answered, per tenant:

```
   tenant-a model    -> gloo-auto
   tenant-b model    -> gloo-auto
   tenant-c model    -> gloo-auto
```

**If any line says `stub`, stop and fix it before demonstrating anything.**

```bash
kubectl -n tenant-b get secret gloo-credentials
kubectl -n tenant-b rollout restart deploy/api
```

## 4. Open the shops

The script prints three URLs of the form:

```
https://<codespace-name>-8081.app.github.dev/shop   electronics
https://<codespace-name>-8082.app.github.dev/shop   outdoor gear
https://<codespace-name>-8083.app.github.dev/shop   homeware
```

These work from any machine with internet, including a lab PC.

**If a URL shows a GitHub sign-in page instead of a shop**, that port is still
private. Open the **PORTS** panel, right-click the port, then **Port
Visibility → Public**. The script tries to do this with the `gh` CLI, but the
Codespace token does not always carry the scope.

---

## How the tenant hostnames survive port forwarding

Worth understanding, because it is the one real difference from the VM path
and a reviewer may well ask.

Codespaces forwards **ports**. This architecture routes on **hostnames**: one
ingress controller, one address, and the `Host` header decides whose namespace
a request lands in. Those two models do not meet on their own.

The tempting workaround is `kubectl port-forward` straight to each tenant's
Service. **It is the wrong one**, and
[`charts/tenant/templates/ingress.yaml`](charts/tenant/templates/ingress.yaml)
already explains why: a port-forward bypasses the ingress, and the ingress is
where a request for a scaled-to-zero tenant is *held* while its pods start.
Bypass it and claim C1 has nowhere left to happen — while the shop still
loads and everything still looks fine. That is the worst kind of broken.

So instead the script puts one nginx front door per tenant on its own port:

```
:8081  ->  Host: tenant-a.localhost  ->  Traefik :8080  ->  tenant-a namespace
:8082  ->  Host: tenant-b.localhost  ->  Traefik :8080  ->  tenant-b namespace
:8083  ->  Host: tenant-c.localhost  ->  Traefik :8080  ->  tenant-c namespace
```

Every request still goes through the real ingress, still matches a real host
rule, and still lands in the right namespace by hostname. The ingress and its
rules are completely untouched. The only thing that changed is how the
hostname gets attached to a request that arrived on a port.

This is the same trick `curl --http-host-header` and
`cloudflared --http-host-header` use, written out in nginx so it is visible.

## The demonstrations

Run these in the Codespace terminal. Because `baseDomain` is `localhost` and
Traefik is on `:8080`, both scripts work on their defaults — no overrides,
exactly as on a development laptop:

```bash
bash demo/c1-idle.sh tenant-b
bash demo/c2-tokens.sh
```

## Caveats

- **It stops after 30 idle minutes**, and the shops and tunnels go with it.
  Reopen the Codespace and run `bash deploy/codespaces-resume.sh`: it restarts
  the cluster and the front doors, checks the model credentials are still
  wired, and prints fresh public links. Do not re-run the installer without
  credentials - `helm upgrade` would reset `glooSecret` and every tenant would
  silently fall back to stub answers. The idle timeout can be raised to 240
  minutes under github.com/settings/codespaces before a demo.
- **The URL changes if you delete and recreate the Codespace.** It is derived
  from the Codespace name.
- **Single node**, like the VM path, rather than the three-node k3d cluster
  the report's measurements came from. Tenant isolation, scale-to-zero and CPU
  contention are all node-local mechanisms, so all three still demonstrate
  correctly — CPU shares are a cgroup weight enforced on one node, and
  contention split across nodes is contention that never happens.

## See also

- [`AZURE.md`](AZURE.md) — a real VM, no card, needs academic verification.
- [`ORACLE.md`](ORACLE.md) — a real VM, always-free, needs a card.
