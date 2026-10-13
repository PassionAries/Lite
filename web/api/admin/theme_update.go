package admin

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/database/auditlog"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/config"
	logger "github.com/nuomiiiii/lite/utils/log"
	"github.com/nuomiiiii/lite/web/api"
)

const (
	themeUpdateStatusUpdated = "updated"
	themeUpdateStatusCurrent = "current"
	themeUpdateStatusSkipped = "skipped"
	themeUpdateStatusFailed  = "failed"

	themeAutoUpdateIdleWait  = 6 * time.Hour
	themeAutoUpdateBusyDelay = 5 * time.Minute
)

var (
	errThemeUpdateBusy = errors.New("theme update already running")
	errThemeNoSource   = errors.New("没有来源地址，无法自动更新")

	themeUpdateAllMu    sync.Mutex
	themeUpdateAllBusy  bool
	themeAutoUpdateWake = make(chan struct{}, 1)
)

// ThemeUpdateItem is one installed theme's result in a batch update.
type ThemeUpdateItem struct {
	Short   string `json:"short"`
	Name    any    `json:"name,omitempty"`
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
	Message string `json:"message,omitempty"`
}

// ThemeUpdateSummary is the outcome of updating every installed theme.
type ThemeUpdateSummary struct {
	Results []ThemeUpdateItem `json:"results"`
	Updated int               `json:"updated"`
	Current int               `json:"current"`
	Skipped int               `json:"skipped"`
	Failed  int               `json:"failed"`
}

type themeAutoUpdateView struct {
	Enabled       bool                `json:"enabled"`
	IntervalHours int                 `json:"interval_hours"`
	NextRunAt     int64               `json:"next_run_at,omitempty"`
	LastRunAt     int64               `json:"last_run_at,omitempty"`
	LastSummary   *ThemeUpdateSummary `json:"last_summary,omitempty"`
}

func normalizeThemeVersion(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	return strings.TrimPrefix(value, "v")
}

func themeVersionsEqual(installed, latest string) bool {
	left := normalizeThemeVersion(installed)
	right := normalizeThemeVersion(latest)
	return left != "" && left == right
}

func normalizeThemeAutoUpdateHours(hours int) (int, error) {
	if hours < config.ThemeAutoUpdateIntervalMinHours || hours > config.ThemeAutoUpdateIntervalMaxHours {
		return 0, fmt.Errorf("更新间隔需要是 %d 到 %d 之间的小时数", config.ThemeAutoUpdateIntervalMinHours, config.ThemeAutoUpdateIntervalMaxHours)
	}
	return hours, nil
}

func themeAutoUpdateDelay(now time.Time, enabled bool, nextUnix int64) time.Duration {
	if !enabled {
		return themeAutoUpdateIdleWait
	}
	if nextUnix <= 0 {
		return 0
	}
	delay := time.Unix(nextUnix, 0).Sub(now)
	if delay < time.Second {
		return 0
	}
	return delay
}

func themeAutoUpdateNextUnix(now time.Time, hours int) int64 {
	return now.Add(time.Duration(hours) * time.Hour).Unix()
}

func shouldResetThemeAutoUpdate(prevEnabled bool, prevHours int, enabled bool, hours int) bool {
	return enabled && (!prevEnabled || prevHours != hours)
}

func themeUpdateAuditParams(summary ThemeUpdateSummary) map[string]string {
	return map[string]string{
		"updated": strconv.Itoa(summary.Updated),
		"current": strconv.Itoa(summary.Current),
		"skipped": strconv.Itoa(summary.Skipped),
		"failed":  strconv.Itoa(summary.Failed),
	}
}

func themeAutoUpdateAuditEvent(prevEnabled bool, prevHours int, enabled bool, hours int) (string, map[string]string) {
	if enabled && !prevEnabled {
		return "audit.theme_auto_update_on", map[string]string{"hours": strconv.Itoa(hours)}
	}
	if !enabled && prevEnabled {
		return "audit.theme_auto_update_off", nil
	}
	if enabled && prevHours != hours {
		return "audit.theme_auto_update_interval", map[string]string{"hours": strconv.Itoa(hours)}
	}
	return "", nil
}

func adminUUID(c *gin.Context) string {
	uuid, _ := c.Get("uuid")
	if s, ok := uuid.(string); ok {
		return s
	}
	if uuid == nil {
		return ""
	}
	return fmt.Sprint(uuid)
}

func beginThemeUpdateAll() bool {
	themeUpdateAllMu.Lock()
	defer themeUpdateAllMu.Unlock()
	if themeUpdateAllBusy {
		return false
	}
	themeUpdateAllBusy = true
	return true
}

func endThemeUpdateAll() {
	themeUpdateAllMu.Lock()
	themeUpdateAllBusy = false
	themeUpdateAllMu.Unlock()
}

func updateAllInstalledThemes() (ThemeUpdateSummary, error) {
	if !beginThemeUpdateAll() {
		return ThemeUpdateSummary{}, errThemeUpdateBusy
	}
	defer endThemeUpdateAll()

	themes, err := installedThemes()
	if err != nil {
		return ThemeUpdateSummary{}, err
	}
	summary := ThemeUpdateSummary{Results: make([]ThemeUpdateItem, 0, len(themes))}
	for _, theme := range themes {
		item := updateInstalledThemeToLatest(theme)
		summary.Results = append(summary.Results, item)
		switch item.Status {
		case themeUpdateStatusUpdated:
			summary.Updated++
		case themeUpdateStatusCurrent:
			summary.Current++
		case themeUpdateStatusSkipped:
			summary.Skipped++
		default:
			summary.Failed++
		}
	}
	return summary, nil
}

func updateInstalledThemeToLatest(theme models.Theme) ThemeUpdateItem {
	item := ThemeUpdateItem{Short: theme.Short, Name: theme.Name, Version: theme.Version}
	if strings.TrimSpace(theme.URL) == "" {
		item.Status = themeUpdateStatusSkipped
		item.Message = errThemeNoSource.Error()
		return item
	}

	downloadURL := strings.TrimSpace(theme.URL)
	if isGitHub, owner, repo := isGitHubRepoURL(downloadURL); isGitHub {
		release, err := getGitHubLatestRelease(owner, repo)
		if err != nil {
			item.Status = themeUpdateStatusFailed
			item.Message = "从GitHub获取下载链接失败: " + err.Error()
			return item
		}
		if themeVersionsEqual(theme.Version, release.TagName) {
			item.Status = themeUpdateStatusCurrent
			item.Message = "已是最新版本"
			return item
		}
		if strings.TrimSpace(release.DownloadURL) == "" {
			item.Status = themeUpdateStatusFailed
			item.Message = "GitHub release中没有可下载的资源"
			return item
		}
		downloadURL = release.DownloadURL
	}

	archivePath, err := downloadThemeArchive(downloadURL)
	if err != nil {
		item.Status = themeUpdateStatusFailed
		item.Message = "下载主题失败: " + err.Error()
		return item
	}
	defer os.Remove(archivePath)

	downloaded, err := peekThemeFromZip(archivePath)
	if err != nil {
		item.Status = themeUpdateStatusFailed
		item.Message = err.Error()
		return item
	}
	if downloaded.Short != theme.Short {
		item.Status = themeUpdateStatusFailed
		item.Message = "更新包主题标识与当前主题不一致"
		return item
	}
	if themeVersionsEqual(downloaded.Version, theme.Version) {
		item.Status = themeUpdateStatusCurrent
		item.Version = theme.Version
		item.Message = "已是最新版本"
		return item
	}

	updated, err := extractAndValidateTheme(archivePath)
	if err != nil {
		item.Status = themeUpdateStatusFailed
		item.Message = err.Error()
		return item
	}
	item.Status = themeUpdateStatusUpdated
	item.Name = updated.Name
	item.Version = updated.Version
	item.Message = "主题更新成功"
	return item
}

// UpdateAllThemes updates every installed theme that has a source URL.
func UpdateAllThemes(c *gin.Context) {
	summary, err := updateAllInstalledThemes()
	actor := adminUUID(c)
	if errors.Is(err, errThemeUpdateBusy) {
		api.RespondError(c, http.StatusConflict, "已有主题更新正在进行")
		return
	}
	if err != nil {
		auditlog.Event(c.ClientIP(), actor, "error", "audit.theme_update_all_fail", map[string]string{"error": err.Error()})
		api.RespondError(c, http.StatusInternalServerError, "读取主题目录失败: "+err.Error())
		return
	}
	auditlog.Event(c.ClientIP(), actor, "info", "audit.theme_update_all", themeUpdateAuditParams(summary))
	api.RespondSuccessMessage(c, "主题更新完成", summary)
}

func loadThemeAutoUpdateView() themeAutoUpdateView {
	view := themeAutoUpdateView{
		IntervalHours: config.ThemeAutoUpdateDefaultIntervalHours,
	}
	if enabled, err := config.GetAs[bool](config.ThemeAutoUpdateEnabledKey); err == nil {
		view.Enabled = enabled
	}
	if hours, err := config.GetAs[int](config.ThemeAutoUpdateIntervalHoursKey); err == nil {
		if normalized, normalizeErr := normalizeThemeAutoUpdateHours(hours); normalizeErr == nil {
			view.IntervalHours = normalized
		}
	}
	if view.Enabled {
		if next, err := config.GetAs[int64](config.ThemeAutoUpdateNextUnixKey); err == nil && next > 0 {
			view.NextRunAt = next
		}
	}
	if last, err := config.GetAs[int64](config.ThemeAutoUpdateLastUnixKey); err == nil && last > 0 {
		view.LastRunAt = last
	}
	if summary, err := config.GetAs[ThemeUpdateSummary](config.ThemeAutoUpdateLastSummaryKey); err == nil && view.LastRunAt > 0 {
		view.LastSummary = &summary
	}
	return view
}

// GetThemeAutoUpdate returns the saved automatic theme update schedule.
func GetThemeAutoUpdate(c *gin.Context) {
	api.RespondSuccess(c, loadThemeAutoUpdateView())
}

// SetThemeAutoUpdate saves whether themes update on a schedule, and the interval.
func SetThemeAutoUpdate(c *gin.Context) {
	var req struct {
		Enabled       bool `json:"enabled"`
		IntervalHours int  `json:"interval_hours"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		api.RespondError(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}
	hours, err := normalizeThemeAutoUpdateHours(req.IntervalHours)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, err.Error())
		return
	}

	prev := loadThemeAutoUpdateView()
	values := map[string]any{
		config.ThemeAutoUpdateEnabledKey:       req.Enabled,
		config.ThemeAutoUpdateIntervalHoursKey: hours,
	}
	if req.Enabled {
		values[config.ThemeAutoUpdateNextUnixKey] = themeAutoUpdateNextUnix(time.Now(), hours)
	} else {
		values[config.ThemeAutoUpdateNextUnixKey] = int64(0)
	}
	if err := config.SetMany(values); err != nil {
		api.RespondError(c, http.StatusInternalServerError, "保存自动更新设置失败: "+err.Error())
		return
	}
	if key, params := themeAutoUpdateAuditEvent(prev.Enabled, prev.IntervalHours, req.Enabled, hours); key != "" {
		auditlog.Event(c.ClientIP(), adminUUID(c), "info", key, params)
	}
	notifyThemeAutoUpdate()
	api.RespondSuccessMessage(c, "自动更新设置已保存", loadThemeAutoUpdateView())
}

func notifyThemeAutoUpdate() {
	select {
	case themeAutoUpdateWake <- struct{}{}:
	default:
	}
}

func themeAutoUpdateEnabled() bool {
	enabled, err := config.GetAs[bool](config.ThemeAutoUpdateEnabledKey)
	return err == nil && enabled
}

func themeAutoUpdateIntervalHours() int {
	hours, err := config.GetAs[int](config.ThemeAutoUpdateIntervalHoursKey)
	if err != nil {
		return config.ThemeAutoUpdateDefaultIntervalHours
	}
	normalized, normalizeErr := normalizeThemeAutoUpdateHours(hours)
	if normalizeErr != nil {
		return config.ThemeAutoUpdateDefaultIntervalHours
	}
	return normalized
}

func themeAutoUpdateNext() int64 {
	next, err := config.GetAs[int64](config.ThemeAutoUpdateNextUnixKey)
	if err != nil {
		return 0
	}
	return next
}

// StartThemeAutoUpdate runs the background schedule until the returned stop function is called.
func StartThemeAutoUpdate() func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	config.Subscribe(func(event config.ConfigEvent) {
		if event.IsChanged(config.ThemeAutoUpdateEnabledKey) || event.IsChanged(config.ThemeAutoUpdateIntervalHoursKey) {
			notifyThemeAutoUpdate()
		}
	})
	go runThemeAutoUpdateLoop(stop, done)
	return func() {
		close(stop)
		select {
		case <-done:
		case <-time.After(8 * time.Second):
			logger.Infof("theme", "theme auto update did not stop before shutdown timeout")
		}
	}
}

func runThemeAutoUpdateLoop(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	var prevEnabled bool
	var prevHours int
	loaded := false
	for {
		enabled := themeAutoUpdateEnabled()
		hours := themeAutoUpdateIntervalHours()
		if !loaded {
			loaded = true
			prevEnabled = enabled
			prevHours = hours
		} else if shouldResetThemeAutoUpdate(prevEnabled, prevHours, enabled, hours) {
			if err := config.Set(config.ThemeAutoUpdateNextUnixKey, themeAutoUpdateNextUnix(time.Now(), hours)); err != nil {
				logger.Errorf("theme", "failed to reschedule theme auto update: %v", err)
			}
			prevEnabled = true
			prevHours = hours
		} else {
			prevEnabled = enabled
			prevHours = hours
		}

		delay := themeAutoUpdateDelay(time.Now(), enabled, themeAutoUpdateNext())
		timer := time.NewTimer(delay)
		select {
		case <-stop:
			timer.Stop()
			return
		case <-themeAutoUpdateWake:
			timer.Stop()
		case <-timer.C:
			if enabled {
				runThemeAutoUpdate()
			}
		}
	}
}

func runThemeAutoUpdate() {
	if !themeAutoUpdateEnabled() {
		return
	}
	summary, err := updateAllInstalledThemes()
	now := time.Now()
	hours := themeAutoUpdateIntervalHours()
	next := themeAutoUpdateNextUnix(now, hours)
	if errors.Is(err, errThemeUpdateBusy) {
		next = now.Add(themeAutoUpdateBusyDelay).Unix()
		if setErr := config.Set(config.ThemeAutoUpdateNextUnixKey, next); setErr != nil {
			logger.Errorf("theme", "failed to postpone theme auto update: %v", setErr)
			time.Sleep(time.Minute)
		}
		logger.Infof("theme", "theme auto update skipped because another update is running")
		return
	}
	values := map[string]any{
		config.ThemeAutoUpdateNextUnixKey: next,
		config.ThemeAutoUpdateLastUnixKey: now.Unix(),
	}
	if err != nil {
		auditlog.Event("", "", "error", "audit.theme_auto_update_fail", map[string]string{"error": err.Error()})
		logger.Errorf("theme", "theme auto update failed: %v", err)
	} else {
		values[config.ThemeAutoUpdateLastSummaryKey] = summary
		auditlog.Event("", "", "info", "audit.theme_auto_update_run", themeUpdateAuditParams(summary))
		logger.Infof("theme", "theme auto update finished: updated=%d current=%d skipped=%d failed=%d", summary.Updated, summary.Current, summary.Skipped, summary.Failed)
	}
	if setErr := config.SetMany(values); setErr != nil {
		logger.Errorf("theme", "failed to save theme auto update result: %v", setErr)
		time.Sleep(time.Minute)
	}
}
