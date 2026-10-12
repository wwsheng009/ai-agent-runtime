"""Generate the gateway console's PNG assets (run manually after icon changes).

iOS and Android system notifications do not render SVG icons, and iOS home
screen icons require a PNG apple-touch-icon, so the console ships bitmap
variants of icon.svg plus a white notification badge.

Usage:
    python backend/scripts/gateway_console_icons.py
"""

from pathlib import Path

from PIL import Image, ImageDraw

OUT = Path(__file__).resolve().parents[1] / "cmd" / "gateway" / "console"
BG = (15, 17, 21, 255)
ACCENT = (76, 139, 245, 255)
SS = 4  # supersample factor for smooth edges


def icon(size: int) -> Image.Image:
    s = size * SS
    img = Image.new("RGBA", (s, s), (0, 0, 0, 0))
    draw = ImageDraw.Draw(img)
    k = s / 64.0
    draw.rounded_rectangle([0, 0, s - 1, s - 1], radius=12 * k, fill=BG)
    pts = [(16, 40), (16, 24), (32, 15), (48, 24), (48, 40), (32, 49)]
    outline = [(x * k, y * k) for x, y in pts] + [(pts[0][0] * k, pts[0][1] * k)]
    draw.line(outline, fill=ACCENT, width=max(1, round(4 * k)), joint="curve")
    cx, cy, r = 32 * k, 33 * k, 5 * k
    draw.ellipse([cx - r, cy - r, cx + r, cy + r], fill=ACCENT)
    return img.resize((size, size), Image.LANCZOS)


def badge(size: int) -> Image.Image:
    s = size * SS
    img = Image.new("RGBA", (s, s), (0, 0, 0, 0))
    draw = ImageDraw.Draw(img)
    k = s / 96.0
    white = (255, 255, 255, 255)
    # Bell dome + body + flare + clapper (monochrome badge for Android).
    draw.pieslice([24 * k, 16 * k, 72 * k, 64 * k], start=180, end=360, fill=white)
    draw.rectangle([24 * k, 40 * k, 72 * k, 58 * k], fill=white)
    draw.rounded_rectangle([18 * k, 52 * k, 78 * k, 64 * k], radius=6 * k, fill=white)
    draw.ellipse([40 * k, 62 * k, 56 * k, 78 * k], fill=white)
    return img.resize((size, size), Image.LANCZOS)


def main() -> None:
    icon(192).save(OUT / "icon-192.png")
    icon(512).save(OUT / "icon-512.png")
    icon(180).save(OUT / "icon-180.png")  # apple-touch-icon
    badge(96).save(OUT / "badge-96.png")
    print("wrote:", ", ".join(sorted(p.name for p in OUT.glob("*.png"))))


if __name__ == "__main__":
    main()
