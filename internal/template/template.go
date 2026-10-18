package template

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"samvideo/internal/config"
	"samvideo/internal/models"
	"samvideo/internal/utils"
)

// Template 视频模板
type Template struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	Description      string  `json:"description"`
	Category         string  `json:"category"`
	Aspect           string  `json:"aspect"`
	ClipDuration     int     `json:"clip_duration"`
	ClipSpeed        float64 `json:"clip_speed"`
	TransitionMode   string  `json:"transition_mode"`
	BGMType          string  `json:"bgm_type"`
	BGMVolume        float64 `json:"bgm_volume"`
	SubtitleEnabled  bool    `json:"subtitle_enabled"`
	VoiceName        string  `json:"voice_name"`
	ScriptPrompt     string  `json:"script_prompt"`
	ImagePromptStyle string  `json:"image_prompt_style"`
	ImageCount       int     `json:"image_count"`
	VideoRequirement string  `json:"video_requirement"`
}

// BuiltinTemplates 内置模板列表
var BuiltinTemplates = []Template{
	{
		ID:               "tech_app",
		Name:             "应用展示",
		Description:      "适合代码、截图、运行效果图等技术应用类内容",
		Category:         "tech",
		Aspect:           "16:9",
		ClipDuration:     4,
		ClipSpeed:        1.2,
		TransitionMode:   "shuffle",
		BGMType:          "random",
		BGMVolume:        0.10,
		SubtitleEnabled:  true,
		VoiceName:        "zh-CN-YunxiNeural",
		ScriptPrompt: `你是一个技术博主。请根据主题写一段约200字的短视频解说脚本。
要求：
1. 语言简洁有力，像在演示一个技术产品
2. 突出功能亮点和使用场景
3. 适合配合代码截图和运行效果图展示
4. 口语化，节奏明快`,
		ImagePromptStyle: "clean UI screenshot style, modern interface design, professional tech presentation",
		ImageCount:       8,
		VideoRequirement: `关键页面涉及产品名称、版本号、功能数据时，用局部半透明遮罩层高亮显示关键数据和功能图标，配以简洁标注线指向具体功能区域`,
	},
	{
		ID:               "tech_app_vertical",
		Name:             "应用展示(竖屏)",
		Description:      "适合手机端展示代码、截图、运行效果图",
		Category:         "tech",
		Aspect:           "9:16",
		ClipDuration:     4,
		ClipSpeed:        1.2,
		TransitionMode:   "shuffle",
		BGMType:          "random",
		BGMVolume:        0.10,
		SubtitleEnabled:  true,
		VoiceName:        "zh-CN-YunxiNeural",
		ScriptPrompt: `你是一个技术博主。请根据主题写一段约200字的短视频解说脚本。
要求：
1. 语言简洁有力，像在演示一个技术产品
2. 突出功能亮点和使用场景
3. 适合配合代码截图和运行效果图展示
4. 口语化，节奏明快`,
		ImagePromptStyle: "mobile app screenshot style, clean interface, modern UI design",
		ImageCount:       8,
		VideoRequirement: `手机端展示时，关键界面涉及用户数据、通知内容、应用名称时，用局部半透明遮罩层突出显示关键数字和图标，确保竖屏下信息清晰可读`,
	},
	{
		ID:               "motivational",
		Name:             "励志鸡汤",
		Description:      "适合鸡汤、励志、人生感悟类内容，图片切换为主",
		Category:         "motivational",
		Aspect:           "9:16",
		ClipDuration:     6,
		ClipSpeed:        0.8,
		TransitionMode:   "fade_in",
		BGMType:          "random",
		BGMVolume:        0.10,
		SubtitleEnabled:  true,
		VoiceName:        "zh-CN-YunyangNeural",
		ScriptPrompt: `你是一个励志演讲者。请根据主题写一段约200字的短视频解说脚本。
要求：
1. 语言温暖有力，富有感染力
2. 使用排比、比喻等修辞手法
3. 适合配合风景、人物、意境图片展示
4. 节奏舒缓，有停顿感`,
		ImagePromptStyle: "cinematic landscape, golden hour photography, emotional and inspiring atmosphere",
		ImageCount:       6,
		VideoRequirement: `关键画面涉及励志金句、人生感悟时，用半透明深色遮罩层叠加白色大字显示核心语句，配合金色或暖色调高亮关键词`,
	},
	{
		ID:               "motivational_landscape",
		Name:             "励志鸡汤(横屏)",
		Description:      "适合鸡汤、励志类内容，横屏宽幅展示",
		Category:         "motivational",
		Aspect:           "16:9",
		ClipDuration:     6,
		ClipSpeed:        0.8,
		TransitionMode:   "fade_in",
		BGMType:          "random",
		BGMVolume:        0.10,
		SubtitleEnabled:  true,
		VoiceName:        "zh-CN-YunyangNeural",
		ScriptPrompt: `你是一个励志演讲者。请根据主题写一段约200字的短视频解说脚本。
要求：
1. 语言温暖有力，富有感染力
2. 使用排比、比喻等修辞手法
3. 适合配合风景、人物、意境图片展示
4. 节奏舒缓，有停顿感`,
		ImagePromptStyle: "wide cinematic landscape, breathtaking scenery, emotional photography",
		ImageCount:       6,
		VideoRequirement: `横屏风景画面中，关键励志语句用半透明遮罩层叠加显示，文字居中，关键词用对比色高亮，确保文字与风景画面层次分明`,
	},
	{
		ID:               "academic",
		Name:             "学术科普",
		Description:      "适合学术讲解、知识科普、数据分析类内容",
		Category:         "academic",
		Aspect:           "16:9",
		ClipDuration:     5,
		ClipSpeed:        1.0,
		TransitionMode:   "shuffle",
		BGMType:          "random",
		BGMVolume:        0.10,
		SubtitleEnabled:  true,
		VoiceName:        "zh-CN-YunxiNeural",
		ScriptPrompt: `你是一个学术科普作者。请根据主题写一段约200字的短视频解说脚本。
要求：
1. 内容严谨准确，信息密度高
2. 用通俗易懂的语言解释专业概念
3. 适合配合图表、数据可视化、示意图展示
4. 逻辑清晰，层次分明`,
		ImagePromptStyle: "infographic style, data visualization, clean academic presentation, charts and diagrams",
		ImageCount:       8,
		VideoRequirement: `关键页面涉及研究数据、统计数字、公式定理时，用局部半透明遮罩层高亮显示关键数据和图表标注，配以箭头指向具体数值，确保学术信息准确传达`,
	},
	{
		ID:               "news",
		Name:             "资讯播报",
		Description:      "适合新闻资讯、行业动态、财经分析类内容",
		Category:         "news",
		Aspect:           "16:9",
		ClipDuration:     4,
		ClipSpeed:        1.3,
		TransitionMode:   "shuffle",
		BGMType:          "random",
		BGMVolume:        0.10,
		SubtitleEnabled:  true,
		VoiceName:        "zh-CN-YunxiNeural",
		ScriptPrompt: `你是一个财经新闻主播。请根据主题写一段约200字的短视频解说脚本。
要求：
1. 语言专业客观，信息密度高
2. 突出关键数据和趋势分析
3. 适合配合建筑、场景、人物、图表展示
4. 节奏紧凑，有新闻感`,
		ImagePromptStyle: "professional news photography, financial district, business environment, documentary style",
		ImageCount:       8,
		VideoRequirement: `关键画面涉及新闻数据、股价指数、地名地址、人名职务时，用局部半透明遮罩层显示关键信息，数据用大号字体突出，地名和人名配以图标标注`,
	},
	{
		ID:               "marketing",
		Name:             "营销推广",
		Description:      "适合产品推广、品牌宣传、活动营销类内容",
		Category:         "marketing",
		Aspect:           "9:16",
		ClipDuration:     3,
		ClipSpeed:        1.5,
		TransitionMode:   "shuffle",
		BGMType:          "random",
		BGMVolume:        0.10,
		SubtitleEnabled:  true,
		VoiceName:        "zh-CN-YunyangNeural",
		ScriptPrompt: `你是一个营销文案专家。请根据主题写一段约200字的短视频解说脚本。
要求：
1. 语言有冲击力，吸引眼球
2. 突出卖点和用户价值
3. 适合配合产品图、场景图、人物图展示
4. 节奏快，有紧迫感`,
		ImagePromptStyle: "commercial product photography, vibrant colors, marketing campaign style, eye-catching",
		ImageCount:       8,
		VideoRequirement: `关键页面涉及产品价格、折扣力度、品牌名称、活动时间时，用局部半透明遮罩层高亮显示价格和优惠信息，配以醒目图标和倒计时样式`,
	},
	{
		ID:               "story",
		Name:             "故事叙述",
		Description:      "适合故事讲述、案例分享、情感叙事类内容",
		Category:         "story",
		Aspect:           "16:9",
		ClipDuration:     7,
		ClipSpeed:        0.9,
		TransitionMode:   "fade_in",
		BGMType:          "random",
		BGMVolume:        0.10,
		SubtitleEnabled:  true,
		VoiceName:        "zh-CN-YunyangNeural",
		ScriptPrompt: `你是一个故事讲述者。请根据主题写一段约200字的短视频解说脚本。
要求：
1. 语言有画面感，像在讲故事
2. 有起承转合，有悬念和转折
3. 适合配合场景图、人物图、意境图展示
4. 节奏舒缓，有代入感`,
		ImagePromptStyle: "cinematic storytelling, dramatic lighting, film still, narrative photography",
		ImageCount:       6,
		VideoRequirement: `关键画面涉及人物名称、地点场景、时间线索时，用局部半透明遮罩层显示关键叙事信息，人物名配头像图标，地点配定位图标，时间配时钟图标`,
	},
	{
		ID:               "code_project",
		Name:             "代码项目介绍",
		Description:      "适合开源项目、技术架构、代码展示类内容",
		Category:         "tech",
		Aspect:           "16:9",
		ClipDuration:     5,
		ClipSpeed:        1.0,
		TransitionMode:   "shuffle",
		BGMType:          "random",
		BGMVolume:        0.10,
		SubtitleEnabled:  true,
		VoiceName:        "zh-CN-YunxiNeural",
		ScriptPrompt:     `你是一个技术布道者。请根据主题写一段约200字的短视频解说脚本。
要求：
1. 语言专业但不枯燥，像在向开发者介绍一个项目
2. 突出技术亮点、架构设计和核心功能
3. 适合配合代码截图、架构图、终端演示展示
4. 节奏明快，有技术感`, 
		ImagePromptStyle: "clean code editor screenshot, terminal output, architecture diagram, modern IDE interface, dark theme, syntax highlighting",
		ImageCount:       8,
		VideoRequirement: `关键页面涉及项目名称、GitHub星数、版本号、技术栈名称时，用局部半透明遮罩层高亮显示关键数据和技术标签，代码片段用深色背景框突出显示，配以语言图标标注`,
	},
	{
		ID:               "finance_news",
		Name:             "新闻财经",
		Description:      "适合财经分析、股市行情、经济政策解读类内容",
		Category:         "news",
		Aspect:           "16:9",
		ClipDuration:     4,
		ClipSpeed:        1.3,
		TransitionMode:   "shuffle",
		BGMType:          "random",
		BGMVolume:        0.10,
		SubtitleEnabled:  true,
		VoiceName:        "zh-CN-YunxiNeural",
		ScriptPrompt:     `你是一个资深财经分析师。请根据主题写一段约200字的短视频解说脚本。
要求：
1. 语言专业客观，数据准确
2. 突出关键经济指标、政策影响和市场趋势
3. 适合配合K线图、数据面板、新闻截图展示
4. 节奏紧凑，有权威感`, 
		ImagePromptStyle: "professional financial news studio, stock market charts, economic data dashboard, business news photography",
		ImageCount:       8,
		VideoRequirement: `关键画面涉及股价数据、涨跌幅、经济指标、政策名称时，用局部半透明遮罩层高亮显示关键数字，涨用红色跌用绿色标注，数据配以趋势箭头图标，政策名称用醒目标签框`,
	},
}

// ClassifyTopic 用 LLM 对用户主题进行分类，返回最佳模板
func ClassifyTopic(subject string, cfg *config.Config) (*Template, error) {
	utils.LogInfo("classifying topic: %s", subject)

	prompt := fmt.Sprintf(`请分析以下视频主题，从这些类别中选择最合适的一个：
- tech: 技术应用、代码、软件、工具、产品演示
- motivational: 励志、鸡汤、人生感悟、正能量、情感
- academic: 学术、科普、知识、教育、数据分析
- news: 新闻、资讯、财经、行业动态、时事
- marketing: 营销、推广、广告、品牌、活动
- story: 故事、案例、叙事、情感故事

主题："%s"

请只返回一个JSON：{"category": "类别名", "reason": "简短原因", "aspect": "16:9或9:16"}
- 如果是手机端/短视频平台内容，aspect 选 9:16
- 如果是演示/讲解/宽幅内容，aspect 选 16:9`, subject)

	resp, err := callLLM(prompt, cfg)
	if err != nil {
		utils.LogWarn("LLM classification failed: %v, using default template", err)
		return GetDefaultTemplate(), nil
	}

	resp = cleanJSONResponse(resp)

	var result struct {
		Category string `json:"category"`
		Reason   string `json:"reason"`
		Aspect   string `json:"aspect"`
	}

	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		utils.LogWarn("failed to parse classification response: %v, using default", err)
		return GetDefaultTemplate(), nil
	}

	utils.LogInfo("classification result: category=%s, aspect=%s, reason=%s", result.Category, result.Aspect, result.Reason)

	tmpl := SelectTemplate(result.Category, result.Aspect)
	return tmpl, nil
}

// SelectTemplate 根据类别和宽高比选择模板
func SelectTemplate(category, aspect string) *Template {
	category = strings.ToLower(strings.TrimSpace(category))
	aspect = strings.TrimSpace(aspect)
	if aspect == "" {
		aspect = "16:9"
	}

	for i := range BuiltinTemplates {
		if BuiltinTemplates[i].Category == category && BuiltinTemplates[i].Aspect == aspect {
			return &BuiltinTemplates[i]
		}
	}

	for i := range BuiltinTemplates {
		if BuiltinTemplates[i].Category == category {
			return &BuiltinTemplates[i]
		}
	}

	return GetDefaultTemplate()
}

// GetDefaultTemplate 返回默认模板
// GetTemplate 按 ID 查找模板
func GetTemplate(id string) *Template {
	for i := range BuiltinTemplates {
		if BuiltinTemplates[i].ID == id {
			return &BuiltinTemplates[i]
		}
	}
	return nil
}

func GetDefaultTemplate() *Template {
	return &BuiltinTemplates[4] // academic 16:9
}

// ApplyTemplate 将模板参数应用到 VideoParams
func ApplyTemplate(tmpl *Template, params *models.VideoParams) {
	if tmpl.Aspect != "" {
		params.VideoAspect = tmpl.Aspect
	}
	if tmpl.ClipDuration > 0 {
		params.VideoClipDuration = tmpl.ClipDuration
	}
	if tmpl.ClipSpeed > 0 {
		params.VideoClipSpeed = tmpl.ClipSpeed
	}
	if tmpl.TransitionMode != "" {
		params.VideoTransitionMode = tmpl.TransitionMode
	}
	if tmpl.BGMType != "" {
		params.BGMType = tmpl.BGMType
	}
	if tmpl.BGMVolume > 0 {
		params.BGMVolume = tmpl.BGMVolume
	}
	if tmpl.VideoRequirement != "" {
		params.VideoRequirement = tmpl.VideoRequirement
	}
	params.SubtitleEnabled = tmpl.SubtitleEnabled
	if tmpl.VoiceName != "" {
		params.VoiceName = tmpl.VoiceName
	}
}

// GetScriptPrompt 获取模板的脚本提示词
func (t *Template) GetScriptPrompt() string {
	return t.ScriptPrompt
}

// GetImagePromptStyle 获取图片 prompt 风格
func (t *Template) GetImagePromptStyle() string {
	return t.ImagePromptStyle
}

// callLLM 调用 LLM API
func callLLM(prompt string, cfg *config.Config) (string, error) {
	baseURL := cfg.OpenAI.BaseURL
	apiKey := cfg.OpenAI.APIKey
	model := cfg.OpenAI.ModelName

	if baseURL == "" || apiKey == "" || model == "" {
		return "", fmt.Errorf("LLM not configured")
	}

	url := strings.TrimSuffix(baseURL, "/") + "/chat/completions"

	reqBody := map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"max_tokens":  500,
		"temperature": 0.3,
	}

	bodyJSON, _ := json.Marshal(reqBody)

	req, err := http.NewRequest("POST", url, strings.NewReader(string(bodyJSON)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

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

// cleanJSONResponse 清理 LLM 返回的 JSON
func cleanJSONResponse(s string) string {
	s = strings.TrimSpace(s)
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
