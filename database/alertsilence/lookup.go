package alertsilence

import (
	"strings"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"gorm.io/gorm"
)

const (
	KindOffline     = models.AlertKindOffline
	KindLoad        = models.AlertKindLoad
	KindLatency     = models.AlertKindLatency
	KindReturnRoute = models.AlertKindReturnRoute
)

func Active(until *time.Time, forever bool, now time.Time) bool {
	return forever || (until != nil && until.After(now))
}

func IsSilencedWithDB(db *gorm.DB, kind, target string, now time.Time) bool {
	if db == nil {
		return false
	}
	var row models.AlertSilence
	err := db.Where("kind = ? AND target = ?", strings.TrimSpace(kind), strings.TrimSpace(target)).First(&row).Error
	if err != nil {
		return false
	}
	return Active(row.SilencedUntil, row.SilencedForever, now)
}
