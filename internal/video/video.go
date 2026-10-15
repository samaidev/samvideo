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

	// 变速：speed > 1 加速（素材变短），speed < 1 减速（素材变长）
	clipSpeed := params.VideoClipSpeed
	if clipSpeed <= 0 {
		clipSpeed = 1.0
	}
	if clipSpeed < 0.5 {
		clipSpeed = 0.5
	}
	if clipSpeed > 2.0 {
		clipSpeed = 2.0
	}

	// 变速后每个片段的实际输出时长
	effectiveClipDuration := float64(clipDuration)
	if clipSpeed != 1.0 {
		effectiveClipDuration = float64(clipDuration) / clipSpeed
	}

	totalNeeded := int(audioDuration/effectiveClipDuration) + 1
	utils.LogInfo("clips: %d source, need %d clips for %.1fs audio (clip dur=%ds, speed=%.2f, effective=%.1fs)",
		len(clipPaths), totalNeeded, audioDuration, clipDuration, clipSpeed, effectiveClipDuration)

	taskDir := filepath.Dir(outputPath)
	processedClips := []string{}
	for i, clip := range clipPaths {
		processed := filepath.Join(taskDir, fmt.Sprintf("processed_%03d.mp4", i))
		err := processOneClip(clip, processed, width, height, clipDuration, params, i, clipSpeed)
		if err != nil {
			utils.LogWarn("failed to process clip %d (%s): %v, skipping", i, clip, err)
			continue
		}
		processedClips = append(processedClips, processed)
		utils.LogInfo("processed clip %d: %s -> %s (speed=%.2f)", i, clip, processed, clipSpeed)
	}

	if len(processedClips) == 0 {
		return fmt.Errorf("no clips processed successfully")
	}

	allClips := []string{}
	for len(allClips) < totalNeeded {
		allClips = append(allClips, processedClips...)
	}
	allClips = allClips[:totalNeeded]

	return concatClips(allClips, outputPath)
}

// processOneClip 处理单个片段：resize + 裁剪到指定时长 + 添加转场效果 + 变速
func processOneClip(input, output string, width, height, duration int, params *models.VideoParams, clipIndex int, speed float64) error {
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

	// 变速：setpts=PTS/speed 加速，setpts=PTS*speed 减速
	if speed != 1.0 {
		vf = vf + fmt.Sprintf(",setpts=PTS/%.4f", speed)
	}

	// 转场效果
	transitionMode := params.VideoTransitionMode
	if transitionMode == "" {
		transitionMode = "shuffle"
	}
	transition := selectTransition(transitionMode, clipIndex)
	if transition != "" {
		vf = vf + "," + transition
	}

	// 变速后输出时长 = 原时长 / speed
	outputDuration := float64(duration)
	if speed != 1.0 {
		outputDuration = float64(duration) / speed
	}

	args := []string{
		"-y", "-i", input,
		"-t", fmt.Sprintf("%.3f", outputDuration),
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

// selectTransition 根据转场模式和片段索引选择转场效果
func selectTransition(mode string, clipIndex int) string {
	transitions := []string{
		"fade=t=in:st=0:d=0.5",
		"fade=t=out:st=4.5:d=0.5",
		"fade=t=in:st=0:d=0.4,fade=t=out:st=4.6:d=0.4",
		"fade=t=in:st=0:d=0.5:color=white",
		"fade=t=in:st=0:d=0.8",
		"fade=t=out:st=4.2:d=0.8:color=white",
		"fade=t=in:st=0:d=0.3:color=white,fade=t=out:st=4.7:d=0.3:color=white",
		"",
	}

	switch mode {
	case "none":
		return ""
	case "fade_in":
		return "fade=t=in:st=0:d=0.5"
	case "fade_out":
		return "fade=t=out:st=4.5:d=0.5"
	case "shuffle":
		return transitions[clipIndex%len(transitions)]
	default:
		return transitions[clipIndex%len(transitions)]
	}
}

// concatClips 用 concat demuxer 拼接
func concatClips(clips []string, output string) error {
	listFile := filepath.Join(filepath.Dir(output), "concat_list.txt")
	var lines []string
	for _, c := range clips {
		abs, _ := filepath.Abs(c)
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

// hasSubtitlesFilter 检测 ffmpeg 是否支持 subtitles/ass 滤镜
func hasSubtitlesFilter() bool {
	cmd := exec.Command("ffmpeg", "-hide_banner", "-filters")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	output := string(out)
	return strings.Contains(output, " subtitles") || strings.Contains(output, "... subtitles") ||
		strings.Contains(output, " ass ") || strings.Contains(output, "... ass")
}

// FinalCompose 最终合成：视频 + 音频 + 字幕 + 背景音乐
func FinalCompose(videoPath, audioPath, assPath, srtPath, outputPath string, params *models.VideoParams, cfg *config.Config) error {
	utils.LogInfo("final composition: video=%s, audio=%s, subtitle=%s", videoPath, audioPath, assPath)

	args := []string{"-y", "-i", videoPath, "-i", audioPath}

	// 背景音乐
	bgmPath := ""
	hasBGM := false
	if params.BGMType != "none" && params.BGMType != "" {
		bgmPath = selectBGM(cfg, params)
		if bgmPath != "" {
			args = append(args, "-i", bgmPath)
			hasBGM = true
			utils.LogInfo("BGM selected: %s", bgmPath)
		} else {
			utils.LogWarn("BGM requested (%s) but no BGM file found, continuing without BGM", params.BGMType)
		}
	}

	// 字幕烧录
	canBurnSubs := false
	if params.SubtitleEnabled && assPath != "" {
		if _, err := os.Stat(assPath); err == nil {
			if hasSubtitlesFilter() {
				canBurnSubs = true
			} else {
				utils.LogWarn("ffmpeg does not support subtitles/ass filter, skipping subtitle burn-in (SRT file is still available: %s)", srtPath)
			}
		}
	}

	if canBurnSubs {
		escapedAss := strings.ReplaceAll(assPath, "\\", "/")
		escapedAss = strings.ReplaceAll(escapedAss, ":", "\\:")
		args = append(args, "-vf", fmt.Sprintf("subtitles=%s", escapedAss))
	}

	// 音频处理
	if hasBGM {
		bgmVol := params.BGMVolume
		if bgmVol <= 0 {
			bgmVol = cfg.BGMVolume
		}
		if bgmVol <= 0 {
			bgmVol = 0.2
		}
		// 关键修复：
		// 1. 用 weights=1 1 避免 amix 默认除以输入数（否则 BGM 实际音量 = bgmVol / 2）
		// 2. BGM 先做 volume 再 aloop，确保循环后音量一致
		// 3. 旁白保持原音量 1.0
		af := fmt.Sprintf(
			"[1:a]volume=1.0[a1];"+
				"[2:a]aloop=loop=-1:size=2e9,volume=%.2f[a2];"+
				"[a1][a2]amix=inputs=2:duration=first:dropout_transition=0:weights=1 1[aout]",
			bgmVol,
		)
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
	utils.LogInfo("ffmpeg args: %v", args)
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
func selectBGM(cfg *config.Config, params *models.VideoParams) string {
	// 优先使用用户指定的 BGM 文件
	if params.BGMFile != "" {
		if _, err := os.Stat(params.BGMFile); err == nil {
			return params.BGMFile
		}
		utils.LogWarn("specified BGM file not found: %s", params.BGMFile)
	}

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
