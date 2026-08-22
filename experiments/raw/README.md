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
