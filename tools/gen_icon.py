#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""生成 LXCPanel 应用图标（无外部素材依赖，纯代码绘制）。"""

import os
from PIL import Image, ImageDraw

S = 4  # supersample factor
SIZE = 256
W = SIZE * S

OUT_DIRS = [
    ("LXCPanel", "ICON_256.PNG", 256),
    ("LXCPanel", "ICON.PNG", 64),
    ("LXCPanel/app/ui/images", "icon_256.png", 256),
    ("LXCPanel/app/ui/images", "icon_64.png", 64),
]

C1 = (47, 107, 255)   # blue
C2 = (124, 92, 255)   # violet


def lerp(a, b, t):
    return tuple(int(round(a[i] + (b[i] - a[i]) * t)) for i in range(3))


def build():
    base = Image.new("RGBA", (W, W), (0, 0, 0, 0))

    # diagonal gradient
    grad = Image.new("RGBA", (W, W))
    gd = grad.load()
    for y in range(W):
        for x in range(0, W, 4):
            t = (x / W * 0.62 + y / W * 0.38)
            col = lerp(C1, C2, min(1.0, max(0.0, t)))
            for dx in range(4):
                if x + dx < W:
                    gd[x + dx, y] = (col[0], col[1], col[2], 255)

    # rounded mask
    mask = Image.new("L", (W, W), 0)
    md = ImageDraw.Draw(mask)
    md.rounded_rectangle([0, 0, W - 1, W - 1], radius=int(W * 0.225), fill=255)
    base.paste(grad, (0, 0), mask)

    # soft top highlight
    hi = Image.new("RGBA", (W, W), (0, 0, 0, 0))
    hd = ImageDraw.Draw(hi)
    hd.ellipse([-W * 0.35, -W * 0.95, W * 1.35, W * 0.42], fill=(255, 255, 255, 34))
    base = Image.alpha_composite(base, Image.composite(hi, Image.new("RGBA", (W, W), (0, 0, 0, 0)), mask))

    d = ImageDraw.Draw(base, "RGBA")
    cx, cy = W / 2, W / 2 + W * 0.008
    w = W * 0.275        # half width
    dy = w * 0.5         # rhombus half height
    H = w * 0.95         # vertical edge length

    p1 = (cx, cy - dy * 2)        # top (far)
    p2 = (cx + w, cy - dy)        # right
    p3 = (cx, cy)                 # center
    p4 = (cx - w, cy - dy)        # left
    bl = (cx - w, cy - dy + H)
    bc = (cx, cy + H)
    br = (cx + w, cy - dy + H)

    # top face (brightest)
    d.polygon([p1, p2, p3, p4], fill=(255, 255, 255, 242))
    # left face
    d.polygon([p4, p3, bc, bl], fill=(255, 255, 255, 178))
    # right face
    d.polygon([p2, p3, bc, br], fill=(255, 255, 255, 122))

    # seam lines to read as a cube
    seam = (52, 84, 196, 120)
    sw = max(1, int(W * 0.0085))
    for a, b in ((p4, p3), (p2, p3), (p3, bc)):
        d.line([a, b], fill=seam, width=sw)

    # terminal prompt on the top face
    ux, uy = W * 0.030, W * 0.026
    ax, ay = cx - ux * 1.55, cy - dy - uy * 0.35
    lw = int(W * 0.0145)
    d.line([(ax, ay), (ax + ux, ay + uy)], fill=(47, 107, 255, 235), width=lw)
    d.line([(ax + ux, ay + uy), (ax, ay + uy * 2)], fill=(47, 107, 255, 235), width=lw)
    d.line([(ax + ux * 1.35, ay + uy * 2), (ax + ux * 2.75, ay + uy * 2)], fill=(124, 92, 255, 225), width=lw)

    out = base.resize((SIZE, SIZE), Image.LANCZOS)
    return out


def main():
    root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    icon = build()
    for sub, name, size in OUT_DIRS:
        d = os.path.join(root, sub)
        os.makedirs(d, exist_ok=True)
        img = icon if size == SIZE else icon.resize((size, size), Image.LANCZOS)
        p = os.path.join(d, name)
        img.save(p, "PNG", optimize=True)
        print("wrote", p, size)


if __name__ == "__main__":
    main()
