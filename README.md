# SamVideo

AI-powered short video generator — give it a topic, get a finished MP4 with voiceover, subtitles, background music, and AI-generated visuals.

## Features

- **One-topic-to-video**: Submit a text topic, get a finished video automatically
- **AI Script Generation**: LLM writes a ~200-word narration script from your topic
- **Text-to-Speech**: Edge TTS with multiple Chinese voices
- **AI Image Generation**: Generates unique images for each scene with 8 zoom-pan variants
- **Background Music**: 20 royalty-free BGM tracks included, auto-selected per video
- **Template System**: 8 built-in templates across 6 categories with LLM auto-classification
- **Video Effects**: Clip speed control, 7 transition styles, subtitle support (SRT + ASS)
- **Single Binary**: Pure Go, no Python runtime needed (except Edge TTS CLI)

## Quick Start

### Prerequisites

- Go 1.21+
- FFmpeg (with libx264, libmp3lame)
- Python 3 with `edge-tts` (`pip install edge-tts`)
- An OpenAI-compatible LLM API key
- An OpenAI-compatible image generation API key

### Build

```bash
go build -o samvideo ./cmd/samvideo/
```

### Configure

Copy `config.example.toml` to `config.toml` and fill in your API keys:

```toml
# LLM for script generation
openai_base_url = "https://your-api.com/v1"
openai_api_key = "your-key"
openai_model = "your-model"

# Image generation
openai_image_base_url = "https://your-api.com/v1"
openai_image_api_keys = ["your-key"]
openai_image_model = "your-image-model"
openai_image_size = "1280x720"

# Server
port = 8100
video_source = "openai_image"
bgm_type = "random"
bgm_volume = 0.55
```

### Run

```bash
./samvideo -config config.toml -port 8100
```

## API Usage

### Create Video

```bash
# Auto-classify topic and select template
curl -X POST http://127.0.0.1:8100/api/v1/videos \
  -H "Content-Type: application/json" \
  -d '{"video_subject": "How AI is changing the future"}'
```

Response:
```json
{
  "status": 200,
  "data": {
    "task_id": "abc-123-...",
    "hint": "task started, poll GET /api/v1/tasks/{task_id} for status"
  }
}
```

### Specify a Template

```bash
curl -X POST "http://127.0.0.1:8100/api/v1/videos?template_id=motivational" \
  -H "Content-Type: application/json" \
  -d '{"video_subject": "Never give up"}'
```

### List Templates

```bash
curl http://127.0.0.1:8100/api/v1/templates
```

### Check Task Status

```bash
curl http://127.0.0.1:8100/api/v1/tasks/{task_id}
```

States: `processing(4)` → `complete(1)` / `failed(5)`

### Download Video

```bash
curl -O http://127.0.0.1:8100/api/v1/download/{task_id}/final.mp4
```

### Stream Video

Open in browser:
```
http://127.0.0.1:8100/api/v1/stream/{task_id}/final.mp4
```

## Templates

| ID | Category | Aspect | Speed | Transition | BGM Volume |
|----|----------|--------|-------|------------|------------|
| tech_app | tech | 16:9 | 1.2x | shuffle | 0.55 |
| tech_app_vertical | tech | 9:16 | 1.2x | shuffle | 0.55 |
| motivational | motivational | 9:16 | 0.8x | fade_in | 0.45 |
| motivational_landscape | motivational | 16:9 | 0.8x | fade_in | 0.45 |
| academic | academic | 16:9 | 1.0x | shuffle | 0.30 |
| news | news | 16:9 | 1.3x | shuffle | 0.25 |
| marketing | marketing | 9:16 | 1.5x | shuffle | 0.40 |
| story | story | 16:9 | 0.9x | fade_in | 0.50 |

When `video_aspect` is omitted, the LLM auto-classifies the topic into one of 6 categories and selects the best template.

## BGM Library

20 royalty-free music tracks are included in `resource/songs/downloaded/`:

- **Pixabay** (8 tracks) — No copyright, free for commercial use
- **Incompetech / Kevin MacLeod** (12 tracks) — CC-BY 4.0

If no downloaded music is found, a Python script (`scripts/gen_bgm.py`) synthesizes piano-style BGM as fallback.

## License

MIT
