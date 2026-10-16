package tts

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"samvideo/internal/config"
	"samvideo/internal/utils"
)

// WordBoundary TTS WordBoundary 时间戳
type WordBoundary struct {
	Offset   float64 `json:"offset"`
	Duration float64 `json:"duration"`
	Text     string  `json:"text"`
}

// Synthesize 使用 Edge TTS (Python edge-tts CLI) 合成语音
func Synthesize(text, voiceName string, rate float64, outputPath string, cfg *config.Config) ([]WordBoundary, error) {
	if voiceName == "" {
		voiceName = "zh-CN-YunxiNeural"
	}
	if rate == 0 {
		rate = 1.0
	}

	utils.LogInfo("TTS: voice=%s, rate=%.1f, text=%d chars", voiceName, rate, len(text))

	// 生成临时文件路径
	taskDir := filepath.Dir(outputPath)
	audioPath := outputPath
	subPath := filepath.Join(taskDir, "tts_sub.json")

	// 构建 edge-tts 命令
	rateStr := fmt.Sprintf("%+d%%", int((rate-1)*100))

	args := []string{
		"-m", "edge_tts",
		"--voice", voiceName,
		"--rate", rateStr,
		"--text", text,
		"--write-media", audioPath,
		"--write-subtitles", subPath,
	}

	utils.LogInfo("calling edge-tts CLI...")
	cmd := exec.Command("python3", args...)
	cmd.Env = append(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("edge-tts CLI failed: %w, output: %s", err, string(out))
	}

	// 检查音频文件
	if _, err := os.Stat(audioPath); err != nil {
		return nil, fmt.Errorf("audio file not created: %w", err)
	}

	// 解析字幕文件生成 WordBoundary
	boundaries, err := parseSRTToBoundaries(subPath)
	if err != nil {
		utils.LogWarn("failed to parse subtitle timestamps: %v, continuing without word boundaries", err)
		// 没有时间戳也能继续，字幕会用 SRT 文件
		boundaries = []WordBoundary{}
	}

	// 获取音频时长
	audioSize, _ := os.Stat(audioPath)
	utils.LogInfo("TTS done: %s (%.1f KB, %d boundaries)", audioPath, float64(audioSize.Size())/1024, len(boundaries))

	return boundaries, nil
}

// parseSRTToBoundaries 解析 edge-tts 生成的 SRT 字幕文件，转为 WordBoundary
func parseSRTToBoundaries(srtPath string) ([]WordBoundary, error) {
	data, err := os.ReadFile(srtPath)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(data), "\n")
	var boundaries []WordBoundary

	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		// 跳过序号行
		if line == "" {
			continue
		}
		// 查找时间行 (00:00:00.000 --> 00:00:00.000)
		if strings.Contains(line, "-->") {
			parts := strings.Split(line, "-->")
			if len(parts) != 2 {
				continue
			}
			start := parseSRTTime(strings.TrimSpace(parts[0]))
			end := parseSRTTime(strings.TrimSpace(parts[1]))

			// 下一行是文本
			if i+1 < len(lines) {
				text := strings.TrimSpace(lines[i+1])
				if text != "" {
					boundaries = append(boundaries, WordBoundary{
						Offset:   start,
						Duration: end - start,
						Text:     text,
					})
				}
			}
		}
	}

	return boundaries, nil
}

// parseSRTTime 解析 SRT 时间戳 (00:00:00.000 或 00:00:00,000)
func parseSRTTime(s string) float64 {
	s = strings.ReplaceAll(s, ",", ".")
	// 格式: HH:MM:SS.mmm
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0
	}

	var h, m, sec float64
	fmt.Sscanf(parts[0], "%f", &h)
	fmt.Sscanf(parts[1], "%f", &m)
	fmt.Sscanf(parts[2], "%f", &sec)

	return h*3600 + m*60 + sec
}

// unused but kept for reference
var _ = json.Marshal
var _ = time.Now

// SynthesizeScenes 逐段合成语音，返回每段音频路径和时长
// 每段 narration 单独做 TTS，确保图片可以按语音时长对齐
func SynthesizeScenes(scenes []SceneNarration, voiceName string, rate float64, taskDir string, cfg *config.Config) ([]SceneAudio, error) {
	if voiceName == "" {
		voiceName = "zh-CN-YunxiNeural"
	}
	if rate == 0 {
		rate = 1.0
	}

	rateStr := fmt.Sprintf("%+d%%", int((rate-1)*100))
	var results []SceneAudio

	for i, scene := range scenes {
		audioPath := filepath.Join(taskDir, fmt.Sprintf("scene_%03d.mp3", i))
		subPath := filepath.Join(taskDir, fmt.Sprintf("scene_%03d.srt", i))

		utils.LogInfo("TTS scene %d/%d: %d chars", i+1, len(scenes), len(scene.Narration))

		args := []string{
			"-m", "edge_tts",
			"--voice", voiceName,
			"--rate", rateStr,
			"--text", scene.Narration,
			"--write-media", audioPath,
			"--write-subtitles", subPath,
		}

		cmd := exec.Command("python3", args...)
		cmd.Env = append(os.Environ())
		out, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("edge-tts scene %d failed: %w, output: %s", i, err, string(out))
		}

		// 获取音频时长
		duration, err := utils.GetMediaDuration(audioPath)
		if err != nil {
			utils.LogWarn("failed to get duration for scene %d: %v, using default 5s", i, err)
			duration = 5.0
		}

		// 解析字幕获取 WordBoundary
		boundaries, _ := parseSRTToBoundaries(subPath)

		results = append(results, SceneAudio{
			AudioPath:  audioPath,
			Duration:   duration,
			Narration:  scene.Narration,
			Index:      i,
			Boundaries: boundaries,
		})

		utils.LogInfo("TTS scene %d done: %.1fs, %s", i+1, duration, audioPath)
	}

	return results, nil
}

// SceneNarration 分镜旁白文本（供 TTS 使用）
type SceneNarration struct {
	Narration string
}

// SceneAudio 单段语音结果
type SceneAudio struct {
	AudioPath  string
	Duration   float64
	Narration  string
	Index      int
	Boundaries []WordBoundary
}
