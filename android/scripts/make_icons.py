#!/usr/bin/env python3
"""Makes the Android launcher icon from the AgentMux brand mark.

assets/branding/agentmux-logo.png is the icon the desktop app uses: a blue
"A" on a dark rounded tile. Android draws its own tile shape, so the adaptive
icon takes the tile's color as its background and the "A", lifted off the
tile, as its foreground (and, in white, as the themed-icon monochrome layer).

Run from the repository root after the brand mark changes (needs Pillow):
    python3 android/scripts/make_icons.py
"""
import os

from PIL import Image

LOGO = "assets/branding/agentmux-logo.png"
RES = "android/app/src/main/res"
# The tile's color (ic_launcher_background.xml) and its texture's brightest
# channel level; anything brighter is the glyph or its glow.
TILE = (30, 32, 40)
TEXTURE = 64
GLYPH_TOP = 200
# The glyph's box and the tile's box in the 1024px logo.
GLYPH_BOX = (172, 158, 850, 830)
TILE_BOX = (133, 124, 893, 892)
# How much of the 108dp adaptive layer the glyph spans.
GLYPH_DP = 52
DENSITIES = {"mdpi": 108, "hdpi": 162, "xhdpi": 216, "xxhdpi": 324, "xxxhdpi": 432}


def lift_glyph(logo):
    """The glyph on transparency, un-blended from the tile, and its white mask."""
    px = logo.load()
    x0, y0, x1, y1 = GLYPH_BOX
    glyph, mono = Image.new("RGBA", (x1 - x0, y1 - y0)), Image.new("RGBA", (x1 - x0, y1 - y0))
    gp, mp = glyph.load(), mono.load()
    for y in range(y0, y1):
        for x in range(x0, x1):
            r, g, b, _ = px[x, y]
            alpha = min(1.0, max(0.0, (max(r, g, b) - TEXTURE) / (GLYPH_TOP - TEXTURE)))
            if alpha <= 0:
                continue
            color = tuple(min(255, max(0, round((c - (1 - alpha) * t) / alpha))) for c, t in zip((r, g, b), TILE))
            gp[x - x0, y - y0] = color + (round(alpha * 255),)
            mp[x - x0, y - y0] = (255, 255, 255, round(alpha * 255))
    return glyph, mono


def main():
    logo = Image.open(LOGO).convert("RGBA")
    glyph, mono = lift_glyph(logo)
    for density, size in DENSITIES.items():
        folder = f"{RES}/mipmap-{density}"
        os.makedirs(folder, exist_ok=True)
        side = round(size * GLYPH_DP / 108)
        scale = side / max(glyph.size)
        w, h = round(glyph.width * scale), round(glyph.height * scale)
        for name, layer in (("ic_launcher_foreground", glyph), ("ic_launcher_monochrome", mono)):
            canvas = Image.new("RGBA", (size, size))
            canvas.alpha_composite(layer.resize((w, h), Image.LANCZOS), ((size - w) // 2, (size - h) // 2))
            canvas.save(f"{folder}/{name}.png", optimize=True)
        # For launchers that skip adaptive icons: the tile itself.
        flat = round(size * 48 / 108)
        logo.crop(TILE_BOX).resize((flat, flat), Image.LANCZOS).save(f"{folder}/ic_launcher.png", optimize=True)
    # The mark the Connect and Sign-in pages show.
    os.makedirs(f"{RES}/drawable-nodpi", exist_ok=True)
    logo.crop(TILE_BOX).resize((288, 288), Image.LANCZOS).save(f"{RES}/drawable-nodpi/agentmux_mark.png", optimize=True)


if __name__ == "__main__":
    main()
