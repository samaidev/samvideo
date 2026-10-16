package material

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"samvideo/internal/config"
	"samvideo/internal/models"
	"samvideo/internal/utils"
)

var pngHeader = []byte{0x89, 0x50, 0x4E, 0x47} // PNG 魔数

// GenerateImagesFromPrompts 用具体的画面描述（image_prompt）生成图片并转为视频片段
// 每个 prompt 直接作为文生图的输入，不加随机风格后缀，确保图文一致
func GenerateImagesFromPrompts(prompts []string, taskDir string, cfg *config.Config, params *models.VideoParams) ([]string, error) {
	if len(prompts) == 0 {
		return nil, fmt.Errorf("no image prompts provided")
	}

	baseURL := cfg.OpenAIImageBaseURL
	apiKey := ""
	if len(cfg.OpenAIImageAPIKeys) > 0 {
		apiKey = cfg.OpenAIImageAPIKeys[0]
	}
	model := cfg.OpenAIImageModel
	size := cfg.OpenAIImageSize
	if size == "" {
		size = "1280x720"
	}

	if baseURL == "" || apiKey == "" || model == "" {
		return nil, fmt.Errorf("openai_image not configured: base_url=%s, api_key=%s, model=%s", baseURL, maskKey(apiKey), model)
	}

	var clipPaths []string

	for i, prompt := range prompts {
		utils.LogInfo("generating image %d/%d: prompt=%s", i+1, len(prompts), truncate(prompt, 80))

		var imageData []byte
		var genErr error
		for retry := 0; retry < 2; retry++ {
			imageData, genErr = generateOneImage(baseURL, apiKey, model, size, prompt)
			if genErr == nil && isValidPNG(imageData) {
				break
			}
			if genErr != nil {
				utils.LogWarn("image generation attempt %d failed: %v", retry+1, genErr)
			} else if !isValidPNG(imageData) {
				utils.LogWarn("image generation attempt %d: invalid PNG data (%d bytes), retrying", retry+1, len(imageData))
			}
			time.Sleep(2 * time.Second)
		}

		if genErr != nil || !isValidPNG(imageData) {
			utils.LogWarn("failed to generate valid image for prompt %d after retries, skipping", i+1)
			continue
		}

		imgPath := filepath.Join(taskDir, fmt.Sprintf("image_%03d.png", i))
		if err := os.WriteFile(imgPath, imageData, 0644); err != nil {
			utils.LogWarn("failed to save image %d: %v, skipping", i, err)
			continue
		}
		utils.LogInfo("image saved: %s (%d bytes)", imgPath, len(imageData))

		clipPath := filepath.Join(taskDir, fmt.Sprintf("clip_%03d.mp4", i))
		clipDuration := params.VideoClipDuration
		if clipDuration <= 0 {
			clipDuration = 5
		}
		if clipDuration > 15 {
			clipDuration = 15
		}

		variantIdx := i % 8
		if err := imageToVideoClip(imgPath, clipPath, clipDuration, params, variantIdx); err != nil {
			utils.LogWarn("failed to convert image to video clip: %v, skipping", err)
			continue
		}

		clipPaths = append(clipPaths, clipPath)
		utils.LogInfo("clip created: %s (effect variant %d)", clipPath, variantIdx)
	}

	if len(clipPaths) == 0 {
		return nil, fmt.Errorf("no clips generated successfully")
	}

	utils.LogInfo("generated %d clips from %d prompts", len(clipPaths), len(prompts))
	return clipPaths, nil
}

// GenerateImages 调用文生图 API 生成图片并转为视频片段（旧模式，用搜索词+风格后缀）
func GenerateImages(terms []string, taskDir string, cfg *config.Config, params *models.VideoParams) ([]string, error) {
	if len(terms) == 0 {
		return nil, fmt.Errorf("no search terms provided")
	}

	// 把搜索词转为带风格后缀的 prompt
	var prompts []string
	for i, term := range terms {
		prompts = append(prompts, buildDiversePrompt(term, i, len(terms)))
	}

	return GenerateImagesFromPrompts(prompts, taskDir, cfg, params)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func isValidPNG(data []byte) bool {
	if len(data) < 8 {
		return false
	}
	return bytes.HasPrefix(data, pngHeader)
}

func buildDiversePrompt(term string, index, total int) string {
	styles := []string{
		"photorealistic, professional photography, natural lighting, high detail",
		"cinematic shot, dramatic lighting, shallow depth of field, 4k",
		"aerial view, drone photography, wide angle, golden hour",
		"interior photography, clean composition, soft lighting, modern aesthetic",
		"abstract digital art, data visualization, futuristic, clean design",
		"documentary style, candid moment, natural colors, journalistic",
		"architectural photography, geometric composition, blue sky, sharp focus",
		"business concept, professional environment, corporate, clean background",
	}
	styleIdx := index % len(styles)
	return fmt.Sprintf("%s, %s", term, styles[styleIdx])
}

func generateOneImage(baseURL, apiKey, model, size, prompt string) ([]byte, error) {
	url := strings.TrimSuffix(baseURL, "/") + "/images/generations"
	reqBody := map[string]interface{}{
		"model":           model,
		"prompt":          prompt,
		"n":               1,
		"size":            size,
		"response_format": "b64_json",
	}
	bodyJSON, _ := json.Marshal(reqBody)
	req, err := http.NewRequest("POST", url, strings.NewReader(string(bodyJSON)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("image API returned %d: %s", resp.StatusCode, string(body))
	}
	var result struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
			URL     string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse image API response: %w", err)
	}
	if len(result.Data) == 0 {
		return nil, fmt.Errorf("image API returned no data")
	}
	if result.Data[0].B64JSON != "" {
		decoded, err := base64.StdEncoding.DecodeString(result.Data[0].B64JSON)
		if err != nil {
			return nil, fmt.Errorf("base64 decode failed: %w", err)
		}
		return decoded, nil
	}
	if result.Data[0].URL != "" {
		client2 := &http.Client{Timeout: 120 * time.Second}
		resp2, err := client2.Get(result.Data[0].URL)
		if err != nil {
			return nil, fmt.Errorf("download image: %w", err)
		}
		defer resp2.Body.Close()
		return io.ReadAll(resp2.Body)
	}
	return nil, fmt.Errorf("image API returned no image data")
}

func zoompanVariant(variantIdx, totalFrames, width, height int) string {
	variants := []func(int, int, int) string{
		func(tf, w, h int) string {
			return fmt.Sprintf("zoompan=z='min(zoom+0.001,1.1)':d=%d:s=%dx%d:fps=30", tf, w, h)
		},
		func(tf, w, h int) string {
			return fmt.Sprintf("zoompan=z='if(eq(on,0),1.1,max(1.1-0.001*on,1.0))':d=%d:s=%dx%d:fps=30", tf, w, h)
		},
		func(tf, w, h int) string {
			return fmt.Sprintf("zoompan=z='1.05':x='iw*on/(%d*30)':y='ih/2-(ih/zoom/2)':d=%d:s=%dx%d:fps=30", tf, tf, w, h)
		},
		func(tf, w, h int) string {
			return fmt.Sprintf("zoompan=z='1.05':x='iw-iw*on/(%d*30)':y='ih/2-(ih/zoom/2)':d=%d:s=%dx%d:fps=30", tf, tf, w, h)
		},
		func(tf, w, h int) string {
			return fmt.Sprintf("zoompan=z='1.05':x='iw/2-(iw/zoom/2)':y='ih*on/(%d*30)':d=%d:s=%dx%d:fps=30", tf, tf, w, h)
		},
		func(tf, w, h int) string {
			return fmt.Sprintf("zoompan=z='1.05':x='iw/2-(iw/zoom/2)':y='ih-ih*on/(%d*30)':d=%d:s=%dx%d:fps=30", tf, tf, w, h)
		},
		func(tf, w, h int) string {
			return fmt.Sprintf("zoompan=z='min(zoom+0.002,1.15)':d=%d:s=%dx%d:fps=30", tf, w, h)
		},
		func(tf, w, h int) string {
			return fmt.Sprintf("zoompan=z='if(eq(on,0),1.15,max(1.15-0.002*on,1.0))':d=%d:s=%dx%d:fps=30", tf, w, h)
		},
	}
	idx := variantIdx % len(variants)
	return variants[idx](totalFrames, width, height)
}

func imageToVideoClip(imgPath, outputPath string, duration int, params *models.VideoParams, variantIdx int) error {
	width, height := getResolution(params.VideoAspect)
	totalFrames := duration * 30
	zoompan := zoompanVariant(variantIdx, totalFrames, width, height)
	args := []string{
		"-y", "-loop", "1", "-i", imgPath,
		"-vf", zoompan,
		"-t", fmt.Sprintf("%d", duration),
		"-c:v", "libx264", "-preset", "fast", "-crf", "23",
		"-pix_fmt", "yuv420p",
		"-an",
		"-shortest",
		outputPath,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg image to video: %w, output: %s", err, string(out))
	}
	return nil
}

// GetLocalMaterials 从本地资源目录获取素材（图片+视频）
func GetLocalMaterials(resourceDir, taskDir string, count int) ([]string, error) {
	var clips []string

	videosDir := filepath.Join(resourceDir, "videos")
	if entries, err := os.ReadDir(videosDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if isVideoFile(name) {
				clips = append(clips, filepath.Join(videosDir, name))
			}
		}
	}

	imagesDir := filepath.Join(resourceDir, "images")
	if entries, err := os.ReadDir(imagesDir); err == nil {
		for i, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !isImageFile(name) {
				continue
			}
			imgPath := filepath.Join(imagesDir, name)
			clipPath := filepath.Join(taskDir, fmt.Sprintf("local_clip_%03d.mp4", i))
			if err := imageToVideoClip(imgPath, clipPath, 5, &models.VideoParams{VideoAspect: "16:9"}, i%8); err == nil {
				clips = append(clips, clipPath)
			}
		}
	}

	materialsDir := filepath.Join(resourceDir, "materials")
	if entries, err := os.ReadDir(materialsDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if isVideoFile(name) {
				clips = append(clips, filepath.Join(materialsDir, name))
			}
		}
	}

	if len(clips) == 0 {
		return nil, fmt.Errorf("no local materials found in %s/videos or %s/images", resourceDir, resourceDir)
	}

	if count > 0 && len(clips) > count {
		clips = clips[:count]
	}

	utils.LogInfo("found %d local materials (videos + images)", len(clips))
	return clips, nil
}

func getResolution(aspect string) (int, int) {
	switch aspect {
	case "9:16":
		return 1080, 1920
	case "1:1":
		return 1080, 1080
	default:
		return 1920, 1080
	}
}

func maskKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "****" + key[len(key)-4:]
}

func isVideoFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".mp4" || ext == ".avi" || ext == ".mov" || ext == ".mkv" || ext == ".webm" || ext == ".flv"
}

func isImageFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".gif" || ext == ".webp" || ext == ".bmp"
}
