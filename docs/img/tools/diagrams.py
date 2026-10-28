# diagrams.py OUTDIR — draws flow's three explanatory diagrams as SVG.
#
# Hand-laid boxes and arrows rather than a layout engine: the pictures are few,
# they change rarely, and an exact layout reads better than a generated one.
# Each SVG follows the reader's light or dark theme.
import os
import sys
from html import escape

OUT = sys.argv[1]

STYLE = """
<style>
  .bg { fill: #ffffff; }
  .box { fill: #f6f8fa; stroke: #d0d7de; stroke-width: 1.5; }
  .flow { fill: #ddf4ff; stroke: #54aeff; }
  .person { fill: #fff8c5; stroke: #d4a72c; }
  .done { fill: #dafbe1; stroke: #4ac26b; }
  .t { font: 600 14px -apple-system, 'Segoe UI', Helvetica, Arial, sans-serif; fill: #1f2328; }
  .s { font: 12px -apple-system, 'Segoe UI', Helvetica, Arial, sans-serif; fill: #59636e; }
  .code { font: 600 13px ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; fill: #1f2328; }
  .h { font: 700 12px -apple-system, 'Segoe UI', Helvetica, Arial, sans-serif; fill: #59636e; letter-spacing: .08em; }
  .a { stroke: #59636e; stroke-width: 1.6; fill: none; marker-end: url(#arrow); }
  .an { stroke: #bf8700; stroke-width: 1.6; fill: none; stroke-dasharray: 5 4; marker-end: url(#arrow-native); }
  .ah { fill: #59636e; } .ahn { fill: #bf8700; }
  .badge { font: 600 11px -apple-system, 'Segoe UI', Helvetica, Arial, sans-serif; }
  .bf { fill: #0969da; } .bn { fill: #9a6700; }
  @media (prefers-color-scheme: dark) {
    .bg { fill: #0d1117; }
    .box { fill: #161b22; stroke: #3d444d; }
    .flow { fill: #0c2d6b; stroke: #1f6feb; }
    .person { fill: #3b2e0a; stroke: #9e6a03; }
    .done { fill: #04260f; stroke: #238636; }
    .t, .code { fill: #f0f6fc; } .s, .h { fill: #9198a1; }
    .a { stroke: #9198a1; } .ah { fill: #9198a1; }
    .an { stroke: #d29922; } .ahn { fill: #d29922; }
    .bf { fill: #4493f8; } .bn { fill: #d29922; }
  }
</style>
<defs>
  <marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path class="ah" d="M0,0 L10,5 L0,10 z"/></marker>
  <marker id="arrow-native" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path class="ahn" d="M0,0 L10,5 L0,10 z"/></marker>
</defs>
"""


def svg(name, w, h, title, body):
    doc = (f'<svg xmlns="http://www.w3.org/2000/svg" width="{w}" height="{h}" viewBox="0 0 {w} {h}" '
           f'role="img" aria-label="{escape(title)}"><title>{escape(title)}</title>{STYLE}'
           f'<rect class="bg" width="{w}" height="{h}" rx="12"/>{body}</svg>\n')
    path = os.path.join(OUT, name)
    open(path, "w", encoding="utf-8").write(doc)
    print(path, w, "x", h)


def box(x, y, w, h, title, sub="", kind="", code=False):
    cls = f"box {kind}".strip()
    out = f'<rect class="{cls}" x="{x}" y="{y}" width="{w}" height="{h}" rx="9"/>'
    ty = y + h / 2 + (-4 if sub else 5)
    out += f'<text class="{"code" if code else "t"}" x="{x + w / 2}" y="{ty}" text-anchor="middle">{escape(title)}</text>'
    if sub:
        out += f'<text class="s" x="{x + w / 2}" y="{ty + 18}" text-anchor="middle">{escape(sub)}</text>'
    return out


def arrow(points, native=False):
    d = "M" + " L".join(f"{x},{y}" for x, y in points)
    return f'<path class="{"an" if native else "a"}" d="{d}"/>'


def label(x, y, text, cls="s", anchor="middle"):
    return f'<text class="{cls}" x="{x}" y="{y}" text-anchor="{anchor}">{escape(text)}</text>'


# 1. The loop: set up once, then every pull request, every hour and every day.
W, BW, BH = 1000, 190, 58
b = label(40, 40, "ONCE, BY A PERSON", "h", "start")
setup = [("flow init", "writes config + CI"), ("flow doctor --fix", "finds the board, owners"),
         ("flow plan", "what would change"), ("flow apply", "protection, columns, files")]
for i, (t, s) in enumerate(setup):
    x = 40 + i * 240
    b += box(x, 56, BW, BH, t, s, "flow", code=True)
    if i:
        b += arrow([(x - 50, 85), (x - 2, 85)])
b += label(960, 182, "FROM THEN ON, IN CI — NOBODY RUNS IT BY HAND", "h", "end")
top = [("pull request", "a person or an agent", "box"), ("flow check pr", "posts flow/policy", "flow"),
       ("merge", "squash, up to date", "box")]
bottom = [("flow doctor", "daily: setup + audit", "flow"), ("deploy", "your pipeline", "box"),
          ("flow sync", "hourly: cards move", "flow")]
for i, (t, s, k) in enumerate(top):
    x = 120 + i * 300
    b += box(x, 196, 210, BH, t, s, "" if k == "box" else k, code=k == "flow")
    if i:
        b += arrow([(x - 88, 225), (x - 2, 225)])
for i, (t, s, k) in enumerate(bottom):
    x = 120 + i * 300
    b += box(x, 330, 210, BH, t, s, "" if k == "box" else k, code=k == "flow")
    if i:
        b += arrow([(x - 2, 359), (x - 88, 359)])
b += arrow([(825, 254), (825, 328)])
b += arrow([(120, 359), (70, 359), (70, 225), (118, 225)])
b += arrow([(805, 114), (805, 150), (225, 150), (225, 194)])
b += label(515, 144, "commit what init wrote through a pull request", "s")
b += box(400, 430, 230, BH, "a person", "follow-ups and attestations", "person")
b += arrow([(225, 388), (225, 459), (398, 459)])
b += label(300, 450, "what flow cannot do", "s")
svg("flow-loop.svg", W, 510, "How flow runs: set up once, then gated, synced and audited in CI", b)

# 2. A ticket's way across the board, and who moves it.
W = 1080
cols = [("Backlog", ""), ("Ready", "meets the Definition of Ready"), ("In progress", "WIP: 2 each"),
        ("In review", "WIP: 3"), ("Done", "")]
b = ""
for i, (t, s) in enumerate(cols):
    x = 30 + i * 210
    b += box(x, 130, 180, 64, t, s, "done" if t == "Done" else "")
events = [(1, "draft pull request", "flow"), (2, "ready for review", "flow"), (3, "merged · issue closed", "flow")]
for i, text, owner in events:
    x0, x1 = 30 + i * 210 + 180, 30 + (i + 1) * 210
    b += arrow([(x0 + 2, 150), (x1 - 2, 150)])
    b += label((x0 + x1) / 2 - 15, 112, text)
    b += f'<text class="badge bf" x="{(x0 + x1) / 2 - 15}" y="96" text-anchor="middle">flow</text>'
b += arrow([(30 + 3 * 210 + 90, 196), (30 + 3 * 210 + 90, 238), (30 + 2 * 210 + 90, 238), (30 + 2 * 210 + 90, 198)], native=True)
b += label(30 + 2.5 * 210 + 90, 258, "changes requested")
b += f'<text class="badge bn" x="{30 + 2.5 * 210 + 90}" y="274" text-anchor="middle">the tracker\'s own automation</text>'
b += arrow([(30 + 90, 196), (30 + 90, 238), (30 + 210 + 88, 238), (30 + 210 + 90, 198)])
b += label(30 + 210, 258, "a person pulls work")
b += label(540, 40, "flow moves the cards it owns from what happens to pull requests; the tracker moves the rest,", "s")
b += label(540, 58, "and doctor checks both did", "s")
svg("ticket-lifecycle.svg", W, 300, "A ticket's way across the board, and who moves it", b)

# 3. Who does what.
W = 1000
b = box(395, 190, 210, 80, "flow", "sets up · gates · verifies", "flow")
around = [
    (40, 40, "the forge", ["protection, required checks, merge settings,", "the CI files, by pull request"], ""),
    (560, 40, "the tracker", ["columns, labels, moving cards,", "follow-up tickets"], ""),
    (40, 350, "CI", ["the gate on every pull request,", "sync hourly, doctor daily"], ""),
    (560, 350, "a person", ["approves agent work, does follow-ups,", "vouches for what flow cannot see"], "person"),
]
for x, y, t, lines, k in around:
    b += f'<rect class="{("box " + k).strip()}" x="{x}" y="{y}" width="400" height="86" rx="9"/>'
    b += label(x + 200, y + 28, t, "t")
    for i, line in enumerate(lines):
        b += label(x + 200, y + 50 + i * 18, line)
b += arrow([(440, 188), (300, 128)])
b += arrow([(560, 188), (700, 128)])
b += arrow([(300, 348), (440, 272)])
b += arrow([(560, 272), (700, 348)])
legend = [("native", "the provider does it, flow checks it"), ("compensated", "flow enforces it another way"),
          ("follow-up", "a ticket for whoever can"), ("attest", "a person vouches; asked again in 30d")]
for i, (t, s) in enumerate(legend):
    b += label(60 + i * 230, 470, t, "t", "start") + label(60 + i * 230, 488, s, "s", "start")
svg("who-does-what.svg", W, 510, "Who does what: flow, the forge, the tracker, CI and a person", b)
