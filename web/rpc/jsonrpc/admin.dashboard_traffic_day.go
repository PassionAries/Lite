package jsonrpc

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/database/trafficledger"
	"github.com/nuomiiiii/lite/pkg/rpc"
	publicweb "github.com/nuomiiiii/lite/web/public"
)

const dashboardTodayTrafficTTL = 2 * time.Minute

type dashboardTodayTrafficSnapshot struct {
	day   string
	at    time.Time
	items []dashboardTrafficRankItem
}

var (
	dashboardTodayTrafficMu sync.Mutex
	dashboardTodayTraffic   dashboardTodayTrafficSnapshot
)

func fillDashboardTrafficPeriod(summary *dashboardTrafficSummary) {
	if summary == nil {
		return
	}
	for _, day := range summary.Daily {
		summary.PeriodUp += day.Up
		summary.PeriodDown += day.Down
		summary.PeriodBillable += day.Billable
	}
	summary.PeriodDays = len(summary.Daily)
	if summary.PeriodDays <= 0 {
		return
	}
	days := int64(summary.PeriodDays)
	summary.DailyAverageUp = summary.PeriodUp / days
	summary.DailyAverageDown = summary.PeriodDown / days
	summary.DailyAverageBillable = summary.PeriodBillable / days
}

func storeDashboardTodayTraffic(day string, items []dashboardTrafficRankItem) {
	cloned := append([]dashboardTrafficRankItem(nil), items...)
	dashboardTodayTrafficMu.Lock()
	dashboardTodayTraffic = dashboardTodayTrafficSnapshot{
		day:   day,
		at:    time.Now().UTC(),
		items: cloned,
	}
	dashboardTodayTrafficMu.Unlock()
}

func loadCachedDashboardTodayTraffic(day string, now time.Time) ([]dashboardTrafficRankItem, bool) {
	dashboardTodayTrafficMu.Lock()
	defer dashboardTodayTrafficMu.Unlock()
	if dashboardTodayTraffic.day != day || len(dashboardTodayTraffic.items) == 0 {
		return nil, false
	}
	if now.Sub(dashboardTodayTraffic.at) > dashboardTodayTrafficTTL {
		return nil, false
	}
	return append([]dashboardTrafficRankItem(nil), dashboardTodayTraffic.items...), true
}

func adminGetDashboardTrafficDay(ctx context.Context, req *rpc.JsonRpcRequest) (any, *rpc.JsonRpcError) {
	rawDay, _ := rpc.GetParamAs[string](req, "day")
	now := time.Now().UTC()
	day, err := parseDashboardTrafficDay(rawDay, now, dashboardClock())
	if err != nil {
		return nil, rpc.MakeError(rpc.InvalidParams, err.Error(), nil)
	}
	items, err := loadDashboardTrafficDay(ctx, day, now)
	if err != nil {
		return nil, rpc.MakeError(rpc.InternalError, err.Error(), nil)
	}
	navigation := publicweb.ActiveThemeNavigation()
	for index := range items {
		items[index].DetailURL = navigation.ServerDetailURL(items[index].UUID, 0)
	}
	return dashboardTrafficDayResponse{
		Day:         day.Format(time.DateOnly),
		Items:       items,
		GeneratedAt: now,
	}, nil
}

func parseDashboardTrafficDay(raw string, now time.Time, loc *time.Location) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, fmt.Errorf("day is required")
	}
	if loc == nil {
		loc = trafficledger.BeijingLocation
	}
	day, err := time.ParseInLocation(time.DateOnly, raw, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid day")
	}
	today := dashboardDay(now, loc)
	start := today.AddDate(0, 0, -(trafficledger.DashboardHistoryDays - 1))
	if day.Before(start) || day.After(today) {
		return time.Time{}, fmt.Errorf("day is outside the dashboard window")
	}
	return day, nil
}

func loadDashboardTrafficDay(ctx context.Context, day, now time.Time) ([]dashboardTrafficRankItem, error) {
	clientList, err := listDashboardTrafficClients(ctx)
	if err != nil {
		return nil, fmt.Errorf("list dashboard traffic clients: %w", err)
	}
	key := day.Format(time.DateOnly)
	today := dashboardDay(now, day.Location())
	if day.Equal(today) {
		if items, ok := loadCachedDashboardTodayTraffic(key, now); ok {
			return items, nil
		}
	}
	usage, err := loadDashboardTrafficDayUsage(ctx, clientList, day, today, now)
	if err != nil {
		return nil, err
	}
	adjustments, err := trafficledger.DailyAdjustments(ctx, dbcore.GetDBInstance(), day, day.AddDate(0, 0, 1))
	if err != nil {
		return nil, fmt.Errorf("read dashboard traffic calibration: %w", err)
	}
	return dashboardTrafficDayItems(clientList, usage, adjustments, key), nil
}

func listDashboardTrafficClients(ctx context.Context) ([]models.Client, error) {
	var list []models.Client
	err := dbcore.GetDBInstance().WithContext(ctx).
		Select("uuid", "name", "price", "traffic_limit_type", "ipv4", "ipv6", "group", "tags").
		Order("name ASC").Order("uuid ASC").
		Find(&list).Error
	if err != nil {
		return nil, err
	}
	return list, nil
}

func loadDashboardTrafficDayUsage(
	ctx context.Context,
	clientList []models.Client,
	day, today, now time.Time,
) (map[string]trafficledger.Usage, error) {
	if day.Equal(today) {
		clientIDs := make([]string, 0, len(clientList))
		for _, client := range clientList {
			if client.UUID != "" {
				clientIDs = append(clientIDs, client.UUID)
			}
		}
		usage, _, err := trafficledger.MetricUsageByHourBatch(ctx, clientIDs, today.UTC(), now.UTC())
		if err != nil {
			return nil, fmt.Errorf("read today's dashboard traffic: %w", err)
		}
		return usage, nil
	}
	var rows []models.TrafficDailyLedger
	if err := dbcore.GetDBInstance().WithContext(ctx).
		Select("client", "up_bytes", "down_bytes").
		Where("day = ?", day.Format(time.DateOnly)).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read dashboard traffic ledger: %w", err)
	}
	usage := make(map[string]trafficledger.Usage, len(rows))
	for _, row := range rows {
		usage[row.Client] = trafficledger.Usage{Up: row.UpBytes, Down: row.DownBytes}
	}
	return usage, nil
}

func dashboardTrafficDayItems(
	clientList []models.Client,
	usage map[string]trafficledger.Usage,
	adjustments map[string]trafficledger.SignedUsage,
	day string,
) []dashboardTrafficRankItem {
	items := make([]dashboardTrafficRankItem, 0, len(clientList))
	for _, client := range clientList {
		applied := trafficledger.ApplyAdjustment(usage[client.UUID], adjustments[client.UUID+"\x00"+day])
		item := dashboardTrafficRankBase(client)
		item.Up = applied.Up
		item.Down = applied.Down
		item.Billable = trafficledger.BillableUsage(client.TrafficLimitType, applied.Up, applied.Down)
		items = append(items, item)
	}
	return items
}

func dashboardTrafficRankBase(client models.Client) dashboardTrafficRankItem {
	name := strings.TrimSpace(client.Name)
	if name == "" {
		name = client.UUID
	}
	return dashboardTrafficRankItem{
		UUID:  client.UUID,
		Name:  name,
		IPv4:  client.IPv4,
		IPv6:  client.IPv6,
		Group: client.Group,
		Tags:  client.Tags,
	}
}
