#!/usr/bin/env python3
"""从 build/appicon.png 生成 Android 各密度图标。

产物：
  res/mipmap-{mdpi,hdpi,xhdpi,xxhdpi,xxxhdpi}/ic_launcher.png       方形（沿用源图的圆角方）
  res/mipmap-{...}/ic_launcher_round.png                            圆形（圆形裁切 + 背景渐变）
  res/mipmap-{...}/ic_launcher_foreground.png                       adaptive icon 前景（透明底，只有头部）
  res/mipmap-{...}/ic_launcher_monochrome.png                       adaptive icon 单色层（Android 13+ 主题图标/状态栏）
  res/mipmap-anydpi-v26/ic_launcher.xml, ic_launcher_round.xml      adaptive icon 声明

为什么这么做：
- 源图是「圆角方 + 细边框 + 深绿底 + 獴头像」。直接把整张图缩成圆形图标，会在
  圆周上切出半截细边框，很难看；所以圆形版重建为「渐变底 + 头部」，不带边框。
- adaptive icon（Android 8+）由启动器自己裁形状，前景必须落在 72dp 安全区里，
  否则各种裁切形（圆/方/水滴）会把耳朵切掉。这里头部宽度取画布的 54%，和源图
  中头部占 tile 的比例一致，既保住品牌观感又留足安全边距。
- monochrome 层（Android 13+）：系统用它画「主题图标」以及部分 ROM 的通知栏图标。
  之前没这一层，国产 ROM（vivo 实测）在通知栏里会退回用**应用图标**渲染，于是
  改了通知 smallIcon 也没变化。这里复用通知图标的白色剪影，保证是獴而不是 W。
"""
from PIL import Image, ImageDraw
import os
import shutil

# 单色层直接复用通知图标的剪影生成逻辑（同源、同形状，避免两套图不一致）
from importlib import util as _imp_util

_here = os.path.dirname(os.path.abspath(__file__))
_spec = _imp_util.spec_from_file_location(
    "notif_icon", os.path.join(_here, "gen-android-notification-icon.py")
)
_notif = _imp_util.module_from_spec(_spec)
_spec.loader.exec_module(_notif)

ROOT = _notif.ROOT
SRC = _notif.SRC

# 源图里头部（不含深绿底）的包围盒，实测值
HEAD_BOX = (258, 292, 766, 760)  # left, top, right, bottom（right/bottom 不含）
# 源图圆角方的范围（alpha bbox）
TILE_BOX = (42, 42, 983, 983)
# 背景垂直渐变的上下两端，从源图采样得到
GRAD_TOP = (15, 46, 39)
GRAD_BOTTOM = (7, 27, 24)

DENSITIES = {
    "mdpi": 1.0,
    "hdpi": 1.5,
    "xhdpi": 2.0,
    "xxhdpi": 3.0,
    "xxxhdpi": 4.0,
}

# 头部占画布的比例（与源图中头部/tile 的 0.54 一致）
HEAD_RATIO = 0.54


def load_head_rgba():
    """裁出头部，并把深绿背景抠成透明。"""
    src = Image.open(SRC).convert("RGBA")
    head = src.crop(HEAD_BOX)
    px = head.load()
    w, h = head.size
    for y in range(h):
        for x in range(w):
            r, g, b, a = px[x, y]
            # 深绿底：绿色是最强通道且偏暗。棕色描边/眼睛是 R 主导，不会被误抠。
            if a > 0 and g > r and g > b and g < 80 and (g - r) > 8:
                px[x, y] = (0, 0, 0, 0)
    return head


def gradient_tile(size):
    """生成 size×size 的深绿垂直渐变方形。"""
    tile = Image.new("RGBA", (size, size))
    draw = ImageDraw.Draw(tile)
    for y in range(size):
        t = y / max(1, size - 1)
        color = tuple(
            round(GRAD_TOP[i] + (GRAD_BOTTOM[i] - GRAD_TOP[i]) * t) for i in range(3)
        ) + (255,)
        draw.line([(0, y), (size - 1, y)], fill=color)
    return tile


def make_round(size, head):
    """圆形图标：渐变圆底 + 头部。"""
    scale = 4  # 超采样再缩，边缘更干净
    big = size * scale
    canvas = gradient_tile(big)
    # 圆形裁切
    mask = Image.new("L", (big, big), 0)
    ImageDraw.Draw(mask).ellipse((0, 0, big - 1, big - 1), fill=255)
    base = Image.new("RGBA", (big, big), (0, 0, 0, 0))
    base.paste(canvas, (0, 0), mask)

    # 头部按 HEAD_RATIO 缩放并居中
    target_w = int(big * HEAD_RATIO)
    ratio = target_w / head.width
    target_h = int(head.height * ratio)
    head_big = head.resize((target_w, target_h), Image.LANCZOS)
    base.alpha_composite(
        head_big, ((big - target_w) // 2, (big - target_h) // 2)
    )
    return base.resize((size, size), Image.LANCZOS)


def make_foreground(size, head):
    """adaptive icon 前景：透明底，头部落在安全区内。

    size 是 108dp 画布对应的像素数。头部宽度取 54%，居中 —— 启动器最激进的
    圆形裁切直径是 72/108 = 66.7%，54% 的头部四周仍有充足留白。
    """
    scale = 4
    big = size * scale
    canvas = Image.new("RGBA", (big, big), (0, 0, 0, 0))
    target_w = int(big * HEAD_RATIO)
    ratio = target_w / head.width
    target_h = int(head.height * ratio)
    head_big = head.resize((target_w, target_h), Image.LANCZOS)
    canvas.alpha_composite(
        head_big, ((big - target_w) // 2, (big - target_h) // 2)
    )
    return canvas.resize((size, size), Image.LANCZOS)


def make_square(size):
    """旧版方形图标：直接缩源图（保留品牌原貌：圆角方 + 细边框 + 渐变底）。"""
    src = Image.open(SRC).convert("RGBA")
    # 裁掉四周 4px 透明边，让图形更贴合画布
    left, top, right, bottom = TILE_BOX
    pad = 10
    box = (
        max(0, left - pad),
        max(0, top - pad),
        min(src.width, right + pad),
        min(src.height, bottom + pad),
    )
    cropped = src.crop(box)
    # 裁出来的区域补成正方形
    side = max(cropped.size)
    square = Image.new("RGBA", (side, side), (0, 0, 0, 0))
    square.alpha_composite(cropped, ((side - cropped.width) // 2, (side - cropped.height) // 2))
    return square.resize((size, size), Image.LANCZOS)


def main():
    head = load_head_rgba()

    for name, factor in DENSITIES.items():
        out_dir = os.path.join(ROOT, f"mipmap-{name}")
        os.makedirs(out_dir, exist_ok=True)

        legacy = round(48 * factor)
        make_square(legacy).save(os.path.join(out_dir, "ic_launcher.png"))
        make_round(legacy, head).save(os.path.join(out_dir, "ic_launcher_round.png"))

        # adaptive icon 画布是 108dp
        adaptive = round(108 * factor)
        make_foreground(adaptive, head).save(
            os.path.join(out_dir, "ic_launcher_foreground.png")
        )
        # monochrome：108dp 画布上的白色剪影（复用通知图标的 master 缩下来）
        _notif.build_master().resize((adaptive, adaptive), Image.LANCZOS).save(
            os.path.join(out_dir, "ic_launcher_monochrome.png")
        )

        print(f"mipmap-{name}: legacy {legacy}px, adaptive {adaptive}px")

    anydpi = os.path.join(ROOT, "mipmap-anydpi-v26")
    os.makedirs(anydpi, exist_ok=True)
    xml = """<?xml version="1.0" encoding="utf-8"?>
<adaptive-icon xmlns:android="http://schemas.android.com/apk/res/android">
    <background android:drawable="@color/ic_launcher_background" />
    <foreground android:drawable="@mipmap/ic_launcher_foreground" />
    <!-- Android 13+：系统画主题图标、部分 ROM 画通知栏图标都取这一层。 -->
    <monochrome android:drawable="@mipmap/ic_launcher_monochrome" />
</adaptive-icon>
"""
    for fname in ("ic_launcher.xml", "ic_launcher_round.xml"):
        with open(os.path.join(anydpi, fname), "w") as fh:
            fh.write(xml)
    print("mipmap-anydpi-v26: ic_launcher.xml, ic_launcher_round.xml")


if __name__ == "__main__":
    main()
