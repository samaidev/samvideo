package llm

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
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
	Text         string `json:"text"`          // 显示的文字
	Label        string `json:"label"`         // 数据标签（如"GDP增长"）
	Position     string `json:"position"`      // 保留兼容: top/center/bottom/top-left/top-right/bottom-left/bottom-right
	Color        string `json:"color"`         // 文字颜色: red/green/white/yellow/blue 或 #RRGGBB
	Style        string `json:"style"`         // 样式: title/dataline/tag/highlight/card
	// LLM 布局字段（坐标为画面百分比 0.0~1.0）
	X            *float64 `json:"x,omitempty"`            // 卡片左上角 X（画面宽度比例）
	Y            *float64 `json:"y,omitempty"`            // 卡片左上角 Y（画面高度比例）
	Width        *float64 `json:"width,omitempty"`        // 卡片宽度（画面宽度比例）
	Height       *float64 `json:"height,omitempty"`       // 卡片高度（画面高度比例）
	FontSize     *int     `json:"font_size,omitempty"`    // 字体大小（像素）
	BgColor      string   `json:"bg_color,omitempty"`     // 背景颜色 #RRGGBBAA
	BorderColor  string   `json:"border_color,omitempty"` // 边框颜色 #RRGGBBAA
	BorderRadius *int     `json:"border_radius,omitempty"` // 圆角半径（像素）
	Align        string   `json:"align,omitempty"`        // 文字对齐: left/center/right
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

【核心要求】
1. 每个分镜包含两个字段：narration（旁白）和 image_prompt（画面描述）
2. narration：中文旁白，口语化，适合语音朗读，每段约30-50字
3. image_prompt：英文画面描述，必须与旁白内容直接相关
4. image_prompt 要具体有画面感，描述旁白中提到的场景、人物、物体或概念
5. 所有分镜的旁白连起来是一段完整的解说
6. 不要包含任何标记符号（如#、*等）
7. image_prompt 只描述纯画面场景，不要在画面中包含任何文字、数字、标签、数据面板等UI元素

【示例】
主题"人工智能改变生活"的分镜1：
{
  "narration": "从清晨的闹钟到深夜的推荐算法，AI已经无处不在。",
  "image_prompt": "smart home morning scene, AI assistant on nightstand, warm sunlight, modern bedroom, photorealistic"
}

请以JSON格式返回：
{"scenes": [{"narration": "旁白文字", "image_prompt": "英文画面描述"}]}`, scriptPrompt, subject, sceneCount)

	// 注意：requirement 不传给脚本生成 LLM，留给后期制作步骤

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
// CallLLM 导出版本的 callLLM，供其他包调用
func CallLLM(prompt string, cfg *config.Config) (string, error) {
	return callLLM(prompt, cfg)
}

// CleanJSONResponse 导出版本，供其他包调用
func CleanJSONResponse(resp string) string {
	return cleanJSONResponse(resp)
}

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

	client := &http.Client{Timeout: 300 * time.Second}
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


// AutoGenerateOverlay 从旁白文本自动提取关键数据，生成结构化卡片 overlay
// 不再碎片化散落，而是生成一个完整的财经数据卡片（标题+数据行+标签）
func AutoGenerateOverlay(narration, requirement string) []OverlayItem {
	if narration == "" {
		return nil
	}
	return AutoGenerateOverlayLLM(narration, requirement, nil)
}

// AutoGenerateOverlayLLM 调用 LLM API 生成 overlay 布局
// LLM 决定每个元素的坐标(x,y,width,height)、字体大小、颜色、背景色、圆角等
func AutoGenerateOverlayLLM(narration, requirement string, cfg *config.Config) []OverlayItem {
	if narration == "" {
		return nil
	}
	if cfg == nil {
		return fallbackOverlay(narration)
	}

	// 截取前500字避免 prompt 过长
	scriptText := narration
	if len([]rune(scriptText)) > 500 {
		scriptText = string([]rune(scriptText)[:500])
	}

	prompt := fmt.Sprintf(`你是一个专业的财经视频画面设计师。请为以下旁白内容设计 overlay 叠加层布局。

画面尺寸：1920x1080 (16:9)
坐标系统：所有坐标和尺寸用画面比例 (0.0~1.0)，左上角为 (0,0)，右下角为 (1,1)

要求：
1. 设计一个或多个信息卡片，展示旁白中的关键数据、术语
2. 卡片位置不要互相覆盖，合理利用画面空间
3. 标题在卡片顶部，数据行在中间，标签在底部
4. 涨用红色(#FF4646)，跌用绿色(#3CC850)，中性用白色(#FFFFFF)，金额用黄色(#FFD23C)
5. 卡片背景用深色半透明(如 #0F192DCC)，圆角 12-16px
6. 字体大小：标题 36-42px，数据值 28-34px，标签 20-24px

请返回 JSON 数组，每个元素格式：
{
  "text": "显示的文字",
  "label": "数据标签（可选）",
  "style": "title|dataline|tag|highlight|card",
  "x": 0.05,           // 卡片左上角X（画面比例）
  "y": 0.65,           // 卡片左上角Y（画面比例）
  "width": 0.35,       // 卡片宽度（画面比例）
  "height": 0.25,      // 卡片高度（画面比例）
  "font_size": 36,      // 字体大小（像素）
  "color": "#FFFFFF",   // 文字颜色
  "bg_color": "#0F192DCC",  // 背景颜色
  "border_color": "#3C5078", // 边框颜色
  "border_radius": 14,     // 圆角半径
  "align": "left"          // 文字对齐: left/center/right
}

注意：多个元素如果属于同一个卡片，它们的 x/y/width/height 应该相同（作为卡片区域），文字会自动排列在卡片内。

旁白内容：
%s

只返回 JSON 数组，不要其他文字。`, scriptText)

	resp, err := callLLM(prompt, cfg)
	if err != nil {
		utils.LogWarn("LLM overlay generation failed: %v, using fallback", err)
		return fallbackOverlay(narration)
	}

	resp = cleanJSONResponse(resp)
	var items []OverlayItem
	if err := json.Unmarshal([]byte(resp), &items); err != nil {
		utils.LogWarn("LLM overlay JSON parse failed: %v, using fallback", err)
		return fallbackOverlay(narration)
	}

	utils.LogInfo("LLM generated %d overlay items", len(items))
	return items
}

// fallbackOverlay 当 LLM 不可用时的简单兜底
func fallbackOverlay(narration string) []OverlayItem {
	runes := []rune(narration)
	title := string(runes)
	if len(runes) > 18 {
		title = string(runes[:18])
	}
	title = strings.TrimRight(title, "。，！？、：；,.!?;:")

	// 简单提取百分比
	rePercent := regexp.MustCompile(`(\d+\.?\d*)\s*%%`)
	percentMatches := rePercent.FindAllStringSubmatch(narration, -1)

	items := []OverlayItem{{
		Text:     title,
		Position: "bottom-left",
		Color:    "white",
		Style:    "title",
	}}

	for _, m := range percentMatches {
		num := m[1]
		f, _ := strconv.ParseFloat(num, 64)
		color := "white"
		if f > 0 {
			color = "red"
		} else if f < 0 {
			color = "green"
		}
		items = append(items, OverlayItem{
			Text:     num + "%%",
			Label:    extractDataLabel(narration, num+"%%"),
			Position: "bottom-left",
			Color:    color,
			Style:    "dataline",
		})
	}

	return items
}

// extractDataLabel 从旁白中提取数字前后的关键词作为数据标签
func extractDataLabel(narration, dataStr string) string {
	idx := strings.Index(narration, dataStr)
	if idx < 0 {
		return ""
	}
	// 取数字前面2-6个字作为标签
	start := idx
	runeStart := len([]rune(narration[:start]))
	runes := []rune(narration)
	labelStart := runeStart - 8
	if labelStart < 0 {
		labelStart = 0
	}
	label := string(runes[labelStart:runeStart])
	// 去掉标点和空格
	label = strings.TrimSpace(label)
	label = strings.Trim(label, ".,!?;: 。！？、：；“”‘’「」【】()（）")
	// 如果太长，截取最后4个字
	if len([]rune(label)) > 6 {
		label = string([]rune(label)[len([]rune(label))-6:])
	}
	return label
}


// GenerateOverlayForScene 后期制作步骤：根据旁白和视频制作要求，为单个分镜生成 overlay 叠加层信息
// 这个函数在图片生成之后调用，overlay 作为独立后期层叠加到干净图片上
func GenerateOverlayForScene(narration, requirement string, sceneIndex int, cfg *config.Config) ([]OverlayItem, error) {
	if requirement == "" {
		return nil, nil
	}

	prompt := fmt.Sprintf(`你是一个视频后期制作师。现在有一段视频分镜，你需要为它设计叠加层（overlay）。

【分镜旁白】
%s

【视频制作要求】
%s

【你的任务】
根据旁白内容和制作要求，为这个分镜设计1-3个叠加层元素。叠加层是在画面上后期合成的半透明遮罩层、高亮文字、箭头标注等。

【overlay字段说明】
- text：要显示的中文文字（从旁白中提取关键数据、名称、标签等）
- position：位置，可选值：top/center/bottom/top-left/top-right/bottom-left/bottom-right
- color：颜色，可选值：red/green/white/yellow
- style：样式，可选值：box(半透明标签框)/highlight(高亮大字)/arrow(箭头标注)

【示例】
旁白"四季度开局，市场情绪略显谨慎，投资者都在问年底行情在哪里。"
要求"涨用红色跌用绿色标注"
overlay: [{"text":"四季度市场展望","position":"top","color":"yellow","style":"highlight"},{"text":"市场情绪谨慎","position":"bottom","color":"white","style":"box"}]

请以JSON格式返回：
{"overlay": [{"text": "...", "position": "...", "color": "...", "style": "..."}]}

注意：只返回JSON，不要其他文字。`, narration, requirement)

	resp, err := callLLM(prompt, cfg)
	if err != nil {
		return nil, fmt.Errorf("overlay LLM call failed: %w", err)
	}

	resp = cleanJSONResponse(resp)

	var result struct {
		Overlay []OverlayItem `json:"overlay"`
	}
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		utils.LogWarn("failed to parse overlay JSON for scene %d: %v, using auto fallback", sceneIndex+1, err)
		return AutoGenerateOverlayLLM(narration, requirement, cfg), nil
	}

	if len(result.Overlay) == 0 {
		utils.LogInfo("LLM returned empty overlay for scene %d, using auto fallback", sceneIndex+1)
		return AutoGenerateOverlayLLM(narration, requirement, cfg), nil
	}

	utils.LogInfo("LLM generated %d overlay items for scene %d (post-production)", len(result.Overlay), sceneIndex+1)
	return result.Overlay, nil
}


// GenerateImagePromptFromNarration 根据旁白内容提取关键词生成图片提示词
// 不额外调用 LLM，直接从文本提取数字、金融术语等
func GenerateImagePromptFromNarration(narration string) string {
	// 提取数字和百分比
	numbers := regexp.MustCompile(`\d+\.?\d*%?`).FindAllString(narration, 5)
	// 提取金融关键词
	keywords := []string{}
	financialTerms := []string{"A股", "港股", "美股", "GDP", "CPI", "PPI", "PMI", "央行", "利率", "降息", "加息",
		"通胀", "通缩", "牛市", "熊市", "涨停", "跌停", "新能源", "半导体", "芯片", "人工智能", "AI",
		"消费", "投资", "出口", "进口", "贸易", "房地产", "基建", "科技", "医药", "金融", "银行",
		"证券", "保险", "基金", "债券", "期货", "外汇", "汇率", "美元", "人民币", "欧元",
		"石油", "黄金", "铜", "铁矿石", "煤炭", "电力", "军工", "航天", "5G", "6G",
		"物联网", "云计算", "大数据", "区块链", "碳中和", "新能源车", "光伏", "风电", "储能",
		"白酒", "食品", "零售", "旅游", "航空", "物流", "钢铁", "化工", "建材", "机械",
		"汽车", "家电", "纺织", "农业", "养殖", "猪肉", "粮食", "大豆", "玉米",
		"科创板", "创业板", "北交所", "沪深", "上证", "深证", "恒生", "纳斯达克", "道琼斯",
		"M2", "社融", "财政", "税收", "专项债", "国债", "地方债", "赤字", "顺差", "逆差",
		"PMI", "规模以上", "工业增加值", "固定资产投资", "社会消费品零售", "城镇调查失业率",
		"高质量发展", "新质生产力", "专精特新", "数字经济", "平台经济", "共同富裕",
		"一带一路", "REITs", "ETF", "LOF", "IPO", "定增", "回购", "分红", "并购",
	}
	for _, term := range financialTerms {
		if strings.Contains(narration, term) {
			keywords = append(keywords, term)
		}
	}

	// 构建图片提示词
	prompt := "professional financial news background, "
	if len(keywords) > 0 {
		// 最多取5个关键词
		if len(keywords) > 5 {
			keywords = keywords[:5]
		}
		prompt += strings.Join(keywords, ", ") + ", "
	}
	if len(numbers) > 0 {
		// 最多取3个数字
		if len(numbers) > 3 {
			numbers = numbers[:3]
		}
		prompt += "data chart showing " + strings.Join(numbers, ", ") + ", "
	}
	prompt += "modern infographic style, cinematic lighting, 4K, high quality"

	return prompt
}

// SplitScriptToScenes 把长文章按段落切分成多个分镜，保留完整原文内容
// 视频总时长 = 所有分镜旁白的语音时长之和，自动跟随脚本长度
func SplitScriptToScenes(script, requirement string, cfg *config.Config) ([]Scene, error) {
	// 按段落切分，保留完整原文
	runes := []rune(script)
	totalLen := len(runes)
	
	// 目标：每段约200-300字，最多20个分镜
	segLen := 250
	segCount := (totalLen + segLen - 1) / segLen
	if segCount > 20 {
		segCount = 20
		segLen = totalLen / segCount
	}
	if segLen < 50 {
		segLen = 50
	}
	
	scenes := make([]Scene, 0, segCount)
	for i := 0; i < segCount && i*segLen < totalLen; i++ {
		start := i * segLen
		end := start + segLen
		if end > totalLen {
			end = totalLen
		}
		narration := string(runes[start:end])
		// 去掉 markdown 标记
		narration = strings.ReplaceAll(narration, "**", "")
		narration = strings.ReplaceAll(narration, "##", "")
		narration = strings.ReplaceAll(narration, "#", "")
		narration = strings.ReplaceAll(narration, "\n", " ")
		narration = strings.TrimSpace(narration)
		if len(narration) < 10 {
			continue
		}
		s := Scene{
			Narration:   narration,
			ImagePrompt: GenerateImagePromptFromNarration(narration),
		}
		s.Overlay = AutoGenerateOverlayLLM(narration, requirement, cfg)
		scenes = append(scenes, s)
	}
	
	if len(scenes) == 0 {
		return nil, fmt.Errorf("无法切分脚本")
	}
	
	utils.LogInfo("split script into %d scenes (total %d chars, ~%.0f min video)", len(scenes), totalLen, float64(totalLen)/4.0/60.0)
	return scenes, nil
}
