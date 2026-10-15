package utils

import (
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// LogInfo 信息日志
func LogInfo(format string, args ...interface{}) {
	log.Printf("[INFO] "+format, args...)
}

// LogWarn 警告日志
func LogWarn(format string, args ...interface{}) {
	log.Printf("[WARN] "+format, args...)
}

// LogError 错误日志
func LogError(format string, args ...interface{}) {
	log.Printf("[ERROR] "+format, args...)
}

// CheckFFmpeg 检查 FFmpeg 是否可用
func CheckFFmpeg() error {
	cmd := exec.Command("ffmpeg", "-version")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg not found in PATH: %w", err)
	}
	return nil
}

// GetMediaDuration 获取媒体文件时长（秒）
func GetMediaDuration(path string) (float64, error) {
	cmd := exec.Command("ffprobe",
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe: %w", err)
	}

	// 从 JSON 输出中解析 duration
	re := regexp.MustCompile(`"duration"\s*:\s*"([\d.]+)"`)
	matches := re.FindStringSubmatch(string(out))
	if len(matches) < 2 {
		return 0, fmt.Errorf("duration not found in ffprobe output")
	}

	duration, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return 0, fmt.Errorf("parse duration: %w", err)
	}

	return duration, nil
}

// FormatTimestamp 格式化时间戳为 SRT 格式 (HH:MM:SS,mmm)
func FormatTimestamp(seconds float64) string {
	hours := int(seconds) / 3600
	minutes := (int(seconds) % 3600) / 60
	secs := int(seconds) % 60
	millis := int((seconds - float64(int(seconds))) * 1000)
	return fmt.Sprintf("%02d:%02d:%02d,%03d", hours, minutes, secs, millis)
}

// FormatTimestampASS 格式化时间戳为 ASS 格式 (H:MM:SS.cc)
func FormatTimestampASS(seconds float64) string {
	hours := int(seconds) / 3600
	minutes := (int(seconds) % 3600) / 60
	secs := int(seconds) % 60
	cs := int((seconds - float64(int(seconds))) * 100)
	return fmt.Sprintf("%d:%02d:%02d.%02d", hours, minutes, secs, cs)
}

// SanitizeFilename 清理文件名
func SanitizeFilename(name string) string {
	re := regexp.MustCompile(`[^\w\-.]`)
	return re.ReplaceAllString(name, "_")
}

// SplitText 将文本按句子分割
func SplitText(text string) []string {
	// 按中文标点和英文标点分割
	re := regexp.MustCompile(`[。！？；\.\!\?\;]`)
	parts := re.Split(text, -1)
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}
