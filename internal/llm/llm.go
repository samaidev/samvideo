package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
	"strings"

	"samvideo/internal/config"
	"samvideo/internal/utils"
)

// Scene 分镜：一段旁白 + 对应的画面描述 + 叠加层信息
type Scene struct {
	Narration   string        `json:"narration"`    // 旁白文字（用于TTS）
	ImagePrompt string        `json:"image_prompt"` // 画面描述（用于文生图）
	Overlay     []OverlayItem `json:"overlay"`      // 叠加层（半透明遮罩/高亮文字/箭头）
}

// OverlayItem 叠加层元素
type OverlayItem struct {
	Text     string `json:"text"`      // 显示的文字
	Position string `json:"position"`  // 位置: top/center/bottom/top-left/top-right/bottom-left/bottom-right
	Color    string `json:"color"`     // 颜色: red/green/white/yellow
	Style    string `json:"style"`      // 样式: box(标签框)/highlight(高亮)/arrow(箭头)
}

// GenerateScriptWithScenes 生成分镜脚本：每段旁白对应一个画面
func GenerateScriptWithScenes(subject, scriptPrompt, requirement string, sceneCount int, cfg *config.Config) (string, []string, []Scene, error) {
	utils.LogInfo("generating scene-based script for subject: %s", subject)

	if scriptPrompt == "" {
		scriptPrompt = `你是一个专业的短视频脚本撰写人。`
	}
	if sceneCount <= 0 {
		sceneCount = 6
	}

	now := time.Now().Format("2006-01-02 15:04 Monday")
	prompt := fmt.Sprintf(`%s

当前时间：` + now + `

请根据主题"%s"创作一个%d个分镜的短视频脚本。

核心要求：
1. 每个分镜包含 narration（旁白）和 image_prompt（画面描述）
2. narration 是这个分镜的旁白文字，中文，口语化，适合语音朗读，每段约30-50字
3. image_prompt 是这个分镜画面的英文描述，必须与旁白内容直接相关
4. image_prompt 要具体有画面感，描述旁白中提到的场景、人物、物体或概念
5. 所有分镜的旁白连起来是一段完整的解说
6. 不要包含任何标记符号（如#、*等）

示例：
主题"人工智能改变生活"的分镜1：
  narration: "从清晨的闹钟到深夜的推荐算法，AI已经无处不在。"
  image_prompt: "smart home morning scene, AI assistant on nightstand, warm sunlight, modern bedroom, photorealistic"

请以JSON格式返回：
{"scenes": [{"narration": "旁白文字", "image_prompt": "英文画面描述", "overlay": [{"text": "显示文字", "position": "top/center/bottom/top-left/top-right/bottom-left/bottom-right", "color": "red/green/white/yellow", "style": "box/highlight/arrow"}]}]}`, scriptPrompt, subject, sceneCount)

	if requirement != "" {
		prompt += "\n\n额外要求：\n" + requirement
		prompt += "\n\n重要：你必须为每个分镜生成 overlay 数组，描述如何在画面上叠加半透明遮罩层、高亮文字、箭头等视觉元素来满足上述要求。overlay 的 text 用中文，描述要显示的关键数据或标签。如果某个分镜确实不需要叠加，overlay 返回空数组。"
	}

	resp, err := callLLM(prompt, cfg)
	if err != nil {
		return "", nil, nil, fmt.Errorf("LLM call failed: %w", err)
	}

	resp = cleanJSONResponse(resp)

	var result struct {
		Scenes []Scene `json:"scenes"`
	}

	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		utils.LogWarn("failed to parse LLM JSON response, falling back to legacy mode: %v", err)
		// 降级到旧模式
		script2, terms2, err2 := GenerateScriptWithPrompt(subject, scriptPrompt, sceneCount, cfg)
		return script2, terms2, nil, err2
	}

	if len(result.Scenes) == 0 {
		utils.LogWarn("no scenes in response, falling back to legacy mode")
		script2, terms2, err2 := GenerateScriptWithPrompt(subject, scriptPrompt, sceneCount, cfg)
		return script2, terms2, nil, err2
	}

	// 拼接完整脚本
	var narrations []string
	var imagePrompts []string
	for _, s := range result.Scenes {
		narrations = append(narrations, s.Narration)
		imagePrompts = append(imagePrompts, s.ImagePrompt)
	}
	fullScript := strings.Join(narrations, "\n")

	utils.LogInfo("scene-based script generated: %d scenes, %d chars total", len(result.Scenes), len(fullScript))
	return fullScript, imagePrompts, result.Scenes, nil
}

// GenerateScript 调用 LLM 生成视频脚本和搜索关键词（旧模式，向后兼容）
func GenerateScript(subject string, cfg *config.Config) (string, []string, error) {
	return GenerateScriptWithPrompt(subject, "", 8, cfg)
}

// GenerateScriptWithPrompt 使用自定义提示词生成脚本（旧模式）
func GenerateScriptWithPrompt(subject, scriptPrompt string, imageCount int, cfg *config.Config) (string, []string, error) {
	utils.LogInfo("generating script for subject: %s", subject)

	if scriptPrompt == "" {
		scriptPrompt = `你是一个专业的短视频脚本撰写人。当前时间：" + now + "\n\n请根据主题撰写一段约200字的短视频解说脚本。
要求：
1. 内容专业、信息密度高
2. 语言口语化，适合语音朗读
3. 不要包含任何标记符号（如#、*等）
4. 直接输出脚本文本，不要解释`
	}

	if imageCount <= 0 {
		imageCount = 8
	}

	prompt := fmt.Sprintf(`%s

主题："%s"

同时，请生成%d个英文关键词，用于搜索视频素材图片。要求：
1. 每个关键词必须与脚本内容直接相关，描述脚本中提到的具体场景
2. 关键词应具体、有画面感，例如"modern bank building exterior"而非仅"bank"
3. 涵盖脚本中提到的不同场景和概念
4. 适合文生图模型理解

请以JSON格式返回：
{"script": "脚本内容", "search_terms": ["关键词1", "关键词2", ...]}`, scriptPrompt, subject, imageCount)

	resp, err := callLLM(prompt, cfg)
	if err != nil {
		return "", nil, fmt.Errorf("LLM call failed: %w", err)
	}

	resp = cleanJSONResponse(resp)

	var result struct {
		Script      string   `json:"script"`
		SearchTerms []string `json:"search_terms"`
	}

	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		utils.LogWarn("failed to parse LLM JSON response, using raw text as script")
		return resp, generateDefaultTerms(subject), nil
	}

	if result.Script == "" {
		result.Script = resp
	}
	if len(result.SearchTerms) == 0 {
		result.SearchTerms = generateDefaultTerms(subject)
	}

	utils.LogInfo("script generated (%d chars), %d search terms", len(result.Script), len(result.SearchTerms))
	return result.Script, result.SearchTerms, nil
}

// GenerateTerms 仅生成搜索关键词
func GenerateTerms(script string, cfg *config.Config) ([]string, error) {
	prompt := fmt.Sprintf(`根据以下视频脚本，生成8个英文关键词，用于搜索视频素材图片。
要求：
1. 每个关键词必须与脚本内容直接相关
2. 关键词应具体、有画面感
3. 涵盖脚本中提到的不同场景和概念

脚本内容：
%s

请以JSON数组格式返回，如：["关键词1", "关键词2", ...]`, script)

	resp, err := callLLM(prompt, cfg)
	if err != nil {
		return nil, fmt.Errorf("LLM call failed: %w", err)
	}

	resp = cleanJSONResponse(resp)

	var terms []string
	if err := json.Unmarshal([]byte(resp), &terms); err != nil {
		for _, line := range strings.Split(resp, "\n") {
			line = strings.TrimSpace(line)
			line = strings.Trim(line, "0123456789.-) \"'")
			if line != "" && len(line) > 2 {
				terms = append(terms, line)
			}
		}
	}

	if len(terms) == 0 {
		terms = []string{"finance", "banking", "investment", "stock market", "economy", "financial district", "trading floor", "bank vault"}
	}

	return terms, nil
}

// callLLM 调用 OpenAI 兼容的 LLM API
func callLLM(prompt string, cfg *config.Config) (string, error) {
	baseURL := cfg.OpenAI.BaseURL
	apiKey := cfg.OpenAI.APIKey
	model := cfg.OpenAI.ModelName

	if baseURL == "" || apiKey == "" || model == "" {
		return "", fmt.Errorf("LLM not configured: base_url=%s, api_key=%s, model=%s", baseURL, maskKey(apiKey), model)
	}

	url := strings.TrimSuffix(baseURL, "/") + "/chat/completions"

	reqBody := map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"max_tokens":  2000,
		"temperature": 0.8,
	}

	bodyJSON, _ := json.Marshal(reqBody)

	req, err := http.NewRequest("POST", url, strings.NewReader(string(bodyJSON)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	utils.LogInfo("calling LLM: %s, model: %s", url, model)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("LLM API returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parse LLM response: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("LLM returned no choices")
	}

	return strings.TrimSpace(result.Choices[0].Message.Content), nil
}

// cleanJSONResponse 清理 LLM 返回的 JSON（去除 markdown 包裹）
func cleanJSONResponse(s string) string {
	s = strings.TrimSpace(s)
	// 去除 ```json 或 ``` 包裹
	if strings.HasPrefix(s, "```") {
		lines := strings.Split(s, "\n")
		var clean []string
		for _, line := range lines {
			if strings.HasPrefix(line, "```") {
				continue
			}
			clean = append(clean, line)
		}
		s = strings.Join(clean, "\n")
	}
	// 如果 JSON 被嵌套在文字中，尝试提取
	if !strings.HasPrefix(s, "{") {
		start := strings.Index(s, "{")
		end := strings.LastIndex(s, "}")
		if start >= 0 && end > start {
			s = s[start : end+1]
		}
	}
	return strings.TrimSpace(s)
}

// generateDefaultTerms 生成默认搜索关键词
func generateDefaultTerms(subject string) []string {
	return []string{
		"modern technology presentation",
		"business professional working",
		"data analysis dashboard screen",
		"city skyline modern architecture",
		"team collaboration meeting",
		"innovation technology concept",
		"digital transformation office",
		"professional presentation slide",
	}
}

// maskKey 隐藏 API Key 中间部分
func maskKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "****" + key[len(key)-4:]
}
