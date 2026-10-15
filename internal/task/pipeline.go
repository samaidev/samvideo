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
