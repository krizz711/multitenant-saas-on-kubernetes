# Deploying to Azure for Students

A public URL for the three storefronts **without a credit card**. Azure for
Students verifies you with an academic email instead of a payment method,
which is the one thing Oracle, AWS and Google Cloud all refuse to do.

Budget about 25 minutes. Azure is x86, so the images compile faster than they
would on Oracle's ARM instances, and there is one firewall to open rather
than two.

---

## Why this one

| | Card needed? | Verified by |
|---|---|---|
| **Azure for Students** | **No** | Academic email, or proof of enrolment |
| Oracle Cloud Free Tier | Yes | Card, for identity |
| AWS / Google Cloud | Yes | Card |
| GitHub Codespaces | No | GitHub Student Pack |

Azure for Students gives **$100 of credit for 12 months** and needs no card
at any point. When the credit runs out the resources stop; nothing is
charged, because there is nothing to charge.

A VM big enough for this stack costs roughly $30–60 a month if it runs
continuously, so $100 is comfortable — and **deallocating the VM between
sessions stops the meter entirely**, which stretches it much further.

---

## 1. Claim the credit

**The VIT address is not the login.** Signing up with
`…@vitstudent.ac.in` fails with *"You can't sign up with a work or school
email"*, because that address already exists inside VIT's Microsoft tenant
as a work/school identity and Microsoft will not create a second, personal
account on top of it.

Azure for Students uses two different identities, and conflating them is the
single most common way to get stuck:

| | Which address |
|---|---|
| **Sign in with** | a **personal** Microsoft account — Outlook, Hotmail, or one created on top of a Gmail address |
| **Verify with** | the **VIT** address, typed into the academic verification box |

So:

1. Create or sign in to a personal Microsoft account at
   <https://account.microsoft.com>. An existing Gmail address can be turned
   into one; it does not have to be an `@outlook.com`.
2. Go to <https://azure.microsoft.com/free/students> and sign in with
   **that** account.
3. When asked to confirm academic status, enter the **VIT** address. This is
   verification only — it tells Microsoft where to send the confirmation,
   and it is what the $100 is attached to.

You must be 18 or over and at an accredited institution.

**If the VIT domain is not recognised automatically**, two documented
fallbacks:

- **Verify with school documents** — upload a dated student ID, enrolment
  letter or transcript.
- **GitHub Student Developer Pack** — if that is already verified, it can
  carry the academic status across, and it includes Azure credit of its own.

Do not use a generated `.edu` address to get past verification. It breaks
Microsoft's terms and can cost the account, taking the deployment with it.

## 2. An SSH key, on your laptop

```bash
ssh-keygen -t ed25519 -f ~/.ssh/azure -N ""
cat ~/.ssh/azure.pub
```

## 3. The VM

Portal → Virtual machines → Create → Azure virtual machine.

| Field | Value |
|---|---|
| Image | **Ubuntu Server 24.04 LTS** |
| Size | **B2ms** — 2 vCPU, 8 GB |
| Authentication | SSH public key |
| Username | `azureuser` |
| SSH public key | paste `~/.ssh/azure.pub` |
| Public inbound ports | SSH (22) **and HTTP (80)** |

**B2s (2 vCPU, 4 GB) also works** and costs about half as much. The stack
idles near 3 GB, so 4 GB leaves little headroom while the images build.
Choose B2s to stretch the credit, B2ms for an easier life.

Do **not** take B1s. It is free for twelve months and has 1 GB of RAM, which
will not hold k3s, PostgreSQL, Redis, KEDA and three tenants; the pods are
OOM-killed in a loop.

Note the public IP when it finishes.

### Port 80

If you ticked HTTP (80) under inbound ports, you are done — unlike Oracle,
Azure's Ubuntu image does not also ship iptables rules that drop traffic, so
there is only the one layer. To add it afterwards: the VM → Networking →
Network settings → Create port rule → Inbound, destination port `80`, TCP,
Allow.

## 4. Install

```bash
ssh -i ~/.ssh/azure azureuser@<PUBLIC_IP>
```

Read the credentials in rather than typing them as arguments, so they never
reach shell history or the process list:

```bash
read -rsp 'gloo client id: '     GLOO_CLIENT_ID     && echo
read -rsp 'gloo client secret: ' GLOO_CLIENT_SECRET && echo
export GLOO_CLIENT_ID GLOO_CLIENT_SECRET

curl -fsSL https://raw.githubusercontent.com/krizz711/multitenant-saas-on-kubernetes/main/deploy/vm-install.sh -o vm-install.sh
sudo -E bash vm-install.sh
```

`sudo -E` is load-bearing: without it the credentials do not survive into the
installer and every tenant quietly falls back to the local stub.

The installer is unchanged from the Oracle path. It detects the public IP and
hangs every tenant hostname off `<IP>.nip.io`, a wildcard DNS service that
resolves `anything.1.2.3.4.nip.io` back to `1.2.3.4` — real per-tenant
hostnames with no domain to buy and no DNS records to create. That matters,
because host-based routing is what separates the tenants at the edge.

## 5. Check it

The installer prints the three URLs and, per tenant, the model that actually
answered:

```
   tenant-a model    -> gloo-auto
   tenant-b model    -> gloo-auto
   tenant-c model    -> gloo-auto
```

**If any line says `stub`, stop.** The site will look completely healthy and
claim C2 will be measuring nothing at all.

```bash
sudo kubectl -n tenant-b get secret gloo-credentials
sudo kubectl -n tenant-b rollout restart deploy/api
```

Then, from any machine with internet — including the lab PC:

```
http://tenant-a.<IP>.nip.io/shop     electronics
http://tenant-b.<IP>.nip.io/shop     outdoor gear
http://tenant-c.<IP>.nip.io/shop     homeware
```

## 6. The demonstrations

```bash
cd /opt/multitenant
sudo KUBECONFIG=/etc/rancher/k3s/k3s.yaml bash demo/c1-idle.sh tenant-b

sudo KUBECONFIG=/etc/rancher/k3s/k3s.yaml \
  DOMAIN="$(curl -s https://api.ipify.org).nip.io" \
  ENDPOINT="http://localhost/ask" \
  bash demo/c2-tokens.sh
```

## 7. Stop the meter between sessions

This is what makes $100 last. **Deallocate**, do not just shut down from
inside the VM — a stopped-but-allocated VM still bills.

Portal → the VM → **Stop**. Confirm the status reads **Stopped
(deallocated)**.

The public IP changes on restart unless it was created as static, and every
tenant hostname is derived from that IP. After restarting, re-run the
installer to pick up the new address:

```bash
cd /opt/multitenant && sudo -E bash deploy/vm-install.sh
```

Or set the public IP to **Static** in the portal — the VM → Networking → the
IP address → Static. Simpler, and free while the VM exists.

## Notes

- **Single node.** k3s, one node, not the three-node k3d cluster the report's
  measurements came from. Tenant isolation, scale-to-zero and CPU contention
  are all node-local mechanisms, so all three still demonstrate correctly.
- **Keep an eye on the credit.** Portal → Cost Management → the subscription
  shows what is left.
