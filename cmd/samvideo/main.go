package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"samvideo/internal/api"
	"samvideo/internal/config"
	"samvideo/internal/task"
	"samvideo/internal/utils"

	"github.com/gin-gonic/gin"
)

func main() {
	// 命令行参数
	configPath := flag.String("config", "config.toml", "path to config.toml")
	host := flag.String("host", "", "override listen host")
	port := flag.Int("port", 0, "override listen port")
	flag.Parse()

	// 加载配置
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	// 命令行覆盖
	if *host != "" {
		cfg.ListenHost = *host
	}
	if *port != 0 {
		cfg.ListenPort = *port
	}

	// 检查 FFmpeg
	if err := utils.CheckFFmpeg(); err != nil {
		fmt.Fprintf(os.Stderr, "ffmpeg not found: %v\n", err)
		os.Exit(1)
	}

	// 确保目录存在
	os.MkdirAll(filepath.Join(cfg.StorageDir, "tasks"), 0755)
	os.MkdirAll(filepath.Join(cfg.ResourceDir, "fonts"), 0755)
	os.MkdirAll(filepath.Join(cfg.ResourceDir, "songs"), 0755)

	// 清理上次未完成的任务（服务重启后 goroutine 已死）
	task.CleanupStaleTasks(cfg)

	// 启动信息
	utils.LogInfo("SamVideo - AI Video Generator")
	utils.LogInfo("config: %s", *configPath)
	utils.LogInfo("listen: %s:%d", cfg.ListenHost, cfg.ListenPort)
	utils.LogInfo("llm_provider: %s, model: %s", cfg.LLMProvider, cfg.OpenAI.ModelName)
	utils.LogInfo("video_source: %s", cfg.VideoSource)
	utils.LogInfo("subtitle_provider: %s", cfg.SubtitleProvider)
	utils.LogInfo("storage: %s", cfg.StorageDir)
	utils.LogInfo("web_ui: http://%s:%d/", cfg.ListenHost, cfg.ListenPort)
	utils.LogInfo("api_docs: http://%s:%d/ping", cfg.ListenHost, cfg.ListenPort)

	// 启动 REST API
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	server := api.NewServer(cfg)
	server.SetupRoutes(r)

	// 静态文件服务 — Web UI
	r.StaticFile("/", "./web/index.html")
	r.Static("/web", "./web")

	addr := fmt.Sprintf("%s:%d", cfg.ListenHost, cfg.ListenPort)
	if err := r.Run(addr); err != nil {
		utils.LogError("failed to start server: %v", err)
		os.Exit(1)
	}
}
