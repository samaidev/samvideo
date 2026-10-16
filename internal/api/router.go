package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"samvideo/internal/config"
	"samvideo/internal/models"
	"samvideo/internal/task"
	"samvideo/internal/template"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type Server struct {
	cfg      *config.Config
	pipeline *task.Pipeline
}

func NewServer(cfg *config.Config) *Server {
	return &Server{
		cfg:      cfg,
		pipeline: task.NewPipeline(cfg),
	}
}

func (s *Server) SetupRoutes(r *gin.Engine) {
	r.GET("/ping", s.ping)

	v1 := r.Group("/api/v1")
	{
		v1.POST("/videos", s.createVideo)
		v1.POST("/scripts", s.generateScript)
		v1.GET("/tasks", s.listTasks)
		v1.GET("/tasks/:task_id", s.getTask)
		v1.DELETE("/tasks/:task_id", s.deleteTask)
		v1.GET("/download/:task_id/:filename", s.downloadFile)
		v1.GET("/stream/:task_id/:filename", s.streamFile)
		v1.GET("/templates", s.listTemplates)

		// 素材库 API（分页）
		v1.GET("/materials/bgm", s.listBGM)
		v1.POST("/materials/bgm/upload", s.uploadBGM)
		v1.DELETE("/materials/bgm/:filename", s.deleteBGM)
		v1.GET("/materials/images", s.listImages)
		v1.POST("/materials/images/upload", s.uploadImages)
		v1.DELETE("/materials/images/:filename", s.deleteImage)
		v1.GET("/materials/videos", s.listVideos)
		v1.POST("/materials/videos/upload", s.uploadVideos)
		v1.DELETE("/materials/videos/:filename", s.deleteVideo)

		// 静态素材文件
		v1.GET("/materials/bgm/file/:filename", s.serveBGMFile)
		v1.GET("/materials/images/file/:filename", s.serveImageFile)
		v1.GET("/materials/videos/file/:filename", s.serveVideoFile)

		// 数字人头像
		v1.POST("/digital-human/portrait", s.uploadDHPortrait)
		v1.POST("/digital-human/portrait/generate", s.generateDHPortrait)
		v1.GET("/digital-human/portrait/:filename", s.serveDHPortrait)
		v1.GET("/digital-human/portraits", s.listDHPortraits)
		v1.DELETE("/digital-human/portrait/:filename", s.deleteDHPortrait)

		// LLM 配置 API
		v1.GET("/config", s.getConfig)
		v1.PUT("/config", s.updateConfig)
		v1.POST("/config/test", s.testLLM)
	}
}

func (s *Server) ping(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "samvideo"})
}

// ==================== Video Generation ====================

func (s *Server) createVideo(c *gin.Context) {
	var params models.VideoParams
	if err := c.ShouldBindJSON(&params); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	tmplID := c.Query("template_id")
	if tmplID != "" {
		for i := range template.BuiltinTemplates {
			if template.BuiltinTemplates[i].ID == tmplID {
				tmpl := &template.BuiltinTemplates[i]
				template.ApplyTemplate(tmpl, &params)
				break
			}
		}
	}

	if params.VideoSource == "" {
		params.VideoSource = s.cfg.VideoSource
	}
	if params.VoiceRate == 0 {
		params.VoiceRate = 1.0
	}
	if params.VideoClipDuration == 0 {
		params.VideoClipDuration = 5
	}
	if params.VideoClipSpeed == 0 {
		params.VideoClipSpeed = 1.0
	}
	if params.VideoTransitionMode == "" {
		params.VideoTransitionMode = "shuffle"
	}
	if params.BGMType == "" {
		params.BGMType = s.cfg.BGMType
	}
	if params.BGMVolume == 0 {
		params.BGMVolume = s.cfg.BGMVolume
	}
	if params.SubtitleProvider == "" {
		params.SubtitleProvider = s.cfg.SubtitleProvider
	}

	taskID := uuid.NewString()
	go func() {
		s.pipeline.RunTaskWithID(taskID, &params)
	}()

	c.JSON(http.StatusOK, gin.H{
		"status":  200,
		"message": "success",
		"data": gin.H{
			"task_id": taskID,
			"hint":    "task started, poll GET /api/v1/tasks/{task_id} for status",
		},
	})
}

func (s *Server) listTemplates(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": 200, "message": "success", "data": template.BuiltinTemplates})
}

func (s *Server) generateScript(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": 200, "message": "success"})
}

// ==================== Tasks ====================

func (s *Server) listTasks(c *gin.Context) {
	state := 0
	if st := c.Query("state"); st != "" {
		state, _ = strconv.Atoi(st)
	}
	page := 1
	if p := c.Query("page"); p != "" {
		page, _ = strconv.Atoi(p)
	}
	pageSize := 30
	if ps := c.Query("size"); ps != "" {
		pageSize, _ = strconv.Atoi(ps)
	}

	tasks, total, err := task.ListTasksFiltered(s.cfg, task.ListTasksOpts{State: state, Page: page, PageSize: pageSize})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	allTasks, _ := task.ListTasks(s.cfg)
	stateCounts := map[string]int{"all": len(allTasks), "completed": 0, "processing": 0, "failed": 0}
	for _, t := range allTasks {
		switch t.State {
		case models.TaskStateComplete:
			stateCounts["completed"]++
		case models.TaskStateProcessing:
			stateCounts["processing"]++
		case models.TaskStateFailed:
			stateCounts["failed"]++
		}
	}

	totalPages := (total + pageSize - 1) / pageSize
	if totalPages == 0 {
		totalPages = 1
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  200,
		"message": "success",
		"data":    tasks,
		"pagination": gin.H{"page": page, "page_size": pageSize, "total": total, "total_pages": totalPages},
		"counts":  stateCounts,
	})
}

func (s *Server) getTask(c *gin.Context) {
	taskID := c.Param("task_id")
	result, err := task.LoadTask(s.cfg, taskID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": 200, "message": "success", "data": result})
}

func (s *Server) deleteTask(c *gin.Context) {
	taskID := c.Param("task_id")
	if err := task.DeleteTask(s.cfg, taskID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": 200, "message": "deleted"})
}

func (s *Server) downloadFile(c *gin.Context) {
	taskID := c.Param("task_id")
	filename := c.Param("filename")
	if strings.Contains(filename, "..") || strings.Contains(filename, "/") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid filename"})
		return
	}
	filePath := filepath.Join(s.cfg.StorageDir, "tasks", taskID, filename)
	if _, err := os.Stat(filePath); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		return
	}
	if strings.HasSuffix(filename, ".jpg") || strings.HasSuffix(filename, ".png") {
		c.Header("Content-Type", "image/jpeg")
		c.File(filePath)
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
	c.File(filePath)
}

func (s *Server) streamFile(c *gin.Context) {
	taskID := c.Param("task_id")
	filename := c.Param("filename")
	if strings.Contains(filename, "..") || strings.Contains(filename, "/") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid filename"})
		return
	}
	filePath := filepath.Join(s.cfg.StorageDir, "tasks", taskID, filename)
	if _, err := os.Stat(filePath); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		return
	}
	c.Header("Content-Type", "video/mp4")
	c.File(filePath)
}

// ==================== Materials Library (paginated) ====================

type MatItem struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"url"`
}

func (s *Server) listBGM(c *gin.Context) {
	bgmDir := filepath.Join(s.cfg.ResourceDir, "songs", "downloaded")
	os.MkdirAll(bgmDir, 0755)
	items := scanMaterialDir(bgmDir, "/api/v1/materials/bgm/file/", isAudioFile)
	s.respondMaterials(c, items)
}

func (s *Server) uploadBGM(c *gin.Context) {
	bgmDir := filepath.Join(s.cfg.ResourceDir, "songs", "downloaded")
	os.MkdirAll(bgmDir, 0755)
	uploaded := s.handleUpload(c, bgmDir, "file", isAudioFile)
	c.JSON(http.StatusOK, gin.H{"status": 200, "message": "uploaded", "data": gin.H{"uploaded": uploaded, "count": len(uploaded)}})
}

func (s *Server) deleteBGM(c *gin.Context) {
	s.handleDelete(c, filepath.Join(s.cfg.ResourceDir, "songs", "downloaded"))
}

func (s *Server) listImages(c *gin.Context) {
	imgDir := filepath.Join(s.cfg.ResourceDir, "images")
	os.MkdirAll(imgDir, 0755)
	items := scanMaterialDir(imgDir, "/api/v1/materials/images/file/", isImageFile)
	s.respondMaterials(c, items)
}

func (s *Server) uploadImages(c *gin.Context) {
	imgDir := filepath.Join(s.cfg.ResourceDir, "images")
	os.MkdirAll(imgDir, 0755)
	uploaded := s.handleUpload(c, imgDir, "files", isImageFile)
	c.JSON(http.StatusOK, gin.H{"status": 200, "message": "uploaded", "data": gin.H{"uploaded": uploaded, "count": len(uploaded)}})
}

func (s *Server) deleteImage(c *gin.Context) {
	s.handleDelete(c, filepath.Join(s.cfg.ResourceDir, "images"))
}

func (s *Server) listVideos(c *gin.Context) {
	vidDir := filepath.Join(s.cfg.ResourceDir, "videos")
	os.MkdirAll(vidDir, 0755)
	items := scanMaterialDir(vidDir, "/api/v1/materials/videos/file/", isVideoFile)
	s.respondMaterials(c, items)
}

func (s *Server) uploadVideos(c *gin.Context) {
	vidDir := filepath.Join(s.cfg.ResourceDir, "videos")
	os.MkdirAll(vidDir, 0755)
	uploaded := s.handleUpload(c, vidDir, "files", isVideoFile)
	c.JSON(http.StatusOK, gin.H{"status": 200, "message": "uploaded", "data": gin.H{"uploaded": uploaded, "count": len(uploaded)}})
}

func (s *Server) deleteVideo(c *gin.Context) {
	s.handleDelete(c, filepath.Join(s.cfg.ResourceDir, "videos"))
}

func (s *Server) serveBGMFile(c *gin.Context) {
	filename := c.Param("filename")
	if strings.Contains(filename, "..") || strings.Contains(filename, "/") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid filename"})
		return
	}
	filePath := filepath.Join(s.cfg.ResourceDir, "songs", "downloaded", filename)
	if _, err := os.Stat(filePath); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		return
	}
	c.Header("Content-Type", "audio/mpeg")
	c.File(filePath)
}

func (s *Server) serveImageFile(c *gin.Context) {
	filename := c.Param("filename")
	if strings.Contains(filename, "..") || strings.Contains(filename, "/") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid filename"})
		return
	}
	filePath := filepath.Join(s.cfg.ResourceDir, "images", filename)
	if _, err := os.Stat(filePath); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		return
	}
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".png":
		c.Header("Content-Type", "image/png")
	case ".jpg", ".jpeg":
		c.Header("Content-Type", "image/jpeg")
	case ".gif":
		c.Header("Content-Type", "image/gif")
	case ".webp":
		c.Header("Content-Type", "image/webp")
	}
	c.File(filePath)
}

func (s *Server) serveVideoFile(c *gin.Context) {
	filename := c.Param("filename")
	if strings.Contains(filename, "..") || strings.Contains(filename, "/") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid filename"})
		return
	}
	filePath := filepath.Join(s.cfg.ResourceDir, "videos", filename)
	if _, err := os.Stat(filePath); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		return
	}
	c.Header("Content-Type", "video/mp4")
	c.File(filePath)
}

// Helpers

func scanMaterialDir(dir, urlPrefix string, filter func(string) bool) []MatItem {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var items []MatItem
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !filter(name) {
			continue
		}
		info, _ := e.Info()
		items = append(items, MatItem{
			Name: name,
			Size: info.Size(),
			URL:  urlPrefix + name,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items
}

func (s *Server) respondMaterials(c *gin.Context, items []MatItem) {
	page := 1
	if p := c.Query("page"); p != "" {
		page, _ = strconv.Atoi(p)
	}
	pageSize := 30
	if ps := c.Query("size"); ps != "" {
		pageSize, _ = strconv.Atoi(ps)
	}
	total := len(items)
	totalPages := (total + pageSize - 1) / pageSize
	if totalPages == 0 {
		totalPages = 1
	}
	start := (page - 1) * pageSize
	if start >= total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	var paged []MatItem = []MatItem{}
	if start < end {
		paged = items[start:end]
	}
	c.JSON(http.StatusOK, gin.H{
		"status":  200,
		"message": "success",
		"data":    paged,
		"pagination": gin.H{"page": page, "page_size": pageSize, "total": total, "total_pages": totalPages},
	})
}

func (s *Server) handleUpload(c *gin.Context, dir, fieldName string, filter func(string) bool) []string {
	form, err := c.MultipartForm()
	if err != nil {
		return nil
	}
	files := form.File[fieldName]
	if len(files) == 0 {
		// try "file" singular
		files = form.File["file"]
	}
	var uploaded []string
	for _, header := range files {
		if !filter(header.Filename) {
			continue
		}
		dst := filepath.Join(dir, header.Filename)
		if err := c.SaveUploadedFile(header, dst); err != nil {
			continue
		}
		uploaded = append(uploaded, header.Filename)
	}
	return uploaded
}

func (s *Server) handleDelete(c *gin.Context, dir string) {
	filename := c.Param("filename")
	if strings.Contains(filename, "..") || strings.Contains(filename, "/") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid filename"})
		return
	}
	filePath := filepath.Join(dir, filename)
	if err := os.Remove(filePath); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": 200, "message": "deleted"})
}

func isAudioFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".mp3" || ext == ".wav" || ext == ".m4a" || ext == ".ogg" || ext == ".flac"
}

func isImageFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".gif" || ext == ".webp" || ext == ".bmp"
}

func isVideoFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".mp4" || ext == ".avi" || ext == ".mov" || ext == ".mkv" || ext == ".webm" || ext == ".flv"
}

// ==================== LLM Config API ====================

func (s *Server) getConfig(c *gin.Context) {
	cfg := s.cfg
	c.JSON(http.StatusOK, gin.H{
		"status":  200,
		"message": "success",
		"data": gin.H{
			"llm_provider":          cfg.LLMProvider,
			"llm_base_url":          cfg.OpenAI.BaseURL,
			"llm_model":             cfg.OpenAI.ModelName,
			"llm_api_key_masked":    maskKey(cfg.OpenAI.APIKey),
			"image_base_url":        cfg.OpenAIImageBaseURL,
			"image_model":           cfg.OpenAIImageModel,
			"image_size":            cfg.OpenAIImageSize,
			"image_api_key_masked":  maskKey(getFirstKey(cfg.OpenAIImageAPIKeys)),
			"video_source":          cfg.VideoSource,
			"subtitle_provider":     cfg.SubtitleProvider,
			"bgm_type":              cfg.BGMType,
			"bgm_volume":            cfg.BGMVolume,
			"voice_name":            "",
			"voice_rate":            1.0,
			"video_clip_duration":   5,
			"video_clip_speed":      1.0,
			"video_transition_mode": "shuffle",
			"public_base_url":         cfg.PublicBaseURL,
			"video_api_base_url":      cfg.GetVideoAPIBaseURL(),
			"video_model":             cfg.GetVideoModel(),
			"video_api_key_masked":    maskKey(cfg.GetFirstVideoAPIKey()),
		},
	})
}

func (s *Server) updateConfig(c *gin.Context) {
	var req struct {
		LLMProvider       *string  `json:"llm_provider"`
		LLMBaseURL        *string  `json:"llm_base_url"`
		LLMModel          *string  `json:"llm_model"`
		LLMAPIKey         *string  `json:"llm_api_key"`
		ImageBaseURL      *string  `json:"image_base_url"`
		ImageModel        *string  `json:"image_model"`
		ImageSize         *string  `json:"image_size"`
		ImageAPIKey       *string  `json:"image_api_key"`
		VideoSource       *string  `json:"video_source"`
		SubtitleProvider  *string  `json:"subtitle_provider"`
		BGMType           *string  `json:"bgm_type"`
		BGMVolume         *float64 `json:"bgm_volume"`
		PublicBaseURL      *string  `json:"public_base_url"`
		VideoAPIBaseURL    *string  `json:"video_api_base_url"`
		VideoModel         *string  `json:"video_model"`
		VideoAPIKey        *string  `json:"video_api_key"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	cfg := s.cfg

	if req.LLMProvider != nil {
		cfg.LLMProvider = *req.LLMProvider
	}
	if req.LLMBaseURL != nil {
		cfg.OpenAI.BaseURL = *req.LLMBaseURL
	}
	if req.LLMModel != nil {
		cfg.OpenAI.ModelName = *req.LLMModel
	}
	if req.LLMAPIKey != nil && *req.LLMAPIKey != "" && !strings.HasPrefix(*req.LLMAPIKey, "****") {
		cfg.OpenAI.APIKey = *req.LLMAPIKey
	}
	if req.ImageBaseURL != nil {
		cfg.OpenAIImageBaseURL = *req.ImageBaseURL
	}
	if req.ImageModel != nil {
		cfg.OpenAIImageModel = *req.ImageModel
	}
	if req.ImageSize != nil {
		cfg.OpenAIImageSize = *req.ImageSize
	}
	if req.ImageAPIKey != nil && *req.ImageAPIKey != "" && !strings.HasPrefix(*req.ImageAPIKey, "****") {
		cfg.OpenAIImageAPIKeys = []string{*req.ImageAPIKey}
	}
	if req.VideoSource != nil {
		cfg.VideoSource = *req.VideoSource
	}
	if req.SubtitleProvider != nil {
		cfg.SubtitleProvider = *req.SubtitleProvider
	}
	if req.BGMType != nil {
		cfg.BGMType = *req.BGMType
	}
	if req.BGMVolume != nil {
		cfg.BGMVolume = *req.BGMVolume
	}
	if req.PublicBaseURL != nil {
		cfg.PublicBaseURL = *req.PublicBaseURL
	}

	// 保存到 config.toml
	s.saveConfigToFile(cfg)

	// 更新全局配置
	config.Update(cfg)

	c.JSON(http.StatusOK, gin.H{"status": 200, "message": "config updated"})
}

func (s *Server) testLLM(c *gin.Context) {
	cfg := s.cfg
	if cfg.OpenAI.BaseURL == "" || cfg.OpenAI.APIKey == "" || cfg.OpenAI.ModelName == "" {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "LLM not configured"})
		return
	}

	// 简单测试：发送一个 hello 请求
	url := strings.TrimSuffix(cfg.OpenAI.BaseURL, "/") + "/chat/completions"
	reqBody := map[string]interface{}{
		"model": cfg.OpenAI.ModelName,
		"messages": []map[string]string{
			{"role": "user", "content": "请回复'你好'两个字"},
		},
		"max_tokens": 50,
	}
	bodyJSON, _ := json.Marshal(reqBody)

	req, err := http.NewRequest("POST", url, strings.NewReader(string(bodyJSON)))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.OpenAI.APIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(body))})
		return
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	json.Unmarshal(body, &result)

	reply := ""
	if len(result.Choices) > 0 {
		reply = result.Choices[0].Message.Content
	}

	c.JSON(http.StatusOK, gin.H{"ok": true, "reply": reply, "model": cfg.OpenAI.ModelName})
}

func (s *Server) saveConfigToFile(cfg *config.Config) {
	configPath := "config.toml"
	var lines []string

	lines = append(lines, "# SamVideo configuration (auto-updated via Web UI)")
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("log_level = \"%s\"", cfg.LogLevel))
	lines = append(lines, fmt.Sprintf("listen_host = \"%s\"", cfg.ListenHost))
	lines = append(lines, fmt.Sprintf("listen_port = %d", cfg.ListenPort))
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("edge_tts_timeout = %d", cfg.EdgeTTS.Timeout))
	lines = append(lines, fmt.Sprintf("video_source = \"%s\"", cfg.VideoSource))
	lines = append(lines, "pexels_api_keys = []")
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("openai_image_base_url = \"%s\"", cfg.OpenAIImageBaseURL))
	imageKey := getFirstKey(cfg.OpenAIImageAPIKeys)
	lines = append(lines, fmt.Sprintf("openai_image_api_keys = [\"%s\"]", imageKey))
	lines = append(lines, fmt.Sprintf("openai_image_model = \"%s\"", cfg.OpenAIImageModel))
	lines = append(lines, fmt.Sprintf("openai_image_size = \"%s\"", cfg.OpenAIImageSize))
	lines = append(lines, fmt.Sprintf("openai_image_prompt_template = \"{keyword}\""))
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("llm_provider = \"%s\"", cfg.LLMProvider))
	lines = append(lines, fmt.Sprintf("subtitle_provider = \"%s\"", cfg.SubtitleProvider))
	lines = append(lines, fmt.Sprintf("bgm_type = \"%s\"", cfg.BGMType))
	lines = append(lines, fmt.Sprintf("bgm_volume = %.2f", cfg.BGMVolume))
	lines = append(lines, fmt.Sprintf("public_base_url = \"%s\"", cfg.PublicBaseURL))
	lines = append(lines, "")
	lines = append(lines, "[app]")
	lines = append(lines, fmt.Sprintf("api_key = \"%s\"", cfg.App.APIKey))
	lines = append(lines, fmt.Sprintf("tls_verify = %v", cfg.App.TLSVerify))
	lines = append(lines, "")
	lines = append(lines, "[openai]")
	lines = append(lines, fmt.Sprintf("api_key = \"%s\"", cfg.OpenAI.APIKey))
	lines = append(lines, fmt.Sprintf("base_url = \"%s\"", cfg.OpenAI.BaseURL))
	lines = append(lines, fmt.Sprintf("model_name = \"%s\"", cfg.OpenAI.ModelName))
	lines = append(lines, "")
	lines = append(lines, "[anthropic]")
	lines = append(lines, "api_key = \"\"")
	lines = append(lines, "base_url = \"\"")
	lines = append(lines, "model_name = \"\"")
	lines = append(lines, "")
	lines = append(lines, "[gemini]")
	lines = append(lines, "api_key = \"\"")
	lines = append(lines, "")
	lines = append(lines, "[moonshot]")
	lines = append(lines, "api_key = \"\"")
	lines = append(lines, "base_url = \"\"")
	lines = append(lines, "model_name = \"\"")
	lines = append(lines, "")
	lines = append(lines, "[whisper]")
	lines = append(lines, "model_path = \"\"")
	lines = append(lines, "use_gpu = false")
	lines = append(lines, "")

	os.WriteFile(configPath, []byte(strings.Join(lines, "\n")), 0644)
}

func maskKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "****" + key[len(key)-4:]
}

func getFirstKey(keys []string) string {
	if len(keys) > 0 {
		return keys[0]
	}
	return ""
}

// ==================== Digital Human Portrait ====================

func (s *Server) uploadDHPortrait(c *gin.Context) {
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no file uploaded"})
		return
	}

	portraitDir := filepath.Join(s.cfg.ResourceDir, "portraits")
	os.MkdirAll(portraitDir, 0755)

	// Save with original filename
	dst := filepath.Join(portraitDir, file.Filename)
	if err := c.SaveUploadedFile(file, dst); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  200,
		"message": "uploaded",
		"data": gin.H{
			"filename": file.Filename,
			"url":      "/api/v1/digital-human/portrait/" + file.Filename,
		},
	})
}

func (s *Server) serveDHPortrait(c *gin.Context) {
	filename := c.Param("filename")
	if strings.Contains(filename, "..") || strings.Contains(filename, "/") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid filename"})
		return
	}
	filePath := filepath.Join(s.cfg.ResourceDir, "portraits", filename)
	if _, err := os.Stat(filePath); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		return
	}
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".png":
		c.Header("Content-Type", "image/png")
	case ".jpg", ".jpeg":
		c.Header("Content-Type", "image/jpeg")
	case ".webp":
		c.Header("Content-Type", "image/webp")
	}
	c.File(filePath)
}

// ==================== Digital Human Portrait List (paginated) ====================

func (s *Server) listDHPortraits(c *gin.Context) {
	portraitDir := filepath.Join(s.cfg.ResourceDir, "portraits")
	os.MkdirAll(portraitDir, 0755)
	items := scanMaterialDir(portraitDir, "/api/v1/digital-human/portrait/", isImageFile)
	s.respondMaterials(c, items)
}

func (s *Server) deleteDHPortrait(c *gin.Context) {
	s.handleDelete(c, filepath.Join(s.cfg.ResourceDir, "portraits"))
}

// generateDHPortrait generates a digital human portrait via image API
func (s *Server) generateDHPortrait(c *gin.Context) {
	var req struct {
		Prompt string `json:"prompt"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	prompt := req.Prompt
	if prompt == "" {
		prompt = "professional portrait photo of a news anchor, studio lighting, upper body, looking at camera, clean background, high quality, photorealistic"
	}

	cfg := s.cfg
	if cfg.OpenAIImageBaseURL == "" || len(cfg.OpenAIImageAPIKeys) == 0 || cfg.OpenAIImageModel == "" {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "文生图模型未配置，请在LLM配置中设置"})
		return
	}

	baseURL := cfg.OpenAIImageBaseURL
	apiKey := cfg.OpenAIImageAPIKeys[0]
	model := cfg.OpenAIImageModel
	size := cfg.OpenAIImageSize
	if size == "" {
		size = "1280x720"
	}

	// Call image generation API
	url := strings.TrimSuffix(baseURL, "/") + "/images/generations"
	reqBody := map[string]interface{}{
		"model":           model,
		"prompt":          prompt,
		"n":               1,
		"size":            size,
		"response_format": "b64_json",
	}
	bodyJSON, _ := json.Marshal(reqBody)
	httpReq, _ := http.NewRequest("POST", url, strings.NewReader(string(bodyJSON)))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "请求文生图API失败: " + err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": fmt.Sprintf("文生图API返回 %d: %s", resp.StatusCode, string(body[:200]))})
		return
	}

	var result struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
			URL     string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil || len(result.Data) == 0 {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "解析文生图响应失败"})
		return
	}

	var imageData []byte
	if result.Data[0].B64JSON != "" {
		imageData, _ = base64.StdEncoding.DecodeString(result.Data[0].B64JSON)
	} else if result.Data[0].URL != "" {
		resp2, err := http.Get(result.Data[0].URL)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"ok": false, "error": "下载图片失败"})
			return
		}
		defer resp2.Body.Close()
		imageData, _ = io.ReadAll(resp2.Body)
	}

	if len(imageData) == 0 {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "图片数据为空"})
		return
	}

	// Save to portraits directory
	portraitDir := filepath.Join(cfg.ResourceDir, "portraits")
	os.MkdirAll(portraitDir, 0755)
	filename := fmt.Sprintf("portrait_%d.png", time.Now().UnixNano())
	dst := filepath.Join(portraitDir, filename)
	if err := os.WriteFile(dst, imageData, 0644); err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "保存图片失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":      true,
		"message": "头像生成成功",
		"data": gin.H{
			"filename": filename,
			"url":      "/api/v1/digital-human/portrait/" + filename,
			"size":      len(imageData),
		},
	})
}
