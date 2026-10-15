package material

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"samvideo/internal/config"
	"samvideo/internal/models"
	"samvideo/internal/utils"
)

// zoompanVariants 不同的 zoompan 效果变体
// 每个函数返回完整的 zoompan 滤镜字符串
func zoompanVariant(idx, totalFrames, width, height int) string {
	variants := []func(int, int, int) string{
		// 0: 缓慢放大
		func(tf, w, h int) string {
			return fmt.Sprintf("zoompan=z='min(zoom+0.001,1.1)':d=%d:s=%dx%d:fps=30", tf, w, h)
		},
		// 1: 缓慢缩小
		func(tf, w, h int) string {
			return fmt.Sprintf("zoompan=z='if(eq(on,0),1.1,max(1.1-0.001*on,1.0))':d=%d:s=%dx%d:fps=30", tf, w, h)
		},
		// 2: 从左向右平移
		func(tf, w, h int) string {
			return fmt.Sprintf("zoompan=z=1.0:x='iw/2-(iw/zoom/2)+((iw-iw/zoom)*on/%d)':y='ih/2-(ih/zoom/2)':d=%d:s=%dx%d:fps=30", tf, tf, w, h)
		},
		// 3: 从右向左平移
		func(tf, w, h int) string {
			return fmt.Sprintf("zoompan=z=1.0:x='iw/2-(iw/zoom/2)-((iw-iw/zoom)*on/%d)':y='ih/2-(ih/zoom/2)':d=%d:s=%dx%d:fps=30", tf, tf, w, h)
		},
		// 4: 从上向下平移
		func(tf, w, h int) string {
			return fmt.Sprintf("zoompan=z=1.0:x='iw/2-(iw/zoom/2)':y='ih/2-(ih/zoom/2)+((ih-ih/zoom)*on/%d)':d=%d:s=%dx%d:fps=30", tf, tf, w, h)
		},
		// 5: 从下向上平移
		func(tf, w, h int) string {
			return fmt.Sprintf("zoompan=z=1.0:x='iw/2-(iw/zoom/2)':y='ih/2-(ih/zoom/2)-((ih-ih/zoom)*on/%d)':d=%d:s=%dx%d:fps=30", tf, tf, w, h)
		},
		// 6: 缓慢放大 + 右移
		func(tf, w, h int) string {
			return fmt.Sprintf("zoompan=z='min(zoom+0.0008,1.08)':x='iw/2-(iw/zoom/2)+((iw-iw/zoom)*on/%d)*0.3':y='ih/2-(ih/zoom/2)':d=%d:s=%dx%d:fps=30", tf, tf, w, h)
		},
		// 7: 缓慢放大 + 左移
		func(tf, w, h int) string {
			return fmt.Sprintf("zoompan=z='min(zoom+0.0008,1.08)':x='iw/2-(iw/zoom/2)-((iw-iw/zoom)*on/%d)*0.3':y='ih/2-(ih/zoom/2)':d=%d:s=%dx%d:fps=30", tf, tf, w, h)
		},
	}
	return variants[idx%len(variants)](totalFrames, width, height)
}

// GenerateImages 调用 OpenAI 兼容的文生图 API 生成图片，然后转为视频片段
func GenerateImages(terms []string, taskDir string, cfg *config.Config, params *models.VideoParams) ([]string, error) {
	if len(terms) == 0 {
		return nil, fmt.Errorf("no search terms for image generation")
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

	for i, term := range terms {
		utils.LogInfo("generating image %d/%d: term=%s, model=%s, size=%s", i+1, len(terms), term, model, size)

		// 构建更丰富的 prompt，让每张图片风格不同
		var prompt string
		promptTmpl := cfg.OpenAIImagePromptTmpl
		if promptTmpl == "" || promptTmpl == "{keyword}" {
			prompt = buildDiversePrompt(term, i, len(terms))
		} else {
			prompt = strings.ReplaceAll(promptTmpl, "{keyword}", term)
		}

		imageData, err := generateOneImage(baseURL, apiKey, model, size, prompt)
		if err != nil {
			utils.LogWarn("failed to generate image for term %s: %v, skipping", term, err)
			continue
		}

		imgPath := filepath.Join(taskDir, fmt.Sprintf("image_%03d.png", i))
		if err := os.WriteFile(imgPath, imageData, 0644); err != nil {
			utils.LogWarn("failed to save image %s: %v, skipping", term, err)
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

		// 每个片段使用不同的 zoompan 效果
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

	utils.LogInfo("generated %d clips from %d terms", len(clipPaths), len(terms))
	return clipPaths, nil
}

// buildDiversePrompt 为每个关键词构建不同风格的 prompt，避免生成相似图片
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

// generateOneImage 调用文生图 API 生成一张图片
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

	resp, err := http.DefaultClient.Do(req)
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
		return base64.StdEncoding.DecodeString(result.Data[0].B64JSON)
	}

	if result.Data[0].URL != "" {
		resp, err := http.Get(result.Data[0].URL)
		if err != nil {
			return nil, fmt.Errorf("download image: %w", err)
		}
		defer resp.Body.Close()
		return io.ReadAll(resp.Body)
	}

	return nil, fmt.Errorf("image API returned no image data")
}

// imageToVideoClip 将图片转为带不同 zoompan 效果的视频片段
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

	cmd := exec.Command("ffmpeg", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg image to video: %w, output: %s", err, string(out))
	}

	return nil
}

// GetLocalMaterials 从本地资源目录获取素材
func GetLocalMaterials(resourceDir, taskDir string, count int) ([]string, error) {
	materialsDir := filepath.Join(resourceDir, "materials")
	entries, err := os.ReadDir(materialsDir)
	if err != nil {
		return nil, fmt.Errorf("no local materials directory: %w", err)
	}

	var clips []string
	for i, entry := range entries {
		if i >= count {
			break
		}
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".mp4") || strings.HasSuffix(name, ".avi") || strings.HasSuffix(name, ".mov") {
			clips = append(clips, filepath.Join(materialsDir, name))
		}
	}

	if len(clips) == 0 {
		return nil, fmt.Errorf("no local video materials found in %s", materialsDir)
	}

	return clips, nil
}

// getResolution 根据宽高比获取分辨率
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

// maskKey 隐藏 API Key
func maskKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "****" + key[len(key)-4:]
}
