package models

import "time"

// TaskState 任务状态
const (
	TaskStateProcessing = 4
	TaskStateComplete   = 1
	TaskStateFailed     = -1
)

// VideoParams 视频生成参数
type VideoParams struct {
	VideoSubject       string   `json:"video_subject"`
	VideoScript        string   `json:"video_script,omitempty"`
	VideoTerms         []string `json:"video_terms,omitempty"`
	VideoAspect        string   `json:"video_aspect,omitempty"`        // 16:9, 9:16, 1:1
	VideoFitMode       string   `json:"video_fit_mode,omitempty"`      // cover, contain
	VideoConcatMode    string   `json:"video_concat_mode,omitempty"`   // random, sequential
	VideoClipDuration  int      `json:"video_clip_duration,omitempty"` // 1-15 seconds
	VideoClipSpeed     float64  `json:"video_clip_speed,omitempty"`
	VideoCount         int      `json:"video_count,omitempty"`
	VideoSource        string   `json:"video_source,omitempty"` // openai_image, pexels, local
	VideoMaterials     []string `json:"video_materials,omitempty"`

	// VideoTransitionMode 转场模式: none, fade_in, fade_out, slide_in, slide_out, zoom_in, zoom_out, shuffle
	VideoTransitionMode string `json:"video_transition_mode,omitempty"`

	VoiceName   string  `json:"voice_name,omitempty"`
	VoiceRate   float64 `json:"voice_rate,omitempty"`
	VoiceVolume float64 `json:"voice_volume,omitempty"`
	VoiceMode   string  `json:"voice_mode,omitempty"` // tts, upload, none

	SubtitleEnabled    bool   `json:"subtitle_enabled,omitempty"`
	SubtitleProvider   string `json:"subtitle_provider,omitempty"`      // edge, whisper
	SubtitleDisplayMode string `json:"subtitle_display_mode,omitempty"` // sentence, word
	SubtitleAnimation  string `json:"subtitle_animation,omitempty"`
	FontName          string `json:"font_name,omitempty"`
	FontSize          int    `json:"font_size,omitempty"`
	TextForeColor    string `json:"text_fore_color,omitempty"`
	StrokeColor       string `json:"stroke_color,omitempty"`
	StrokeWidth       float64 `json:"stroke_width,omitempty"`
	SubtitlePosition  string `json:"subtitle_position,omitempty"` // top, bottom, custom

	BGMType   string  `json:"bgm_type,omitempty"`
	BGMVolume float64 `json:"bgm_volume,omitempty"`
	BGMFile   string  `json:"bgm_file,omitempty"`

	ParagraphNumber int `json:"paragraph_number,omitempty"`
}

// TaskResult 任务结果
type TaskResult struct {
	TaskID       string       `json:"task_id"`
	State        int          `json:"state"`
	Progress     int          `json:"progress"`
	Params       *VideoParams `json:"params,omitempty"`
	Script       string       `json:"script,omitempty"`
	SearchTerms  []string     `json:"search_terms,omitempty"`
	VideoPath    string       `json:"video_path,omitempty"`
	VideoURL     string       `json:"video_url,omitempty"`
	ThumbnailURL string       `json:"thumbnail_url,omitempty"`
	Error        string       `json:"error,omitempty"`
	StartTime    time.Time    `json:"start_time"`
	EndTime      time.Time    `json:"end_time,omitempty"`
	Videos       []string     `json:"videos,omitempty"`
}
