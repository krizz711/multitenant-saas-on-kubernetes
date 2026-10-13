"""Generates the architecture diagram in light and dark variants.

Two files rather than one because GitHub renders READMEs in both themes, and a
diagram that is legible in only one of them is half broken. The README selects
between them with a <picture> element.

Everything is drawn with presentation attributes rather than a <style> block:
GitHub sanitises embedded CSS in SVG, and a diagram that loses its colours
when rendered is worse than one that never had any.

    python docs/make_architecture.py
"""

from pathlib import Path

W, H = 1180, 812

LIGHT = dict(
    name="light",
    bg="#ffffff", panel="#f6f8fa", panel2="#eef2f6", line="#d0d7de",
    text="#1f2328", muted="#636c76",
    plane="#0969da", planefill="#ddf4ff",
    batch="#bc4c00", batchfill="#fff1e5",
    ok="#1a7f37", okfill="#dafbe1",
    tenant="#8250df", tenantfill="#fbefff",
)

DARK = dict(
    name="dark",
    bg="#0d1117", panel="#161b22", panel2="#1c2128", line="#30363d",
    text="#e6edf3", muted="#9198a1",
    plane="#58a6ff", planefill="#121d2f",
    batch="#ff9a52", batchfill="#2b1a0e",
    ok="#3fb950", okfill="#0f2417",
    tenant="#bc8cff", tenantfill="#1d1427",
)

FONT = "ui-sans-serif,-apple-system,Segoe UI,Helvetica,Arial,sans-serif"
MONO = "ui-monospace,SFMono-Regular,Consolas,monospace"


def esc(s):
    return s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


def box(x, y, w, h, fill, stroke, r=10, sw=1.5, dash=None):
    d = f' stroke-dasharray="{dash}"' if dash else ""
    return (f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{r}" '
            f'fill="{fill}" stroke="{stroke}" stroke-width="{sw}"{d}/>')


def text(x, y, s, fill, size=13, weight="400", anchor="start", font=FONT):
    return (f'<text x="{x}" y="{y}" fill="{fill}" font-family="{font}" '
            f'font-size="{size}" font-weight="{weight}" '
            f'text-anchor="{anchor}">{esc(s)}</text>')


def arrow(x1, y1, x2, y2, stroke, marker="arrow"):
    return (f'<line x1="{x1}" y1="{y1}" x2="{x2}" y2="{y2}" stroke="{stroke}" '
            f'stroke-width="1.8" marker-end="url(#{marker})"/>')


def render(c):
    p = []
    p.append(f'<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{H}" '
             f'viewBox="0 0 {W} {H}" role="img" '
             f'aria-label="Architecture of a multi-tenant cost and SLO aware control plane on Kubernetes">')
    p.append('<defs>')
    p.append(f'<marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" '
             f'markerWidth="6" markerHeight="6" orient="auto-start-reverse">'
             f'<path d="M0,0 L10,5 L0,10 z" fill="{c["muted"]}"/></marker>')
    p.append(f'<marker id="arrowp" viewBox="0 0 10 10" refX="9" refY="5" '
             f'markerWidth="6" markerHeight="6" orient="auto-start-reverse">'
             f'<path d="M0,0 L10,5 L0,10 z" fill="{c["plane"]}"/></marker>')
    p.append('</defs>')
    p.append(f'<rect width="{W}" height="{H}" fill="{c["bg"]}"/>')

    # ---------------------------------------------------------- merchants --
    p.append(text(60, 44, "THREE MERCHANTS, ONE CLUSTER", c["muted"], 11, "700"))
    merchants = [
        ("Merchant A", "electronics", "standard tier"),
        ("Merchant B", "outdoor gear", "premium tier"),
        ("Merchant C", "homeware", "standard tier"),
    ]
    cols = [60, 440, 820]
    for (name, trade, tier), x in zip(merchants, cols):
        p.append(box(x, 58, 300, 62, c["tenantfill"], c["tenant"]))
        p.append(text(x + 16, 82, name, c["text"], 14, "700"))
        p.append(text(x + 16, 102, f"{trade} · {tier}", c["muted"], 12))
        p.append(arrow(x + 150, 120, x + 150, 152, c["muted"]))

    # ------------------------------------------------------------ ingress --
    p.append(box(60, 154, 1060, 54, c["panel"], c["line"]))
    p.append(text(80, 178, "Traefik Ingress", c["text"], 14, "700"))
    p.append(text(80, 196, "routes by Host header — the hostname decides whose namespace the request enters",
                  c["muted"], 11.5))
    p.append(text(1100, 184, "tenant-*.example", c["muted"], 11, "400", "end", MONO))
    p.append(arrow(590, 208, 590, 240, c["plane"], "arrowp"))

    # ------------------------------------------------------- control plane --
    p.append(box(60, 242, 1060, 196, c["planefill"], c["plane"], 12, 2))
    p.append(text(80, 268, "CONTROL PLANE", c["plane"], 11, "700"))
    p.append(text(80, 288, "application-agnostic · a CI test fails the build if it ever imports the workload",
                  c["muted"], 11.5))

    stages = [
        ("1  Admission", "tags every request with a",
         "tenant id and a latency class", "interactive | deferrable"),
        ("2  Token gateway", "per-tenant budget, semantic",
         "cache, prompt coalescing", "claim C2"),
        ("3  Elasticity", "KEDA scales each tenant's",
         "workers to zero when idle", "claim C1"),
        ("4  Fair scheduling", "PriorityClass + CPU shares",
         "so batch yields to interactive", "claim C3"),
        ("5  Cost meter", "CPU-seconds + tokens + bytes",
         "joined per tenant", "₹ per tenant"),
    ]
    sx, sw_, gap = 80, 192, 13
    for i, (title, l1, l2, tag) in enumerate(stages):
        x = sx + i * (sw_ + gap)
        p.append(box(x, 306, sw_, 112, c["bg"], c["plane"], 9, 1.3))
        p.append(text(x + 13, 330, title, c["plane"], 12.5, "700"))
        p.append(text(x + 13, 352, l1, c["text"], 10.8))
        p.append(text(x + 13, 367, l2, c["text"], 10.8))
        p.append(text(x + 13, 402, tag, c["muted"], 10, "700", "start", MONO))
        if i < len(stages) - 1:
            p.append(arrow(x + sw_ + 1, 362, x + sw_ + gap - 2, 362, c["plane"], "arrowp"))

    p.append(arrow(590, 438, 590, 470, c["plane"], "arrowp"))

    # ----------------------------------------------------------- workload --
    p.append(text(60, 462, "WORKLOAD — small, frozen, REPLACEABLE  (was a manufacturing tool; swapped for a shop, "
                           "control plane unchanged)", c["muted"], 11, "700"))
    for x in cols:
        p.append(box(x, 472, 300, 128, c["panel"], c["line"], 10, 1.5, "5 4"))
        p.append(text(x + 16, 495, "tenant namespace", c["muted"], 10.5, "700", "start", MONO))

        p.append(box(x + 16, 504, 128, 42, c["okfill"], c["ok"], 7, 1.2))
        p.append(text(x + 26, 522, "api", c["ok"], 12.5, "700"))
        p.append(text(x + 26, 537, "search · ask", c["muted"], 10))

        p.append(box(x + 156, 504, 128, 42, c["batchfill"], c["batch"], 7, 1.2))
        p.append(text(x + 166, 522, "worker", c["batch"], 12.5, "700"))
        p.append(text(x + 166, 537, "reindex · 0..N", c["muted"], 10))

        p.append(text(x + 16, 568, "ResourceQuota · LimitRange", c["muted"], 10))
        p.append(text(x + 16, 584, "NetworkPolicy: default-deny", c["muted"], 10))
        p.append(arrow(x + 150, 600, x + 150, 638, c["muted"]))

    # ----------------------------------------------------------- platform --
    p.append(text(60, 632, "ALWAYS-ON PLATFORM — the only components that never scale to zero", c["muted"], 11, "700"))
    plat = [
        (60, 300, "Redis", "per-tenant job queue (the key KEDA reads)  ·  semantic cache  ·  AOF persisted"),
        (440, 300, "PostgreSQL", "one schema per tenant, one role per tenant  ·  isolation enforced by the database"),
        (820, 300, "Prometheus + Grafana", "per-tenant latency histograms  ·  SLO on an exact bucket edge"),
    ]
    for x, w, title, sub in plat:
        p.append(box(x, 640, w, 68, c["panel2"], c["line"]))
        p.append(text(x + 16, 664, title, c["text"], 13, "700"))
        words, line, lines = sub.split(" "), "", []
        for word in words:
            if len(line) + len(word) > 42:
                lines.append(line.strip()); line = ""
            line += word + " "
        lines.append(line.strip())
        for j, ln in enumerate(lines[:2]):
            p.append(text(x + 16, 682 + j * 14, ln, c["muted"], 10))

    # -------------------------------------------------------------- model --
    p.append(box(820, 242, 300, 0.1, c["bg"], c["bg"]))  # spacer, keeps groups distinct
    p.append(box(60, 726, 1060, 58, c["bg"], c["line"], 10, 1.3, "5 4"))
    p.append(text(80, 750, "Model providers", c["text"], 12.5, "700"))
    p.append(text(80, 768,
                  "Gloo · Gemini · deterministic stub — selected by one environment variable. "
                  "The gateway, budget, cache, coalescer and every metric are indifferent to which is behind them.",
                  c["muted"], 10.5))

    p.append('</svg>')
    return "\n".join(p)


def main():
    out = Path(__file__).parent
    for theme in (LIGHT, DARK):
        path = out / f"architecture-{theme['name']}.svg"
        path.write_text(render(theme), encoding="utf-8")
        print(f"wrote {path} ({path.stat().st_size:,} bytes)")


if __name__ == "__main__":
    main()
