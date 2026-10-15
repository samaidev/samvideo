package task

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"samvideo/internal/bgm"
	"samvideo/internal/config"
	"samvideo/internal/llm"
	"samvideo/internal/material"
	"samvideo/internal/models"
	"samvideo/internal/subtitle"
	"samvideo/internal/tts"
	"samvideo/internal/utils"
	"samvideo/internal/video"
)

// Pipeline 任务编排器
type Pipeline struct {
	cfg *config.Config
	mu  sync.Mutex
}

// NewPipeline 创建编排器
func NewPipeline(cfg *config.Config) *Pipeline {
	return &Pipeline{cfg: cfg}
}

// RunTask 执行完整的视频生成流水线（内部生成 task_id）
func (p *Pipeline) RunTask(params *models.VideoParams) (*models.TaskResult, error) {
	taskID := generateTaskID()
	return p.RunTaskWithID(taskID, params)
}

// RunTaskWithID 使用指定的 task_id 执行完整的视频生成流水线
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

	// Step 1: LLM 脚本生成
	utils.LogInfo("step 1: generating script via LLM...")
	script, terms, err := p.generateScript(params)
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
	}, "", "  ")
	os.WriteFile(filepath.Join(taskDir, "script.json"), scriptData, 0644)

	// Step 2: TTS 语音合成
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

	// Step 3: 字幕生成
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

	// Step 4: 素材获取
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

	// Step 5: 视频片段处理 + 拼接
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

	// Step 5.5: 如果需要 BGM，确保 BGM 文件已生成
	if params.BGMType != "none" && params.BGMType != "" {
		utils.LogInfo("step 5.5: ensuring BGM files...")
		_, bgmErr := bgm.EnsureBGM(p.cfg)
		if bgmErr != nil {
			utils.LogWarn("BGM generation failed: %v, continuing without BGM", bgmErr)
		}
	}

	// Step 6: 最终合成 (视频+音频+字幕+背景音乐)
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

	// Step 7: 完成
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
func (p *Pipeline) generateScript(params *models.VideoParams) (string, []string, error) {
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

	return llm.GenerateScript(params.VideoSubject, p.cfg)
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

// saveTask 保存任务状态到文件
func (p *Pipeline) saveTask(taskDir string, result *models.TaskResult) {
	data, _ := json.MarshalIndent(result, "", "  ")
	os.WriteFile(filepath.Join(taskDir, "task.json"), data, 0644)
}

// LoadTask 从文件加载任务状态
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

// ListTasks 列出所有任务
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
	return tasks, nil
}

// DeleteTask 删除任务
func DeleteTask(cfg *config.Config, taskID string) error {
	taskDir := filepath.Join(cfg.StorageDir, "tasks", taskID)
	return os.RemoveAll(taskDir)
}

// generateTaskID 生成任务 ID
func generateTaskID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
