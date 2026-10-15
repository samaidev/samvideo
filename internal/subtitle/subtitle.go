package subtitle

import (
	"fmt"
	"os"
	"strings"

	"samvideo/internal/models"
	"samvideo/internal/tts"
	"samvideo/internal/utils"
)

// GenerateFromWordBoundaries 从 TTS WordBoundary 时间戳生成字幕
func GenerateFromWordBoundaries(boundaries []tts.WordBoundary, srtPath, assPath string, params *models.VideoParams) error {
	if len(boundaries) == 0 {
		return fmt.Errorf("no word boundaries to generate subtitle")
	}

	if err := generateSRT(boundaries, srtPath, params); err != nil {
		return fmt.Errorf("generate SRT: %w", err)
	}

	if err := generateASS(boundaries, assPath, params); err != nil {
		return fmt.Errorf("generate ASS: %w", err)
	}

	utils.LogInfo("subtitle generated: %s, %s", srtPath, assPath)
	return nil
}

// generateSRT 生成 SRT 字幕文件
func generateSRT(boundaries []tts.WordBoundary, path string, params *models.VideoParams) error {
	var lines []string
	index := 1

	sentences := groupBoundaries(boundaries)

	for _, sent := range sentences {
		if strings.TrimSpace(sent.Text) == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("%d", index))
		lines = append(lines, fmt.Sprintf("%s --> %s",
			utils.FormatTimestamp(sent.Start),
			utils.FormatTimestamp(sent.End)))
		lines = append(lines, sent.Text)
		lines = append(lines, "")
		index++
	}

	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644)
}

// generateASS 生成 ASS 字幕文件（用于 FFmpeg 烧录）
func generateASS(boundaries []tts.WordBoundary, path string, params *models.VideoParams) error {
	fontName := params.FontName
	if fontName == "" {
		fontName = "Noto Sans CJK SC"
	}
	fontSize := params.FontSize
	if fontSize == 0 {
		fontSize = 48
	}
	primaryColor := params.TextForeColor
	if primaryColor == "" {
		primaryColor = "#FFFFFF"
	}
	assColor := hexToASSColor(primaryColor)
	strokeColor := params.StrokeColor
	if strokeColor == "" {
		strokeColor = "#000000"
	}
	assStrokeColor := hexToASSColor(strokeColor)
	strokeWidth := params.StrokeWidth
	if strokeWidth == 0 {
		strokeWidth = 2.0
	}

	alignment := 2
	if params.SubtitlePosition == "top" {
		alignment = 8
	}

	var ass strings.Builder
	ass.WriteString("[Script Info]\n")
	ass.WriteString("Title: SamVideo Subtitle\n")
	ass.WriteString("ScriptType: v4.00+\n")
	ass.WriteString("PlayResX: 1920\n")
	ass.WriteString("PlayResY: 1080\n")
	ass.WriteString("WrapStyle: 0\n\n")

	ass.WriteString("[V4+ Styles]\n")
	ass.WriteString("Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
	ass.WriteString(fmt.Sprintf("Style: Default,%s,%d,%s,%s,%s,%s,1,0,0,0,100,100,0,0,1,%.1f,0,%d,60,60,60,1\n\n",
		fontName, fontSize, assColor, assColor, assStrokeColor, assStrokeColor, strokeWidth, alignment))

	ass.WriteString("[Events]\n")
	ass.WriteString("Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")

	sentences := groupBoundaries(boundaries)
	for _, sent := range sentences {
		if strings.TrimSpace(sent.Text) == "" {
			continue
		}
		ass.WriteString(fmt.Sprintf("Dialogue: 0,%s,%s,Default,,0,0,0,,%s\n",
			utils.FormatTimestampASS(sent.Start),
			utils.FormatTimestampASS(sent.End),
			escapeASSText(sent.Text)))
	}

	return os.WriteFile(path, []byte(ass.String()), 0644)
}

// sentenceGroup 句子分组
type sentenceGroup struct {
	Text  string
	Start float64
	End   float64
}

// groupBoundaries 将 WordBoundary 按句子分组
func groupBoundaries(boundaries []tts.WordBoundary) []sentenceGroup {
	var groups []sentenceGroup
	var current sentenceGroup

	for i, b := range boundaries {
		if current.Text == "" {
			current.Start = b.Offset
		}
		current.Text += b.Text
		current.End = b.Offset + b.Duration

		if isSentenceEnd(b.Text) {
			groups = append(groups, current)
			current = sentenceGroup{}
		}
		_ = i
	}

	if current.Text != "" {
		groups = append(groups, current)
	}

	return groups
}

// isSentenceEnd 判断是否是句子结束（用 rune 正确处理中文）
func isSentenceEnd(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	runes := []rune(text)
	last := runes[len(runes)-1]
	switch last {
	case '.', '!', '?', ';', ',',
		'。', '！', '？', '；', '，':
		return true
	}
	return false
}

// hexToASSColor 将 #RRGGBB 转为 ASS 颜色 &HAABBGGRR
func hexToASSColor(hex string) string {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return "&H00FFFFFF"
	}
	r := hex[0:2]
	g := hex[2:4]
	b := hex[4:6]
	return fmt.Sprintf("&H00%s%s%s", b, g, r)
}

// escapeASSText 转义 ASS 文本
func escapeASSText(text string) string {
	text = strings.ReplaceAll(text, "\n", "\\N")
	text = strings.ReplaceAll(text, "{", "\\{")
	text = strings.ReplaceAll(text, "}", "\\}")
	return text
}
