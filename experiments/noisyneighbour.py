"""E3 - the noisy neighbour. Claim C3.

tenant-a floods its own deferrable queue with CPU-bound analysis jobs.
tenant-b, a different customer on the SAME NODE, is doing nothing but cheap
interactive reads. The question is whether tenant-b's p95 survives.

Measured in three phases against one unchanging victim workload:

  quiet       tenant-b alone. The baseline.
  unprotected tenant-a flooding, no priority classes, equal CPU requests.
  protected   tenant-a flooding, batch priority class and a low CPU share.

Co-location is not incidental. CPU shares are enforced by the kernel cgroup
hierarchy on a node, so two pods on different nodes never compete for the same
cores - the mechanism would not engage and the experiment would show nothing
while appearing to run correctly. Both tenants are pinned to one node.

Run:
    experiments/.venv/Scripts/python.exe experiments/noisyneighbour.py
"""

import argparse
import json
import statistics
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request

INGRESS = "http://localhost:8080"
VICTIM = "tenant-b"
NOISY = "tenant-a"
NODE = "k3d-multitenant-agent-0"

# The node has 12 allocatable CPUs. A first attempt at this experiment showed
# no contention at all, for two compounding reasons worth recording: one worker
# burning one core against twelve is not a noisy neighbour, and tenant-a's
# ResourceQuota was already exhausted at limits.cpu 2 of 2 (api 1 + worker 1),
# so KEDA could not add replicas regardless of its maxReplicaCount. The quota
# silently capped the experiment before KEDA got a say.
#
# To contend for CPU, aggregate batch demand must EXCEED the node. 16 workers
# with a 1-core ceiling each demand 16 cores from a 12-core node.
NOISY_MAX_WORKERS = 16
NOISY_QUOTA_LIMITS_CPU = "24"
NOISY_QUOTA_REQUESTS_CPU = "8"
NOISY_QUOTA_PODS = "40"
FLOOD_THREADS = 16


def noisy_overrides() -> list[str]:
    """Give the noisy tenant room to actually be noisy."""
    return [
        "--set", f"scaling.maxReplicas={NOISY_MAX_WORKERS}",
        "--set", f"quota.standard.limitsCpu={NOISY_QUOTA_LIMITS_CPU}",
        "--set", f"quota.standard.requestsCpu={NOISY_QUOTA_REQUESTS_CPU}",
        "--set", f"quota.standard.pods={NOISY_QUOTA_PODS}",
    ]


def helm(tenant: str, tier: str, *extra: str) -> None:
    subprocess.run(
        ["helm", "upgrade", tenant, "deploy/charts/tenant",
         "--set", f"tenant={tenant}", "--set", f"tier={tier}",
         "--set", rf"nodeSelector.kubernetes\.io/hostname={NODE}", *extra],
        capture_output=True, text=True, timeout=300, check=False,
    )


def wait_ready(tenant: str, timeout: int = 180) -> None:
    subprocess.run(
        ["kubectl", "-n", tenant, "rollout", "status", "deploy/api",
         f"--timeout={timeout}s"],
        capture_output=True, text=True, timeout=timeout + 30, check=False,
    )


def get_summary(tenant: str) -> float | None:
    """One interactive request. Returns latency in ms, or None on failure."""
    req = urllib.request.Request(
        f"{INGRESS}/summary",
        headers={"Host": f"{tenant}.localhost", "X-Tenant": tenant},
    )
    t0 = time.perf_counter()
    try:
        with urllib.request.urlopen(req, timeout=10) as r:
            r.read()
    except (urllib.error.URLError, TimeoutError, OSError):
        return None
    return (time.perf_counter() - t0) * 1000


def post_analyze(tenant: str) -> None:
    req = urllib.request.Request(
        f"{INGRESS}/analyze",
        data=b"{}",
        headers={
            "Host": f"{tenant}.localhost",
            "X-Tenant": tenant,
            "Content-Type": "application/json",
        },
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=15) as r:
            r.read()
    except Exception:
        pass


class Flood:
    """Keeps the noisy tenant's queue deep enough for KEDA to scale right up.

    Many threads rather than one: KEDA targets listLength pending jobs per
    replica, so reaching 16 replicas needs a backlog that outpaces 16 workers
    draining a 223 ms job each. A single enqueue loop cannot produce it.
    """

    def __init__(self, tenant: str, threads: int = FLOOD_THREADS):
        self.tenant = tenant
        self.stop = threading.Event()
        self._sent = 0
        self._lock = threading.Lock()
        self._threads = [
            threading.Thread(target=self._run, daemon=True) for _ in range(threads)
        ]

    def _run(self) -> None:
        while not self.stop.is_set():
            post_analyze(self.tenant)
            with self._lock:
                self._sent += 1

    def start(self) -> None:
        for t in self._threads:
            t.start()

    def join(self, timeout: float = 10) -> None:
        for t in self._threads:
            t.join(timeout=timeout)

    @property
    def sent(self) -> int:
        with self._lock:
            return self._sent


def measure(seconds: int) -> dict:
    """Hammer the victim's cheap read and report the tail."""
    samples, failures = [], 0
    deadline = time.time() + seconds
    while time.time() < deadline:
        ms = get_summary(VICTIM)
        if ms is None:
            failures += 1
        else:
            samples.append(ms)
        time.sleep(0.05)

    samples.sort()
    if not samples:
        return {"n": 0, "failures": failures}

    def pct(p: float) -> float:
        return round(samples[min(len(samples) - 1, int(len(samples) * p))], 1)

    return {
        "n": len(samples),
        "failures": failures,
        "p50_ms": pct(0.50),
        "p95_ms": pct(0.95),
        "p99_ms": pct(0.99),
        "max_ms": round(samples[-1], 1),
        "mean_ms": round(statistics.mean(samples), 1),
    }


def phase(name: str, seconds: int, flood: bool) -> dict:
    f = None
    if flood:
        f = Flood(NOISY)
        f.start()
        # Long enough for the backlog to build AND for KEDA to scale workers up
        # to the ceiling. Sampling before the workers exist would measure an
        # idle node and call it contention.
        time.sleep(25)
        replicas = subprocess.run(
            ["kubectl", "-n", NOISY, "get", "deploy", "worker",
             "-o", "jsonpath={.status.readyReplicas}"],
            capture_output=True, text=True, check=False).stdout.strip()
        print(f"    noisy workers ready: {replicas or 0}", flush=True)

    # Warm the victim before sampling. The first run showed a worse tail in the
    # QUIET phase than under load, which was cold-start noise being mistaken
    # for a measurement.
    for _ in range(40):
        get_summary(VICTIM)

    print(f"  {name}: sampling {seconds}s...", flush=True)
    result = measure(seconds)

    if f:
        f.stop.set()
        f.join(timeout=10)
        result["jobs_enqueued"] = f.sent
    result["phase"] = name
    return result


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--seconds", type=int, default=45)
    ap.add_argument("--out", default="experiments/raw/e3-noisy-neighbour.json")
    args = ap.parse_args()

    results = []

    print("co-locating both tenants on one node...", flush=True)
    print()
    print("PHASE 1 - quiet baseline (protection on, no batch load)")
    helm(VICTIM, "premium")
    helm(NOISY, "standard", *noisy_overrides())
    wait_ready(VICTIM)
    time.sleep(5)
    results.append(phase("quiet", args.seconds, flood=False))

    print()
    print("PHASE 2 - unprotected: no priority classes, equal CPU requests")
    helm(VICTIM, "premium", "--set", "scheduling.enabled=false",
         "--set", "api.resources.requests.cpu=200m")
    helm(NOISY, "standard", *noisy_overrides(),
         "--set", "scheduling.enabled=false",
         "--set", "worker.resources.requests.cpu=300m")
    wait_ready(VICTIM)
    time.sleep(5)
    results.append(phase("unprotected", args.seconds, flood=True))

    print()
    print("PHASE 3 - protected: batch priority class, low batch CPU share")
    helm(VICTIM, "premium")
    helm(NOISY, "standard", *noisy_overrides())
    wait_ready(VICTIM)
    time.sleep(5)
    results.append(phase("protected", args.seconds, flood=True))

    with open(args.out, "w", encoding="utf-8") as fh:
        json.dump({"victim": VICTIM, "noisy": NOISY, "node": NODE,
                   "phases": results}, fh, indent=2)

    print()
    print(f"{'phase':<12}{'n':>6}{'p50':>9}{'p95':>9}{'p99':>9}{'max':>10}{'fail':>6}")
    for r in results:
        print(f"{r['phase']:<12}{r.get('n',0):>6}{r.get('p50_ms',0):>9}"
              f"{r.get('p95_ms',0):>9}{r.get('p99_ms',0):>9}"
              f"{r.get('max_ms',0):>10}{r.get('failures',0):>6}")
    print()
    print(f"written to {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
