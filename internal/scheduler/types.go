package scheduler

import "time"

const (
	DefaultStorePath = "reports/schedules/tasks.json"

	StatusRunning   = "running"
	StatusFailed    = "failed"
	StatusEnabled   = "enabled"
	StatusDisabled  = "disabled"
	StatusCompleted = "completed"

	RepeatOnce     = "once"
	RepeatDaily    = "daily"
	RepeatWeekly   = "weekly"
	RepeatInterval = "interval"

	KindInspection = "inspection"
	KindAgent      = "agent"

	NotifyNone     = "none"
	NotifyDingTalk = "dingtalk"
	NotifyFeishu   = "feishu"
	NotifyEmail    = "email"
)

type Task struct {
	TimeoutSec     int        `json:"timeout_sec,omitempty"`
	FailureCount   int        `json:"failure_count,omitempty"`
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Prompt         string     `json:"prompt"`
	Kind           string     `json:"kind"`
	Selector       string     `json:"selector,omitempty"`
	RunAt          time.Time  `json:"run_at"`
	Timezone       string     `json:"timezone"`
	Repeat         string     `json:"repeat"`
	Weekday        int        `json:"weekday,omitempty"`
	IntervalSec    int        `json:"interval_sec,omitempty"`
	Report         bool       `json:"report"`
	Notify         string     `json:"notify"`
	AllowBatch     bool       `json:"allow_batch,omitempty"`
	ReplyText      string     `json:"reply_text,omitempty"`
	ReplySession   string     `json:"reply_session,omitempty"`
	Status         string     `json:"status"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	LastRunAt      *time.Time `json:"last_run_at,omitempty"`
	LastResult     string     `json:"last_result,omitempty"`
	LastReportPath string     `json:"last_report_path,omitempty"`
	RunCount       int        `json:"run_count"`
}

type PlanInput struct {
	TimeoutSec   string
	Text         string
	Prompt       string
	RunAt        string
	Repeat       string
	IntervalSec  string
	Notify       string
	Selector     string
	Kind         string
	Timezone     string
	Report       *bool
	AllowBatch   bool
	ReplyText    string
	ReplySession string
}

type Plan struct {
	Task  Task     `json:"task"`
	Notes []string `json:"notes"`
}
