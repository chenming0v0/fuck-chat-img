package model

import (
	"time"

	"gorm.io/gorm"
)

// User 管理员用户(用于 Web UI 鉴权)
type User struct {
	ID           uint           `gorm:"primarykey" json:"id"`
	Username     string         `gorm:"uniqueIndex;size:64;not null" json:"username"`
	PasswordHash string         `gorm:"size:128;not null" json:"-"`
	Role         string         `gorm:"size:16;default:admin" json:"role"`
	Status       int            `gorm:"default:1" json:"status"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	DeletedAt    gorm.DeletedAt `gorm:"index" json:"-"`
}

// UpstreamModel 上游模型配置
type UpstreamModel struct {
	BaseURL    string `json:"base_url"`     // 例如 https://api.openai.com/v1
	APIKey     string `json:"api_key"`      // 上游密钥
	Model      string `json:"model"`        // 上游真实模型名
	APIType    string `json:"api_type"`     // openai(默认) | azure | ...
	ExtraURL   string `json:"extra_url"`    // 可选: 自定义完整路径覆盖
	MaxRetries int    `json:"max_retries"`  // 单模型重试次数, 默认1
	Weight     int    `json:"weight"`       // 轮询权重(预留), 默认1
}

// ModelGroup 模型组: 一个主对话模型 + 多个图片模型(轮询)
type ModelGroup struct {
	ID               uint           `gorm:"primarykey" json:"id"`
	Name             string         `gorm:"uniqueIndex;size:128;not null" json:"name"` // 暴露给客户端的模型名
	Description      string         `gorm:"size:512" json:"description"`
	MainTextModel    string         `gorm:"type:text" json:"main_text_model"`    // JSON: UpstreamModel
	ImageModels      string         `gorm:"type:text" json:"image_models"`        // JSON: []UpstreamModel
	ImageStrategy    string         `gorm:"size:32;default:round_robin" json:"image_strategy"` // round_robin | failover
	ImagePrompt      string         `gorm:"type:text" json:"image_prompt"`        // 识别图片时给图片模型的额外提示词
	ReplaceImage     bool           `gorm:"default:true" json:"replace_image"`    // 是否用识别文本替换图片(true)还是追加(false)
	Enabled          bool           `gorm:"default:true" json:"enabled"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
	DeletedAt        gorm.DeletedAt `gorm:"index" json:"-"`
}

// History 历史记录
type History struct {
	ID              uint      `gorm:"primarykey" json:"id"`
	RequestID       string    `gorm:"index;size:64" json:"request_id"`
	ModelGroup      string    `gorm:"index;size:128" json:"model_group"`
	Endpoint        string    `gorm:"size:32" json:"endpoint"` // responses | chat | messages
	UserID          uint      `gorm:"index" json:"user_id"`      // 关联用户(0 表示代理匿名调用)
	HasImage        bool      `json:"has_image"`
	ImageCount      int       `json:"image_count"`
	ImageModelUsed  string    `gorm:"size:256" json:"image_model_used"`
	MainModelUsed   string    `gorm:"size:256" json:"main_model_used"`
	CacheHit        bool      `gorm:"index" json:"cache_hit"`
	Success         bool      `gorm:"index" json:"success"`
	ErrorMessage    string    `gorm:"type:text" json:"error_message"`
	PromptTokens    int       `json:"prompt_tokens"`
	CompletionTokens int      `json:"completion_tokens"`
	TotalTokens     int       `json:"total_tokens"`
	LatencyMs       int64     `json:"latency_ms"`
	InputSummary    string    `gorm:"type:text" json:"input_summary"`
	OutputSummary   string    `gorm:"type:text" json:"output_summary"`
	CreatedAt       time.Time `gorm:"index" json:"created_at"`
}
