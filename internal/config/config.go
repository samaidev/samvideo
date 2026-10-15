package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/BurntSushi/toml"
)

// Config 全局配置
type Config struct {
	LogLevel    string `toml:"log_level"`
	ListenHost  string `toml:"listen_host"`
	ListenPort  int    `toml:"listen_port"`

	App App `toml:"app"`

	EdgeTTS EdgeTTSConfig `toml:"edge_tts"`

	VideoSource         string `toml:"video_source"`
	PexelsAPIKeys       []string `toml:"pexels_api_keys"`

	OpenAIImageBaseURL      string   `toml:"openai_image_base_url"`
	OpenAIImageAPIKeys      []string `toml:"openai_image_api_keys"`
	OpenAIImageModel        string   `toml:"openai_image_model"`
	OpenAIImageSize         string   `toml:"openai_image_size"`
	OpenAIImagePromptTmpl   string   `toml:"openai_image_prompt_template"`

	LLMProvider string `toml:"llm_provider"`

	OpenAI        OpenAIConfig        `toml:"openai"`
	Anthropic     AnthropicConfig     `toml:"anthropic"`
	Gemini        GeminiConfig        `toml:"gemini"`
	Moonshot      MoonshotConfig      `toml:"moonshot"`

	SubtitleProvider string `toml:"subtitle_provider"`

	Whisper WhisperConfig `toml:"whisper"`

	BGMType   string  `toml:"bgm_type"`
	BGMVolume float64 `toml:"bgm_volume"`

	// 运行时字段
	WorkDir     string `toml:"-"`
	ResourceDir string `toml:"-"`
	StorageDir  string `toml:"-"`
}

type App struct {
	APIKey      string `toml:"api_key"`
	HideConfig  bool   `toml:"hide_config"`
	TLSVerify   bool   `toml:"tls_verify"`
}

type EdgeTTSConfig struct {
	Timeout int `toml:"timeout"`
}

type OpenAIConfig struct {
	APIKey    string `toml:"api_key"`
	BaseURL   string `toml:"base_url"`
	ModelName string `toml:"model_name"`
}

type AnthropicConfig struct {
	APIKey    string `toml:"api_key"`
	BaseURL   string `toml:"base_url"`
	ModelName string `toml:"model_name"`
}

type GeminiConfig struct {
	APIKey string `toml:"api_key"`
}

type MoonshotConfig struct {
	APIKey    string `toml:"api_key"`
	BaseURL   string `toml:"base_url"`
	ModelName string `toml:"model_name"`
}

type WhisperConfig struct {
	ModelPath string `toml:"model_path"`
	UseGPU    bool   `toml:"use_gpu"`
}

var (
	global *Config
	once   sync.Once
	mu     sync.RWMutex
)

// Load 加载配置文件
func Load(path string) (*Config, error) {
	c := defaultConfig()

	if _, err := toml.DecodeFile(path, c); err != nil {
		if os.IsNotExist(err) {
			// 配置文件不存在，用默认配置
			return c, nil
		}
		return nil, fmt.Errorf("decode config: %w", err)
	}

	// 设置运行时路径
	exe, _ := os.Executable()
	baseDir := filepath.Dir(exe)
	// 如果是 go run，用当前目录
	if wd, err := os.Getwd(); err == nil {
		baseDir = wd
	}

	c.WorkDir = baseDir
	c.ResourceDir = filepath.Join(baseDir, "resource")
	c.StorageDir = filepath.Join(baseDir, "storage")

	// 确保 storage 目录存在
	os.MkdirAll(c.StorageDir, 0755)
	os.MkdirAll(filepath.Join(c.StorageDir, "tasks"), 0755)

	mu.Lock()
	global = c
	mu.Unlock()

	return c, nil
}

// Get 获取全局配置
func Get() *Config {
	mu.RLock()
	defer mu.RUnlock()
	if global == nil {
		return defaultConfig()
	}
	return global
}

// Update 更新全局配置
func Update(c *Config) {
	mu.Lock()
	global = c
	mu.Unlock()
}

func defaultConfig() *Config {
	return &Config{
		LogLevel:   "INFO",
		ListenHost: "0.0.0.0",
		ListenPort: 8080,
		App: App{
			TLSVerify: true,
		},
		EdgeTTS: EdgeTTSConfig{
			Timeout: 30,
		},
		VideoSource:       "openai_image",
		SubtitleProvider:  "edge",
		BGMType:           "random",
		BGMVolume:         0.2,
		LLMProvider:       "openai",
		OpenAIImageSize:   "1280x720",
		OpenAIImagePromptTmpl: "{keyword}",
	}
}
