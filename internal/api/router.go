package api

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

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

		// 素材库 API
		v1.GET("/materials/bgm", s.listBGM)
		v1.POST("/materials/bgm/upload", s.uploadBGM)
		v1.DELETE("/materials/bgm/:filename", s.deleteBGM)
		v1.GET("/materials/bgm/file/:filename", s.serveBGMFile)

		v1.GET("/materials/images", s.listImages)
		v1.POST("/materials/images/upload", s.uploadImages)
		v1.DELETE("/materials/images/:filename", s.deleteImage)
		v1.GET("/materials/images/file/:filename", s.serveImageFile)

		v1.GET("/materials/videos", s.listVideos)
		v1.POST("/materials/videos/upload", s.uploadVideos)
		v1.DELETE("/materials/videos/:filename", s.deleteVideo)
		v1.GET("/materials/videos/file/:filename", s.serveVideoFile)
	}
}

func (s *Server) ping(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "samvideo"})
}

// createVideo 提交视频生成任务
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
	c.JSON(http.StatusOK, gin.H{
		"status":  200,
		"message": "success",
		"data":    template.BuiltinTemplates,
	})
}

func (s *Server) generateScript(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  200,
		"message": "success",
		"note":    "use POST /api/v1/videos with video_subject to generate full video",
	})
}

// listTasks 支持分页和状态筛选
func (s *Server) listTasks(c *gin.Context) {
	state := 0
	if s := c.Query("state"); s != "" {
		state, _ = strconv.Atoi(s)
	}
	page := 1
	if p := c.Query("page"); p != "" {
		page, _ = strconv.Atoi(p)
	}
	pageSize := 30
	if ps := c.Query("size"); ps != "" {
		pageSize, _ = strconv.Atoi(ps)
	}

	tasks, total, err := task.ListTasksFiltered(s.cfg, task.ListTasksOpts{
		State:    state,
		Page:     page,
		PageSize: pageSize,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	allTasks, _ := task.ListTasks(s.cfg)
	stateCounts := map[string]int{
		"all":        len(allTasks),
		"completed":  0,
		"processing": 0,
		"failed":     0,
	}
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
		"pagination": gin.H{
			"page":        page,
			"page_size":   pageSize,
			"total":       total,
			"total_pages": totalPages,
		},
		"counts": stateCounts,
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

// ==================== 素材库 API ====================

type MaterialItem struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"url"`
}

func (s *Server) listBGM(c *gin.Context) {
	bgmDir := filepath.Join(s.cfg.ResourceDir, "songs", "downloaded")
	os.MkdirAll(bgmDir, 0755)
	items := scanDir(bgmDir, isAudioFile, "/api/v1/materials/bgm/file/%s")
	c.JSON(http.StatusOK, gin.H{"status": 200, "message": "success", "data": items, "total": len(items)})
}

func (s *Server) uploadBGM(c *gin.Context) {
	bgmDir := filepath.Join(s.cfg.ResourceDir, "songs", "downloaded")
	os.MkdirAll(bgmDir, 0755)
	uploaded, err := saveUploads(c, bgmDir, "file", isAudioFile)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": 200, "message": "uploaded", "data": gin.H{"uploaded": uploaded, "count": len(uploaded)}})
}

func (s *Server) deleteBGM(c *gin.Context) {
	s.deleteMaterial(c, filepath.Join(s.cfg.ResourceDir, "songs", "downloaded"))
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

func (s *Server) listImages(c *gin.Context) {
	imgDir := filepath.Join(s.cfg.ResourceDir, "images")
	os.MkdirAll(imgDir, 0755)
	items := scanDir(imgDir, isImageFile, "/api/v1/materials/images/file/%s")
	c.JSON(http.StatusOK, gin.H{"status": 200, "message": "success", "data": items, "total": len(items)})
}

func (s *Server) uploadImages(c *gin.Context) {
	imgDir := filepath.Join(s.cfg.ResourceDir, "images")
	os.MkdirAll(imgDir, 0755)
	uploaded, err := saveUploads(c, imgDir, "files", isImageFile)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": 200, "message": "uploaded", "data": gin.H{"uploaded": uploaded, "count": len(uploaded)}})
}

func (s *Server) deleteImage(c *gin.Context) {
	s.deleteMaterial(c, filepath.Join(s.cfg.ResourceDir, "images"))
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

func (s *Server) listVideos(c *gin.Context) {
	vidDir := filepath.Join(s.cfg.ResourceDir, "videos")
	os.MkdirAll(vidDir, 0755)
	items := scanDir(vidDir, isVideoFile, "/api/v1/materials/videos/file/%s")
	c.JSON(http.StatusOK, gin.H{"status": 200, "message": "success", "data": items, "total": len(items)})
}

func (s *Server) uploadVideos(c *gin.Context) {
	vidDir := filepath.Join(s.cfg.ResourceDir, "videos")
	os.MkdirAll(vidDir, 0755)
	uploaded, err := saveUploads(c, vidDir, "files", isVideoFile)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": 200, "message": "uploaded", "data": gin.H{"uploaded": uploaded, "count": len(uploaded)}})
}

func (s *Server) deleteVideo(c *gin.Context) {
	s.deleteMaterial(c, filepath.Join(s.cfg.ResourceDir, "videos"))
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

// ==================== Helpers ====================

func scanDir(dir string, filter func(string) bool, urlTmpl string) []MaterialItem {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []MaterialItem{}
	}
	var items []MaterialItem
	for _, e := range entries {
		if e.IsDir() || !filter(e.Name()) {
			continue
		}
		info, _ := e.Info()
		items = append(items, MaterialItem{
			Name: e.Name(),
			Size: info.Size(),
			URL:  fmt.Sprintf(urlTmpl, e.Name()),
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items
}

func saveUploads(c *gin.Context, dir, field string, filter func(string) bool) ([]string, error) {
	form, err := c.MultipartForm()
	if err != nil {
		return nil, fmt.Errorf("no files uploaded")
	}
	files := form.File[field]
	if len(files) == 0 {
		// try "file" as fallback
		files = form.File["file"]
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no files found in field '%s' or 'file'", field)
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
	return uploaded, nil
}

func (s *Server) deleteMaterial(c *gin.Context, dir string) {
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

// avoid unused import
var _ = io.Copy
