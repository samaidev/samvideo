package api

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"samvideo/internal/config"
	"samvideo/internal/models"
	"samvideo/internal/task"

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

	// 设置默认值
	if params.VideoAspect == "" {
		params.VideoAspect = "16:9"
	}
	if params.VideoSource == "" {
		params.VideoSource = s.cfg.VideoSource
	}
	if params.VoiceName == "" {
		params.VoiceName = "zh-CN-YunxiNeural"
	}
	if params.VoiceRate == 0 {
		params.VoiceRate = 1.0
	}
	if params.VideoClipDuration == 0 {
		params.VideoClipDuration = 5
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

// generateScript 仅生成脚本
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

// listTasks 列出所有任务
func (s *Server) listTasks(c *gin.Context) {
	tasks, err := task.ListTasks(s.cfg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":  200,
		"message": "success",
		"data":    tasks,
	})
}

// getTask 查询任务状态
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

// deleteTask 删除任务
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

// downloadFile 下载文件
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

	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
	c.File(filePath)
}

// streamFile 流式播放
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
