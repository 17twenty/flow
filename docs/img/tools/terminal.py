# terminal.py IN OUT TITLE COMMAND — draws flow's plain output as a terminal window
# in SVG, coloured by the glyph each line leads with: the same output CI logs show.
import re
import sys
from html import escape

src, out, title, command = sys.argv[1:5]
lines = open(src, encoding="utf-8").read().rstrip("\n").splitlines()

# Tidy what does not belong in a picture: this machine's paths, and the token's
# full scope list, which is evidence for the run and noise for a reader.
tidy = []
for line in lines:
    line = re.sub(r"/Users/[^ ]*/demo/", "~/flow-demo-web/", line)
    if "X-OAuth-Scopes:" in line:
        continue
    line = re.sub(r"(credential is accepted by [\w-]+) and holds .*", r"\1", line)
    tidy.append(line)
lines = [f"$ {command}", ""] + tidy

PALETTE = {
    "bg": "#0d1117", "bar": "#161b22", "text": "#e6edf3", "dim": "#8b949e",
    "pass": "#3fb950", "warn": "#d29922", "fail": "#f85149", "info": "#58a6ff",
    "attest": "#bc8cff", "prompt": "#7ee787",
}
GLYPHS = {"✓": "pass", "+": "pass", "⚠": "warn", "!": "warn", "✗": "fail", "x": "fail",
          "·": "dim", ".": "dim", "↻": "info", "✎": "attest", "?": "attest", "⏸": "dim"}

FONT, LINE, CHAR, PAD = 13, 19, 7.83, 20
cols = max(len(line) for line in lines)
width = int(cols * CHAR + PAD * 2)
height = int(len(lines) * LINE + PAD * 2 + 34)


def styled(line):
    """The line as tspans: the leading glyph in its colour, the rest by kind."""
    if line.startswith("$ "):
        return f'<tspan fill="{PALETTE["prompt"]}">$</tspan> {escape(line[2:])}'
    stripped = line.lstrip(" ")
    indent = line[: len(line) - len(stripped)]
    if not stripped:
        return ""
    if indent == "" and re.fullmatch(r"[a-z]+", stripped):
        return f'<tspan font-weight="700" fill="{PALETTE["text"]}">{escape(stripped)}</tspan>'
    if stripped.startswith("- ") or stripped[:1].isdigit() and indent:
        return f'{escape(indent)}<tspan fill="{PALETTE["dim"]}">{escape(stripped)}</tspan>'
    if stripped.startswith(("fix:", "run:", "→ ")):
        return f'{escape(indent)}<tspan fill="{PALETTE["info"]}">{escape(stripped)}</tspan>'
    glyph = stripped[0]
    kind = GLYPHS.get(glyph)
    if kind and (len(stripped) == 1 or stripped[1] == " "):
        rest = stripped[1:]
        head, _, tail = rest.lstrip(" ").partition("  ")
        body = escape(head)
        if tail:
            body += f'<tspan fill="{PALETTE["dim"]}">  {escape(tail)}</tspan>'
        return f'{escape(indent)}<tspan fill="{PALETTE[kind]}">{escape(glyph)}</tspan> {body}'
    if re.search(r"exit \d+$|\d+ items ·", stripped):
        return f'<tspan font-weight="700">{escape(line)}</tspan>'
    return escape(line)


rows = []
for i, line in enumerate(lines):
    y = PAD + 34 + (i + 1) * LINE - 5
    rows.append(f'<text x="{PAD}" y="{y}" xml:space="preserve">{styled(line)}</text>')

svg = f'''<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" viewBox="0 0 {width} {height}" role="img" aria-label="{escape(title)}">
<title>{escape(title)}</title>
<rect width="{width}" height="{height}" rx="10" fill="{PALETTE["bg"]}"/>
<rect width="{width}" height="34" rx="10" fill="{PALETTE["bar"]}"/>
<rect y="24" width="{width}" height="10" fill="{PALETTE["bar"]}"/>
<circle cx="20" cy="17" r="6" fill="#ff5f57"/><circle cx="40" cy="17" r="6" fill="#febc2e"/><circle cx="60" cy="17" r="6" fill="#28c840"/>
<text x="{width / 2}" y="22" text-anchor="middle" font-family="-apple-system, Segoe UI, Helvetica, Arial, sans-serif" font-size="12" fill="{PALETTE["dim"]}">{escape(title)}</text>
<g font-family="ui-monospace, SFMono-Regular, Menlo, Consolas, 'Liberation Mono', monospace" font-size="{FONT}" fill="{PALETTE["text"]}">
{chr(10).join(rows)}
</g>
</svg>
'''
open(out, "w", encoding="utf-8").write(svg)
print(out, width, "x", height)
