package config

// ChatConfig is used only by the explicit --chat service.
type ChatConfig struct {
	ProgressIntervalSec int           `mapstructure:"progress_interval_sec"`
	ProgressMaxMessages int           `mapstructure:"progress_max_messages"`
	TranscriptionURL    string        `mapstructure:"transcription_url"`
	TranscriptionKeyEnv string        `mapstructure:"transcription_key_env"`
	TranscriptionModel  string        `mapstructure:"transcription_model"`
	WecomSource         string        `mapstructure:"wecom_source"`
	BindingFile         string        `mapstructure:"binding_file"`
	Listen              string        `mapstructure:"listen"`
	Store               string        `mapstructure:"store"`
	TaskTimeoutSec      int           `mapstructure:"task_timeout_sec"`
	MaxConcurrent       int           `mapstructure:"max_concurrent"`
	AllowBatch          bool          `mapstructure:"allow_batch"`
	Channels            []ChatChannel `mapstructure:"channels"`
}

// Manual configuration references environment variables; QR credentials are loaded from the private binding store.
type ChatChannel struct {
	PairCode       string   `mapstructure:"-" json:"-"`
	Generation     int      `mapstructure:"-" json:"-"`
	AppSecret      string   `mapstructure:"-" json:"-"`
	AllowBatch     bool     `mapstructure:"-" json:"-"`
	Name           string   `mapstructure:"name"`
	Platform       string   `mapstructure:"platform"` // feishu, dingtalk, wecom, onebot, wechat
	AllowedUsers   []string `mapstructure:"allowed_users"`
	AllowedChats   []string `mapstructure:"allowed_chats"` // required for group messages
	SecretEnv      string   `mapstructure:"secret_env"`
	TokenEnv       string   `mapstructure:"token_env"`
	EncryptKeyEnv  string   `mapstructure:"encrypt_key_env"`
	AppID          string   `mapstructure:"app_id"`
	AgentID        int64    `mapstructure:"agent_id"`
	MediaRoot      string   `mapstructure:"media_root"` // optional shared OneBot media directory
	APIURL         string   `mapstructure:"api_url"`    // OneBot/WeChat bridge only
	AccessTokenEnv string   `mapstructure:"access_token_env"`
}
