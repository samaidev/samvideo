package video

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"samvideo/internal/config"
)

// AgnesVideoClient wraps the Agnes Video 2.5 Flash API (via apishare.cc)
type AgnesVideoClient struct {
	BaseURL string
	APIKey  string
	Model   string
}

func NewAgnesVideoClient(cfg *config.Config) *AgnesVideoClient {
	baseURL := cfg.GetVideoAPIBaseURL() // same base as image API (apishare.cc)
	if baseURL == "" {
		baseURL = "https://apishare.cc"
	}
	return &AgnesVideoClient{
		BaseURL: strings.TrimSuffix(baseURL, "/"),
		APIKey:  cfg.GetFirstVideoAPIKey(),
		Model:   cfg.GetVideoModel(),
	}
}

// CreateVideoRequest is the request body for POST /v1/videos
type CreateVideoRequest struct {
	Model       string   `json:"model"`
	Prompt      string   `json:"prompt"`
	Mode        string   `json:"mode,omitempty"`         // text, keyframe, reference
	Seconds     string   `json:"seconds,omitempty"`      // "4" - "12"
	Size        string   `json:"size,omitempty"`         // "720P"
	AspectRatio string   `json:"aspect_ratio,omitempty"` // "16:9", "9:16", etc
	FirstFrame  string   `json:"first_frame,omitempty"`  // keyframe mode
	LastFrame   string   `json:"last_frame,omitempty"`   // keyframe mode
	Images      []string `json:"images,omitempty"`       // reference mode (max 5)
	Audios      []string `json:"audios,omitempty"`       // reference mode (max 3)
	N           int      `json:"n,omitempty"`             // always 1
	Seed        int64    `json:"seed,omitempty"`
}

// CreateVideoResponse is the response from POST /v1/videos
type CreateVideoResponse struct {
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	VideoID   string `json:"video_id"`
	Status    string `json:"status"`
	Error     string `json:"error"`
	Detail    string `json:"detail"`
}

// VideoTaskStatus is the response from GET /v1/videos/{id}
type VideoTaskStatus struct {
	ID        string `json:"id"`
	Status    string `json:"status"`     // "queued", "processing", "completed", "failed"
	Progress  int    `json:"progress"`
	URL       string `json:"url"`        // video download URL when completed
	VideoURL  string `json:"video_url"`  // alternative field name
	Error     string `json:"error"`
	Detail    string `json:"detail"`
}

// CreateTask submits a video generation task
func (c *AgnesVideoClient) CreateTask(req *CreateVideoRequest) (*CreateVideoResponse, error) {
	if req.Model == "" {
		req.Model = c.Model
	}
	if req.Size == "" {
		req.Size = "720P"
	}
	if req.N == 0 {
		req.N = 1
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := c.BaseURL + "/v1/videos"
	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	var result CreateVideoResponse
	json.Unmarshal(respBody, &result)

	// apishare.cc may wrap errors in "detail" field
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	if result.ID == "" {
		return nil, fmt.Errorf("no task ID in response: %s", string(respBody))
	}

	return &result, nil
}

// PollTask polls the task status until completed or failed
func (c *AgnesVideoClient) PollTask(taskID string, timeoutSec int) (*VideoTaskStatus, error) {
	url := c.BaseURL + "/v1/videos/" + taskID
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)

	for time.Now().Before(deadline) {
		httpReq, _ := http.NewRequest("GET", url, nil)
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)

		resp, err := http.DefaultClient.Do(httpReq)
		if err != nil {
			time.Sleep(5 * time.Second)
			continue
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var status VideoTaskStatus
		json.Unmarshal(respBody, &status)

		switch status.Status {
		case "completed":
			return &status, nil
		case "failed":
			return nil, fmt.Errorf("video task failed: %s (detail: %s)", status.Error, status.Detail)
		}

		time.Sleep(5 * time.Second)
	}

	return nil, fmt.Errorf("video task timed out after %d seconds", timeoutSec)
}

// DownloadVideo downloads the video from the given URL to the specified path
func (c *AgnesVideoClient) DownloadVideo(videoURL, destPath string) error {
	resp, err := http.Get(videoURL)
	if err != nil {
		return fmt.Errorf("download video: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	os.MkdirAll(filepath.Dir(destPath), 0755)
	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	_, err = io.Copy(f, resp.Body)
	return err
}

// GenerateTextVideo creates a text-to-video task and waits for completion.
// Returns the local file path of the downloaded video.
func (c *AgnesVideoClient) GenerateTextVideo(prompt, aspectRatio string, seconds int, destPath string) (string, error) {
	if seconds < 4 {
		seconds = 4
	}
	if seconds > 12 {
		seconds = 12
	}
	if aspectRatio == "" {
		aspectRatio = "16:9"
	}

	req := &CreateVideoRequest{
		Prompt:      prompt,
		Mode:        "text",
		Seconds:     fmt.Sprintf("%d", seconds),
		Size:        "720P",
		AspectRatio: aspectRatio,
	}

	task, err := c.CreateTask(req)
	if err != nil {
		return "", fmt.Errorf("create video task: %w", err)
	}

	status, err := c.PollTask(task.ID, 300) // 5 min timeout
	if err != nil {
		return "", fmt.Errorf("poll video task: %w", err)
	}

	videoURL := status.URL
	if videoURL == "" {
		videoURL = status.VideoURL
	}
	if videoURL == "" {
		return "", fmt.Errorf("no video URL in completed response")
	}

	if err := c.DownloadVideo(videoURL, destPath); err != nil {
		return "", fmt.Errorf("download video: %w", err)
	}

	return destPath, nil
}

// GenerateDigitalHumanVideo creates a digital human speaking video using reference mode.
// portraitURL: publicly accessible URL of the person's portrait photo
// audioURL: publicly accessible URL of the TTS audio
// aspectRatio: "16:9" or "9:16"
// destPath: local file path to save the video
func (c *AgnesVideoClient) GenerateDigitalHumanVideo(portraitURL, audioURL, aspectRatio string, seconds int, destPath string) (string, error) {
	if seconds < 4 {
		seconds = 4
	}
	if seconds > 12 {
		seconds = 12
	}
	if aspectRatio == "" {
		aspectRatio = "16:9"
	}

	prompt := "The person in <Picture 1> speaks naturally to the camera, lip-synced to the audio in <Audio 1>. Professional news anchor style, natural facial expressions and head movements, upper body visible."

	req := &CreateVideoRequest{
		Prompt:      prompt,
		Mode:        "reference",
		Seconds:     fmt.Sprintf("%d", seconds),
		Size:        "720P",
		AspectRatio: aspectRatio,
		Images:      []string{portraitURL},
		Audios:      []string{audioURL},
	}

	task, err := c.CreateTask(req)
	if err != nil {
		return "", fmt.Errorf("create digital human task: %w", err)
	}

	status, err := c.PollTask(task.ID, 300)
	if err != nil {
		return "", fmt.Errorf("poll digital human task: %w", err)
	}

	videoURL := status.URL
	if videoURL == "" {
		videoURL = status.VideoURL
	}
	if videoURL == "" {
		return "", fmt.Errorf("no video URL in completed response")
	}

	if err := c.DownloadVideo(videoURL, destPath); err != nil {
		return "", fmt.Errorf("download digital human video: %w", err)
	}

	return destPath, nil
}

// GenerateKeyframeVideo creates a video from first/last frame images
func (c *AgnesVideoClient) GenerateKeyframeVideo(prompt, firstFrame, lastFrame, aspectRatio string, seconds int, destPath string) (string, error) {
	if seconds < 4 {
		seconds = 4
	}
	if seconds > 12 {
		seconds = 12
	}
	if aspectRatio == "" {
		aspectRatio = "16:9"
	}

	req := &CreateVideoRequest{
		Prompt:      prompt,
		Mode:        "keyframe",
		Seconds:     fmt.Sprintf("%d", seconds),
		Size:        "720P",
		AspectRatio: aspectRatio,
		FirstFrame:  firstFrame,
		LastFrame:   lastFrame,
	}

	task, err := c.CreateTask(req)
	if err != nil {
		return "", fmt.Errorf("create keyframe task: %w", err)
	}

	status, err := c.PollTask(task.ID, 300)
	if err != nil {
		return "", fmt.Errorf("poll keyframe task: %w", err)
	}

	videoURL := status.URL
	if videoURL == "" {
		videoURL = status.VideoURL
	}
	if videoURL == "" {
		return "", fmt.Errorf("no video URL in completed response")
	}

	if err := c.DownloadVideo(videoURL, destPath); err != nil {
		return "", fmt.Errorf("download keyframe video: %w", err)
	}

	return destPath, nil
}
