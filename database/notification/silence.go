package notification

import (
	"fmt"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/database/alertsilence"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	KindOffline     = models.AlertKindOffline
	KindLoad        = models.AlertKindLoad
	KindLatency     = models.AlertKindLatency
	KindReturnRoute = models.AlertKindReturnRoute
)

func ParseSilenceMode(mode string, now time.Time) (until *time.Time, forever bool, err error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "off":
		return nil, false, nil
	case "24h":
		value := now.UTC().Add(24 * time.Hour)
		return &value, false, nil
	case "3d":
		value := now.UTC().Add(3 * 24 * time.Hour)
		return &value, false, nil
	case "7d":
		value := now.UTC().Add(7 * 24 * time.Hour)
		return &value, false, nil
	case "forever":
		return nil, true, nil
	default:
		return nil, false, fmt.Errorf("unsupported silence mode")
	}
}

func SilenceActive(until *time.Time, forever bool, now time.Time) bool {
	return alertsilence.Active(until, forever, now)
}

func IsSilenced(kind, target string, now time.Time) bool {
	return alertsilence.IsSilencedWithDB(dbcore.GetDBInstance(), kind, target, now)
}

func IsSilencedWithDB(db *gorm.DB, kind, target string, now time.Time) bool {
	return alertsilence.IsSilencedWithDB(db, kind, target, now)
}

func SetGenericSilence(db *gorm.DB, kind, target, label, mode string, now time.Time) error {
	kind = strings.TrimSpace(kind)
	target = strings.TrimSpace(target)
	if kind == "" || target == "" {
		return fmt.Errorf("kind and target are required")
	}
	if kind != KindOffline && kind != KindLatency && kind != KindReturnRoute {
		return fmt.Errorf("unsupported alert kind")
	}
	until, forever, err := ParseSilenceMode(mode, now)
	if err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(mode), "off") {
		return db.Where("kind = ? AND target = ?", kind, target).Delete(&models.AlertSilence{}).Error
	}
	row := models.AlertSilence{
		Kind:            kind,
		Target:          target,
		Label:           strings.TrimSpace(label),
		SilencedUntil:   until,
		SilencedForever: forever,
		UpdatedAt:       now.UTC(),
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "kind"}, {Name: "target"}},
		DoUpdates: clause.AssignmentColumns([]string{"label", "silenced_until", "silenced_forever", "updated_at"}),
	}).Create(&row).Error
}

func listActiveGenericSilences(db *gorm.DB, now time.Time) ([]models.AlertSilence, error) {
	var rows []models.AlertSilence
	if err := db.Find(&rows).Error; err != nil {
		return nil, err
	}
	active := make([]models.AlertSilence, 0, len(rows))
	for _, row := range rows {
		if SilenceActive(row.SilencedUntil, row.SilencedForever, now) {
			active = append(active, row)
		}
	}
	return active, nil
}

func silenceLookup(rows []models.AlertSilence) map[string]models.AlertSilence {
	out := make(map[string]models.AlertSilence, len(rows))
	for _, row := range rows {
		out[silenceKey(row.Kind, row.Target)] = row
	}
	return out
}

func silenceKey(kind, target string) string {
	return kind + "\x1f" + target
}

func applyGenericSilence(item *AlertCenterItem, rows map[string]models.AlertSilence, now time.Time) {
	row, ok := rows[silenceKey(item.Kind, item.Target)]
	if !ok || !SilenceActive(row.SilencedUntil, row.SilencedForever, now) {
		return
	}
	item.Silenced = true
	item.SilencedForever = row.SilencedForever
	if !row.SilencedForever {
		item.SilencedUntil = row.SilencedUntil
	}
}

