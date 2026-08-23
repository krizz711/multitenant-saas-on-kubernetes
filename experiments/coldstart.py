"""E1 - cold-start penalty for a scaled-to-zero tenant. Claim C1.

Measures what it actually costs a customer to be woken from nothing: the
tenant's worker Deployment sits at zero replicas, one job is enqueued, and we
time how long until a pod is Running and until the queue is drained.

Run:
    experiments/.venv/Scripts/python.exe experiments/coldstart.py --trials 3

Reported per trial, all measured from the instant the job is accepted:
  scale_up_s  - until a worker pod reports Running
  drain_s     - until the queue is empty, i.e. the customer has their answer

drain_s is the number that belongs in the paper. scale_up_s alone understates
the penalty, because a pod that is Running has still not necessarily pulled a
job yet.
"""

import argparse
import json
import subprocess
import sys
import time

REDIS_NS = "platform"


def sh(*args: str, timeout: int = 60) -> str:
    return subprocess.run(
        args, capture_output=True, text=True, timeout=timeout
    ).stdout.strip()


def replicas(tenant: str) -> int:
    out = sh("kubectl", "-n", tenant, "get", "deploy", "worker",
             "-o", "jsonpath={.status.replicas}")
    return int(out) if out.isdigit() else 0


def queue_depth(tenant: str) -> int:
    out = sh("kubectl", "-n", REDIS_NS, "exec", "deploy/redis", "--",
             "redis-cli", "LLEN", f"q:{tenant}").strip()
    return int(out) if out.isdigit() else -1


def worker_running(tenant: str) -> bool:
    out = sh("kubectl", "-n", tenant, "get", "pods",
             "-l", "app=worker", "-o",
             "jsonpath={.items[*].status.containerStatuses[*].ready}")
    return "true" in out


def enqueue(tenant: str) -> None:
    subprocess.run(
        ["curl", "-s", "--max-time", "15", "-X", "POST",
         "-H", f"Host: {tenant}.localhost", "-H", f"X-Tenant: {tenant}",
         "-H", "Content-Type: application/json", "-d", "{}",
         "http://localhost:8080/analyze"],
        capture_output=True, text=True, timeout=30,
    )


def wait_for_zero(tenant: str, limit: int = 180) -> bool:
    """Wait out KEDA's cooldown so each trial starts genuinely cold."""
    deadline = time.time() + limit
    while time.time() < deadline:
        if replicas(tenant) == 0 and queue_depth(tenant) == 0:
            return True
        time.sleep(2)
    return False


def trial(tenant: str) -> dict:
    if not wait_for_zero(tenant):
        raise RuntimeError(f"{tenant} never reached zero replicas")

    t0 = time.perf_counter()
    enqueue(tenant)

    scale_up = None
    deadline = time.time() + 180
    while time.time() < deadline:
        if scale_up is None and worker_running(tenant):
            scale_up = time.perf_counter() - t0
        if scale_up is not None and queue_depth(tenant) == 0:
            return {"scale_up_s": round(scale_up, 2),
                    "drain_s": round(time.perf_counter() - t0, 2)}
        time.sleep(0.5)
    raise RuntimeError(f"{tenant} did not drain within 180s")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--tenant", default="tenant-c",
                    help="the idle tenant; tenant-c works a 2-hour shift by design")
    ap.add_argument("--trials", type=int, default=3)
    ap.add_argument("--out", default="experiments/raw/e1-coldstart.json")
    args = ap.parse_args()

    results = []
    for i in range(1, args.trials + 1):
        print(f"trial {i}/{args.trials}: waiting for {args.tenant} to reach zero...",
              flush=True)
        r = trial(args.tenant)
        r["trial"] = i
        results.append(r)
        print(f"  scale_up={r['scale_up_s']}s  drain={r['drain_s']}s", flush=True)

    ups = sorted(r["scale_up_s"] for r in results)
    drains = sorted(r["drain_s"] for r in results)
    summary = {
        "tenant": args.tenant,
        "trials": results,
        "scale_up_s_median": ups[len(ups) // 2],
        "drain_s_median": drains[len(drains) // 2],
        "scale_up_s_max": ups[-1],
        "drain_s_max": drains[-1],
    }
    with open(args.out, "w", encoding="utf-8") as fh:
        json.dump(summary, fh, indent=2)

    print()
    print(f"median scale-up : {summary['scale_up_s_median']}s")
    print(f"median drain    : {summary['drain_s_median']}s")
    print(f"worst drain     : {summary['drain_s_max']}s")
    print(f"written to {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
