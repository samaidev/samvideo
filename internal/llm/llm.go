package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"samvideo/internal/config"
	"samvideo/internal/utils"
)

// GenerateScript 调用 LLM 生成视频脚本和搜索关键词
func GenerateScript(subject string, cfg *config.Config) (string, []string, error) {
	utils.LogInfo("generating script for subject: %s", subject)

	prompt := fmt.Sprintf(`你是一个专业的短视频脚本撰写人。请根据主题"%s"撰写一段约200字的短视频解说脚本。
要求：
1. 内容专业、信息密度高
2. 语言口语化，适合语音朗读
3. 不要包含任何标记符号（如#、*等）
4. 直接输出脚本文本，不要解释

同时，请生成5个英文关键词，用于搜索视频素材图片。每个关键词应该与脚本内容相关。

请以JSON格式返回：
{"script": "脚本内容", "search_terms": ["关键词1", "关键词2", "关键词3", "关键词4", "关键词5"]}`, subject)

	resp, err := callLLM(prompt, cfg)
	if err != nil {
		return "", nil, fmt.Errorf("LLM call failed: %w", err)
	}

	// 尝试解析 JSON 响应
	resp = cleanJSONResponse(resp)

	var result struct {
		Script      string   `json:"script"`
		SearchTerms []string `json:"search_terms"`
	}

	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		// JSON 解析失败，把整个响应当作脚本
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
	prompt := fmt.Sprintf(`根据以下视频脚本，生成5个英文关键词，用于搜索视频素材图片。
每行一个关键词，不要编号，不要解释。

脚本内容：
%s`, script)

	resp, err := callLLM(prompt, cfg)
	if err != nil {
		return nil, fmt.Errorf("LLM call failed: %w", err)
	}

	terms := []string{}
	for _, line := range strings.Split(resp, "\n") {
		line = strings.TrimSpace(line)
		line = strings.Trim(line, "0123456789.-) ")
		if line != "" && len(line) > 2 {
			terms = append(terms, line)
		}
	}

	if len(terms) == 0 {
		terms = []string{"finance", "banking", "investment", "stock market", "economy"}
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
		"temperature": 0.7,
	}

	bodyJSON, _ := json.Marshal(reqBody)

	req, err := http.NewRequest("POST", url, strings.NewReader(string(bodyJSON)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	utils.LogInfo("calling LLM: %s, model: %s", url, model)

	resp, err := http.DefaultClient.Do(req)
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
	// 去除 ```json ... ``` 包裹
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
	return strings.TrimSpace(s)
}

// generateDefaultTerms 生成默认搜索关键词
func generateDefaultTerms(subject string) []string {
	return []string{"finance", "banking", "investment", "stock market", "economy"}
}

// maskKey 隐藏 API Key 中间部分
func maskKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "****" + key[len(key)-4:]
}
