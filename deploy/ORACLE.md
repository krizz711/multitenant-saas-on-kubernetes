# Deploying to Oracle Cloud Always Free

A public URL for the three storefronts, on a VM that costs nothing, so the
system can be opened from any machine rather than from the laptop it was
built on.

Budget about 45 minutes, most of which is the two container images compiling
on a 2-core ARM node.

---

## What "Always Free" actually gives you

Oracle halved the Ampere allowance in August 2026. The figure in most
tutorials online — 4 OCPU and 24 GB — is out of date and an instance that
size will be refused, or terminated if it already exists.

| | Always Free, current |
|---|---|
| Ampere A1 (`VM.Standard.A1.Flex`, ARM) | **2 OCPU, 12 GB RAM** |
| AMD micro (`VM.Standard.E2.1.Micro`, x86) | 1/8 OCPU, 1 GB RAM, two of them |
| Block storage | 200 GB total |
| Outbound transfer | 10 TB/month |

**Take the Ampere A1 at 2 OCPU / 12 GB.** The AMD micro instances cannot run
this stack — 1 GB of RAM will not hold k3s, PostgreSQL, Redis, KEDA and three
tenants, and the pods will be OOM-killed in a loop.

2 OCPU is enough. The stack idles at roughly 3 GB and about 1.4 of the 2
cores in scheduled requests. A smaller node also makes claim C3 sharper, not
weaker: CPU shares are a cgroup weight, and contention between a batch job
and an interactive request is easier to provoke on two cores than on twelve.

---

## 1. Account

Sign up at <https://cloud.oracle.com>. A card is required for identity
verification and a small refundable hold is placed on it; Always Free
resources do not charge it.

**Choose the home region carefully — it cannot be changed afterwards.** The
nearest region is not always the best choice, because the busiest regions are
the ones that run out of Ampere capacity.

## 2. An SSH key, on your laptop

```bash
ssh-keygen -t ed25519 -f ~/.ssh/oracle -N ""
cat ~/.ssh/oracle.pub
```

Paste the public key into the instance creation form. Keep the private key.

## 3. The instance

Compute → Instances → Create instance.

- **Image:** Canonical Ubuntu 24.04
- **Shape:** `VM.Standard.A1.Flex`, **2 OCPUs, 12 GB** memory
- **SSH key:** paste `~/.ssh/oracle.pub`
- **Public IPv4 address:** assign one

Write down the public IP.

### If it says "Out of host capacity"

This is the single most common obstacle and it is not a mistake on your part
— the free Ampere pool in that region is genuinely full. In order of effort:

1. Change the **Availability Domain** in the create form and try again.
2. Retry later. Capacity is released continuously.
3. Upgrade the tenancy to **Pay As You Go**. Always Free resources stay free
   on a PAYG account, and PAYG tenancies get priority for Ampere capacity.
   This is the reliable fix, and it is what to do if a deadline is close.

## 4. Open port 80 — in both places

Traffic has to pass two independent firewalls. Opening only one is the usual
reason a correct deployment looks broken.

**a) The virtual network,** in the console: Networking → Virtual Cloud
Networks → your VCN → the public subnet → its Security List → Add Ingress
Rule.

| Field | Value |
|---|---|
| Source CIDR | `0.0.0.0/0` |
| IP protocol | TCP |
| Destination port | `80` |

**b) The VM's own iptables.** Oracle's Ubuntu image ships with rules that
drop everything except SSH. This surprises nearly everyone:

```bash
sudo iptables -I INPUT 6 -m state --state NEW -p tcp --dport 80 -j ACCEPT
sudo netfilter-persistent save
```

## 5. Install

```bash
ssh -i ~/.ssh/oracle ubuntu@<PUBLIC_IP>
```

Read the credentials in rather than typing them on the command line, so they
never enter shell history or the process list:

```bash
read -rsp 'gloo client id: '     GLOO_CLIENT_ID     && echo
read -rsp 'gloo client secret: ' GLOO_CLIENT_SECRET && echo
export GLOO_CLIENT_ID GLOO_CLIENT_SECRET

curl -fsSL https://raw.githubusercontent.com/krizz711/multitenant-saas-on-kubernetes/main/deploy/vm-install.sh -o vm-install.sh
sudo -E bash vm-install.sh
```

`sudo -E` is load-bearing: without it the credentials do not survive into the
installer and every tenant falls back to the local stub.

Omit the two `read` lines to run on the stub deliberately. Everything works
except claim C2, which is about the cost of real model tokens and cannot be
demonstrated against a stub.

The installer detects the public IP and hangs every tenant hostname off
`<IP>.nip.io`, a wildcard DNS service that resolves `anything.1.2.3.4.nip.io`
back to `1.2.3.4`. No domain to buy and no DNS records to create, while the
tenants still get distinct hostnames — which matters, because host-based
routing is what separates them at the edge.

## 6. Check it

The installer ends by printing the three URLs and, for each tenant, the model
that actually answered:

```
   tenant-a model    -> gloo-auto
   tenant-b model    -> gloo-auto
   tenant-c model    -> gloo-auto
```

**If any line says `stub`, the credentials did not reach the pod.** The site
will look completely healthy and claim C2 will be measuring nothing. Fix it
with:

```bash
sudo kubectl -n tenant-b get secret gloo-credentials
sudo kubectl -n tenant-b rollout restart deploy/api
```

Then open, from any machine:

```
http://tenant-a.<IP>.nip.io/shop     electronics
http://tenant-b.<IP>.nip.io/shop     outdoor gear
http://tenant-c.<IP>.nip.io/shop     homeware
```

## 7. The demonstrations, on the VM

```bash
cd /opt/multitenant
sudo KUBECONFIG=/etc/rancher/k3s/k3s.yaml bash demo/c1-idle.sh tenant-b
sudo KUBECONFIG=/etc/rancher/k3s/k3s.yaml bash demo/c2-tokens.sh
```

`c2-tokens.sh` defaults to the local k3d cluster, where Traefik is on 8080
and tenants live under `.localhost`. On the VM, Traefik is on port 80 and the
tenants hang off `<IP>.nip.io`, so point the script at both:

```bash
sudo KUBECONFIG=/etc/rancher/k3s/k3s.yaml \
  DOMAIN="$(curl -s https://api.ipify.org).nip.io" \
  ENDPOINT="http://localhost/ask" \
  bash demo/c2-tokens.sh
```

Routing is by `Host` header, so the request can stay on the loopback address
and no DNS lookup has to succeed for this to work.

## Notes

- **Single node.** k3s, one node, not the three-node k3d cluster used for the
  measurements in the report. Tenant isolation, scale-to-zero and CPU
  contention are all node-local mechanisms, so all three still demonstrate
  correctly; the numbers in the report were taken locally and are not
  re-measured here.
- **ARM.** Images are built on the VM, natively, so the laptop's x86 build
  never has to cross-compile. This is why the installer takes twenty minutes.
- **Keep the account active.** Oracle reclaims idle Always Free tenancies.
