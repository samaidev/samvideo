package api

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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

	// 如果用户指定了模板 ID，直接使用
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

	// 设置默认值（仅当模板没有覆盖时）
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

	// 注意：如果用户没有指定 video_aspect，pipeline 会自动分类选择模板
	// 如果用户指定了 video_aspect，则跳过自动分类，使用用户指定的值

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

// listTemplates 列出所有可用模板
func (s *Server) listTemplates(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  200,
		"message": "success",
		"data":    template.BuiltinTemplates,
	})
}

func (s *Server) generateScript(c *gin.Context) {
	var req struct {
		VideoSubject string `json:"video_subject"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  200,
		"message": "success",
		"note":    "use POST /api/v1/videos with video_subject to generate full video",
	})
}

// listTasks 支持分页和状态筛选
// GET /api/v1/tasks                          — 全部任务
// GET /api/v1/tasks?state=1                  — 只看完成
// GET /api/v1/tasks?state=4&page=1&size=30   — 处理中，第1页，每页30条
func (s *Server) listTasks(c *gin.Context) {
	state := 0 // 0 = 全部
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

	// 计算各状态计数
	allTasks, _ := task.ListTasks(s.cfg)
	stateCounts := map[string]int{
		"all":       len(allTasks),
		"completed": 0,
		"processing": 0,
		"failed":    0,
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
	c.JSON(http.StatusOK, gin.H{
		"status":  200,
		"message": "success",
		"data":    result,
	})
}

func (s *Server) deleteTask(c *gin.Context) {
	taskID := c.Param("task_id")
	if err := task.DeleteTask(s.cfg, taskID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":  200,
		"message": "deleted",
	})
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

	// 缩略图直接返回图片
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
