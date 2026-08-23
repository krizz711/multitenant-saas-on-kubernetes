# Raw experiment output

Committed deliberately: these files are the evidence behind every figure, and a
result whose raw data is not in the repository is a claim rather than a finding.

## e0-baseline (2026-08-22)

One compressed simulated day (SIM_HOUR_SECONDS=5, SIM_DAYS=1), three tenants,
no control-plane features active. This is the "before" column.

| Route     | Requests | Failures | p50    | p95    | p99    |
|-----------|----------|----------|--------|--------|--------|
| /summary  | 726      | 0        | 4 ms   | 12 ms  | 65 ms  |
| /ask      | 94       | 0        | 210 ms | 240 ms | 260 ms |
| /analyze  | 35       | 0        | 47 ms  | 53 ms  | 94 ms  |
| Aggregate | 855      | 0        | 4 ms   | 210 ms | 240 ms |

Load shape: peak 22 concurrent users at ~10:00, and 29% of the day at exactly
zero users. That idle fraction is the window claim C1 has to exploit; it is a
property of the shift model and should be reported alongside any C1 result.

p95 /summary of 12 ms against a 400 ms objective means the cluster is nowhere
near saturated at this load. That is intended for a baseline - C3 needs
headroom to then take away.

## e1-coldstart (2026-08-23) — claim C1

tenant-c scaled to zero by KEDA on Redis queue depth, then woken by a single
job. Timed from the instant the job is accepted. `drain_s` is the number that
matters: it is when the customer has their answer, whereas a pod reporting
Running has not necessarily pulled a job yet.

| pollingInterval | n | median scale-up | median drain | worst drain |
|-----------------|---|-----------------|--------------|-------------|
| 1 s             | 6 | 3.14 s          | **3.70 s**   | 5.11 s      |
| 5 s             | 6 | 3.85 s          | **6.01 s**   | 14.34 s     |

**The penalty is dominated by the scaler's polling interval, not by container
startup.** KEDA polls Redis every `pollingInterval` seconds, so detection delay
is uniform in [0, P] with mean P/2. That predicts a median difference of
(5-1)/2 = 2.00 s between these configurations; the measured difference is
2.31 s.

The raw poll=5s drains make the mechanism visible directly:
`[6.37, 0.48, 6.01, 1.12, 14.34, 1.47]`. Jobs arriving just before a poll are
served almost immediately; jobs arriving just after wait out the interval.

Two honest caveats:

- n=6 per configuration is small. The plan's experiment matrix calls for three
  repeats per cell; this is above that, but the variance is still wide enough
  that the medians should be treated as indicative rather than precise.
- The 14.34 s outlier is not explained by polling, which cannot exceed 5 s of
  delay. It is most likely node scheduling or a slow container start, and it
  should be investigated before this table goes in the paper rather than
  quietly dropped.

**Why this result is favourable to the design.** Container start is negligible
here because the image is 10.4 MB and a Go binary starts in milliseconds. That
moves the bottleneck to a *tunable parameter* rather than a property of the
workload, which is a much stronger position: the cold-start penalty is not a
fixed cost of scale-to-zero, it is a cost the operator chooses by trading
polling frequency against load on Redis.

## e2-cache-similarity (2026-08-23) — claim C2, a negative result

Cosine similarity under the local `lexical-hash-256` embedder, against the
benchmark pairs in `experiments/questions.yaml`:

| pair                                    | score | desired  |
|-----------------------------------------|-------|----------|
| identical text                          | 1.000 | hit      |
| paraphrase (%GR&R 18% ↔ "18 percent gauge R and R acceptable?") | **0.219** | **hit**  |
| distinct (%GR&R of 18 ↔ ndc of 18)      | **0.794** | **miss** |
| unrelated (line 3 drift)                | 0.097 | miss     |

**The lexical embedder ranks the distinct pair four times higher than the
paraphrase pair.** No threshold can separate them: any cut-off low enough to
catch the paraphrase at 0.219 would also merge the distinct pair at 0.794 and
answer the wrong question.

The cause is structural rather than a tuning problem. "What does a %GR&R of 18%
mean for this gauge?" and "What does an ndc of 18 mean for this gauge?" share
almost every word and differ in one term that carries the entire meaning. A
bag-of-words vector cannot see that. Only an embedder trained on meaning can.

### What this means for C2

At the configured threshold of 0.90 the system is **safe but limited**: the
distinct pair at 0.794 correctly misses, and the paraphrase at 0.219 also
misses. In this configuration the cache serves only near-identical prompts.

That is still a real saving, and it is the dominant one in the motivating
system: its monitoring agent re-asks *the same* question every thirty seconds
per series. Those are exact repeats, and they are caught. Prompt coalescing
catches the concurrent case a cache cannot reach at all — 20 simultaneous
identical prompts collapse to one upstream call.

So C2 should be stated as two separate results rather than one:

- **Supported now**: exact and near-exact repeat elimination, plus concurrent
  coalescing, with no loss of answer quality and no cross-tenant leakage.
- **Not supported by the local embedder**: paraphrase collapse. Claiming it
  would require a semantic embedding model, which is future work with a stated
  cost — a network call in the measurement path, and a rate-limited free tier.

Reporting the 0.219/0.794 inversion is worth more than a flattering hit rate.
It is measured, it is explained, and it names exactly what would fix it.

## e3-noisy-neighbour (2026-08-23) — claim C3, partially supported

tenant-a floods its deferrable queue; tenant-b, a different customer pinned to
the SAME node, does nothing but cheap interactive reads. Victim workload is
identical in all three phases; only tenant-a's behaviour and the scheduling
configuration change.

| phase       | n   | p50     | p95      | p99      | max     |
|-------------|-----|---------|----------|----------|---------|
| quiet       | 748 | 6.5 ms  | 26.9 ms  | 29.0 ms  | 103.7   |
| unprotected | 529 | 28.6 ms | 75.2 ms  | 123.7 ms | 209.7   |
| protected   | 546 | 28.0 ms | **61.4 ms** | **96.5 ms** | 189.4   |

**Contention is real**: p95 degrades 2.8x from 26.9 ms to 75.2 ms when a
neighbour floods the node. That is the noisy-neighbour problem, reproduced.

**Protection helps, but does not restore the baseline**: p95 recovers 18%
(75.2 → 61.4) and p99 recovers 22% (123.7 → 96.5). It does not return to
26.9 ms. C3 as stated - "class separation HOLDS the interactive objective under
heavy batch load" - is therefore only partially supported by this run. Both
loaded phases stay under the 400 ms objective, but that is headroom, not the
mechanism succeeding.

### Three caveats that must not be dropped

**1. The degradation is not cleanly attributable to worker CPU.** The flood
reaches tenant-a through the same Traefik ingress the victim uses, and also
loads tenant-a's own API pod. So the quiet→unprotected gap mixes node CPU
contention with shared ingress and API load.

The unprotected→protected comparison IS clean: identical load, identical
ingress path, and the only difference is the priority class and the CPU
request. The 18% recovery is attributable to the scheduling change. The 2.8x
degradation is not attributable to any single cause.

**2. KEDA never reached the requested worker count.** maxReplicaCount was 16
and the queue depth reached 18,075 against a target of 2 per replica, so KEDA
wanted the ceiling. Only 3-4 workers existed during sampling, because the
underlying HorizontalPodAutoscaler rate-limits scale-up (roughly doubling per
15 s cycle) and each phase restarted the Deployment at one replica. The
contention measured is therefore MILDER than intended; a longer ramp would
likely widen both the degradation and the recovery.

This is also a finding for C1: scale-from-zero to N is not one step. Reaching
capacity after a burst takes several HPA cycles, and that ramp is a cost of
scale-to-zero that the cold-start measurement in e1 does not capture, because
e1 only ever needed one replica.

**3. A first attempt showed nothing at all**, and the reason is worth
recording rather than hiding: the node has 12 allocatable CPUs, one worker was
burning one core, and tenant-a's ResourceQuota was already exhausted at
limits.cpu 2 of 2 so KEDA could not add replicas regardless of its ceiling. The
quota silently capped the experiment before KEDA got a say. An experiment that
runs cleanly and measures nothing is more dangerous than one that fails.
