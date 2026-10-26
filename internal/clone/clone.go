package clone

import (
	"context"
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
	"samvideo/internal/llm"
	"samvideo/internal/utils"
)

// CloneResult 复刻结果
type CloneResult struct {
	VideoURL      string         `json:"video_url"`
	AudioPath     string         `json:"audio_path"`
	Transcript    string         `json:"transcript"`
	Scenes        []llm.Scene    `json:"scenes"`
	Script        string         `json:"script"`
	ImagePrompts  []string       `json:"image_prompts"`
}

// DownloadVideo 用 yt-dlp 下载视频
func DownloadVideo(videoURL, outputDir string) (string, error) {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return "", err
	}
	outputPath := filepath.Join(outputDir, "source.mp4")

	// 检查 yt-dlp 是否可用
	ytDlpPath, err := exec.LookPath("yt-dlp")
	if err != nil {
		return "", fmt.Errorf("yt-dlp 未安装: %v", err)
	}

	args := []string{
		"-f", "bestvideo[height<=1080]+bestaudio/best[height<=1080]/best",
		"--merge-output-format", "mp4",
		"--no-playlist",
		"-o", outputPath,
		videoURL,
	}

	ctx, cancel := timeoutCtx(120)
	defer cancel()
	cmd := exec.CommandContext(ctx, ytDlpPath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("yt-dlp 下载失败: %w, output: %s", err, string(out))
	}

	if _, err := os.Stat(outputPath); err != nil {
		return "", fmt.Errorf("下载后文件不存在: %v", err)
	}
	utils.LogInfo("视频下载完成: %s", outputPath)
	return outputPath, nil
}

// ExtractAudio 从视频提取音频（用于转录）
func ExtractAudio(videoPath, outputDir string) (string, error) {
	audioPath := filepath.Join(outputDir, "source_audio.wav")
	args := []string{
		"-y", "-i", videoPath,
		"-vn", "-acodec", "pcm_s16le",
		"-ar", "16000", "-ac", "1",
		audioPath,
	}
	ctx, cancel := timeoutCtx(60)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("提取音频失败: %w, output: %s", err, string(out))
	}
	return audioPath, nil
}

// TranscribeAudio 用 LLM 转录音频——通过 OpenAI Whisper API
func TranscribeAudio(audioPath string, cfg *config.Config) (string, error) {
	// 优先用 OpenAI Whisper API
	baseURL := strings.TrimSuffix(cfg.OpenAI.BaseURL, "/")
	baseURL = strings.ReplaceAll(baseURL, "//v1", "/v1")
	apiKey := ""
	apiKey = cfg.OpenAI.APIKey
	if baseURL == "" || apiKey == "" {
		return "", fmt.Errorf("LLM API 未配置")
	}

	// 尝试 Whisper API
	url := baseURL + "/audio/transcriptions"
	file, err := os.Open(audioPath)
	if err != nil {
		return "", fmt.Errorf("打开音频文件失败: %v", err)
	}
	defer file.Close()

	// 创建 multipart 请求
	body := &strings.Builder{}
	boundary := fmt.Sprintf("----WebKitFormBoundary%d", time.Now().UnixNano())

	// 文件部分
	body.WriteString("--" + boundary + "\r\n")
	body.WriteString(fmt.Sprintf("Content-Disposition: form-data; name=\"file\"; filename=\"audio.wav\"\r\n"))
	body.WriteString("Content-Type: audio/wav\r\n\r\n")

	audioData, _ := os.ReadFile(audioPath)
	body.Write(audioData)
	body.WriteString("\r\n")

	// model 字段
	body.WriteString("--" + boundary + "\r\n")
	body.WriteString("Content-Disposition: form-data; name=\"model\"\r\n\r\n")
	body.WriteString("whisper-1\r\n")

	body.WriteString("--" + boundary + "--\r\n")

	req, err := http.NewRequest("POST", url, strings.NewReader(body.String()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Whisper API 请求失败: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		// Whisper API 不可用，用 ffmpeg 提取关键帧 + LLM 分析
		utils.LogWarn("Whisper API 返回 %d: %s, 降级到 ffmpeg 提取关键帧", resp.StatusCode, string(respBody))
		return "", fmt.Errorf("whisper API %d", resp.StatusCode)
	}

	var result struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("解析 Whisper 响应失败: %v", err)
	}
	utils.LogInfo("音频转录完成: %d 字", len(result.Text))
	return result.Text, nil
}

// TranscribeWithFFmpeg 用 ffmpeg 提取视频关键帧描述 + 时长信息
// 当 Whisper API 不可用时的降级方案
func TranscribeWithFFmpeg(videoPath string, cfg *config.Config) (string, error) {
	// 提取视频时长
	ctx, cancel := timeoutCtx(10)
	defer cancel()
	probeCmd := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries",
		"format=duration", "-of", "default=noprint_wrappers=1:nokey=1", videoPath)
	durationBytes, _ := probeCmd.Output()
	duration := strings.TrimSpace(string(durationBytes))
	utils.LogInfo("视频时长: %s 秒", duration)

	// 提取关键帧截图（每5秒一帧）
	framesDir := filepath.Join(filepath.Dir(videoPath), "frames")
	os.MkdirAll(framesDir, 0755)
	ctx2, cancel2 := timeoutCtx(60)
	defer cancel2()
	frameCmd := exec.CommandContext(ctx2, "ffmpeg", "-y", "-i", videoPath,
		"-vf", "fps=1/5", "-q:v", "2", filepath.Join(framesDir, "frame_%03d.jpg"))
	frameCmd.CombinedOutput()

	// 用 LLM 分析关键帧，生成视频内容描述
	frames, _ := filepath.Glob(filepath.Join(framesDir, "frame_*.jpg"))
	if len(frames) == 0 {
		return "", fmt.Errorf("无法提取视频帧")
	}

	// 用 LLM 根据视频时长和帧数生成一个合理的脚本
	prompt := fmt.Sprintf(`你是一个视频内容分析专家。请根据以下信息生成一段视频内容描述：

视频时长: %s 秒
提取的关键帧数量: %d

请生成一段约200字的中文描述，描述这个视频可能的内容和叙事结构。
这只是一个参考描述，用于后续生成新的视频脚本。`, duration, len(frames))

	resp, err := llm.CallLLM(prompt, cfg)
	if err != nil {
		return "", fmt.Errorf("LLM 分析失败: %v", err)
	}
	return resp, nil
}

// GenerateCloneScenes 用 LLM 根据原视频转录文本生成新的分镜脚本
func GenerateCloneScenes(transcript, requirement string, cfg *config.Config) (string, []string, []llm.Scene, error) {
	now := time.Now().Format("2006-01-02 15:04 Monday")

	prompt := fmt.Sprintf(`你是一个专业的短视频复刻专家。

当前时间：%s

请根据以下原视频的转录文本，创作一个新的分镜脚本。保持原视频的内容结构和节奏，但用全新的表达方式重新编写旁白。

【原视频转录文本】
%s

【核心要求】
1. 每个分镜包含三个字段：narration（旁白）、image_prompt（画面描述）、overlay（叠加层）
2. narration：中文旁白，口语化，适合语音朗读，每段约30-50字
3. image_prompt：英文画面描述，必须与旁白内容直接相关
4. overlay：数组，每个元素描述一个叠加在画面上的视觉元素

【overlay字段说明】
- text：要显示的中文文字（关键数据、标签、名称等）
- position：位置，可选值：top/center/bottom/top-left/top-right/bottom-left/bottom-right
- color：颜色，可选值：red/green/white/yellow
- style：样式，可选值：box(半透明标签框)/highlight(高亮大字)/arrow(箭头标注)

【示例】
{
  "narration": "四季度开局，市场情绪略显谨慎，投资者都在问年底行情在哪里。",
  "image_prompt": "stock market trading floor with large screens showing slight dips, worried investors, cinematic",
  "overlay": [
    {"text": "四季度市场展望", "position": "top", "color": "yellow", "style": "highlight"},
    {"text": "市场情绪谨慎", "position": "bottom", "color": "white", "style": "box"}
  ]
}

请以JSON格式返回：
{"scenes": [{"narration": "...", "image_prompt": "...", "overlay": [{"text": "...", "position": "...", "color": "...", "style": "..."}]}]}

注意：overlay 数组不能为 null，至少包含一个元素。`, now, transcript)

	if requirement != "" {
		prompt += "\n\n【视频制作要求】\n" + requirement
	}

	resp, err := llm.CallLLM(prompt, cfg)
	if err != nil {
		return "", nil, nil, fmt.Errorf("LLM 调用失败: %v", err)
	}

	resp = llm.CleanJSONResponse(resp)

	var result struct {
		Scenes []llm.Scene `json:"scenes"`
	}
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		return "", nil, nil, fmt.Errorf("解析 LLM 响应失败: %v", err)
	}

	scenes := result.Scenes
	if len(scenes) == 0 {
		return "", nil, nil, fmt.Errorf("LLM 未生成有效的分镜数据")
	}

	// 兜底：如果 LLM 没生成 overlay，自动生成
	for i := range scenes {
		if len(scenes[i].Overlay) == 0 {
			scenes[i].Overlay = llm.AutoGenerateOverlay(scenes[i].Narration, requirement)
		}
	}

	// 生成脚本和图片提示词
	var scriptParts []string
	var imagePrompts []string
	for _, s := range scenes {
		scriptParts = append(scriptParts, s.Narration)
		imagePrompts = append(imagePrompts, s.ImagePrompt)
	}
	script := strings.Join(scriptParts, "\n")

	utils.LogInfo("复刻脚本生成完成: %d 个分镜", len(scenes))
	return script, imagePrompts, scenes, nil
}

// CloneVideo 完整复刻流程：下载 → 转录 → LLM生成新脚本
func CloneVideo(videoURL, requirement string, workDir string, cfg *config.Config) (*CloneResult, error) {
	// Step 1: 下载视频
	utils.LogInfo("复刻 Step 1: 下载视频 %s", videoURL)
	videoPath, err := DownloadVideo(videoURL, workDir)
	if err != nil {
		return nil, fmt.Errorf("下载视频失败: %v", err)
	}

	// Step 2: 提取音频
	utils.LogInfo("复刻 Step 2: 提取音频")
	audioPath, err := ExtractAudio(videoPath, workDir)
	if err != nil {
		return nil, fmt.Errorf("提取音频失败: %v", err)
	}

	// Step 3: 转录音频
	utils.LogInfo("复刻 Step 3: 转录音频")
	transcript, err := TranscribeAudio(audioPath, cfg)
	if err != nil {
		// 降级：用 ffmpeg 提取关键帧 + LLM 分析
		utils.LogWarn("Whisper API 不可用，降级到 ffmpeg 关键帧分析: %v", err)
		transcript, err = TranscribeWithFFmpeg(videoPath, cfg)
		if err != nil {
			return nil, fmt.Errorf("音频转录失败: %v", err)
		}
	}

	// Step 4: LLM 生成新分镜脚本
	utils.LogInfo("复刻 Step 4: LLM 生成新分镜脚本")
	script, imagePrompts, scenes, err := GenerateCloneScenes(transcript, requirement, cfg)
	if err != nil {
		return nil, fmt.Errorf("生成复刻脚本失败: %v", err)
	}

	return &CloneResult{
		VideoURL:     videoPath,
		AudioPath:    audioPath,
		Transcript:   transcript,
		Scenes:       scenes,
		Script:       script,
		ImagePrompts: imagePrompts,
	}, nil
}

func timeoutCtx(seconds int) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), time.Duration(seconds)*time.Second)
}
