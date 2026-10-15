package task

import (
	"encoding/json"
	"fmt"
	"os"
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
	cfg *config.Config
	mu  sync.Mutex
}

func NewPipeline(cfg *config.Config) *Pipeline {
	return &Pipeline{cfg: cfg}
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

	// ====== 普通视频生成模式 ======

	// Step 0: 模板分类（如果用户没有指定 aspect，自动分类）
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

	// Step 1: LLM 脚本生成
	utils.LogInfo("step 1: generating script via LLM...")
	script, terms, err := p.generateScript(params, tmpl)
	if err != nil {
		result.State = models.TaskStateFailed
		result.Error = fmt.Sprintf("script generation failed: %v", err)
		result.EndTime = time.Now()
		p.saveTask(taskDir, result)
		return result, err
	}
	result.Script = script
	result.SearchTerms = terms
	save(10)

	scriptData, _ := json.MarshalIndent(map[string]interface{}{
		"script":       script,
		"search_terms": terms,
		"params":       params,
		"template":     tmpl,
	}, "", "  ")
	os.WriteFile(filepath.Join(taskDir, "script.json"), scriptData, 0644)

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
	clipPaths, err := p.fetchMaterials(terms, taskDir, params)
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

	// Step 7: 生成缩略图
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
		// Agnes Video 限制 4-12 秒，需要分段
		utils.LogInfo("DH: audio duration %.1fs exceeds 12s limit, will generate multiple segments", audioDuration)
	}

	utils.LogInfo("DH step 3: audio duration=%.1fs, video seconds=%d", audioDuration, videoSeconds)
	save(35)

	// Step 4: 生成数字人视频
	utils.LogInfo("DH step 4: generating digital human video via Agnes Video API...")

	// 获取人脸照片 URL
	portraitURL := params.DigitalHumanPortrait
	if portraitURL == "" {
		// 如果没有提供照片，用文生图生成一个
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

	// 音频需要公开可访问的 URL
	// 由于本地音频没有公开 URL，我们需要先上传或使用 base64
	// 方案：启动一个临时 HTTP 服务来提供音频文件
	audioURL, cleanup, err := p.serveFileTemporarily(audioPath, taskID)
	if err != nil {
		result.State = models.TaskStateFailed
		result.Error = fmt.Sprintf("failed to serve audio: %v", err)
		result.EndTime = time.Now()
		p.saveTask(taskDir, result)
		return result, err
	}
	defer cleanup()

	// 如果照片也是本地文件，同样需要公开 URL
	if !isURL(portraitURL) {
		portraitURL2, cleanup2, err := p.serveFileTemporarily(portraitURL, taskID)
		if err == nil {
			portraitURL = portraitURL2
			defer cleanup2()
		}
	}

	// 调用 Agnes Video API 生成数字人视频
	agnesClient := video.NewAgnesVideoClient(p.cfg)
	aspectRatio := params.VideoAspect
	if aspectRatio == "" {
		aspectRatio = "16:9"
	}

	finalVideoPath := filepath.Join(taskDir, "final.mp4")

	// 如果音频 <= 12秒，直接生成一段视频
	if videoSeconds <= 12 {
		utils.LogInfo("DH: generating single segment video (%ds)...", videoSeconds)
		_, err = agnesClient.GenerateDigitalHumanVideo(portraitURL, audioURL, aspectRatio, videoSeconds, finalVideoPath)
	} else {
		// 音频 > 12秒，生成多段视频然后拼接
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
		// BGM 混音在 FinalCompose 中处理，但数字人模式需要单独处理
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

	// Step 6: 音频替换（用原始 TTS 音频替换视频中的音频）
	// Agnes Video 生成的视频可能有自己的音频，我们需要用 TTS 音频替换
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
	segmentDuration := 12 // 每段最多12秒
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
			segDuration = 4 // Agnes 最少4秒
		}

		utils.LogInfo("DH segment %d/%d: %ds-%ds (%ds)", i+1, numSegments, startSec, endSec, segDuration)

		segPath := filepath.Join(taskDir, fmt.Sprintf("segment_%d.mp4", i))
		_, err := client.GenerateDigitalHumanVideo(portraitURL, audioURL, aspectRatio, segDuration, segPath)
		if err != nil {
			return "", fmt.Errorf("segment %d failed: %w", i, err)
		}
		segmentPaths = append(segmentPaths, segPath)
	}

	// 拼接所有段
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

	// 重命名第一张图片
	os.Rename(imgPaths[0], destPath)
	return destPath, nil
}

// serveFileTemporarily 返回本地文件的公开可访问 URL
// 使用 PublicBaseURL 配置项拼接 URL
func (p *Pipeline) serveFileTemporarily(filePath, taskID string) (string, func(), error) {
	baseURL := p.cfg.GetPublicBaseURL()
	if baseURL == "" {
		return "", nil, fmt.Errorf("public_base_url not configured, cannot serve files to external API")
	}

	// 计算文件相对于 storage 目录的相对路径
	storageDir := p.cfg.StorageDir
	relPath, err := filepath.Rel(filepath.Join(storageDir, "tasks"), filePath)
	if err != nil {
		// 如果不在 tasks 目录下，直接用文件名
		relPath = filepath.Base(filePath)
	}
	relPath = filepath.ToSlash(relPath)

	// 拼接公开 URL: baseURL/api/v1/download/taskID/filename
	publicURL := fmt.Sprintf("%s/api/v1/download/%s/%s", baseURL, taskID, filepath.Base(filePath))
	utils.LogInfo("serving file publicly: %s", publicURL)
	return publicURL, func() {}, nil
}

// isURL checks if a string is a URL
func isURL(s string) bool {
	return len(s) > 4 && (s[:4] == "http" || s[:5] == "https")
}

// generateScript 调用 LLM 生成脚本
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

	// 如果有模板，使用模板的脚本提示词
	scriptPrompt := ""
	imageCount := 8
	if tmpl != nil {
		scriptPrompt = tmpl.GetScriptPrompt()
		imageCount = tmpl.ImageCount
	}

	return llm.GenerateScriptWithPrompt(params.VideoSubject, scriptPrompt, imageCount, p.cfg)
}

// fetchMaterials 获取视频素材
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
	State    int  // 0=全部, 1=完成, 4=处理中, -1=失败
	Page     int  // 从1开始
	PageSize int  // 默认30
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

		// 状态筛选
		if opts.State != 0 && result.State != opts.State {
			continue
		}

		tasks = append(tasks, &result)
	}

	// 按开始时间降序排列（最新的在前）
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].StartTime.After(tasks[j].StartTime)
	})

	total := len(tasks)

	// 分页
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

	// 按开始时间降序排列
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
