package notification

import (
	"fmt"
	"testing"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newAlertCenterTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:alert-center-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Client{},
		&models.LoadNotification{},
		&models.LoadNotificationState{},
		&models.OfflineNotification{},
		&models.PingTask{},
		&models.PingLossNotification{},
		&models.ReturnRouteTask{},
		&models.ReturnRouteStatus{},
		&models.AlertSilence{},
	))
	return db
}

func TestGenericSilenceModes(t *testing.T) {
	db := newAlertCenterTestDB(t)
	now := time.Date(2026, 10, 16, 2, 0, 0, 0, time.UTC)

	require.NoError(t, SetGenericSilence(db, KindLatency, "12", "node / task", "24h", now))
	assert.True(t, IsSilencedWithDB(db, KindLatency, "12", now.Add(time.Hour)))
	assert.False(t, IsSilencedWithDB(db, KindLatency, "12", now.Add(25*time.Hour)))

	require.NoError(t, SetGenericSilence(db, KindLatency, "12", "node / task", "forever", now))
	assert.True(t, IsSilencedWithDB(db, KindLatency, "12", now.Add(365*24*time.Hour)))

	require.NoError(t, SetGenericSilence(db, KindLatency, "12", "node / task", "off", now))
	assert.False(t, IsSilencedWithDB(db, KindLatency, "12", now.Add(time.Minute)))
}

func TestAlertCenterListsCurrentAndLeftoverSilence(t *testing.T) {
	db := newAlertCenterTestDB(t)
	now := time.Date(2026, 10, 16, 2, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create(&models.Client{UUID: "node-a", Token: "token-a", Name: "Alpha"}).Error)
	require.NoError(t, db.Create(&models.OfflineNotification{Client: "node-a", Enable: true}).Error)
	require.NoError(t, db.Create(&models.PingTask{Name: "Ping SG", Type: "icmp", Target: "1.1.1.1"}).Error)
	require.NoError(t, db.Create(&models.PingLossNotification{
		Client: "node-a", TaskId: 1, Enable: true, LossEnabled: true, AlertActive: true,
	}).Error)
	require.NoError(t, db.Create(&models.ReturnRouteTask{
		Name: "LAX", Client: "node-a", Enabled: true, Carrier: "CMI", Region: "LAX",
		Target: "1.1.1.1", ExpectedLine: "CMI",
	}).Error)
	require.NoError(t, db.Create(&models.ReturnRouteStatus{TaskId: 1, State: "switched", CurrentLine: "CMI"}).Error)
	require.NoError(t, SetGenericSilence(db, KindOffline, "node-b", "Beta", "forever", now))

	items, err := listAlertCenter(db, now, map[string]struct{}{})
	require.NoError(t, err)

	kinds := map[string]int{}
	var leftover bool
	for _, item := range items {
		kinds[item.Kind]++
		if item.Kind == KindOffline && item.Target == "node-a" {
			assert.True(t, item.Current)
			assert.False(t, item.Silenced)
		}
		if item.Kind == KindOffline && item.Target == "node-b" {
			leftover = true
			assert.False(t, item.Current)
			assert.True(t, item.SilencedForever)
		}
		if item.Kind == KindLatency {
			assert.Equal(t, "1", item.Target)
			assert.True(t, item.Current)
		}
		if item.Kind == KindReturnRoute {
			assert.Equal(t, "1", item.Target)
			assert.True(t, item.Current)
		}
	}
	assert.GreaterOrEqual(t, kinds[KindOffline], 2)
	assert.Equal(t, 1, kinds[KindLatency])
	assert.Equal(t, 1, kinds[KindReturnRoute])
	assert.True(t, leftover)

	require.NoError(t, setAlertCenterSilence(db, KindLatency, "1", "Alpha / Ping SG", "7d", now))
	assert.True(t, IsSilencedWithDB(db, KindLatency, "1", now.Add(time.Hour)))
}

func TestAlertCenterUnsilenceLoadLeftover(t *testing.T) {
	db := newAlertCenterTestDB(t)
	now := time.Now().UTC()
	require.NoError(t, db.Create(&models.Client{UUID: "client-a", Token: "token-a", Name: "Alpha"}).Error)
	rule := models.LoadNotification{Name: "CPU", Clients: models.StringArray{"client-a"}, Metric: "cpu", Threshold: 80, Ratio: 0.8, Interval: 15}
	require.NoError(t, db.Create(&rule).Error)
	until := now.Add(24 * time.Hour)
	require.NoError(t, db.Create(&models.LoadNotificationState{
		NotificationID: rule.Id, Client: "client-a", AlertActive: false,
		LastEvaluatedAt: now, SilencedUntil: &until,
	}).Error)

	items, err := listAlertCenter(db, now, map[string]struct{}{"client-a": {}})
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, KindLoad, items[0].Kind)
	assert.False(t, items[0].Current)
	assert.True(t, items[0].Silenced)

	require.NoError(t, setAlertCenterSilence(db, KindLoad, LoadSilenceTarget(rule.Id, "client-a"), "", "off", now))
	items, err = listAlertCenter(db, now, map[string]struct{}{"client-a": {}})
	require.NoError(t, err)
	assert.Empty(t, items)
}
