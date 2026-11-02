#!/usr/bin/env python3
"""PIL overlay 渲染引擎 — 完全按 LLM 返回的坐标和样式绘制，不做固定布局"""
import sys, json, os
from PIL import Image, ImageDraw, ImageFont

def find_font(size, bold=False):
    paths = [
        '/System/Library/Fonts/PingFang.ttc',
        '/System/Library/Fonts/STHeiti Light.ttc',
        '/System/Library/Fonts/Helvetica.ttc',
        '/Library/Fonts/Arial Unicode.ttf',
    ]
    for p in paths:
        if os.path.exists(p):
            try:
                font = ImageFont.truetype(p, size)
                if bold:
                    try:
                        font = ImageFont.truetype(p, size, index=1)
                    except:
                        pass
                return font
            except:
                pass
    return ImageFont.load_default()

def parse_color(color_str, default=(255, 255, 255, 255)):
    """解析颜色字符串：支持 #RRGGBB, #RRGGBBAA, red/green/white/yellow/blue"""
    if not color_str:
        return default
    color_map = {
        'red':    (255, 70, 70, 255),
        'green':  (60, 200, 80, 255),
        'white':  (255, 255, 255, 255),
        'yellow': (255, 210, 60, 255),
        'blue':   (80, 160, 255, 255),
    }
    if color_str.lower() in color_map:
        return color_map[color_str.lower()]
    if color_str.startswith('#'):
        hex_str = color_str[1:]
        if len(hex_str) == 6:
            r = int(hex_str[0:2], 16)
            g = int(hex_str[2:4], 16)
            b = int(hex_str[4:6], 16)
            return (r, g, b, 255)
        elif len(hex_str) == 8:
            r = int(hex_str[0:2], 16)
            g = int(hex_str[2:4], 16)
            b = int(hex_str[4:6], 16)
            a = int(hex_str[6:8], 16)
            return (r, g, b, a)
    return default

def rounded_rectangle(draw, xy, radius, fill=None, outline=None, width=1):
    """画圆角矩形"""
    x1, y1, x2, y2 = xy
    r = min(radius, (x2 - x1) // 2, (y2 - y1) // 2)
    if r < 1:
        draw.rectangle(xy, fill=fill, outline=outline, width=width)
        return
    draw.pieslice([x1, y1, x1+2*r, y1+2*r], 180, 270, fill=fill)
    draw.pieslice([x2-2*r, y1, x2, y1+2*r], 270, 360, fill=fill)
    draw.pieslice([x1, y2-2*r, x1+2*r, y2], 90, 180, fill=fill)
    draw.pieslice([x2-2*r, y2-2*r, x2, y2], 0, 90, fill=fill)
    draw.rectangle([x1+r, y1, x2-r, y2], fill=fill)
    draw.rectangle([x1, y1+r, x2, y2-r], fill=fill)
    if outline:
        draw.arc([x1, y1, x1+2*r, y1+2*r], 180, 270, fill=outline, width=width)
        draw.arc([x2-2*r, y1, x2, y1+2*r], 270, 360, fill=outline, width=width)
        draw.arc([x1, y2-2*r, x1+2*r, y2], 90, 180, fill=outline, width=width)
        draw.arc([x2-2*r, y2-2*r, x2, y2], 0, 90, fill=outline, width=width)
        draw.line([x1+r, y1, x2-r, y1], fill=outline, width=width)
        draw.line([x1+r, y2, x2-r, y2], fill=outline, width=width)
        draw.line([x1, y1+r, x1, y2-r], fill=outline, width=width)
        draw.line([x2, y1+r, x2, y2-r], fill=outline, width=width)

def draw_text_with_shadow(draw, xy, text, font, fill, shadow=(0,0,0,200), offset=2):
    x, y = xy
    for dx in range(-offset, offset+1):
        for dy in range(-offset, offset+1):
            if dx == 0 and dy == 0:
                continue
            draw.text((x+dx, y+dy), text, font=font, fill=shadow)
    draw.text((x, y), text, font=font, fill=fill)

def wrap_text(text, font, max_width, draw):
    if not text:
        return [""]
    runes = list(text)
    lines = []
    current = ""
    for ch in runes:
        test = current + ch
        bbox = draw.textbbox((0, 0), test, font=font)
        w = bbox[2] - bbox[0]
        if w > max_width and current:
            lines.append(current)
            current = ch
        else:
            current = test
    if current:
        lines.append(current)
    return lines

def draw_overlay(image_path, output_path, overlay_items):
    img = Image.open(image_path).convert('RGBA')
    W, H = img.size
    overlay = Image.new('RGBA', (W, H), (0, 0, 0, 0))
    draw = ImageDraw.Draw(overlay)

    # ========== 按 style 分组，同一卡片区域的元素排在一起 ==========
    # 用 (x,y,width,height) 作为 key 分组
    cards = {}  # key -> {"items": [], "rect": (x,y,w,h)}
    standalone = []  # 不属于卡片的独立元素

    for item in overlay_items:
        text = item.get('text', '')
        if not text:
            continue
        has_layout = item.get('x') is not None and item.get('y') is not None
        if has_layout:
            x = item.get('x', 0)
            y = item.get('y', 0)
            w = item.get('width', 0.3)
            h = item.get('height', 0.2)
            key = (round(x, 3), round(y, 3), round(w, 3), round(h, 3))
            if key not in cards:
                cards[key] = {"items": [], "rect": (x, y, w, h)}
            cards[key]["items"].append(item)
        else:
            standalone.append(item)

    # ========== 绘制每个卡片 ==========
    for key, card in cards.items():
        items = card["items"]
        rx, ry, rw, rh = card["rect"]
        # 转换为像素坐标
        px = int(W * rx)
        py = int(H * ry)
        pw = int(W * rw)
        ph = int(H * rh)

        # 取卡片的背景色和边框（从第一个有 bg_color 的元素取）
        bg_color = parse_color(None, (15, 25, 45, 200))
        border_color = None
        border_radius = 14
        for item in items:
            if item.get('bg_color'):
                bg_color = parse_color(item['bg_color'], bg_color)
            if item.get('border_color'):
                border_color = parse_color(item['border_color'], None)
            if item.get('border_radius') is not None:
                border_radius = item['border_radius']

        # 画卡片背景
        rounded_rectangle(draw, [px, py, px+pw, py+ph], border_radius, fill=bg_color, outline=border_color, width=2)

        # 左侧装饰条
        accent_x = px + 5
        accent_top = py + border_radius
        accent_bottom = py + ph - border_radius
        if accent_bottom > accent_top:
            segments = 20
            seg_h = (accent_bottom - accent_top) / segments
            for i in range(segments):
                ratio = i / segments
                r = int(255 * (1 - ratio) + 80 * ratio)
                g = int(100 * (1 - ratio) + 160 * ratio)
                b = int(60 * (1 - ratio) + 255 * ratio)
                draw.rectangle([accent_x, accent_top + i*seg_h, accent_x + 3, accent_top + (i+1)*seg_h], fill=(r, g, b, 220))

        # 按 style 排序：title -> dataline -> tag -> highlight
        style_order = {'title': 0, 'dataline': 1, 'tag': 2, 'highlight': 3, 'card': 0}
        items_sorted = sorted(items, key=lambda x: style_order.get(x.get('style', ''), 9))

        # 在卡片内排列元素
        content_x = px + 18
        content_w = pw - 36
        y_cursor = py + 12

        # 先画 title
        title_items = [i for i in items_sorted if i.get('style') == 'title']
        data_items = [i for i in items_sorted if i.get('style') == 'dataline']
        tag_items = [i for i in items_sorted if i.get('style') == 'tag']
        highlight_items = [i for i in items_sorted if i.get('style') == 'highlight']
        other_items = [i for i in items_sorted if i.get('style') not in ('title', 'dataline', 'tag', 'highlight')]

        # 标题
        for item in title_items:
            fs = item.get('font_size') or int(H * 0.035)
            font = find_font(fs, bold=True)
            color = parse_color(item.get('color'), (255, 255, 255, 255))
            text = item.get('text', '')
            lines = wrap_text(text, font, content_w, draw)
            if len(lines) > 2:
                lines = lines[:2]
            # 标题背景条
            title_bg_h = len(lines) * (fs + 4) + 8
            rounded_rectangle(draw, [px+14, y_cursor-4, px+pw-10, y_cursor+title_bg_h], 6, fill=(30, 50, 80, 180))
            for tl in lines:
                draw_text_with_shadow(draw, (content_x, y_cursor), tl, font, color, offset=1)
                y_cursor += fs + 4
            y_cursor += 12

        # 数据行
        for idx, item in enumerate(data_items):
            fs = item.get('font_size') or int(H * 0.028)
            font = find_font(fs, bold=True)
            label_font = find_font(max(fs - 6, 16))
            color = parse_color(item.get('color'), (255, 255, 255, 255))
            text = item.get('text', '')
            label = item.get('label', '')
            align = item.get('align', 'left')

            if not text:
                continue

            bbox = draw.textbbox((0, 0), text, font=font)
            tw = bbox[2] - bbox[0]

            # 标签
            if label:
                draw.text((content_x, y_cursor), label, font=label_font, fill=(180, 190, 210, 220))

            # 数据值
            if align == 'right':
                data_x = px + pw - 18 - tw
            elif align == 'center':
                data_x = content_x + (content_w - tw) // 2
            else:
                data_x = content_x
            draw_text_with_shadow(draw, (data_x, y_cursor), text, font, color, offset=1)

            # 分隔线
            if idx < len(data_items) - 1:
                line_y = y_cursor + fs + 6
                draw.line([content_x, line_y, px+pw-18, line_y], fill=(60, 80, 110, 120), width=1)

            y_cursor += fs + 14

        # 标签行
        if tag_items:
            y_cursor += 6
            tag_x = content_x
            tag_fs = tag_items[0].get('font_size') or int(H * 0.022)
            tag_font = find_font(tag_fs)
            for item in tag_items:
                tag_text = item.get('text', '')
                tag_color = parse_color(item.get('color'), (80, 160, 255, 255))
                if not tag_text:
                    continue
                bbox = draw.textbbox((0, 0), tag_text, font=tag_font)
                tw = bbox[2] - bbox[0]
                th = bbox[3] - bbox[1]
                tag_w = tw + 20
                tag_h = tag_fs + 10
                # 圆角标签
                tag_bg = (*tag_color[:3], 60)
                tag_border = (*tag_color[:3], 180)
                rounded_rectangle(draw, [tag_x, y_cursor, tag_x+tag_w, y_cursor+tag_h], 8, fill=tag_bg, outline=tag_border, width=1)
                text_y = y_cursor + (tag_h - th) // 2 - 2
                draw.text((tag_x+10, text_y), tag_text, font=tag_font, fill=tag_color)
                tag_x += tag_w + 8
            y_cursor += tag_fs + 16

        # 高亮数字
        for item in highlight_items:
            fs = item.get('font_size') or int(H * 0.07)
            font = find_font(fs, bold=True)
            color = parse_color(item.get('color'), (255, 255, 255, 255))
            text = item.get('text', '')
            bbox = draw.textbbox((0, 0), text, font=font)
            tw = bbox[2] - bbox[0]
            th = bbox[3] - bbox[1]
            # 右上角
            hx = px + pw - tw - 20
            hy = py + 10
            pad = 14
            rounded_rectangle(draw, [hx-pad, hy-pad//2, hx+tw+pad, hy+th+pad//2], 10,
                              fill=(15, 25, 45, 180), outline=(*color[:3], 120), width=2)
            draw_text_with_shadow(draw, (hx, hy), text, font, color, offset=2)

        # 其他元素
        for item in other_items:
            fs = item.get('font_size') or int(H * 0.028)
            font = find_font(fs)
            color = parse_color(item.get('color'), (255, 255, 255, 255))
            text = item.get('text', '')
            draw_text_with_shadow(draw, (content_x, y_cursor), text, font, color, offset=1)
            y_cursor += fs + 8

    # ========== 绘制独立元素（无卡片坐标的，兼容旧格式） ==========
    pos_map = {
        'top': (0.5, 0.08), 'center': (0.5, 0.5), 'bottom': (0.5, 0.92),
        'top-left': (0.15, 0.08), 'top-right': (0.85, 0.08),
        'bottom-left': (0.15, 0.85), 'bottom-right': (0.85, 0.85),
    }
    for item in standalone:
        text = item.get('text', '')
        if not text:
            continue
        position = item.get('position', 'bottom')
        color = parse_color(item.get('color'), (255, 255, 255, 255))
        style = item.get('style', 'box')
        fs = item.get('font_size') or int(H * 0.04)
        font = find_font(fs, bold=(style == 'title' or style == 'highlight'))

        px, py = pos_map.get(position, pos_map['bottom'])
        cx, cy = int(W * px), int(H * py)
        bbox = draw.textbbox((0, 0), text, font=font)
        tw, th = bbox[2]-bbox[0], bbox[3]-bbox[1]
        x = cx - tw // 2
        y = cy - th // 2
        pad = 14
        rounded_rectangle(draw, [x-pad, y-pad, x+tw+pad, y+th+pad], 10,
                          fill=(15, 25, 45, 180), outline=(*color[:3], 120), width=2)
        draw_text_with_shadow(draw, (x, y), text, font, color, offset=2)

    # 合并图层
    result = Image.alpha_composite(img, overlay)
    result = result.convert('RGB')
    result.save(output_path, 'PNG')
    return True

if __name__ == '__main__':
    image_path = sys.argv[1]
    output_path = sys.argv[2]
    overlay_json = sys.argv[3]
    overlay_items = json.loads(overlay_json)
    draw_overlay(image_path, output_path, overlay_items)
