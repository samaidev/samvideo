# SamVideo

[English](README.md) | 中文

AI 驱动的短视频生成器 —— 输入一个主题，自动生成带配音、字幕、背景音乐和 AI 配图的完整 MP4 视频。

## 功能

- **主题转视频**：提交一段文字主题，自动生成完整视频
- **AI 脚本生成**：LLM 根据主题撰写约 200 字解说脚本
- **语音合成**：Edge TTS，多种中文音色可选
- **AI 文生图**：每个场景生成独特图片，8 种 zoom-pan 效果轮换
- **背景音乐**：内置 20 首无版权 BGM，自动随机选择
- **模板系统**：6 类 8 个模板，LLM 自动分类选模板
- **视频效果**：片段变速、7 种转场效果、字幕（SRT + ASS）
- **单二进制**：纯 Go 编写，无需 Python 运行时（Edge TTS CLI 除外）

## 快速开始

### 环境要求

- Go 1.21+
- FFmpeg（需含 libx264、libmp3lame）
- Python 3 + `edge-tts`（`pip install edge-tts`）
- OpenAI 兼容的 LLM API Key
- OpenAI 兼容的文生图 API Key

### 编译

```bash
go build -o samvideo ./cmd/samvideo/
```

### 配置

复制 `config.example.toml` 为 `config.toml`，填入 API Key：

```toml
# LLM 脚本生成
openai_base_url = "https://your-api.com/v1"
openai_api_key = "your-key"
openai_model = "your-model"

# 文生图
openai_image_base_url = "https://your-api.com/v1"
openai_image_api_keys = ["your-key"]
openai_image_model = "your-image-model"
openai_image_size = "1280x720"

# 服务
port = 8100
video_source = "openai_image"
bgm_type = "random"
bgm_volume = 0.55
```

### 启动

```bash
./samvideo -config config.toml -port 8100
```

## API 用法

### 创建视频

```bash
# 自动分类主题并选择模板
curl -X POST http://127.0.0.1:8100/api/v1/videos \
  -H "Content-Type: application/json" \
  -d '{"video_subject": "人工智能如何改变未来生活"}'
```

返回：
```json
{
  "status": 200,
  "data": {
    "task_id": "abc-123-...",
    "hint": "task started, poll GET /api/v1/tasks/{task_id} for status"
  }
}
```

### 指定模板

```bash
curl -X POST "http://127.0.0.1:8100/api/v1/videos?template_id=motivational" \
  -H "Content-Type: application/json" \
  -d '{"video_subject": "坚持就是胜利"}'
```

### 查看模板列表

```bash
curl http://127.0.0.1:8100/api/v1/templates
```

### 查询任务状态

```bash
curl http://127.0.0.1:8100/api/v1/tasks/{task_id}
```

状态：`processing(4)` → `complete(1)` / `failed(5)`

### 下载视频

```bash
curl -O http://127.0.0.1:8100/api/v1/download/{task_id}/final.mp4
```

### 在线播放

浏览器打开：
```
http://127.0.0.1:8100/api/v1/stream/{task_id}/final.mp4
```

## 模板

| 模板 ID | 类别 | 横竖屏 | 变速 | 转场 | BGM 音量 |
|---------|------|--------|------|------|---------|
| tech_app | 技术应用 | 16:9 | 1.2x | shuffle | 0.55 |
| tech_app_vertical | 技术应用 | 9:16 | 1.2x | shuffle | 0.55 |
| motivational | 励志鸡汤 | 9:16 | 0.8x | fade_in | 0.45 |
| motivational_landscape | 励志鸡汤 | 16:9 | 0.8x | fade_in | 0.45 |
| academic | 学术科普 | 16:9 | 1.0x | shuffle | 0.30 |
| news | 资讯播报 | 16:9 | 1.3x | shuffle | 0.25 |
| marketing | 营销推广 | 9:16 | 1.5x | shuffle | 0.40 |
| story | 故事叙述 | 16:9 | 0.9x | fade_in | 0.50 |

不指定 `video_aspect` 时，LLM 自动将主题分类到 6 个类别之一，并选择最佳模板。

## BGM 素材库

内置 20 首无版权音乐，位于 `resource/songs/downloaded/`：

- **Pixabay**（8 首）— 无版权，可免费商用
- **Incompetech / Kevin MacLeod**（12 首）— CC-BY 4.0

若无下载音乐，Python 脚本（`scripts/gen_bgm.py`）会合成钢琴风格 BGM 作为后备。

## 许可证

MIT
