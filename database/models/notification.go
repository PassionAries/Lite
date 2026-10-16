package models

import (
	"crypto/sha256"
	"fmt"
	"math"
	"strings"
	"time"
)

// Notification 定义了通知相关的数据库模型
type OfflineNotification struct {
	Client     string `json:"client" gorm:"type:varchar(36);not null;index;unique;constraint:OnDelete:CASCADE,OnUpdate:CASCADE;foreignKey:client;references:UUID"`
	ClientInfo Client `json:"client_info,omitempty" gorm:"foreignKey:Client;references:UUID"`
	Enable     bool   `json:"enable" gorm:"type:boolean;default:false"`
	//Cooldown     int       `json:"cooldown" gorm:"type:int;not null;default:1800"`                // 冷却时间（秒），默认 30 分钟
	GracePeriod  int        `json:"grace_period" gorm:"type:int;not null;default:180"` // 宽限期（秒），默认 3 分钟
	LastNotified *time.Time `json:"last_notified"`                                     // 上次通知时间
}

// LoadNotification 定义了基于资源占用达标时间比的负载通知规则
type LoadNotification struct {
	Id           uint        `json:"id,omitempty" gorm:"primaryKey;autoIncrement"`
	Name         string      `json:"name" gorm:"type:varchar(255)"`
	Clients      StringArray `json:"clients" gorm:"type:longtext"`
	DefaultOn    bool        `json:"default_on" gorm:"column:all_clients;not null;default:false"` // 新加入的服务器是否自动启用此告警；现有服务器不受此字段影响
	Metric       string      `json:"metric" gorm:"type:varchar(50);not null;default:'cpu'"`       // 监控指标，如 cpu, ram, load
	Threshold    float32     `json:"threshold" gorm:"type:decimal(5,2);not null;default:80.00"`   // 阈值百分比
	Ratio        float32     `json:"ratio" gorm:"type:decimal(5,2);not null;default:0.80"`        // 达标时间比
	Interval     int         `json:"interval" gorm:"type:int;not null;default:15"`                // 监测间隔（分钟）
	LastNotified *time.Time  `json:"last_notified"`                                               // 上次通知时间
}

// LoadNotificationRuleFingerprint identifies the fields that define one load
// alert incident. Presentation and assignment changes deliberately do not
// alter it, while a metric, threshold, ratio, or interval change starts a new
// incident and must not inherit the previous silence state.
func LoadNotificationRuleFingerprint(rule LoadNotification) string {
	payload := fmt.Sprintf("%s:%08x:%08x:%d",
		strings.ToLower(strings.TrimSpace(rule.Metric)),
		math.Float32bits(rule.Threshold), math.Float32bits(rule.Ratio), rule.Interval)
	sum := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("%x", sum)
}

// LoadNotificationState stores the latest evaluation and silence preference
// for one load notification rule and one assigned client.
type LoadNotificationState struct {
	NotificationID  uint             `json:"notification_id" gorm:"primaryKey;not null;index"`
	Notification    LoadNotification `json:"notification,omitempty" gorm:"foreignKey:NotificationID;references:Id;constraint:OnDelete:CASCADE,OnUpdate:CASCADE"`
	Client          string           `json:"client" gorm:"type:varchar(36);primaryKey;not null;index"`
	ClientInfo      Client           `json:"client_info,omitempty" gorm:"foreignKey:Client;references:UUID;constraint:OnDelete:CASCADE,OnUpdate:CASCADE"`
	RuleFingerprint string           `json:"rule_fingerprint" gorm:"type:varchar(64);not null;default:''"`
	AlertActive     bool             `json:"alert_active" gorm:"type:boolean;not null;default:false;index"`
	ActiveSince     *time.Time       `json:"active_since"`
	LastEvaluatedAt time.Time        `json:"last_evaluated_at" gorm:"not null;index"`
	LatestValue     float64          `json:"latest_value" gorm:"not null;default:0"`
	MatchedSamples  int              `json:"matched_samples" gorm:"not null;default:0"`
	TotalSamples    int              `json:"total_samples" gorm:"not null;default:0"`
	LastNotified    *time.Time       `json:"last_notified"`
	RecoveryPending bool             `json:"recovery_pending" gorm:"type:boolean;not null;default:false;index"`
	SilencedUntil   *time.Time       `json:"silenced_until"`
	SilencedForever bool             `json:"silenced_forever" gorm:"type:boolean;not null;default:false"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
}

// TrafficReportNotification 定义了流量定时报告的数据库模型
type TrafficReportNotification struct {
	Client         string `json:"client" gorm:"type:varchar(36);not null;index;unique;constraint:OnDelete:CASCADE,OnUpdate:CASCADE;foreignKey:client;references:UUID"`
	ClientInfo     Client `json:"client_info,omitempty" gorm:"foreignKey:Client;references:UUID"`
	Enable         bool   `json:"enable" gorm:"type:boolean;default:false"`
	Daily          bool   `json:"daily" gorm:"type:boolean;default:false"`           // 日报
	Weekly         bool   `json:"weekly" gorm:"type:boolean;default:false"`          // 周报
	Monthly        bool   `json:"monthly" gorm:"type:boolean;default:false"`         // 月报
	IncludeTraffic bool   `json:"include_traffic" gorm:"type:boolean;default:true"`  // 上行/下行流量
	IncludeBilling bool   `json:"include_billing" gorm:"type:boolean;default:false"` // 按服务器计费规则计算的流量
}

// TrafficCycleFirstDay stores post-reset usage for the first Beijing day of a
// custom clock cycle so calibration can outlive metric retention.
type TrafficCycleFirstDay struct {
	Client       string    `json:"client" gorm:"type:varchar(36);primaryKey;not null"`
	ClientInfo   Client    `json:"-" gorm:"foreignKey:Client;references:UUID;constraint:OnDelete:CASCADE,OnUpdate:CASCADE"`
	Cycle        string    `json:"cycle" gorm:"type:varchar(64);primaryKey;not null"`
	Day          string    `json:"day" gorm:"type:varchar(10);not null;index"`
	UpBytes      int64     `json:"up_bytes" gorm:"type:bigint;not null;default:0"`
	DownBytes    int64     `json:"down_bytes" gorm:"type:bigint;not null;default:0"`
	CoveredUntil time.Time `json:"covered_until" gorm:"type:timestamp"`
	Sealed       bool      `json:"sealed" gorm:"not null;default:false"`
	Recovered    bool      `json:"recovered" gorm:"not null;default:false"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// TrafficDailyLedger stores exact report traffic for one Beijing calendar day.
// The daily ledger is intentionally separate from the general metric store so
// weekly and monthly reports do not require long retention for four metrics.
type TrafficDailyLedger struct {
	Client     string    `json:"client" gorm:"type:varchar(36);primaryKey;not null"`
	ClientInfo Client    `json:"-" gorm:"foreignKey:Client;references:UUID;constraint:OnDelete:CASCADE,OnUpdate:CASCADE"`
	Day        string    `json:"day" gorm:"type:varchar(10);primaryKey;not null;index:idx_traffic_daily_ledger_day"`
	UpBytes    int64     `json:"up_bytes" gorm:"type:bigint;not null;default:0"`
	DownBytes  int64     `json:"down_bytes" gorm:"type:bigint;not null;default:0"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// TrafficCalibrationAdjustment stores one auditable traffic correction
// allocated to a Beijing calendar day. Raw Agent metrics remain unchanged.
type TrafficCalibrationAdjustment struct {
	ID            uint64    `json:"id" gorm:"primaryKey;autoIncrement"`
	CalibrationID string    `json:"calibration_id" gorm:"type:varchar(32);not null;index;uniqueIndex:idx_traffic_calibration_day"`
	Client        string    `json:"client" gorm:"type:varchar(36);not null;index;constraint:OnDelete:CASCADE,OnUpdate:CASCADE;foreignKey:Client;references:UUID"`
	ClientInfo    Client    `json:"-" gorm:"foreignKey:Client;references:UUID;constraint:OnDelete:CASCADE,OnUpdate:CASCADE"`
	Cycle         string    `json:"cycle" gorm:"type:varchar(64);not null;index"`
	Day           string    `json:"day" gorm:"type:varchar(10);not null;index;uniqueIndex:idx_traffic_calibration_day"`
	UpDelta       int64     `json:"up_delta" gorm:"type:bigint;not null;default:0"`
	DownDelta     int64     `json:"down_delta" gorm:"type:bigint;not null;default:0"`
	TargetUp      int64     `json:"target_up" gorm:"type:bigint;not null;default:0"`
	TargetDown    int64     `json:"target_down" gorm:"type:bigint;not null;default:0"`
	Operator      string    `json:"operator,omitempty" gorm:"type:varchar(36);not null;default:''"`
	CreatedAt     time.Time `json:"created_at" gorm:"index"`
}

const (
	LatencyAlertNormal = "normal"
	LatencyAlertHigh   = "high"
	LatencyAlertLow    = "low"

	AdaptiveBaselineWarming      = "warming"
	AdaptiveBaselineReady        = "ready"
	AdaptiveBaselineFrozen       = "frozen"
	AdaptiveBaselineRecoveryHold = "recovery_hold"
)

// PingLossNotification defines packet-loss and latency-anomaly alerts for one
// client and ping task.
type PingLossNotification struct {
	Id                   uint       `json:"id,omitempty" gorm:"primaryKey;autoIncrement"`
	Client               string     `json:"client" gorm:"type:varchar(36);not null;uniqueIndex:idx_ping_loss_notification_target"`
	ClientInfo           Client     `json:"client_info,omitempty" gorm:"foreignKey:Client;references:UUID;constraint:OnDelete:CASCADE,OnUpdate:CASCADE"`
	TaskId               uint       `json:"task_id" gorm:"not null;uniqueIndex:idx_ping_loss_notification_target"`
	Task                 PingTask   `json:"task,omitempty" gorm:"foreignKey:TaskId;references:Id;constraint:OnDelete:CASCADE,OnUpdate:CASCADE"`
	Enable               bool       `json:"enable" gorm:"type:boolean;default:false"`
	LossEnabled          bool       `json:"loss_enabled" gorm:"type:boolean;not null;default:false"`
	WindowSeconds        int        `json:"window_seconds" gorm:"type:int;not null;default:60"`
	LossThreshold        float64    `json:"loss_threshold" gorm:"type:decimal(5,2);not null;default:5.00"`
	MinimumSamples       int        `json:"minimum_samples" gorm:"type:int;not null;default:1"`
	CooldownSeconds      int        `json:"cooldown_seconds" gorm:"type:int;not null;default:300"`
	LastNotified         *time.Time `json:"last_notified"`
	AlertActive          bool       `json:"alert_active" gorm:"type:boolean;not null;default:false"`
	LossIncidentNotified bool       `json:"loss_incident_notified" gorm:"type:boolean;not null;default:false"`

	LatencyEnabled                bool       `json:"latency_enabled" gorm:"type:boolean;not null;default:false"`
	AdaptiveBaselineEnabled       bool       `json:"adaptive_baseline_enabled" gorm:"type:boolean;not null;default:false"`
	LatencyWindowSeconds          int        `json:"latency_window_seconds" gorm:"type:int;not null;default:300"`
	LatencyMinimumSamples         int        `json:"latency_minimum_samples" gorm:"type:int;not null;default:3"`
	LatencyCooldownSeconds        int        `json:"latency_cooldown_seconds" gorm:"type:int;not null;default:1800"`
	FixedBaselineMs               float64    `json:"fixed_baseline_ms" gorm:"type:decimal(12,3);not null;default:0"`
	LowLatencyThresholdMs         float64    `json:"low_latency_threshold_ms" gorm:"type:decimal(12,3);not null;default:0"`
	HighLatencyThresholdMs        float64    `json:"high_latency_threshold_ms" gorm:"type:decimal(12,3);not null;default:0"`
	AdaptiveLowerDeviationPercent float64    `json:"adaptive_lower_deviation_percent" gorm:"type:decimal(8,2);not null;default:20"`
	AdaptiveUpperDeviationPercent float64    `json:"adaptive_upper_deviation_percent" gorm:"type:decimal(8,2);not null;default:20"`
	BaselineWindowSeconds         int        `json:"baseline_window_seconds" gorm:"type:int;not null;default:86400"`
	BaselineMinimumSamples        int        `json:"baseline_minimum_samples" gorm:"type:int;not null;default:30"`
	LatencyAlertState             string     `json:"latency_alert_state" gorm:"type:varchar(16);not null;default:'normal'"`
	LatencyIncidentNotified       bool       `json:"latency_incident_notified" gorm:"type:boolean;not null;default:false"`
	LatencyActiveSince            *time.Time `json:"latency_active_since"`
	LatencyLastNotified           *time.Time `json:"latency_last_notified"`
	LatencyLastEvaluatedAt        *time.Time `json:"latency_last_evaluated_at"`
	LatencyLatestAverageMs        float64    `json:"latency_latest_average_ms" gorm:"type:decimal(12,3);not null;default:0"`
	LatencySuccessfulSamples      int        `json:"latency_successful_samples" gorm:"type:int;not null;default:0"`
	AdaptiveBaselineMs            *float64   `json:"adaptive_baseline_ms"`
	AdaptiveBaselineStatus        string     `json:"adaptive_baseline_status" gorm:"type:varchar(32);not null;default:'warming'"`
	AdaptiveBaselineSampleCount   int        `json:"adaptive_baseline_sample_count" gorm:"type:int;not null;default:0"`
	AdaptiveBaselineUpdatedAt     *time.Time `json:"adaptive_baseline_updated_at"`
	AdaptiveBaselineResumeAt      *time.Time `json:"adaptive_baseline_resume_at"`
	AdaptiveBaselineFingerprint   string     `json:"adaptive_baseline_fingerprint" gorm:"type:varchar(255);not null;default:''"`
	AdaptiveBaselineStartedAt     *time.Time `json:"adaptive_baseline_started_at"`
}

func NormalizeLatencyAlertState(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case LatencyAlertHigh:
		return LatencyAlertHigh
	case LatencyAlertLow:
		return LatencyAlertLow
	default:
		return LatencyAlertNormal
	}
}

func NormalizeAdaptiveBaselineStatus(value string, hasBaseline bool) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case AdaptiveBaselineReady:
		if hasBaseline {
			return AdaptiveBaselineReady
		}
	case AdaptiveBaselineFrozen:
		if hasBaseline {
			return AdaptiveBaselineFrozen
		}
	case AdaptiveBaselineRecoveryHold:
		if hasBaseline {
			return AdaptiveBaselineRecoveryHold
		}
	}
	return AdaptiveBaselineWarming
}

func (n PingLossNotification) NormalizedLatencyAlertState() string {
	return NormalizeLatencyAlertState(n.LatencyAlertState)
}

func (n PingLossNotification) HasAdaptiveBaseline() bool {
	return n.AdaptiveBaselineMs != nil && *n.AdaptiveBaselineMs > 0
}

func (n PingLossNotification) NormalizedAdaptiveBaselineStatus() string {
	return NormalizeAdaptiveBaselineStatus(n.AdaptiveBaselineStatus, n.HasAdaptiveBaseline())
}

func (n PingLossNotification) LatencyAlerting() bool {
	state := n.NormalizedLatencyAlertState()
	return state == LatencyAlertHigh || state == LatencyAlertLow
}

const (
	AlertKindOffline     = "offline"
	AlertKindLoad        = "load"
	AlertKindLatency     = "latency"
	AlertKindReturnRoute = "return_route"
)

// AlertSilence stores a mute window for one alert source in the alert center.
// Load alerts keep silence on LoadNotificationState; other kinds use this table.
type AlertSilence struct {
	Kind            string     `json:"kind" gorm:"primaryKey;type:varchar(32)"`
	Target          string     `json:"target" gorm:"primaryKey;type:varchar(80)"`
	Label           string     `json:"label" gorm:"type:varchar(255);not null;default:''"`
	SilencedUntil   *time.Time `json:"silenced_until"`
	SilencedForever bool       `json:"silenced_forever" gorm:"not null;default:false"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (AlertSilence) TableName() string { return "alert_silences" }

func AdaptiveBaselineFingerprint(taskType, target string, baselineWindowSeconds, baselineMinimumSamples int) string {
	payload := fmt.Sprintf("%s\x1f%s\x1f%d\x1f%d", strings.TrimSpace(taskType), strings.TrimSpace(target), baselineWindowSeconds, baselineMinimumSamples)
	sum := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("%x", sum[:8])
}
