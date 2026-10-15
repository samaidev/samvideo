package video

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"samvideo/internal/config"
	"samvideo/internal/models"
	"samvideo/internal/utils"
)

// ProcessAndConcatenate 处理视频片段并拼接
func ProcessAndConcatenate(clipPaths []string, outputPath string, audioDuration float64, params *models.VideoParams, cfg *config.Config) error {
	if len(clipPaths) == 0 {
		return fmt.Errorf("no clips to process")
	}

	width, height := getResolution(params.VideoAspect)
	clipDuration := params.VideoClipDuration
	if clipDuration <= 0 {
		clipDuration = 5
	}
	if clipDuration > 15 {
		clipDuration = 15
	}

	totalNeeded := int(audioDuration/float64(clipDuration)) + 1
	utils.LogInfo("clips: %d source, need %d clips for %.1fs audio (clip dur=%ds)", len(clipPaths), totalNeeded, audioDuration, clipDuration)

	// 处理每个片段：resize + 裁剪到指定时长
	// 注意：用 processed_ 前缀避免和素材文件重名
	taskDir := filepath.Dir(outputPath)
	processedClips := []string{}
	for i, clip := range clipPaths {
		processed := filepath.Join(taskDir, fmt.Sprintf("processed_%03d.mp4", i))
		err := processOneClip(clip, processed, width, height, clipDuration, params)
		if err != nil {
			utils.LogWarn("failed to process clip %d (%s): %v, skipping", i, clip, err)
			continue
		}
		processedClips = append(processedClips, processed)
		utils.LogInfo("processed clip %d: %s -> %s", i, clip, processed)
	}

	if len(processedClips) == 0 {
		return fmt.Errorf("no clips processed successfully")
	}

	// 循环补齐到所需数量
	allClips := []string{}
	for len(allClips) < totalNeeded {
		allClips = append(allClips, processedClips...)
	}
	allClips = allClips[:totalNeeded]

	return concatClips(allClips, outputPath)
}

// processOneClip 处理单个片段：resize + 裁剪到指定时长
func processOneClip(input, output string, width, height, duration int, params *models.VideoParams) error {
	fitMode := params.VideoFitMode
	if fitMode == "" {
		fitMode = "cover"
	}

	var vf string
	if fitMode == "cover" {
		vf = fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d:(iw-%d)/2:(ih-%d)/2",
			width, height, width, height, width, height)
	} else {
		vf = fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(%d-iw)/2:(%d-ih)/2:black",
			width, height, width, height, width, height)
	}

	args := []string{
		"-y", "-i", input,
		"-t", fmt.Sprintf("%d", duration),
		"-vf", vf,
		"-r", "30",
		"-c:v", "libx264", "-preset", "fast", "-crf", "23",
		"-pix_fmt", "yuv420p",
		"-an",
		"-shortest",
		output,
	}

	cmd := exec.Command("ffmpeg", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg process clip: %w, output: %s", err, string(out))
	}
	return nil
}

// concatClips 用 concat demuxer 拼接
func concatClips(clips []string, output string) error {
	listFile := filepath.Join(filepath.Dir(output), "concat_list.txt")
	var lines []string
	for _, c := range clips {
		abs, _ := filepath.Abs(c)
		// 转义单引号
		abs = strings.ReplaceAll(abs, "'", "\\'")
		lines = append(lines, fmt.Sprintf("file '%s'", abs))
	}
	os.WriteFile(listFile, []byte(strings.Join(lines, "\n")), 0644)
	defer os.Remove(listFile)

	utils.LogInfo("concatenating %d clips with ffmpeg...", len(clips))

	cmd := exec.Command("ffmpeg", "-y", "-f", "concat", "-safe", "0", "-i", listFile,
		"-c", "copy", output)
	out, err := cmd.CombinedOutput()
	if err != nil {
		utils.LogWarn("concat copy failed, retrying with re-encode: %s", string(out))
		cmd = exec.Command("ffmpeg", "-y", "-f", "concat", "-safe", "0", "-i", listFile,
			"-c:v", "libx264", "-preset", "fast", "-crf", "23",
			"-pix_fmt", "yuv420p", "-an", output)
		out, err = cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("ffmpeg concat: %w, output: %s", err, string(out))
		}
	}

	utils.LogInfo("concatenation done: %s", output)
	return nil
}

// FinalCompose 最终合成：视频 + 音频 + 字幕 + 背景音乐
func FinalCompose(videoPath, audioPath, assPath, srtPath, outputPath string, params *models.VideoParams, cfg *config.Config) error {
	utils.LogInfo("final composition: video=%s, audio=%s, subtitle=%s", videoPath, audioPath, assPath)

	args := []string{"-y", "-i", videoPath, "-i", audioPath}

	bgmPath := ""
	hasBGM := false
	if params.BGMType != "none" && params.BGMType != "" {
		bgmPath = selectBGM(cfg)
		if bgmPath != "" {
			args = append(args, "-i", bgmPath)
			hasBGM = true
		}
	}

	// 视频滤镜（字幕烧录）
	vf := ""
	if params.SubtitleEnabled && assPath != "" {
		if _, err := os.Stat(assPath); err == nil {
			escapedAss := strings.ReplaceAll(assPath, "\\", "/")
			escapedAss = strings.ReplaceAll(escapedAss, ":", "\\:")
			vf = fmt.Sprintf("ass='%s'", escapedAss)
		}
	}

	// 音频滤镜
	af := ""
	if hasBGM {
		bgmVol := params.BGMVolume
		if bgmVol <= 0 {
			bgmVol = cfg.BGMVolume
		}
		if bgmVol <= 0 {
			bgmVol = 0.2
		}
		af = fmt.Sprintf("[1:a]volume=1.0[a1];[2:a]volume=%.2f,aloop=loop=-1:size=2e9[a2];[a1][a2]amix=inputs=2:duration=first:dropout_transition=0[aout]", bgmVol)
	}

	if vf != "" {
		args = append(args, "-vf", vf)
	}
	if hasBGM {
		args = append(args, "-filter_complex", af, "-map", "0:v", "-map", "[aout]")
	} else {
		args = append(args, "-map", "0:v", "-map", "1:a")
	}

	args = append(args,
		"-c:v", "libx264", "-preset", "medium", "-crf", "20",
		"-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "192k", "-ar", "44100", "-ac", "2",
		"-shortest",
		outputPath,
	)

	utils.LogInfo("running ffmpeg final compose...")
	cmd := exec.Command("ffmpeg", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg final compose: %w, output: %s", err, string(out))
	}

	info, err := os.Stat(outputPath)
	if err != nil {
		return fmt.Errorf("output file not found: %w", err)
	}
	utils.LogInfo("final video: %s (%.1f MB)", outputPath, float64(info.Size())/1024/1024)

	return nil
}

// getResolution 根据宽高比获取分辨率
func getResolution(aspect string) (int, int) {
	switch aspect {
	case "9:16":
		return 1080, 1920
	case "1:1":
		return 1080, 1080
	default:
		return 1920, 1080
	}
}

// selectBGM 选择背景音乐
func selectBGM(cfg *config.Config) string {
	songsDir := filepath.Join(cfg.ResourceDir, "songs")
	entries, err := os.ReadDir(songsDir)
	if err != nil || len(entries) == 0 {
		utils.LogWarn("no BGM found in %s", songsDir)
		return ""
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".mp3") || strings.HasSuffix(name, ".m4a") || strings.HasSuffix(name, ".wav") {
			return filepath.Join(songsDir, name)
		}
	}
	return ""
}
