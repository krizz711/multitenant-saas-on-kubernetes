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
