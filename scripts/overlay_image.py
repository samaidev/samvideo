#!/usr/bin/env python3
"""用 PIL 在图片上叠加半透明遮罩层、高亮文字、箭头等"""
import sys, json, os
from PIL import Image, ImageDraw, ImageFont

def find_font(size):
    for p in ['/System/Library/Fonts/PingFang.ttc', '/System/Library/Fonts/STHeiti Light.ttc',
              '/Library/Fonts/Arial Unicode.ttf', '/System/Library/Fonts/Helvetica.ttc']:
        if os.path.exists(p):
            try:
                return ImageFont.truetype(p, size)
            except:
                pass
    return ImageFont.load_default()

def draw_overlay(image_path, output_path, overlay_items):
    img = Image.open(image_path).convert('RGBA')
    W, H = img.size
    
    # 创建 overlay 图层
    overlay = Image.new('RGBA', (W, H), (0, 0, 0, 0))
    draw = ImageDraw.Draw(overlay)
    
    # 位置映射（基于图片尺寸比例）
    pos_map = {
        'top':          (0.5, 0.08),
        'center':       (0.5, 0.5),
        'bottom':       (0.5, 0.92),
        'top-left':     (0.15, 0.08),
        'top-right':    (0.85, 0.08),
        'bottom-left':  (0.15, 0.92),
        'bottom-right': (0.85, 0.92),
    }
    
    color_map = {
        'red':    (255, 60, 60, 255),
        'green':  (50, 200, 50, 255),
        'white':  (255, 255, 255, 255),
        'yellow': (255, 220, 50, 255),
    }
    
    for item in overlay_items:
        text = item.get('text', '')
        if not text:
            continue
        position = item.get('position', 'bottom')
        color_name = item.get('color', 'white')
        style = item.get('style', 'box')
        
        color = color_map.get(color_name, (255, 255, 255, 255))
        
        # 计算位置
        px, py = pos_map.get(position, pos_map['bottom'])
        cx, cy = int(W * px), int(H * py)
        
        if style == 'highlight':
            # 高亮大字 + 黑色描边
            font_size = int(H * 0.06)
            font = find_font(font_size)
            bbox = draw.textbbox((0, 0), text, font=font)
            tw, th = bbox[2] - bbox[0], bbox[3] - bbox[1]
            x = cx - tw // 2
            y = cy - th // 2
            # 半透明背景框
            box_pad = 15
            draw.rectangle([x - box_pad, y - box_pad, x + tw + box_pad, y + th + box_pad],
                          fill=(0, 0, 0, 140))
            # 描边
            for dx in [-2, -1, 0, 1, 2]:
                for dy in [-2, -1, 0, 1, 2]:
                    if dx == 0 and dy == 0:
                        continue
                    draw.text((x + dx, y + dy), text, font=font, fill=(0, 0, 0, 255))
            # 主文字
            draw.text((x, y), text, font=font, fill=color)
            
        elif style == 'box':
            # 半透明标签框
            font_size = int(H * 0.04)
            font = find_font(font_size)
            bbox = draw.textbbox((0, 0), text, font=font)
            tw, th = bbox[2] - bbox[0], bbox[3] - bbox[1]
            x = cx - tw // 2
            y = cy - th // 2
            # 半透明背景框
            box_pad = 12
            draw.rectangle([x - box_pad, y - box_pad, x + tw + box_pad, y + th + box_pad],
                          fill=(0, 0, 0, 160))
            # 边框线
            draw.rectangle([x - box_pad, y - box_pad, x + tw + box_pad, y + th + box_pad],
                          outline=color, width=2)
            # 文字
            draw.text((x, y), text, font=font, fill=color)
            
        elif style == 'arrow':
            # 箭头标注
            font_size = int(H * 0.045)
            font = find_font(font_size)
            arrow_text = '↑ ' + text
            bbox = draw.textbbox((0, 0), arrow_text, font=font)
            tw, th = bbox[2] - bbox[0], bbox[3] - bbox[1]
            x = cx - tw // 2
            y = cy - th // 2
            # 半透明背景
            box_pad = 10
            draw.rectangle([x - box_pad, y - box_pad, x + tw + box_pad, y + th + box_pad],
                          fill=(0, 0, 0, 140))
            # 描边
            for dx in [-2, -1, 0, 1, 2]:
                for dy in [-2, -1, 0, 1, 2]:
                    if dx == 0 and dy == 0:
                        continue
                    draw.text((x + dx, y + dy), arrow_text, font=font, fill=(0, 0, 0, 255))
            # 主文字
            draw.text((x, y), arrow_text, font=font, fill=color)
    
    # 合并图层
    result = Image.alpha_composite(img, overlay)
    result = result.convert('RGB')
    result.save(output_path, 'PNG')
    return True

if __name__ == '__main__':
    # 参数: image_path output_path overlay_json
    image_path = sys.argv[1]
    output_path = sys.argv[2]
    overlay_json = sys.argv[3]
    overlay_items = json.loads(overlay_json)
    draw_overlay(image_path, output_path, overlay_items)
