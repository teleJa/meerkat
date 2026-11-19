#!/usr/bin/env python3
"""从 build/appicon.png 生成 Android 通知栏小图标（白色剪影 + 挖空五官）。

为什么需要单独一套：
- 状态栏小图标必须是**纯白 + 透明**的单色图，系统只取 alpha 通道。直接把彩色
  appicon 塞进去，Android 会渲染成一坨白色方块（9.0+ 尤其明显）。
- 桌面图标是「深绿底 + 米色脸」，缩小到 24dp 就糊了；所以通知图标只保留头部
  轮廓 + 挖空的五官，在 24dp 下仍然认得出是獴。

抠图规则（不靠手工描点，跟着源图走）：
- 背景 = 深绿（G 最强、偏暗）。源图是圆角方，四角是背景。
- 耳朵是深棕、贴着头部外轮廓 → 「接触背景的深色连通块」，保留为实体。
- 眼睛/鼻子是深棕、被脸包住 → 「不接触背景的深色连通块」，挖成透明洞。
- 眼睛里的白色高光是更亮的圆点，会被「填洞」一并填掉，避免 24dp 下变成噪点。
"""
from PIL import Image
import numpy as np
from collections import deque
import os

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(HERE)
SRC = os.path.join(REPO, "desktop/MeerkatDesktop/build/appicon.png")
ROOT = os.path.join(REPO, "desktop/MeerkatDesktop/build/android/app/src/main/res")
HEAD_BOX = (258, 292, 766, 760)  # 与 gen-android-icons.py 保持一致

# 内容占画布的比例。Android 状态栏图标是 24dp，图形太满会顶到边、太小又发虚。
ICON_FILL = 0.90


def load_head():
    return Image.open(SRC).convert("RGB").crop(HEAD_BOX)


def green_background(rgb):
    r, g, b = rgb[:, :, 0].astype(np.int16), rgb[:, :, 1].astype(np.int16), rgb[:, :, 2].astype(np.int16)
    return (g > r) & (g > b) & (g < 85) & ((g - r) > 8)


def dark_pixels(rgb):
    r, g, b = rgb[:, :, 0].astype(np.int16), rgb[:, :, 1].astype(np.int16), rgb[:, :, 2].astype(np.int16)
    return (r < 110) & (g < 90) & (b < 80)


def components(mask):
    """4 邻域连通块，返回每个块的坐标列表。图很小（508×468），BFS 足够。"""
    h, w = mask.shape
    seen = np.zeros_like(mask, dtype=bool)
    out = []
    for y in range(h):
        for x in range(w):
            if not mask[y, x] or seen[y, x]:
                continue
            q = deque([(y, x)])
            seen[y, x] = True
            pixels = []
            while q:
                cy, cx = q.popleft()
                pixels.append((cy, cx))
                for dy, dx in ((1, 0), (-1, 0), (0, 1), (0, -1)):
                    ny, nx = cy + dy, cx + dx
                    if 0 <= ny < h and 0 <= nx < w and mask[ny, nx] and not seen[ny, nx]:
                        seen[ny, nx] = True
                        q.append((ny, nx))
            out.append(pixels)
    return out


def touches(mask, pixels, background):
    """块边缘是否挨着背景（= 属于轮廓的一部分，比如耳朵）。"""
    h, w = mask.shape
    for y, x in pixels:
        for dy, dx in ((1, 0), (-1, 0), (0, 1), (0, -1)):
            ny, nx = y + dy, x + dx
            if not (0 <= ny < h and 0 <= nx < w) or background[ny, nx]:
                return True
    return False


def fill_holes(source, pixels):
    """把 source 上一个连通块内部的孔洞填满，返回 bbox 内的实心块。"""
    ys = [p[0] for p in pixels]
    xs = [p[1] for p in pixels]
    y0, y1, x0, x1 = min(ys), max(ys), min(xs), max(xs)
    sub = source[y0:y1 + 1, x0:x1 + 1].copy()
    h, w = sub.shape
    outside = np.zeros_like(sub, dtype=bool)
    q = deque()
    for x in range(w):
        for y in (0, h - 1):
            if not sub[y, x] and not outside[y, x]:
                outside[y, x] = True
                q.append((y, x))
    for y in range(h):
        for x in (0, w - 1):
            if not sub[y, x] and not outside[y, x]:
                outside[y, x] = True
                q.append((y, x))
    while q:
        cy, cx = q.popleft()
        for dy, dx in ((1, 0), (-1, 0), (0, 1), (0, -1)):
            ny, nx = cy + dy, cx + dx
            if 0 <= ny < h and 0 <= nx < w and not sub[ny, nx] and not outside[ny, nx]:
                outside[ny, nx] = True
                q.append((ny, nx))
    filled = sub | (~outside)
    return y0, y1, x0, x1, filled


def build_master():
    head = load_head()
    rgb = np.array(head)
    bg = green_background(rgb)
    head_shape = ~bg
    dark = dark_pixels(rgb) & head_shape

    holes = np.zeros_like(head_shape)
    for pixels in components(dark):
        if len(pixels) < 200:      # 噪点
            continue
        if touches(dark, pixels, bg):
            continue               # 耳朵：贴轮廓，保留为实体
        y0, y1, x0, x1, filled = fill_holes(dark, pixels)
        holes[y0:y1 + 1, x0:x1 + 1] |= filled

    alpha = (head_shape & ~holes).astype(np.uint8) * 255
    out = np.zeros((head.height, head.width, 4), dtype=np.uint8)
    out[:, :, 0] = out[:, :, 1] = out[:, :, 2] = 255
    out[:, :, 3] = alpha
    img = Image.fromarray(out)

    bbox = img.getbbox()
    img = img.crop(bbox)
    side = max(img.size)
    canvas = Image.new("RGBA", (side, side), (0, 0, 0, 0))
    canvas.alpha_composite(img, ((side - img.width) // 2, (side - img.height) // 2))

    # 收到 ICON_FILL，留出状态栏图标自己的安全边
    inner = Image.new("RGBA", (side, side), (0, 0, 0, 0))
    scaled = canvas.resize((int(side * ICON_FILL), int(side * ICON_FILL)), Image.LANCZOS)
    inner.alpha_composite(scaled, ((side - scaled.width) // 2, (side - scaled.height) // 2))
    return inner.resize((1024, 1024), Image.LANCZOS)


def main():
    master = build_master()
    # drawable 而不是 mipmap：通知图标不是启动器图标，放 drawable-* 更符合惯例
    for name, px in (("mdpi", 24), ("hdpi", 36), ("xhdpi", 48), ("xxhdpi", 72), ("xxxhdpi", 96)):
        out_dir = os.path.join(ROOT, f"drawable-{name}")
        os.makedirs(out_dir, exist_ok=True)
        master.resize((px, px), Image.LANCZOS).save(os.path.join(out_dir, "ic_notification.png"))
        print(f"drawable-{name}/ic_notification.png  {px}x{px}")


if __name__ == "__main__":
    main()
