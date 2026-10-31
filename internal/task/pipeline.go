package task

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"samvideo/internal/config"
	"samvideo/internal/llm"
	"samvideo/internal/material"
	"samvideo/internal/models"
	"samvideo/internal/subtitle"
	"samvideo/internal/template"
	"samvideo/internal/tts"
	"samvideo/internal/utils"
	"samvideo/internal/video"
	"samvideo/internal/bgm"
)

// Pipeline 任务编排器
type Pipeline struct {
	cfg     *config.Config
	mu      sync.Mutex
	cancels map[string]chan struct{} // taskID -> cancel channel
}

func NewPipeline(cfg *config.Config) *Pipeline {
	return &Pipeline{cfg: cfg, cancels: make(map[string]chan struct{})}
}

// CancelTask 取消正在运行的任务
func (p *Pipeline) CancelTask(taskID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ch, ok := p.cancels[taskID]; ok {
		close(ch)
		delete(p.cancels, taskID)
		return true
	}
	return false
}

// isCancelled 检查任务是否被取消
func (p *Pipeline) isCancelled(taskID string) bool {
	p.mu.Lock()
	ch, ok := p.cancels[taskID]
	p.mu.Unlock()
	if !ok {
		return true // 不在 map 里说明已取消或已完成
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// startCancel 注册任务可取消
func (p *Pipeline) startCancel(taskID string) {
	p.mu.Lock()
	p.cancels[taskID] = make(chan struct{})
	p.mu.Unlock()
}

// finishCancel 任务完成时清理
func (p *Pipeline) finishCancel(taskID string) {
	p.mu.Lock()
	delete(p.cancels, taskID)
	p.mu.Unlock()
}

// CleanupStaleTasks 清理上次未完成的任务（服务重启后 goroutine 已死，标记为 failed）
func CleanupStaleTasks(cfg *config.Config) {
	tasksDir := filepath.Join(cfg.StorageDir, "tasks")
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return
	}
	cleaned := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		taskFile := filepath.Join(tasksDir, entry.Name(), "task.json")
		data, err := os.ReadFile(taskFile)
		if err != nil {
			continue
		}
		var result models.TaskResult
		if err := json.Unmarshal(data, &result); err != nil {
			continue
		}
		if result.State == models.TaskStateProcessing {
			result.State = models.TaskStateFailed
			result.Error = "服务重启，任务中断"
			result.EndTime = time.Now()
			updated, _ := json.MarshalIndent(result, "", "  ")
			os.WriteFile(taskFile, updated, 0644)
			cleaned++
		}
	}
	if cleaned > 0 {
		utils.LogInfo("cleaned up %d stale tasks (marked as failed)", cleaned)
	}
}

func (p *Pipeline) RunTask(params *models.VideoParams) (*models.TaskResult, error) {
	taskID := generateTaskID()
	return p.RunTaskWithID(taskID, params)
}

func (p *Pipeline) RunTaskWithID(taskID string, params *models.VideoParams) (*models.TaskResult, error) {
	p.startCancel(taskID)
	defer p.finishCancel(taskID)
	taskDir := filepath.Join(p.cfg.StorageDir, "tasks", taskID)
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		return nil, fmt.Errorf("create task dir: %w", err)
	}

	result := &models.TaskResult{
		TaskID:    taskID,
		State:     models.TaskStateProcessing,
		Progress:  5,
		Params:    params,
		StartTime: time.Now(),
	}

	p.saveTask(taskDir, result)

	save := func(progress int) {
		result.Progress = progress
		p.saveTask(taskDir, result)
	}

	// ====== 数字人口播模式 ======
	if params.DigitalHumanEnabled {
		return p.runDigitalHuman(taskID, taskDir, params, result, save)
	}

	// ====== 普通视频生成模式（逐段对齐）======

	// Step 0: 模板分类
	var tmpl *template.Template
	if params.VideoAspect == "" || params.VideoTransitionMode == "" {
		utils.LogInfo("step 0: auto classifying topic and selecting template...")
		tmpl, _ = template.ClassifyTopic(params.VideoSubject, p.cfg)
		if tmpl != nil {
			utils.LogInfo("template selected: %s (%s, %s)", tmpl.Name, tmpl.Category, tmpl.Aspect)
			template.ApplyTemplate(tmpl, params)
			result.Params = params
			p.saveTask(taskDir, result)
		}
	}

	// Step 1: LLM 分镜脚本生成
	utils.LogInfo("step 1: generating scene-based script via LLM...")
	script, imagePrompts, scenes, err := p.generateScriptWithScenes(params, tmpl)
	if err != nil {
		result.State = models.TaskStateFailed
		result.Error = fmt.Sprintf("script generation failed: %v", err)
		result.EndTime = time.Now()
		p.saveTask(taskDir, result)
		return result, err
	}
	result.Script = script
	result.SearchTerms = imagePrompts
	save(10)

	scriptData, _ := json.MarshalIndent(map[string]interface{}{
		"script":        script,
		"image_prompts": imagePrompts,
		"scenes":        scenes,
		"params":        params,
		"template":      tmpl,
	}, "", "  ")
	os.WriteFile(filepath.Join(taskDir, "script.json"), scriptData, 0644)

	// ====== 逐段对齐模式（有分镜数据时）======
	if len(scenes) > 0 {
		return p.runSceneAlignedPipeline(taskID, taskDir, params, result, save, scenes, tmpl)
	}

	// ====== 旧模式降级（无分镜数据时）======
	utils.LogInfo("no scene data, falling back to legacy pipeline...")

	// Step 2: TTS
	utils.LogInfo("step 2: generating TTS audio...")
	audioPath := filepath.Join(taskDir, "audio.mp3")
	wordBoundaries, err := tts.Synthesize(script, params.VoiceName, params.VoiceRate, audioPath, p.cfg)
	if err != nil {
		result.State = models.TaskStateFailed
		result.Error = fmt.Sprintf("TTS failed: %v", err)
		result.EndTime = time.Now()
		p.saveTask(taskDir, result)
		return result, err
	}
	save(20)

	// Step 3: 字幕
	utils.LogInfo("step 3: generating subtitle...")
	srtPath := filepath.Join(taskDir, "subtitle.srt")
	assPath := filepath.Join(taskDir, "subtitle.ass")
	if params.SubtitleEnabled {
		err = subtitle.GenerateFromWordBoundaries(wordBoundaries, srtPath, assPath, params)
		if err != nil {
			utils.LogWarn("subtitle generation failed: %v, continuing without subtitle", err)
		}
	}
	save(30)

	// Step 4: 素材
	utils.LogInfo("step 4: fetching video materials...")
	clipPaths, err := p.fetchMaterialsScene(imagePrompts, taskDir, params)
	if err != nil {
		result.State = models.TaskStateFailed
		result.Error = fmt.Sprintf("material fetch failed: %v", err)
		result.EndTime = time.Now()
		p.saveTask(taskDir, result)
		return result, err
	}
	save(50)

	// Step 5: 拼接
	utils.LogInfo("step 5: processing and concatenating clips...")
	audioDuration := p.getAudioDuration(audioPath)
	combinedPath := filepath.Join(taskDir, "combined.mp4")
	err = video.ProcessAndConcatenate(clipPaths, combinedPath, audioDuration, params, p.cfg)
	if err != nil {
		result.State = models.TaskStateFailed
		result.Error = fmt.Sprintf("video concatenation failed: %v", err)
		result.EndTime = time.Now()
		p.saveTask(taskDir, result)
		return result, err
	}
	save(70)

	// Step 5.5: BGM
	if params.BGMType != "none" && params.BGMType != "" {
		utils.LogInfo("step 5.5: ensuring BGM files...")
		_, bgmErr := bgm.EnsureBGM(p.cfg)
		if bgmErr != nil {
			utils.LogWarn("BGM generation failed: %v, continuing without BGM", bgmErr)
		}
	}

	// Step 6: 最终合成
	utils.LogInfo("step 6: final composition...")
	finalPath := filepath.Join(taskDir, "final.mp4")
	err = video.FinalCompose(combinedPath, audioPath, assPath, srtPath, finalPath, params, p.cfg)
	if err != nil {
		result.State = models.TaskStateFailed
		result.Error = fmt.Sprintf("final composition failed: %v", err)
		result.EndTime = time.Now()
		p.saveTask(taskDir, result)
		return result, err
	}
	save(90)

	// Step 7: 缩略图
	thumbPath := filepath.Join(taskDir, "thumbnail.jpg")
	if err := GenerateThumbnail(finalPath, thumbPath); err != nil {
		utils.LogWarn("thumbnail generation failed: %v", err)
	} else {
		result.ThumbnailURL = fmt.Sprintf("/api/v1/download/%s/thumbnail.jpg", taskID)
	}

	// Step 8: 完成
	result.State = models.TaskStateComplete
	result.Progress = 100
	result.VideoPath = finalPath
	result.VideoURL = fmt.Sprintf("/api/v1/download/%s/final.mp4", taskID)
	result.EndTime = time.Now()
	p.saveTask(taskDir, result)

	utils.LogInfo("task %s completed: %s", taskID, finalPath)
	return result, nil
}

// runSceneAlignedPipeline 逐段对齐流水线
// 每段旁白单独做TTS得到时长，图片视频片段时长=对应语音时长，图文在时间线上完美对齐
func (p *Pipeline) runSceneAlignedPipeline(taskID, taskDir string, params *models.VideoParams, result *models.TaskResult, save func(int), scenes []llm.Scene, tmpl *template.Template) (*models.TaskResult, error) {
	utils.LogInfo("=== Scene-Aligned Pipeline: %d scenes ===", len(scenes))

	// Step 2: 逐段 TTS——每段旁白单独合成语音，得到精确时长
	utils.LogInfo("step 2: per-scene TTS synthesis...")
	sceneNarrations := make([]tts.SceneNarration, len(scenes))
	for i, s := range scenes {
		sceneNarrations[i] = tts.SceneNarration{Narration: s.Narration}
	}
	sceneAudios, err := tts.SynthesizeScenes(sceneNarrations, params.VoiceName, params.VoiceRate, taskDir, p.cfg)
	if err != nil {
		result.State = models.TaskStateFailed
		result.Error = fmt.Sprintf("per-scene TTS failed: %v", err)
		result.EndTime = time.Now()
		p.saveTask(taskDir, result)
		return result, err
	}

	// 拼接所有段语音为完整音频
	audioPath := filepath.Join(taskDir, "audio.mp3")
	var audioPaths []string
	for _, sa := range sceneAudios {
		audioPaths = append(audioPaths, sa.AudioPath)
	}
	if err := video.ConcatenateAudioFiles(audioPaths, audioPath); err != nil {
		result.State = models.TaskStateFailed
		result.Error = fmt.Sprintf("audio concatenation failed: %v", err)
		result.EndTime = time.Now()
		p.saveTask(taskDir, result)
		return result, err
	}

	// 计算每段的累计时间偏移（用于字幕）
	var subEntries []subtitle.SceneSubtitleEntry
	cumulativeOffset := 0.0
	for i, sa := range sceneAudios {
		subEntries = append(subEntries, subtitle.SceneSubtitleEntry{
			Text:  sa.Narration,
			Start: cumulativeOffset,
			End:   cumulativeOffset + sa.Duration,
		})
		cumulativeOffset += sa.Duration
		utils.LogInfo("scene %d: dur=%.1fs, offset=%.1f-%.1fs, text=%s",
			i+1, sa.Duration, subEntries[i].Start, subEntries[i].End, truncateText(sa.Narration, 30))
	}
	save(25)

	// Step 3: 字幕
	utils.LogInfo("step 3: generating scene-based subtitle...")
	srtPath := filepath.Join(taskDir, "subtitle.srt")
	assPath := filepath.Join(taskDir, "subtitle.ass")
	if params.SubtitleEnabled {
		err = subtitle.GenerateFromScenes(subEntries, srtPath, assPath, params)
		if err != nil {
			utils.LogWarn("subtitle generation failed: %v, continuing without subtitle", err)
		}
	}
	save(30)

	if p.isCancelled(taskID) {
		return nil, fmt.Errorf("任务已取消")
	}
	// Step 4: 逐段生成图片——用 scene 的 image_prompt 生成图片
	utils.LogInfo("step 4: generating images for each scene...")
	var imgPaths []string
	for i, scene := range scenes {
		imgPath, err := material.GenerateImageForScene(scene.ImagePrompt, taskDir, i, p.cfg)
		if err != nil {
			utils.LogWarn("failed to generate image for scene %d: %v, skipping", i+1, err)
			continue
		}
		imgPaths = append(imgPaths, imgPath)
	}
	if len(imgPaths) == 0 {
		result.State = models.TaskStateFailed
		result.Error = "no images generated"
		result.EndTime = time.Now()
		p.saveTask(taskDir, result)
		return result, err
	}
	save(50)

	if p.isCancelled(taskID) {
		return nil, fmt.Errorf("任务已取消")
	}
	// Step 4.5: 后期制作——为每张图片生成并叠加 overlay
	if params.VideoRequirement != "" {
		utils.LogInfo("step 4.5: post-production overlay generation...")
		for i, imgPath := range imgPaths {
			sceneIdx := i
			if sceneIdx >= len(scenes) {
				sceneIdx = len(scenes) - 1
			}
			narration := scenes[sceneIdx].Narration
			overlayItems, err := llm.GenerateOverlayForScene(narration, params.VideoRequirement, i, p.cfg)
			if err != nil {
				utils.LogWarn("overlay generation failed for scene %d: %v, using auto fallback", i+1, err)
				overlayItems = llm.AutoGenerateOverlay(narration, params.VideoRequirement)
			}
			if len(overlayItems) == 0 {
				continue
			}
			overlayedPath := imgPath + ".overlay.png"
			voItems := make([]video.OverlayItem, len(overlayItems))
			for j, oi := range overlayItems {
				voItems[j] = video.OverlayItem{
					Text:     oi.Text,
					Position: oi.Position,
					Color:    oi.Color,
					Style:    oi.Style,
				}
			}
			if err := video.ApplyOverlayToImage(imgPath, overlayedPath, voItems); err != nil {
				utils.LogWarn("overlay image failed for scene %d: %v, using original", i+1, err)
			} else {
				imgPaths[i] = overlayedPath
				utils.LogInfo("  scene %d: overlay applied (%d items)", i+1, len(overlayItems))
			}
		}
	}

	// Step 5: 图片转视频片段——每段时长=对应语音时长
	utils.LogInfo("step 5: converting images to video clips with aligned durations...")
	var clipPaths []string
	var clipDurations []float64
	for i, imgPath := range imgPaths {
		// 该段的语音时长
		dur := 5.0
		if i < len(sceneAudios) {
			dur = sceneAudios[i].Duration
		}
		if dur < 1.0 {
			dur = 1.0
		}

		clipPath := filepath.Join(taskDir, fmt.Sprintf("clip_%03d.mp4", i))
		variantIdx := i % 8
		err := material.ImageToVideoClipWithDuration(imgPath, clipPath, dur, params, variantIdx)
		if err != nil {
			utils.LogWarn("failed to convert image %d to clip: %v, skipping", i, err)
			continue
		}
		clipPaths = append(clipPaths, clipPath)
		clipDurations = append(clipDurations, dur)
		utils.LogInfo("clip %d: dur=%.1fs (aligned with scene audio)", i+1, dur)
	}
	if len(clipPaths) == 0 {
		result.State = models.TaskStateFailed
		result.Error = "no clips generated"
		result.EndTime = time.Now()
		p.saveTask(taskDir, result)
		return result, fmt.Errorf("no clips generated")
	}
	save(60)

	// Step 5.5: 叠加 overlay 层（半透明遮罩/高亮文字/箭头）
	if params.VideoRequirement != "" {
		utils.LogInfo("step 5.5: applying overlay layers to clips...")
		var overlayedClips []string
		for i, clipPath := range clipPaths {
			sceneIdx := i
			if sceneIdx >= len(scenes) {
				sceneIdx = len(scenes) - 1
			}
			overlayItems := scenes[sceneIdx].Overlay
			if len(overlayItems) == 0 {
				overlayedClips = append(overlayedClips, clipPath)
				continue
			}
			overlayedPath := filepath.Join(taskDir, fmt.Sprintf("clip_%03d_overlay.mp4", i))
			voItems := make([]video.OverlayItem, len(overlayItems))
			for j, oi := range overlayItems {
				voItems[j] = video.OverlayItem{
					Text:     oi.Text,
					Position: oi.Position,
					Color:    oi.Color,
					Style:    oi.Style,
				}
			}
			if err := video.ApplyOverlay(clipPath, overlayedPath, voItems); err != nil {
				utils.LogWarn("overlay failed for clip %d: %v, using original", i+1, err)
				overlayedClips = append(overlayedClips, clipPath)
			} else {
				overlayedClips = append(overlayedClips, overlayedPath)
			}
		}
		clipPaths = overlayedClips
		utils.LogInfo("overlay applied to %d clips", len(clipPaths))
	}

	if p.isCancelled(taskID) {
		return nil, fmt.Errorf("任务已取消")
	}
	// Step 6: 拼接所有片段
	utils.LogInfo("step 6: concatenating %d aligned clips...", len(clipPaths))
	combinedPath := filepath.Join(taskDir, "combined.mp4")
	err = video.ConcatenateClipsWithDurations(clipPaths, clipDurations, combinedPath, params)
	if err != nil {
		result.State = models.TaskStateFailed
		result.Error = fmt.Sprintf("video concatenation failed: %v", err)
		result.EndTime = time.Now()
		p.saveTask(taskDir, result)
		return result, err
	}
	save(70)

	// Step 6.5: BGM
	if params.BGMType != "none" && params.BGMType != "" {
		utils.LogInfo("step 6.5: ensuring BGM files...")
		_, bgmErr := bgm.EnsureBGM(p.cfg)
		if bgmErr != nil {
			utils.LogWarn("BGM generation failed: %v, continuing without BGM", bgmErr)
		}
	}

	// Step 7: 最终合成
	utils.LogInfo("step 7: final composition...")
	finalPath := filepath.Join(taskDir, "final.mp4")
	
	// 后台更新进度（final compose 耗时较长）
	progressDone := make(chan bool)
	go func() {
		prog := 71
		for {
			select {
			case <-progressDone:
				return
			case <-time.After(15 * time.Second):
				if prog < 89 {
					save(prog)
					prog += 3
				}
			}
		}
	}()
	
	err = video.FinalCompose(combinedPath, audioPath, assPath, srtPath, finalPath, params, p.cfg)
	close(progressDone)
	if err != nil {
		result.State = models.TaskStateFailed
		result.Error = fmt.Sprintf("final composition failed: %v", err)
		result.EndTime = time.Now()
		p.saveTask(taskDir, result)
		return result, err
	}
	save(90)

	// Step 8: 缩略图
	thumbPath := filepath.Join(taskDir, "thumbnail.jpg")
	if err := GenerateThumbnail(finalPath, thumbPath); err != nil {
		utils.LogWarn("thumbnail generation failed: %v", err)
	} else {
		result.ThumbnailURL = fmt.Sprintf("/api/v1/download/%s/thumbnail.jpg", taskID)
	}

	// Step 9: 完成
	result.State = models.TaskStateComplete
	result.Progress = 100
	result.VideoPath = finalPath
	result.VideoURL = fmt.Sprintf("/api/v1/download/%s/final.mp4", taskID)
	result.EndTime = time.Now()
	p.saveTask(taskDir, result)

	utils.LogInfo("scene-aligned task %s completed: %s", taskID, finalPath)
	return result, nil
}

func truncateText(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// generateScriptWithScenes 使用分镜模式生成脚本
// 优先调用 llm.GenerateScriptWithScenes，失败时降级到旧模式
func (p *Pipeline) generateScriptWithScenes(params *models.VideoParams, tmpl *template.Template) (string, []string, []llm.Scene, error) {
	// 如果用户提供了自定义脚本
	if params.VideoScript != "" {
		// 如果前端同时传来了 scenes，转换为 llm.Scene 使用
		if len(params.VideoScenes) > 0 {
			scenes := make([]llm.Scene, len(params.VideoScenes))
			imagePrompts := make([]string, len(params.VideoScenes))
			for i, sd := range params.VideoScenes {
				scenes[i] = llm.Scene{
					Narration:   sd.Narration,
					ImagePrompt: sd.ImagePrompt,
				}
				imagePrompts[i] = sd.ImagePrompt
			}
			utils.LogInfo("using user-provided script with %d scenes", len(scenes))
			return params.VideoScript, imagePrompts, scenes, nil
		}
		// 没有 scenes，用 LLM 把脚本改写成分镜
		utils.LogInfo("user provided script (%d chars) but no scenes, using LLM to split into scenes...", len(params.VideoScript))
		scenes := autoSplitScriptToScenes(params.VideoScript, params.VideoRequirement, p.cfg)
		if len(scenes) > 0 {
			imagePrompts := make([]string, len(scenes))
			for i, s := range scenes {
				imagePrompts[i] = s.ImagePrompt
			}
			utils.LogInfo("LLM split user script into %d scenes", len(scenes))
			return params.VideoScript, imagePrompts, scenes, nil
		}
		// LLM 分段失败，简单按字数切分成6段
		utils.LogWarn("LLM split failed, doing simple split into 6 segments")
		runes := []rune(params.VideoScript)
		totalLen := len(runes)
		segLen := totalLen / 6
		if segLen < 20 {
			segLen = 20
		}
		scenes2 := make([]llm.Scene, 0, 6)
		for i := 0; i < 6 && i*segLen < totalLen; i++ {
			start := i * segLen
			end := start + segLen
			if end > totalLen {
				end = totalLen
			}
			narration := string(runes[start:end])
			// 去掉 markdown 标记
			narration = strings.ReplaceAll(narration, "**", "")
			narration = strings.ReplaceAll(narration, "#", "")
			narration = strings.TrimSpace(narration)
			if len(narration) < 10 {
				continue
			}
			s := llm.Scene{
				Narration:   narration,
				ImagePrompt: "financial data visualization, stock market chart, modern infographic, cinematic",
			}
			s.Overlay = llm.AutoGenerateOverlay(narration, params.VideoRequirement)
			scenes2 = append(scenes2, s)
		}
		if len(scenes2) > 0 {
			imagePrompts := make([]string, len(scenes2))
			for i, s := range scenes2 {
				imagePrompts[i] = s.ImagePrompt
			}
			utils.LogInfo("simple split into %d scenes", len(scenes2))
			return params.VideoScript, imagePrompts, scenes2, nil
		}
		return params.VideoScript, nil, nil, fmt.Errorf("无法处理长文章，请使用AI生成脚本按钮")
	}

	// 确定分镜数量
	sceneCount := 6
	if tmpl != nil && tmpl.ImageCount > 0 {
		sceneCount = tmpl.ImageCount
	}

	// 获取模板的脚本提示词
	scriptPrompt := ""
	if tmpl != nil {
		scriptPrompt = tmpl.GetScriptPrompt()
	}

	// 优先使用前端传入的 video_scenes（带 overlay 数据）
	if len(params.VideoScenes) > 0 {
		utils.LogInfo("using frontend-provided scenes with overlay data: %d scenes", len(params.VideoScenes))
		scenes := make([]llm.Scene, len(params.VideoScenes))
		imagePrompts := make([]string, len(params.VideoScenes))
		scriptParts := make([]string, len(params.VideoScenes))
		for i, ss := range params.VideoScenes {
			scenes[i] = llm.Scene{
				Narration:   ss.Narration,
				ImagePrompt: ss.ImagePrompt,
			}
			// 合并 overlay 数据
			if len(ss.Overlay) > 0 {
				scenes[i].Overlay = make([]llm.OverlayItem, len(ss.Overlay))
				for j, oi := range ss.Overlay {
					scenes[i].Overlay[j] = llm.OverlayItem{
						Text:     oi.Text,
						Position: oi.Position,
						Color:    oi.Color,
						Style:    oi.Style,
					}
				}
				utils.LogInfo("  scene %d: %d overlay items (from frontend)", i+1, len(ss.Overlay))
			} else if params.VideoRequirement != "" {
				// 前端没传 overlay，用 LLM 兜底生成
				scenes[i].Overlay = llm.AutoGenerateOverlay(ss.Narration, params.VideoRequirement)
				if len(scenes[i].Overlay) > 0 {
					utils.LogInfo("  scene %d: %d overlay items (auto-generated)", i+1, len(scenes[i].Overlay))
				}
			}
			imagePrompts[i] = ss.ImagePrompt
			scriptParts[i] = ss.Narration
		}
		script := strings.Join(scriptParts, "\n")
		return script, imagePrompts, scenes, nil
	}

	// 没有前端传入的 scenes，调用 LLM 生成
	utils.LogInfo("no frontend scenes, calling LLM to generate...")
	script, imagePrompts, scenes, err := llm.GenerateScriptWithScenes(params.VideoSubject, scriptPrompt, params.VideoRequirement, sceneCount, p.cfg)
	if err != nil {
		utils.LogWarn("scene-based script generation failed: %v, falling back to legacy mode", err)
		// 降级到旧模式
		script2, terms2, err2 := llm.GenerateScriptWithPrompt(params.VideoSubject, scriptPrompt, sceneCount, p.cfg)
		if err2 != nil {
			return "", nil, nil, err2
		}
		return script2, terms2, nil, nil
	}

	// 如果 scenes 为空但 script 很长，自动按段落切分成分镜
	if len(scenes) == 0 && len(script) > 500 {
		utils.LogInfo("scenes is empty but script is long (%d chars), auto-splitting into scenes...", len(script))
		scenes = autoSplitScriptToScenes(script, params.VideoRequirement, p.cfg)
		if len(scenes) > 0 {
			imagePrompts = make([]string, len(scenes))
			for i, s := range scenes {
				imagePrompts[i] = s.ImagePrompt
			}
			utils.LogInfo("auto-split into %d scenes", len(scenes))
		}
	}

	// 如果分镜模式返回了 scenes，用 scene 的 image_prompt 作为图片生成提示词
	if len(scenes) > 0 {
		utils.LogInfo("scene-based script: %d scenes, %d image prompts", len(scenes), len(imagePrompts))
		return script, imagePrompts, scenes, nil
	}

	// 分镜模式降级了，返回旧模式结果
	utils.LogInfo("script generated via legacy mode: %d chars, %d terms", len(script), len(imagePrompts))
	return script, imagePrompts, nil, nil
}

// fetchMaterialsScene 用分镜的 image_prompt 生成图片素材
func (p *Pipeline) fetchMaterialsScene(imagePrompts []string, taskDir string, params *models.VideoParams) ([]string, error) {
	source := params.VideoSource
	if source == "" {
		source = p.cfg.VideoSource
	}

	switch source {
	case "local":
		return material.GetLocalMaterials(p.cfg.ResourceDir, taskDir, len(imagePrompts))
	default:
		// 用分镜的 image_prompt 直接生成图片，不加随机风格后缀
		return material.GenerateImagesFromPrompts(imagePrompts, taskDir, p.cfg, params)
	}
}

// runDigitalHuman 数字人口播模式
// 流程: LLM脚本 → TTS语音 → (人脸照片+语音 → Agnes Video reference模式 → 说话视频) → BGM混音 → 成品
func (p *Pipeline) runDigitalHuman(taskID, taskDir string, params *models.VideoParams, result *models.TaskResult, save func(int)) (*models.TaskResult, error) {
	utils.LogInfo("=== Digital Human Mode ===")

	// Step 1: LLM 脚本生成
	utils.LogInfo("DH step 1: generating script via LLM...")
	script, _, err := p.generateScript(params, nil)
	if err != nil {
		result.State = models.TaskStateFailed
		result.Error = fmt.Sprintf("script generation failed: %v", err)
		result.EndTime = time.Now()
		p.saveTask(taskDir, result)
		return result, err
	}
	result.Script = script
	save(10)

	// Step 2: TTS 语音合成
	utils.LogInfo("DH step 2: generating TTS audio...")
	audioPath := filepath.Join(taskDir, "audio.mp3")
	_, err = tts.Synthesize(script, params.VoiceName, params.VoiceRate, audioPath, p.cfg)
	if err != nil {
		result.State = models.TaskStateFailed
		result.Error = fmt.Sprintf("TTS failed: %v", err)
		result.EndTime = time.Now()
		p.saveTask(taskDir, result)
		return result, err
	}
	save(25)

	// Step 3: 获取音频时长，计算视频秒数
	audioDuration := p.getAudioDuration(audioPath)
	videoSeconds := int(audioDuration)
	if videoSeconds < 4 {
		videoSeconds = 4
	}
	if videoSeconds > 12 {
		utils.LogInfo("DH: audio duration %.1fs exceeds 12s limit, will generate multiple segments", audioDuration)
	}

	utils.LogInfo("DH step 3: audio duration=%.1fs, video seconds=%d", audioDuration, videoSeconds)
	save(35)

	// Step 4: 生成数字人视频
	utils.LogInfo("DH step 4: generating digital human video via Agnes Video API...")

	portraitURL := params.DigitalHumanPortrait
	if portraitURL == "" {
		utils.LogInfo("DH: no portrait provided, generating one via image API...")
		portraitPath := filepath.Join(taskDir, "portrait.png")
		portraitURL, err = p.generatePortrait(params, portraitPath)
		if err != nil {
			result.State = models.TaskStateFailed
			result.Error = fmt.Sprintf("portrait generation failed: %v", err)
			result.EndTime = time.Now()
			p.saveTask(taskDir, result)
			return result, err
		}
	}

	audioURL, cleanup, err := p.serveFileTemporarily(audioPath, taskID)
	if err != nil {
		result.State = models.TaskStateFailed
		result.Error = fmt.Sprintf("failed to serve audio: %v", err)
		result.EndTime = time.Now()
		p.saveTask(taskDir, result)
		return result, err
	}
	defer cleanup()

	if !isURL(portraitURL) {
		portraitURL2, cleanup2, err := p.serveFileTemporarily(portraitURL, taskID)
		if err == nil {
			portraitURL = portraitURL2
			defer cleanup2()
		}
	}

	agnesClient := video.NewAgnesVideoClient(p.cfg)
	aspectRatio := params.VideoAspect
	if aspectRatio == "" {
		aspectRatio = "16:9"
	}

	finalVideoPath := filepath.Join(taskDir, "final.mp4")

	if videoSeconds < 12 {
		utils.LogInfo("DH: generating single segment video (%ds)...", videoSeconds)
		_, err = agnesClient.GenerateDigitalHumanVideo(portraitURL, audioURL, aspectRatio, videoSeconds, finalVideoPath)
	} else {
		utils.LogInfo("DH: generating multiple segments for %.1fs audio...", audioDuration)
		_, err = p.generateMultiSegmentDigitalHuman(agnesClient, portraitURL, audioURL, aspectRatio, audioDuration, taskDir, finalVideoPath)
	}

	if err != nil {
		result.State = models.TaskStateFailed
		result.Error = fmt.Sprintf("digital human video generation failed: %v", err)
		result.EndTime = time.Now()
		p.saveTask(taskDir, result)
		return result, err
	}
	save(75)

	// Step 5: BGM 混音
	if params.BGMType != "none" && params.BGMType != "" {
		utils.LogInfo("DH step 5: mixing BGM...")
		_, bgmErr := bgm.EnsureBGM(p.cfg)
		if bgmErr != nil {
			utils.LogWarn("BGM ensure failed: %v, skipping BGM", bgmErr)
		}
		bgmPath := video.SelectBGM(p.cfg)
		if bgmPath != "" {
			bgmMixedPath := filepath.Join(taskDir, "with_bgm.mp4")
			err = video.MixBGM(finalVideoPath, bgmPath, bgmMixedPath, params.BGMVolume)
			if err != nil {
				utils.LogWarn("BGM mix failed: %v, keeping without BGM", err)
			} else {
				finalVideoPath = bgmMixedPath
			}
		}
	}

	// Step 6: 音频替换
	utils.LogInfo("DH step 6: replacing audio with TTS...")
	audioReplacedPath := filepath.Join(taskDir, "final_audio.mp4")
	err = video.ReplaceAudio(finalVideoPath, audioPath, audioReplacedPath)
	if err != nil {
		utils.LogWarn("audio replace failed: %v, keeping original audio", err)
	} else {
		finalVideoPath = audioReplacedPath
	}
	save(90)

	// Step 7: 缩略图
	thumbPath := filepath.Join(taskDir, "thumbnail.jpg")
	if err := GenerateThumbnail(finalVideoPath, thumbPath); err != nil {
		utils.LogWarn("thumbnail generation failed: %v", err)
	} else {
		result.ThumbnailURL = fmt.Sprintf("/api/v1/download/%s/thumbnail.jpg", taskID)
	}

	// Step 8: 完成
	result.State = models.TaskStateComplete
	result.Progress = 100
	result.VideoPath = finalVideoPath
	result.VideoURL = fmt.Sprintf("/api/v1/download/%s/final.mp4", taskID)
	result.EndTime = time.Now()
	p.saveTask(taskDir, result)

	utils.LogInfo("digital human task %s completed: %s", taskID, finalVideoPath)
	return result, nil
}

// generateMultiSegmentDigitalHuman 生成多段数字人视频并拼接
func (p *Pipeline) generateMultiSegmentDigitalHuman(client *video.AgnesVideoClient, portraitURL, audioURL, aspectRatio string, totalDuration float64, taskDir, finalPath string) (string, error) {
	segmentDuration := 12
	numSegments := int(totalDuration) / segmentDuration
	if int(totalDuration)%segmentDuration != 0 {
		numSegments++
	}

	utils.LogInfo("DH multi-segment: %d segments for %.1fs", numSegments, totalDuration)

	var segmentPaths []string
	for i := 0; i < numSegments; i++ {
		startSec := i * segmentDuration
		endSec := (i + 1) * segmentDuration
		if endSec > int(totalDuration) {
			endSec = int(totalDuration)
		}
		segDuration := endSec - startSec
		if segDuration < 4 {
			segDuration = 4
		}

		utils.LogInfo("DH segment %d/%d: %ds-%ds (%ds)", i+1, numSegments, startSec, endSec, segDuration)

		segPath := filepath.Join(taskDir, fmt.Sprintf("segment_%d.mp4", i))
		_, err := client.GenerateDigitalHumanVideo(portraitURL, audioURL, aspectRatio, segDuration, segPath)
		if err != nil {
			return "", fmt.Errorf("segment %d failed: %w", i, err)
		}
		segmentPaths = append(segmentPaths, segPath)
	}

	utils.LogInfo("DH: concatenating %d segments...", len(segmentPaths))
	err := video.ConcatenateSegments(segmentPaths, finalPath)
	if err != nil {
		return "", fmt.Errorf("concatenation failed: %w", err)
	}

	return finalPath, nil
}

// generatePortrait 生成数字人肖像照片
func (p *Pipeline) generatePortrait(params *models.VideoParams, destPath string) (string, error) {
	portraitPrompt := "professional portrait photo of a news anchor, studio lighting, upper body, looking at camera, clean background, high quality"
	if params.VideoSubject != "" {
		portraitPrompt = fmt.Sprintf("professional portrait photo of a person for video about: %s, studio lighting, upper body, looking at camera, clean background", params.VideoSubject)
	}

	terms := []string{portraitPrompt}
	imgPaths, err := material.GenerateImages(terms, filepath.Dir(destPath), p.cfg, params)
	if err != nil || len(imgPaths) == 0 {
		return "", fmt.Errorf("portrait generation failed: %w", err)
	}

	os.Rename(imgPaths[0], destPath)
	return destPath, nil
}

// serveFileTemporarily 返回本地文件的公开可访问 URL
func (p *Pipeline) serveFileTemporarily(filePath, taskID string) (string, func(), error) {
	baseURL := p.cfg.GetPublicBaseURL()
	if baseURL == "" {
		return "", nil, fmt.Errorf("public_base_url not configured, cannot serve files to external API")
	}

	storageDir := p.cfg.StorageDir
	relPath, err := filepath.Rel(filepath.Join(storageDir, "tasks"), filePath)
	if err != nil {
		relPath = filepath.Base(filePath)
	}
	relPath = filepath.ToSlash(relPath)

	publicURL := fmt.Sprintf("%s/api/v1/download/%s/%s", baseURL, taskID, filepath.Base(filePath))
	utils.LogInfo("serving file publicly: %s", publicURL)
	return publicURL, func() {}, nil
}

func isURL(s string) bool {
	return len(s) > 4 && (s[:4] == "http" || s[:5] == "https")
}

// generateScript 旧模式脚本生成（数字人模式使用）
func (p *Pipeline) generateScript(params *models.VideoParams, tmpl *template.Template) (string, []string, error) {
	if params.VideoScript != "" {
		terms := params.VideoTerms
		if len(terms) == 0 {
			generatedTerms, err := llm.GenerateTerms(params.VideoScript, p.cfg)
			if err == nil {
				terms = generatedTerms
			}
		}
		return params.VideoScript, terms, nil
	}

	scriptPrompt := ""
	imageCount := 8
	if tmpl != nil {
		scriptPrompt = tmpl.GetScriptPrompt()
		imageCount = tmpl.ImageCount
	}

	return llm.GenerateScriptWithPrompt(params.VideoSubject, scriptPrompt, imageCount, p.cfg)
}

// fetchMaterials 获取视频素材（旧模式）
func (p *Pipeline) fetchMaterials(terms []string, taskDir string, params *models.VideoParams) ([]string, error) {
	source := params.VideoSource
	if source == "" {
		source = p.cfg.VideoSource
	}

	switch source {
	case "local":
		return material.GetLocalMaterials(p.cfg.ResourceDir, taskDir, len(terms))
	default:
		return material.GenerateImages(terms, taskDir, p.cfg, params)
	}
}

// getAudioDuration 获取音频时长
func (p *Pipeline) getAudioDuration(audioPath string) float64 {
	dur, err := utils.GetMediaDuration(audioPath)
	if err != nil {
		utils.LogWarn("failed to get audio duration: %v, using default 60s", err)
		return 60.0
	}
	return dur
}

// saveTask 保存任务状态
func (p *Pipeline) saveTask(taskDir string, result *models.TaskResult) {
	data, _ := json.MarshalIndent(result, "", "  ")
	os.WriteFile(filepath.Join(taskDir, "task.json"), data, 0644)
}

func LoadTask(cfg *config.Config, taskID string) (*models.TaskResult, error) {
	taskFile := filepath.Join(cfg.StorageDir, "tasks", taskID, "task.json")
	data, err := os.ReadFile(taskFile)
	if err != nil {
		return nil, err
	}
	var result models.TaskResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ListTasksOpts 带筛选和分页的任务列表查询
type ListTasksOpts struct {
	State    int
	Page     int
	PageSize int
}

// ListTasksFiltered 带筛选和分页的任务列表
func ListTasksFiltered(cfg *config.Config, opts ListTasksOpts) ([]*models.TaskResult, int, error) {
	tasksDir := filepath.Join(cfg.StorageDir, "tasks")
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return nil, 0, err
	}

	var tasks []*models.TaskResult
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		taskFile := filepath.Join(tasksDir, entry.Name(), "task.json")
		data, err := os.ReadFile(taskFile)
		if err != nil {
			continue
		}
		var result models.TaskResult
		if err := json.Unmarshal(data, &result); err != nil {
			continue
		}

		if opts.State != 0 && result.State != opts.State {
			continue
		}

		tasks = append(tasks, &result)
	}

	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].StartTime.After(tasks[j].StartTime)
	})

	total := len(tasks)

	if opts.PageSize <= 0 {
		opts.PageSize = 30
	}
	if opts.Page <= 0 {
		opts.Page = 1
	}
	start := (opts.Page - 1) * opts.PageSize
	if start >= total {
		return []*models.TaskResult{}, total, nil
	}
	end := start + opts.PageSize
	if end > total {
		end = total
	}

	return tasks[start:end], total, nil
}

func ListTasks(cfg *config.Config) ([]*models.TaskResult, error) {
	tasksDir := filepath.Join(cfg.StorageDir, "tasks")
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return nil, err
	}

	var tasks []*models.TaskResult
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		taskFile := filepath.Join(tasksDir, entry.Name(), "task.json")
		data, err := os.ReadFile(taskFile)
		if err != nil {
			continue
		}
		var result models.TaskResult
		if err := json.Unmarshal(data, &result); err != nil {
			continue
		}
		tasks = append(tasks, &result)
	}

	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].StartTime.After(tasks[j].StartTime)
	})

	return tasks, nil
}

func DeleteTask(cfg *config.Config, taskID string) error {
	taskDir := filepath.Join(cfg.StorageDir, "tasks", taskID)
	return os.RemoveAll(taskDir)
}

func generateTaskID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}


// autoSplitScriptToScenes 用 LLM 把长文章改写成多个分镜
func autoSplitScriptToScenes(script, requirement string, cfg *config.Config) []llm.Scene {
	utils.LogInfo("using LLM to split long script into scenes...")
	scenes, err := llm.SplitScriptToScenes(script, requirement, cfg)
	if err != nil {
		utils.LogWarn("LLM split script failed: %v, falling back to simple split", err)
		return nil
	}
	return scenes
}
