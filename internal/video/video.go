package video

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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
	transition := selectTransition(transitionMode, clipIndex, float64(duration))
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
// duration 为当前片段时长，fade out 的 st 根据时长动态计算，避免提前淡出导致黑屏
func selectTransition(mode string, clipIndex int, duration float64) string {
	if duration < 2.0 {
		return "" // 太短的片段不加转场
	}
	fadeOutStart := duration - 0.5 // 淡出从最后0.5秒开始
	fadeOutStartLong := duration - 0.8
	fadeOutStartShort := duration - 0.3

	switch mode {
	case "none":
		return ""
	case "fade_in":
		return "fade=t=in:st=0:d=0.5"
	case "fade_out":
		return fmt.Sprintf("fade=t=out:st=%.1f:d=0.5", fadeOutStart)
	case "shuffle":
		transitions := []string{
			"fade=t=in:st=0:d=0.5",
			fmt.Sprintf("fade=t=out:st=%.1f:d=0.5", fadeOutStart),
			fmt.Sprintf("fade=t=in:st=0:d=0.4,fade=t=out:st=%.1f:d=0.4", fadeOutStartShort),
			"fade=t=in:st=0:d=0.5:color=white",
			"fade=t=in:st=0:d=0.8",
			fmt.Sprintf("fade=t=out:st=%.1f:d=0.8:color=white", fadeOutStartLong),
			fmt.Sprintf("fade=t=in:st=0:d=0.3:color=white,fade=t=out:st=%.1f:d=0.3:color=white", fadeOutStartShort),
			"",
		}
		return transitions[clipIndex%len(transitions)]
	default:
		return ""
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

	// 1. 优先从 downloaded/ 目录随机选择
	downloadedDir := filepath.Join(cfg.ResourceDir, "songs", "downloaded")
	var bgmFiles []string
	if entries, err := os.ReadDir(downloadedDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if isAudioExt(name) {
				bgmFiles = append(bgmFiles, filepath.Join(downloadedDir, name))
			}
		}
	}

	// 2. 回退到 songs/ 根目录
	if len(bgmFiles) == 0 {
		songsDir := filepath.Join(cfg.ResourceDir, "songs")
		if entries, err := os.ReadDir(songsDir); err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				name := entry.Name()
				if isAudioExt(name) {
					bgmFiles = append(bgmFiles, filepath.Join(songsDir, name))
				}
			}
		}
	}

	if len(bgmFiles) == 0 {
		utils.LogWarn("no BGM found in %s/songs/downloaded/", cfg.ResourceDir)
		return ""
	}

	// 随机选择
	idx := rand.Intn(len(bgmFiles))
	utils.LogInfo("BGM selected: %s (from %d files)", filepath.Base(bgmFiles[idx]), len(bgmFiles))
	return bgmFiles[idx]
}

func isAudioExt(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".mp3" || ext == ".m4a" || ext == ".wav" || ext == ".ogg" || ext == ".flac"
}

// SelectBGM is a standalone BGM selector (for digital human mode)
func SelectBGM(cfg *config.Config) string {
	params := &models.VideoParams{BGMType: cfg.BGMType}
	return selectBGM(cfg, params)
}

// MixBGM mixes background music into a video
func MixBGM(videoPath, bgmPath, outputPath string, bgmVolume float64) error {
	if bgmVolume <= 0 {
		bgmVolume = 0.55
	}
	// Get video duration
	durCmd := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", videoPath)
	durOut, err := durCmd.Output()
	if err != nil {
		return fmt.Errorf("get video duration: %w", err)
	}
	duration := strings.TrimSpace(string(durOut))

	// Mix: original audio + BGM (BGM looped and trimmed to video duration)
	cmd := exec.Command("ffmpeg", "-y",
		"-i", videoPath,
		"-i", bgmPath,
		"-filter_complex",
		fmt.Sprintf("[1:a]aloop=loop=-1:size=2e9,atrim=0:%s,volume=%.2f[bgm];[0:a]volume=1.0[orig];[orig][bgm]amix=inputs=2:duration=first:dropout_transition=0[aout]",
			duration, bgmVolume),
		"-map", "0:v",
		"-map", "[aout]",
		"-c:v", "copy",
		"-c:a", "aac",
		"-shortest",
		outputPath,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg mix BGM: %w, output: %s", err, string(output))
	}
	return nil
}

// ReplaceAudio replaces the audio track of a video with a new audio file
func ReplaceAudio(videoPath, audioPath, outputPath string) error {
	cmd := exec.Command("ffmpeg", "-y",
		"-i", videoPath,
		"-i", audioPath,
		"-map", "0:v",
		"-map", "1:a",
		"-c:v", "copy",
		"-c:a", "aac",
		"-shortest",
		outputPath,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg replace audio: %w, output: %s", err, string(output))
	}
	return nil
}

// ConcatenateSegments concatenates multiple video segments into one
func ConcatenateSegments(segmentPaths []string, outputPath string) error {
	if len(segmentPaths) == 0 {
		return fmt.Errorf("no segments to concatenate")
	}
	if len(segmentPaths) == 1 {
		// Just copy the single segment
		data, err := os.ReadFile(segmentPaths[0])
		if err != nil {
			return err
		}
		return os.WriteFile(outputPath, data, 0644)
	}

	// Create concat list file
	listFile := outputPath + ".concat.txt"
	var lines []string
	for _, p := range segmentPaths {
		abs, _ := filepath.Abs(p)
		lines = append(lines, fmt.Sprintf("file '%s'", abs))
	}
	os.WriteFile(listFile, []byte(strings.Join(lines, "\n")), 0644)
	defer os.Remove(listFile)

	cmd := exec.Command("ffmpeg", "-y",
		"-f", "concat",
		"-safe", "0",
		"-i", listFile,
		"-c", "copy",
		outputPath,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg concat: %w, output: %s", err, string(output))
	}
	return nil
}

// ConcatenateClipsWithDurations 按指定时长拼接视频片段
// 每个 clip 对应一个 duration，片段按顺序拼接
func ConcatenateClipsWithDurations(clipPaths []string, durations []float64, outputPath string, params *models.VideoParams) error {
	if len(clipPaths) == 0 {
		return fmt.Errorf("no clips to concatenate")
	}

	width, height := getResolution(params.VideoAspect)
	taskDir := filepath.Dir(outputPath)

	// 逐个处理：resize + 裁剪到对应时长 + 转场
	var processedClips []string
	for i, clip := range clipPaths {
		dur := 5.0
		if i < len(durations) {
			dur = durations[i]
		}
		if dur < 1.0 {
			dur = 1.0
		}

		processed := filepath.Join(taskDir, fmt.Sprintf("aligned_%03d.mp4", i))
		err := processClipWithDuration(clip, processed, width, height, dur, params, i)
		if err != nil {
			utils.LogWarn("failed to process clip %d: %v, skipping", i, err)
			continue
		}
		processedClips = append(processedClips, processed)
		utils.LogInfo("aligned clip %d: dur=%.1fs -> %s", i, dur, processed)
	}

	if len(processedClips) == 0 {
		return fmt.Errorf("no clips processed successfully")
	}

	return concatClips(processedClips, outputPath)
}

// processClipWithDuration 处理单个片段到指定时长
func processClipWithDuration(input, output string, width, height int, duration float64, params *models.VideoParams, clipIndex int) error {
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

	// 转场效果
	transitionMode := params.VideoTransitionMode
	if transitionMode == "" {
		transitionMode = "shuffle"
	}
	transition := selectTransition(transitionMode, clipIndex, duration)
	if transition != "" {
		vf = vf + "," + transition
	}

	args := []string{
		"-y", "-i", input,
		"-t", fmt.Sprintf("%.3f", duration),
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

// ConcatenateAudioFiles 拼接多个音频文件
func ConcatenateAudioFiles(audioPaths []string, outputPath string) error {
	if len(audioPaths) == 0 {
		return fmt.Errorf("no audio files to concatenate")
	}
	if len(audioPaths) == 1 {
		data, err := os.ReadFile(audioPaths[0])
		if err != nil {
			return err
		}
		return os.WriteFile(outputPath, data, 0644)
	}

	listFile := outputPath + ".concat.txt"
	var lines []string
	for _, p := range audioPaths {
		abs, _ := filepath.Abs(p)
		abs = strings.ReplaceAll(abs, "'", "\\'")
		lines = append(lines, fmt.Sprintf("file '%s'", abs))
	}
	os.WriteFile(listFile, []byte(strings.Join(lines, "\n")), 0644)
	defer os.Remove(listFile)

	cmd := exec.Command("ffmpeg", "-y",
		"-f", "concat", "-safe", "0",
		"-i", listFile,
		"-c", "copy",
		outputPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// re-encode fallback
		cmd = exec.Command("ffmpeg", "-y",
			"-f", "concat", "-safe", "0",
			"-i", listFile,
			"-c:a", "libmp3lame", "-b:a", "192k",
			outputPath,
		)
		out, err = cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("ffmpeg concat audio: %w, output: %s", err, string(out))
		}
	}
	return nil
}

// ApplyOverlayToImage 用 Python PIL 直接在 PNG 图片上叠加 overlay（后期制作）
func ApplyOverlayToImage(imagePath, outputPath string, overlayItems []OverlayItem) error {
	if len(overlayItems) == 0 {
		return nil
	}
	overlayJSON, _ := json.Marshal(overlayItems)
	scriptPath := "scripts/overlay_image.py"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3.14", scriptPath, imagePath, outputPath, string(overlayJSON))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("PIL overlay: %w, output: %s", err, string(out))
	}
	return nil
}

// ApplyOverlay 在视频片段上叠加半透明遮罩层、高亮文字、箭头等
// 使用 Python PIL 在图片上画 overlay，然后重新生成视频片段
func ApplyOverlay(inputPath, outputPath string, overlayItems []OverlayItem) error {
	if len(overlayItems) == 0 {
		data, err := os.ReadFile(inputPath)
		if err != nil {
			return err
		}
		return os.WriteFile(outputPath, data, 0644)
	}

	// 1. 从视频片段提取第一帧作为图片
	tmpFrame := outputPath + "_frame.png"
	extractCtx, extractCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer extractCancel()
	extractCmd := exec.CommandContext(extractCtx, "ffmpeg", "-y", "-i", inputPath, "-frames:v", "1", "-q:v", "2", tmpFrame)
	if out, err := extractCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("extract frame: %w, output: %s", err, string(out))
	}
	defer os.Remove(tmpFrame)

	// 2. 用 Python PIL 在图片上画 overlay
	overlayedFrame := outputPath + "_overlayed.png"
	overlayJSON, _ := json.Marshal(overlayItems)
	scriptPath := filepath.Join(filepath.Dir(os.Args[0]), "scripts", "overlay_image.py")
	if _, err := os.Stat(scriptPath); err != nil {
		// 尝试相对路径
		scriptPath = "scripts/overlay_image.py"
	}
	pilCtx, pilCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer pilCancel()
	pilCmd := exec.CommandContext(pilCtx, "python3.14", scriptPath, tmpFrame, overlayedFrame, string(overlayJSON))
	if out, err := pilCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("PIL overlay: %w, output: %s", err, string(out))
	}
	defer os.Remove(overlayedFrame)

	// 3. 获取原视频时长
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer probeCancel()
	probeCmd := exec.CommandContext(probeCtx, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", inputPath)
	durationBytes, _ := probeCmd.Output()
	duration := strings.TrimSpace(string(durationBytes))

	// 4. 用 overlay 图片重新生成视频片段（保持原时长）
	args := []string{
		"-y", "-loop", "1", "-i", overlayedFrame,
		"-t", duration,
		"-c:v", "libx264", "-preset", "fast", "-crf", "23",
		"-pix_fmt", "yuv420p",
		"-vf", "scale=1920:1080",
		"-an",
		outputPath,
	}
	encCtx, encCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer encCancel()
	encCmd := exec.CommandContext(encCtx, "ffmpeg", args...)
	if out, err := encCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("re-encode with overlay: %w, output: %s", err, string(out))
	}
	return nil
}

// OverlayItem 叠加层元素（video 包用）
type OverlayItem struct {
	Text     string `json:"text"`
	Position string `json:"position"`
	Color    string `json:"color"`
	Style    string `json:"style"`
}


