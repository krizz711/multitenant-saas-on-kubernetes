"""Shift-shaped multi-tenant load model.

Every number this project reports depends on the load that produced it, so the
load model is part of the result and not a test harness detail.

A sine wave or a constant rate would flatter the results badly. Constant load
never lets a tenant go idle, so claim C1 - that an idle tenant costs nothing -
could never be demonstrated at all. Industrial users work shifts, so tenants
here work shifts: long busy stretches, hard edges, and genuinely dead hours.

Time is compressed. One simulated hour lasts SIM_HOUR_SECONDS real seconds, so
a full 24-hour day runs in about two minutes and an experiment matrix finishes
in an evening. THE COMPRESSION RATIO MUST BE STATED IN THE PAPER: it does not
change the shape of the load, but it does change how much wall-clock idle time
a scale-to-zero mechanism gets to exploit, and a reviewer is entitled to know.

Run:
    locust -f experiments/tenants.py --headless -u 30 -r 5 -t 4m \
           --csv experiments/raw/e0-baseline
"""

import os
import random

from locust import HttpUser, LoadTestShape, between, task

# One simulated hour in real seconds. 5s => a 24h day in 2 minutes.
SIM_HOUR_SECONDS = float(os.getenv("SIM_HOUR_SECONDS", "5"))

# How many simulated days to run before stopping.
#
# This exists because of a Locust behaviour that is easy to lose an evening to:
# when a LoadTestShape is defined, the --run-time flag is IGNORED. The shape is
# the sole authority on when the test ends, and it ends only when tick()
# returns None. A shape that never returns None runs forever, and -t 2m does
# nothing at all.
SIM_DAYS = float(os.getenv("SIM_DAYS", "1"))

# The ingress routes by Host header, so every tenant shares one address.
INGRESS = os.getenv("INGRESS", "http://localhost:8080")


class TenantUser(HttpUser):
    """One simulated operator working for one tenant.

    The task weights are the class mix from the architecture: mostly cheap
    interactive reads, a few expensive model calls, and the occasional
    deferrable analysis. Getting this mix wrong would move every latency
    number in the results, so it is stated here rather than buried.
    """

    abstract = True
    tenant = "unset"
    host = INGRESS
    wait_time = between(0.5, 2.0)

    def on_start(self):
        # Host selects the tenant at the ingress; X-Tenant is what the
        # admission middleware reads. In production the second would be
        # derived from an authenticated token rather than sent by the client.
        self.client.headers.update(
            {"Host": f"{self.tenant}.localhost", "X-Tenant": self.tenant}
        )

    @task(20)
    def summary(self):
        # The cheap interactive read. This is the request the 400 ms objective
        # is written about, and the one claim C3 protects.
        self.client.get("/summary", name="/summary")

    @task(3)
    def ask(self):
        # Expensive in money rather than CPU. Deliberately drawn from a small
        # pool with paraphrases, so Module 7's semantic cache has something
        # real to collapse - and so the hit rate measured then is not an
        # artefact of every question being unique.
        question = random.choice(QUESTIONS)
        self.client.post("/ask", json={"question": question}, name="/ask")

    @task(1)
    def analyze(self):
        # Deferrable: returns 202 immediately, the work happens in the worker.
        # 202 must be declared a success or Locust counts every job as a
        # failure and the error rate becomes meaningless.
        with self.client.post(
            "/analyze", json={}, name="/analyze", catch_response=True
        ) as r:
            if r.status_code == 202:
                r.success()


QUESTIONS = [
    "What does a %GR&R of 18% mean for this gauge?",
    "Is 18 percent gauge R and R acceptable?",
    "Why did line 3 drift out of control at 14:20 today?",
    "What does an ndc of 4 tell me about this measurement system?",
    "Should I re-run the study with more operators?",
]


class TenantA(TenantUser):
    """Standard tier, one day shift. The ordinary case."""

    tenant = "tenant-a"


class TenantB(TenantUser):
    """Premium tier, two shifts back to back. The heaviest customer."""

    tenant = "tenant-b"


class TenantC(TenantUser):
    """Standard tier, a two-hour window and nothing else.

    This tenant exists to make claim C1 measurable. It is idle for twenty-two
    hours of every simulated day, which is exactly the customer whose pods
    should not exist and whose bill should be near zero.
    """

    tenant = "tenant-c"


# Users active per simulated hour, per tenant. Hard edges rather than a smooth
# curve because a shift starts when the whistle goes, not gradually.
SHIFTS = {
    TenantA: [(8, 16, 8)],
    TenantB: [(6, 14, 10), (14, 22, 7)],
    TenantC: [(10, 12, 4)],
}


def users_at(cls, sim_hour: float) -> int:
    for start, end, users in SHIFTS[cls]:
        if start <= sim_hour < end:
            return users
    return 0


class ShiftShape(LoadTestShape):
    """Drives the tenant population around a compressed 24-hour clock.

    Locust splits the total user count between classes by their weights, so
    the shape sets each class's weight to the number of users that tenant
    should have this hour and returns the sum. A tenant off shift is dropped
    from the class list entirely, which is what produces genuinely zero
    traffic rather than merely low traffic - the distinction claim C1 lives or
    dies on.
    """

    def tick(self):
        elapsed_hours = self.get_run_time() / SIM_HOUR_SECONDS
        if elapsed_hours >= SIM_DAYS * 24:
            # Returning None is the only thing that stops a shaped run.
            return None

        sim_hour = elapsed_hours % 24

        active, total = [], 0
        for cls in (TenantA, TenantB, TenantC):
            n = users_at(cls, sim_hour)
            if n:
                cls.weight = n
                active.append(cls)
                total += n

        if not active:
            # The night shift: nobody is working. Returning zero users is the
            # point of the whole model.
            return 0, 1, []

        return total, max(1, total // 2), active
