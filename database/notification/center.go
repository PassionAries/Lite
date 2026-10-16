package notification

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/gorm"
)

type AlertCenterItem struct {
	Kind            string     `json:"kind"`
	ID              string     `json:"id"`
	Target          string     `json:"target"`
	Title           string     `json:"title"`
	Summary         string     `json:"summary,omitempty"`
	NodeUUID        string     `json:"node_uuid,omitempty"`
	NodeName        string     `json:"node_name,omitempty"`
	TaskID          uint       `json:"task_id,omitempty"`
	TaskName        string     `json:"task_name,omitempty"`
	OccurredAt      *time.Time `json:"occurred_at,omitempty"`
	Href            string     `json:"href"`
	Silenced        bool       `json:"silenced"`
	SilencedUntil   *time.Time `json:"silenced_until,omitempty"`
	SilencedForever bool       `json:"silenced_forever"`
	Current         bool       `json:"current"`
}

func ListAlertCenter(now time.Time, online map[string]struct{}) ([]AlertCenterItem, error) {
	return listAlertCenter(dbcore.GetDBInstance(), now, online)
}

func SetAlertCenterSilence(kind, target, label, mode string, now time.Time) error {
	return setAlertCenterSilence(dbcore.GetDBInstance(), kind, target, label, mode, now)
}

func LoadSilenceTarget(notificationID uint, client string) string {
	return fmt.Sprintf("%d:%s", notificationID, strings.TrimSpace(client))
}

func ParseLoadSilenceTarget(target string) (uint, string, error) {
	idText, client, ok := strings.Cut(strings.TrimSpace(target), ":")
	if !ok || strings.TrimSpace(client) == "" {
		return 0, "", fmt.Errorf("invalid load target")
	}
	id, err := strconv.ParseUint(idText, 10, 64)
	if err != nil || id == 0 {
		return 0, "", fmt.Errorf("invalid load target")
	}
	return uint(id), strings.TrimSpace(client), nil
}

func listAlertCenter(db *gorm.DB, now time.Time, online map[string]struct{}) ([]AlertCenterItem, error) {
	if online == nil {
		online = map[string]struct{}{}
	}
	items := make([]AlertCenterItem, 0)
	seen := make(map[string]struct{})

	loadAlerts, err := listCurrentLoadAlerts(db, now)
	if err != nil {
		return nil, err
	}
	for _, alert := range loadAlerts {
		item := loadAlertCenterItem(alert, true)
		items = append(items, item)
		seen[silenceKey(item.Kind, item.Target)] = struct{}{}
	}

	offlineItems, err := listOfflineAlertCenter(db, online)
	if err != nil {
		return nil, err
	}
	items = append(items, offlineItems...)
	for _, item := range offlineItems {
		seen[silenceKey(item.Kind, item.Target)] = struct{}{}
	}

	latencyItems, err := listLatencyAlertCenter(db)
	if err != nil {
		return nil, err
	}
	items = append(items, latencyItems...)
	for _, item := range latencyItems {
		seen[silenceKey(item.Kind, item.Target)] = struct{}{}
	}

	returnItems, err := listReturnRouteAlertCenter(db)
	if err != nil {
		return nil, err
	}
	items = append(items, returnItems...)
	for _, item := range returnItems {
		seen[silenceKey(item.Kind, item.Target)] = struct{}{}
	}

	silences, err := listActiveGenericSilences(db, now)
	if err != nil {
		return nil, err
	}
	silenceRows := silenceLookup(silences)
	for i := range items {
		if items[i].Kind != KindLoad {
			applyGenericSilence(&items[i], silenceRows, now)
		}
	}

	leftoverLoad, err := listLeftoverLoadSilences(db, now, seen)
	if err != nil {
		return nil, err
	}
	items = append(items, leftoverLoad...)

	for _, row := range silences {
		key := silenceKey(row.Kind, row.Target)
		if _, exists := seen[key]; exists {
			continue
		}
		items = append(items, leftoverGenericSilence(row))
	}
	return items, nil
}

func setAlertCenterSilence(db *gorm.DB, kind, target, label, mode string, now time.Time) error {
	kind = strings.TrimSpace(kind)
	target = strings.TrimSpace(target)
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch kind {
	case KindLoad:
		notificationID, client, err := ParseLoadSilenceTarget(target)
		if err != nil {
			return err
		}
		if mode == "off" {
			return clearLoadAlertSilence(db, notificationID, client)
		}
		return setLoadAlertSilence(db, notificationID, client, mode, now)
	case KindOffline, KindLatency, KindReturnRoute:
		return SetGenericSilence(db, kind, target, label, mode, now)
	default:
		return fmt.Errorf("unsupported alert kind")
	}
}

func loadAlertCenterItem(alert CurrentLoadAlert, current bool) AlertCenterItem {
	summary := strings.TrimSpace(alert.Metric)
	if summary != "" {
		summary = strings.ToUpper(summary)
	}
	return AlertCenterItem{
		Kind:            KindLoad,
		ID:              "load:" + LoadSilenceTarget(alert.NotificationID, alert.Client),
		Target:          LoadSilenceTarget(alert.NotificationID, alert.Client),
		Title:           strings.TrimSpace(alert.NotificationName),
		Summary:         summary,
		NodeUUID:        alert.Client,
		NodeName:        alert.ClientName,
		OccurredAt:      alert.ActiveSince,
		Href:            "/admin/notification/load",
		Silenced:        alert.Silenced,
		SilencedUntil:   alert.SilencedUntil,
		SilencedForever: alert.SilencedForever,
		Current:         current,
	}
}

func listLeftoverLoadSilences(db *gorm.DB, now time.Time, seen map[string]struct{}) ([]AlertCenterItem, error) {
	var states []models.LoadNotificationState
	if err := db.Preload("Notification").Preload("ClientInfo").
		Where("silenced_forever = ? OR silenced_until > ?", true, now.UTC()).
		Find(&states).Error; err != nil {
		return nil, err
	}
	items := make([]AlertCenterItem, 0)
	for _, state := range states {
		if !SilenceActive(state.SilencedUntil, state.SilencedForever, now) {
			continue
		}
		target := LoadSilenceTarget(state.NotificationID, state.Client)
		if _, exists := seen[silenceKey(KindLoad, target)]; exists {
			continue
		}
		until := state.SilencedUntil
		if !state.SilencedForever && (until == nil || !until.After(now)) {
			until = nil
		}
		item := AlertCenterItem{
			Kind:            KindLoad,
			ID:              "load-silence:" + target,
			Target:          target,
			Title:           strings.TrimSpace(state.Notification.Name),
			Summary:         strings.ToUpper(strings.TrimSpace(state.Notification.Metric)),
			NodeUUID:        state.Client,
			NodeName:        state.ClientInfo.Name,
			Href:            "/admin/notification/load",
			Silenced:        true,
			SilencedUntil:   until,
			SilencedForever: state.SilencedForever,
			Current:         false,
		}
		items = append(items, item)
		seen[silenceKey(KindLoad, target)] = struct{}{}
	}
	return items, nil
}

func listOfflineAlertCenter(db *gorm.DB, online map[string]struct{}) ([]AlertCenterItem, error) {
	var notifications []models.OfflineNotification
	if err := db.Preload("ClientInfo").Where("enable = ?", true).Find(&notifications).Error; err != nil {
		return nil, err
	}
	items := make([]AlertCenterItem, 0)
	for _, row := range notifications {
		if _, isOnline := online[row.Client]; isOnline {
			continue
		}
		name := strings.TrimSpace(row.ClientInfo.Name)
		if name == "" {
			name = row.Client
		}
		items = append(items, AlertCenterItem{
			Kind:       KindOffline,
			ID:         "offline:" + row.Client,
			Target:     row.Client,
			Title:      name,
			NodeUUID:   row.Client,
			NodeName:   name,
			OccurredAt: row.LastNotified,
			Href:       "/admin/servers/" + row.Client,
			Current:    true,
		})
	}
	return items, nil
}

func listLatencyAlertCenter(db *gorm.DB) ([]AlertCenterItem, error) {
	var notifications []models.PingLossNotification
	if err := db.Preload("ClientInfo").Preload("Task").Where("enable = ?", true).Find(&notifications).Error; err != nil {
		return nil, err
	}
	items := make([]AlertCenterItem, 0)
	for _, row := range notifications {
		target := strconv.FormatUint(uint64(row.Id), 10)
		nodeName := strings.TrimSpace(row.ClientInfo.Name)
		if nodeName == "" {
			nodeName = row.Client
		}
		taskName := strings.TrimSpace(row.Task.Name)
		href := fmt.Sprintf("/admin/notification/ping-loss?node=%s&task=%d", row.Client, row.TaskId)
		if row.LossEnabled && row.AlertActive {
			items = append(items, AlertCenterItem{
				Kind:       KindLatency,
				ID:         "latency:loss:" + target,
				Target:     target,
				Title:      "丢包异常",
				Summary:    taskName,
				NodeUUID:   row.Client,
				NodeName:   nodeName,
				TaskID:     row.TaskId,
				TaskName:   taskName,
				OccurredAt: row.LastNotified,
				Href:       href,
				Current:    true,
			})
		}
		if row.LatencyAlerting() {
			title := "延迟过高"
			if row.NormalizedLatencyAlertState() == models.LatencyAlertLow {
				title = "延迟过低"
			}
			items = append(items, AlertCenterItem{
				Kind:       KindLatency,
				ID:         "latency:delay:" + target,
				Target:     target,
				Title:      title,
				Summary:    taskName,
				NodeUUID:   row.Client,
				NodeName:   nodeName,
				TaskID:     row.TaskId,
				TaskName:   taskName,
				OccurredAt: row.LatencyLastNotified,
				Href:       href,
				Current:    true,
			})
		}
	}
	return items, nil
}

func listReturnRouteAlertCenter(db *gorm.DB) ([]AlertCenterItem, error) {
	var tasks []models.ReturnRouteTask
	switched := db.Model(&models.ReturnRouteStatus{}).Select("task_id").Where("state = ?", "switched")
	if err := db.Preload("ClientInfo").Where("enabled = ? AND id IN (?)", true, switched).Order("id ASC").Find(&tasks).Error; err != nil {
		return nil, err
	}
	items := make([]AlertCenterItem, 0, len(tasks))
	for _, task := range tasks {
		target := strconv.FormatUint(uint64(task.Id), 10)
		name := strings.TrimSpace(task.Name)
		if name == "" {
			name = target
		}
		nodeName := strings.TrimSpace(task.ClientInfo.Name)
		items = append(items, AlertCenterItem{
			Kind:     KindReturnRoute,
			ID:       "return_route:" + target,
			Target:   target,
			Title:    name,
			NodeUUID: task.Client,
			NodeName: nodeName,
			TaskID:   task.Id,
			TaskName: name,
			Href:     "/admin/return-route?task=" + target,
			Current:  true,
		})
	}
	return items, nil
}

func leftoverGenericSilence(row models.AlertSilence) AlertCenterItem {
	label := strings.TrimSpace(row.Label)
	if label == "" {
		label = row.Target
	}
	href := "/admin/notification/center"
	switch row.Kind {
	case KindOffline:
		href = "/admin/servers/" + row.Target
	case KindLatency:
		href = "/admin/notification/ping-loss?state=active"
	case KindReturnRoute:
		href = "/admin/return-route?task=" + row.Target
	}
	until := row.SilencedUntil
	if row.SilencedForever {
		until = nil
	}
	return AlertCenterItem{
		Kind:            row.Kind,
		ID:              row.Kind + "-silence:" + row.Target,
		Target:          row.Target,
		Title:           label,
		Href:            href,
		Silenced:        true,
		SilencedUntil:   until,
		SilencedForever: row.SilencedForever,
		Current:         false,
	}
}
