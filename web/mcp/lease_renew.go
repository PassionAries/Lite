package mcp

import (
	"strings"
	"sync"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
	agent_runtime "github.com/nuomiiiii/lite/web/agent"
	"github.com/nuomiiiii/lite/web/api/remote"
)

const executionLeaseWindow = 15 * time.Second

var mcpBGOnce sync.Once

func startMCPBackground() {
	mcpBGOnce.Do(func() {
		go runExecutionLeaseRenewal()
	})
}

func runExecutionLeaseRenewal() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		renewRunningExecutionLeases()
	}
}

func renewRunningExecutionLeases() {
	if !mcpEnabled() || !remote.RemoteManagementEnabled() {
		return
	}
	now := time.Now().UTC()
	var ops []models.MCPOperation
	_ = database().Where("state IN ? AND finished_at IS NULL", []string{opAccepted, opRunning, opCancelRequested}).Find(&ops).Error
	grouped := map[string][]models.MCPOperation{}
	for _, op := range ops {
		key := op.LeaseID + "\n" + op.AgentUUID
		grouped[key] = append(grouped[key], op)
	}
	for key, group := range grouped {
		leaseID, agentUUID, _ := strings.Cut(key, "\n")
		if !groupHasLiveOperation(group, now) {
			continue
		}
		lease, err := loadLiveLease(leaseID, now)
		if err != nil || !leaseContainsNode(lease, agentUUID) {
			continue
		}
		deadline := minTime(lease.ExpiresAt, now.Add(executionLeaseWindow))
		if deadline.IsZero() || !deadline.After(now) {
			continue
		}
		agent_runtime.DispatchV2Event(agentUUID, v2.MethodAgentMCPRenew, v2.MCPRenewParams{
			LeaseID:                lease.ID,
			ExecutionLeaseDeadline: deadline,
		})
	}
}

func groupHasLiveOperation(ops []models.MCPOperation, now time.Time) bool {
	for _, op := range ops {
		if op.Deadline.After(now) {
			return true
		}
	}
	return false
}
