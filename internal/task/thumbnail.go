package task

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"samvideo/internal/utils"
)

// GenerateThumbnail 用 ffmpeg 从视频第1秒截取缩略图
func GenerateThumbnail(videoPath, thumbPath string) error {
	// 先检查视频文件是否存在
	if _, err := os.Stat(videoPath); err != nil {
		return fmt.Errorf("video file not found: %w", err)
	}

	// ffmpeg -ss 1 -i input.mp4 -vframes 1 -s 640x360 -q:v 2 output.jpg
	cmd := exec.Command("ffmpeg",
		"-y",
		"-ss", "1",       // 跳到第1秒
		"-i", videoPath,
		"-vframes", "1",   // 只取1帧
		"-s", "640x360",   // 缩略图尺寸
		"-q:v", "2",       // 高质量
		thumbPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg thumbnail: %w, output: %s", err, string(out))
	}

	// 验证文件生成
	if _, err := os.Stat(thumbPath); err != nil {
		return fmt.Errorf("thumbnail file not created: %w", err)
	}

	utils.LogInfo("thumbnail generated: %s", filepath.Base(thumbPath))
	return nil
}
