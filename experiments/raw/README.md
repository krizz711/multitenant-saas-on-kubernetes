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
