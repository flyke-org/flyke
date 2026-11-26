# Generates the Flyke brand SVGs. usage: build.py <fontdir> <outdir>
import os
import sys

from fontTools.pens.boundsPen import BoundsPen
from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen
from fontTools.ttLib import TTFont

FONTDIR, OUT = sys.argv[1], sys.argv[2]
os.makedirs(OUT, exist_ok=True)

INK = "#0f172a"  # wordmark on light backgrounds
PAPER = "#ffffff"
NIGHT = "#0b1020"  # dark background

# ---------------------------------------------------------------- bird paths
# Drawn in a 1080x840 space; the bird's bounding box is x 30..1046, y 33..797.
BODY = "M160 795 L465 515 C600 505 700 490 765 480 L735 330 L748 292 C790 255 840 229 886 229 C930 229 966 250 990 285 C1012 295 1032 310 1042 323 C1045 330 1038 331 1030 330 L985 331 C935 333 893 355 860 393 C820 442 735 550 676 622 C640 660 580 688 508 692 C380 702 260 745 160 795 Z"
SHADE = "M160 795 L465 515 L780 470 L676 622 C640 660 580 688 508 692 C380 702 260 745 160 795 Z"
UPPER = "M32 38 C110 78 250 103 454 113 C560 122 660 180 728 270 C770 330 786 400 778 492 C766 432 724 378 650 344 C590 316 500 296 432 289 L265 274 C185 266 130 240 92 192 C62 150 42 98 32 38 Z"
LOWER = "M173 308 C280 328 380 334 432 337 C520 343 570 350 605 360 C645 370 680 382 702 397 C725 412 745 428 756 441 C768 456 774 470 776 486 C800 500 820 520 810 545 L720 660 L676 624 C688 596 678 560 622 543 C570 527 520 520 464 518 C380 512 330 498 290 470 C248 440 200 382 173 308 Z"
NECK = "M0 0 H1080 V380 L860 393 C820 442 735 550 676 622 L560 840 H0 Z"
BX, BY, BW, BH = 30, 33, 1016, 764  # bird bbox


def bird_color(p):
    """Full-colour gradient bird. p = id prefix (unique per document)."""
    return f'''<defs>
  <linearGradient id="{p}body" gradientUnits="userSpaceOnUse" x1="160" y1="795" x2="980" y2="240"><stop offset="0" stop-color="#3d35f2"/><stop offset=".5" stop-color="#4128ec"/><stop offset="1" stop-color="#5c3aff"/></linearGradient>
  <radialGradient id="{p}shade" gradientUnits="userSpaceOnUse" cx="650" cy="600" r="260"><stop offset="0" stop-color="#3c08b8"/><stop offset=".45" stop-color="#3a10c0" stop-opacity=".75"/><stop offset="1" stop-color="#3a10c0" stop-opacity="0"/></radialGradient>
  <linearGradient id="{p}upper" gradientUnits="userSpaceOnUse" x1="40" y1="60" x2="790" y2="430"><stop offset="0" stop-color="#108cff"/><stop offset=".45" stop-color="#4a3aff"/><stop offset=".75" stop-color="#5a2cf8"/><stop offset="1" stop-color="#1f15b4"/></linearGradient>
  <radialGradient id="{p}tuck" gradientUnits="userSpaceOnUse" cx="800" cy="430" r="190"><stop offset="0" stop-color="#170c9c" stop-opacity=".9"/><stop offset="1" stop-color="#170c9c" stop-opacity="0"/></radialGradient>
  <linearGradient id="{p}lower" gradientUnits="userSpaceOnUse" x1="180" y1="320" x2="780" y2="580"><stop offset="0" stop-color="#5596ff"/><stop offset=".4" stop-color="#4a40ff"/><stop offset=".78" stop-color="#7c2cff"/><stop offset="1" stop-color="#a020ff"/></linearGradient>
  <clipPath id="{p}neck"><path d="{NECK}"/></clipPath>
</defs>
<path fill="url(#{p}body)" d="{BODY}"/>
<path fill="url(#{p}shade)" d="{SHADE}"/>
<path fill="url(#{p}upper)" d="{UPPER}"/>
<path fill="url(#{p}tuck)" d="{UPPER}"/>
<path fill="url(#{p}lower)" clip-path="url(#{p}neck)" d="{LOWER}"/>'''


def bird_mono(p, color, gap=16):
    """Single-colour bird: the wings are cut out of the body by a thin gap."""
    return f'''<defs>
  <clipPath id="{p}neck"><path d="{NECK}"/></clipPath>
  <mask id="{p}cut" maskUnits="userSpaceOnUse" x="0" y="0" width="1080" height="840">
    <rect width="1080" height="840" fill="#fff"/>
    <path d="{UPPER}" fill="#000" stroke="#000" stroke-width="{gap * 2}" stroke-linejoin="round"/>
    <path d="{LOWER}" fill="#000" stroke="#000" stroke-width="{gap * 2}" stroke-linejoin="round" clip-path="url(#{p}neck)"/>
  </mask>
</defs>
<g fill="{color}">
<path mask="url(#{p}cut)" d="{BODY}"/>
<path d="{UPPER}"/>
<path clip-path="url(#{p}neck)" d="{LOWER}"/>
</g>'''


# ---------------------------------------------------------------- text → paths
def text_path(font, text, size, tracking=0.0):
    f = TTFont(os.path.join(FONTDIR, font))
    s = size / f["head"].unitsPerEm
    gs, cmap = f.getGlyphSet(), f.getBestCmap()
    x, ds, bp = 0.0, [], BoundsPen(gs)
    for ch in text:
        g = cmap[ord(ch)]
        pen = SVGPathPen(gs, lambda v: f"{v:.1f}".rstrip("0").rstrip("."))
        gs[g].draw(TransformPen(pen, (s, 0, 0, -s, x, 0)))
        gs[g].draw(TransformPen(bp, (s, 0, 0, -s, x, 0)))
        ds.append(pen.getCommands())
        x += gs[g].width * s + (tracking if ch != " " else 0)
    return " ".join(d for d in ds if d), bp.bounds  # bounds: xmin,ymin,xmax,ymax (y down)


BOLD = "plus-jakarta-sans-latin-700-normal.woff"
MED = "plus-jakarta-sans-latin-500-normal.woff"

# Wordmark sized so the ascender of "l" is 60% of the bird's height, as in the original.
_, b = text_path(BOLD, "l", 1000)
WSIZE = 460 / (-b[1]) * 1000
WD, WB = text_path(BOLD, "flyke", WSIZE, tracking=WSIZE * 0.035)
WX0, WTOP, WX1, WBOT = WB  # baseline at y=0
WW = WX1 - WX0


def svg(vb, body, w=None, h=None, title="Flyke"):
    wh = f' width="{w}" height="{h}"' if w else ""
    return f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="{vb}"{wh} role="img" aria-label="{title}">\n<title>{title}</title>\n{body}\n</svg>\n'


def save(name, content):
    open(os.path.join(OUT, name), "w").write(content)


def square_vb(pad):
    side = max(BW, BH) * (1 + 2 * pad)
    cx, cy = BX + BW / 2, BY + BH / 2
    return f"{cx - side / 2:.0f} {cy - side / 2:.0f} {side:.0f} {side:.0f}"


# ---------------------------------------------------------------- mark
for sub in ("mark", "logo", "wordmark", "icon", "social"):
    os.makedirs(os.path.join(OUT, sub), exist_ok=True)

tight = f"{BX} {BY} {BW} {BH}"
save("mark/flyke-mark.svg", svg(tight, bird_color("m")))
save("mark/flyke-mark-black.svg", svg(tight, bird_mono("m", INK)))
save("mark/flyke-mark-white.svg", svg(tight, bird_mono("m", PAPER)))
save("mark/flyke-mark-square.svg", svg(square_vb(0.02), bird_color("m")))

# ---------------------------------------------------------------- wordmark
wvb = f"{WX0:.0f} {WTOP:.0f} {WW:.0f} {WBOT - WTOP:.0f}"
save("wordmark/flyke-wordmark.svg", svg(wvb, f'<path fill="{INK}" d="{WD}"/>'))
save("wordmark/flyke-wordmark-white.svg", svg(wvb, f'<path fill="{PAPER}" d="{WD}"/>'))

# ---------------------------------------------------------------- horizontal logo
# Same proportions as the source: text starts 114 units right of the bird, baseline at y=590.
TX, TB = 1160 - WX0, 590


def horizontal(bird, ink):
    return f'{bird}\n<path fill="{ink}" transform="translate({TX:.1f} {TB})" d="{WD}"/>'


hx1 = TX + WX1
hvb = f"{BX} {BY} {hx1 - BX:.0f} {BH}"
save("logo/flyke-logo.svg", svg(hvb, horizontal(bird_color("h"), INK)))
save("logo/flyke-logo-white-text.svg", svg(hvb, horizontal(bird_color("h"), PAPER)))
save("logo/flyke-logo-black.svg", svg(hvb, horizontal(bird_mono("h", INK), INK)))
save("logo/flyke-logo-white.svg", svg(hvb, horizontal(bird_mono("h", PAPER), PAPER)))

# ---------------------------------------------------------------- stacked logo
SK = 0.75
sw = max(BW, WW * SK)
s_gap = 130
s_tb = BY + BH + s_gap - WTOP * SK  # baseline
s_tx = BX + BW / 2 - WW * SK / 2 - WX0 * SK


def stacked(bird, ink):
    return f'{bird}\n<path fill="{ink}" transform="translate({s_tx:.1f} {s_tb:.1f}) scale({SK})" d="{WD}"/>'


svb = f"{BX + BW / 2 - sw / 2:.0f} {BY} {sw:.0f} {BH + s_gap + (WBOT - WTOP) * SK:.0f}"
save("logo/flyke-logo-stacked.svg", svg(svb, stacked(bird_color("s"), INK)))
save("logo/flyke-logo-stacked-white-text.svg", svg(svb, stacked(bird_color("s"), PAPER)))

# ---------------------------------------------------------------- favicon / app icon
# favicon.svg: colour bird, no background; reads on light and dark browser tabs.
save("icon/favicon.svg", svg(square_vb(0.0), bird_color("f")))


# App icon: white bird on the brand gradient tile (apple-touch, PWA, avatars).
def tile(rx_ratio, bird_scale=0.70, prefix="t"):
    S = 1024
    k = S * bird_scale / BW
    tx = S / 2 - (BX + BW / 2) * k
    ty = S / 2 - (BY + BH / 2) * k + S * 0.01
    rx = f' rx="{S * rx_ratio:.0f}"' if rx_ratio else ""
    return svg(
        f"0 0 {S} {S}",
        f'''<defs><linearGradient id="{prefix}bg" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#108cff"/><stop offset=".55" stop-color="#4a2cf5"/><stop offset="1" stop-color="#8a24ff"/></linearGradient>
<radialGradient id="{prefix}hl" cx=".25" cy=".1" r=".9"><stop offset="0" stop-color="#fff" stop-opacity=".22"/><stop offset=".6" stop-color="#fff" stop-opacity="0"/></radialGradient></defs>
<rect width="{S}" height="{S}"{rx} fill="url(#{prefix}bg)"/><rect width="{S}" height="{S}"{rx} fill="url(#{prefix}hl)"/>
<g transform="translate({tx:.1f} {ty:.1f}) scale({k:.5f})">{bird_mono(prefix, PAPER, gap=20)}</g>''',
    )


save("icon/flyke-app-icon.svg", tile(0.225))  # rounded (web, README, avatars)
save("icon/flyke-app-icon-square.svg", tile(0))  # full bleed (iOS/maskable crop themselves)
save("icon/flyke-app-icon-maskable.svg", tile(0, bird_scale=0.56))  # inside the 80% safe zone

# ---------------------------------------------------------------- social / blog cards
TAG = "Durable workflows in plain async Python, from one binary."


def card(W, H, dark, prefix, tagline=True):
    bg = NIGHT if dark else PAPER
    ink = PAPER if dark else INK
    sub = "#c3cbe6" if dark else "#334155"
    # logo scaled to ~58% of card width
    lw = hx1 - BX
    k = W * 0.50 / lw
    lh = BH * k
    TD, TBd = text_path(MED, TAG, 1000)
    tk = W * 0.80 / (TBd[2] - TBd[0])
    th = (TBd[3] - TBd[1]) * tk
    gap = H * 0.11 if tagline else 0
    total = lh + (gap + th if tagline else 0)
    y0 = (H - total) / 2
    lx = (W - lw * k) / 2 - BX * k
    ly = y0 - BY * k
    glow = (
        (
            f'<defs><radialGradient id="{prefix}g1" cx=".18" cy=".05" r=".7"><stop offset="0" stop-color="#108cff" stop-opacity=".35"/><stop offset="1" stop-color="#108cff" stop-opacity="0"/></radialGradient>'
            f'<radialGradient id="{prefix}g2" cx=".9" cy="1" r=".7"><stop offset="0" stop-color="#8a24ff" stop-opacity=".35"/><stop offset="1" stop-color="#8a24ff" stop-opacity="0"/></radialGradient></defs>'
            f'<rect width="{W}" height="{H}" fill="url(#{prefix}g1)"/><rect width="{W}" height="{H}" fill="url(#{prefix}g2)"/>'
        )
        if dark
        else (
            f'<defs><radialGradient id="{prefix}g1" cx=".15" cy="0" r=".8"><stop offset="0" stop-color="#108cff" stop-opacity=".10"/><stop offset="1" stop-color="#108cff" stop-opacity="0"/></radialGradient>'
            f'<radialGradient id="{prefix}g2" cx=".95" cy="1" r=".8"><stop offset="0" stop-color="#8a24ff" stop-opacity=".10"/><stop offset="1" stop-color="#8a24ff" stop-opacity="0"/></radialGradient></defs>'
            f'<rect width="{W}" height="{H}" fill="url(#{prefix}g1)"/><rect width="{W}" height="{H}" fill="url(#{prefix}g2)"/>'
        )
    )
    body = f'<rect width="{W}" height="{H}" fill="{bg}"/>{glow}\n<g transform="translate({lx:.1f} {ly:.1f}) scale({k:.5f})">{horizontal(bird_color(prefix), ink)}</g>'
    if tagline:
        tx = (W - (TBd[2] - TBd[0]) * tk) / 2 - TBd[0] * tk
        ty = y0 + lh + gap - TBd[1] * tk
        body += f'\n<path fill="{sub}" transform="translate({tx:.1f} {ty:.1f}) scale({tk:.5f})" d="{TD}"/>'
    return svg(f"0 0 {W} {H}", body, W, H)


save("social/flyke-og-dark.svg", card(1200, 630, True, "o"))  # Open Graph / Twitter / LinkedIn
save("social/flyke-og-light.svg", card(1200, 630, False, "o"))
save("social/flyke-github-social.svg", card(1280, 640, True, "g"))  # GitHub repo social preview
save("social/flyke-blog-header-dark.svg", card(1600, 640, True, "b", tagline=False))
save("social/flyke-blog-header-light.svg", card(1600, 640, False, "b", tagline=False))
print("ok", WSIZE, WW)
