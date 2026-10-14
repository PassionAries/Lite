package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/nuomiiiii/lite/database/models"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
	agent_runtime "github.com/nuomiiiii/lite/web/agent"
	"github.com/nuomiiiii/lite/web/api/remote"
	"gorm.io/gorm"
)

var (
	opWaitMu   sync.Mutex
	opWait     = map[string]chan struct{}{}
	createOpMu sync.Mutex
)

func waitOperation(id string) <-chan struct{} {
	opWaitMu.Lock()
	defer opWaitMu.Unlock()
	ch, ok := opWait[id]
	if !ok {
		ch = make(chan struct{})
		opWait[id] = ch
	}
	return ch
}

func signalOperation(id string) {
	opWaitMu.Lock()
	ch := opWait[id]
	delete(opWait, id)
	opWaitMu.Unlock()
	if ch != nil {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
}

func runningOperationCount(leaseID string) int {
	var count int64
	_ = database().Model(&models.MCPOperation{}).
		Where("lease_id = ? AND state IN ?", leaseID, []string{opAccepted, opRunning, opCancelRequested}).
		Count(&count).Error
	count += int64(remote.CountMCPLeaseSessions(leaseID))
	return int(count)
}

func createOperation(lease models.MCPLease, agentUUID, tool, idempotencyKey, digest string, deadline time.Time) (models.MCPOperation, bool, error) {
	return reserveOperation(lease, agentUUID, tool, idempotencyKey, digest, deadline, true)
}

func rememberIdempotentOperation(lease models.MCPLease, agentUUID, tool, idempotencyKey, digest string, deadline time.Time) (models.MCPOperation, bool, error) {
	return reserveOperation(lease, agentUUID, tool, idempotencyKey, digest, deadline, false)
}

func reserveOperation(lease models.MCPLease, agentUUID, tool, idempotencyKey, digest string, deadline time.Time, occupySlot bool) (models.MCPOperation, bool, error) {
	createOpMu.Lock()
	defer createOpMu.Unlock()
	now := time.Now().UTC()
	if idempotencyKey != "" {
		var existing models.MCPOperation
		err := database().Where("lease_id = ? AND idempotency_key = ?", lease.ID, idempotencyKey).First(&existing).Error
		if err == nil {
			if existing.RequestDigest != digest {
				return models.MCPOperation{}, false, errIdempotencyConflict
			}
			return existing, true, nil
		}
		if err != nil && err != gorm.ErrRecordNotFound {
			return models.MCPOperation{}, false, err
		}
	}
	if occupySlot && runningOperationCount(lease.ID) >= lease.MaxConcurrency {
		return models.MCPOperation{}, false, ErrConcurrency
	}
	id, err := newID("op_")
	if err != nil {
		return models.MCPOperation{}, false, err
	}
	op := models.MCPOperation{
		ID:             id,
		LeaseID:        lease.ID,
		AgentUUID:      agentUUID,
		ToolName:       tool,
		IdempotencyKey: idempotencyKey,
		RequestDigest:  digest,
		State:          opAccepted,
		Deadline:       deadline,
		CreatedAt:      now,
	}
	if err := database().Create(&op).Error; err != nil {
		if idempotencyKey != "" && isUniqueConflict(err) {
			var existing models.MCPOperation
			if findErr := database().Where("lease_id = ? AND idempotency_key = ?", lease.ID, idempotencyKey).First(&existing).Error; findErr == nil {
				if existing.RequestDigest != digest {
					return models.MCPOperation{}, false, errIdempotencyConflict
				}
				return existing, true, nil
			}
		}
		return models.MCPOperation{}, false, err
	}
	waitOperation(op.ID)
	return op, false, nil
}

func isUniqueConflict(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") || strings.Contains(msg, "duplicate")
}

var errIdempotencyConflict = errTool("idempotency key was reused with different arguments")

func CompleteOperation(taskID, agentUUID string, params v2.TaskResultParams) bool {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return false
	}
	var op models.MCPOperation
	if err := database().Where("id = ?", taskID).First(&op).Error; err != nil {
		return false
	}
	if op.AgentUUID != agentUUID {
		return false
	}
	agent_runtime.RemoveV2EventsByTaskID(agentUUID, taskID)
	if op.FinishedAt != nil && mcpResultWeak(params) {
		return true
	}
	if op.FinishedAt != nil && !mcpOutputWeak(op.Output) {
		return true
	}
	now := params.FinishedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	state := classifyTaskResult(params)
	output := params.Result
	truncated := false
	if len(output) > MaxOutputCache {
		output = output[:MaxOutputCache]
		truncated = true
	}
	started := op.StartedAt
	if started == nil {
		started = &now
	}
	_ = database().Model(&op).Updates(map[string]any{
		"state":       state,
		"exit_code":   params.ExitCode,
		"output":      output,
		"truncated":   truncated || op.Truncated,
		"started_at":  started,
		"finished_at": now,
	}).Error
	signalOperation(op.ID)
	return true
}

func mcpResultWeak(params v2.TaskResultParams) bool {
	status := strings.ToLower(strings.TrimSpace(params.Status))
	result := strings.ToLower(strings.TrimSpace(params.Result))
	return status == v2.TaskResultStatusInterrupted ||
		result == strings.ToLower(v2.DeliveryTimeoutTaskResult) ||
		result == "execution status unknown"
}

func mcpOutputWeak(output string) bool {
	result := strings.ToLower(strings.TrimSpace(output))
	return result == "" || result == strings.ToLower(v2.DeliveryTimeoutTaskResult) || result == "execution status unknown"
}

func classifyTaskResult(params v2.TaskResultParams) string {
	status := strings.ToLower(strings.TrimSpace(params.Status))
	result := strings.ToLower(params.Result)
	switch {
	case status == v2.TaskResultStatusInterrupted || strings.Contains(result, "unknown"):
		return opUnknown
	case strings.Contains(result, "cancelled"):
		return opCancelled
	case strings.Contains(result, "expired") || strings.Contains(result, "deadline"):
		return opExpired
	case params.ExitCode == 0:
		return opSucceeded
	default:
		return opFailed
	}
}

func markOperationRunning(id string) {
	now := time.Now().UTC()
	_ = database().Model(&models.MCPOperation{}).Where("id = ? AND state = ?", id, opAccepted).Updates(map[string]any{
		"state":      opRunning,
		"started_at": now,
	}).Error
}

func expireOperation(id, message string) {
	now := time.Now().UTC()
	_ = database().Model(&models.MCPOperation{}).Where("id = ? AND finished_at IS NULL", id).Updates(map[string]any{
		"state":       opExpired,
		"output":      message,
		"exit_code":   -1,
		"finished_at": now,
	}).Error
	signalOperation(id)
}

func failOperation(id, message string) {
	now := time.Now().UTC()
	_ = database().Model(&models.MCPOperation{}).Where("id = ? AND finished_at IS NULL", id).Updates(map[string]any{
		"state":       opFailed,
		"output":      message,
		"exit_code":   -1,
		"finished_at": now,
	}).Error
	signalOperation(id)
}

func operationSucceeded(op models.MCPOperation) bool {
	return op.FinishedAt != nil && op.State == opSucceeded
}

func operationRetryable(op models.MCPOperation) bool {
	return op.FinishedAt != nil && (op.State == opFailed || op.State == opExpired)
}

func waitIfUnfinished(leaseID string, op models.MCPOperation) models.MCPOperation {
	if op.FinishedAt != nil {
		return op
	}
	select {
	case <-waitOperation(op.ID):
	case <-time.After(25 * time.Second):
	}
	if updated, err := loadOperation(leaseID, op.ID); err == nil {
		return updated
	}
	return op
}

func finishedOperationError(op models.MCPOperation) error {
	message := strings.TrimSpace(op.Output)
	if message == "" {
		message = op.State
	}
	return errTool(message)
}

func reopenRetryableOperation(id string) (bool, error) {
	result := database().Model(&models.MCPOperation{}).
		Where("id = ? AND finished_at IS NOT NULL AND state IN ?", id, []string{opFailed, opExpired}).
		Select("state", "output", "exit_code", "started_at", "finished_at").
		Updates(map[string]any{
			"state":       opAccepted,
			"output":      "",
			"exit_code":   0,
			"started_at":  gorm.Expr("NULL"),
			"finished_at": gorm.Expr("NULL"),
		})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected != 1 {
		return false, nil
	}
	waitOperation(id)
	return true, nil
}

func claimRetryableOperation(lease models.MCPLease, opID string, occupySlot bool) (bool, error) {
	if occupySlot {
		createOpMu.Lock()
		defer createOpMu.Unlock()
		var current models.MCPOperation
		if err := database().Where("id = ?", opID).First(&current).Error; err != nil {
			return false, err
		}
		if !operationRetryable(current) {
			return false, nil
		}
		if runningOperationCount(lease.ID) >= lease.MaxConcurrency {
			return false, ErrConcurrency
		}
	}
	return reopenRetryableOperation(opID)
}

func resolveIdempotentReuse(lease models.MCPLease, op models.MCPOperation, occupySlot bool) (models.MCPOperation, bool, error) {
	op = waitIfUnfinished(lease.ID, op)
	if operationSucceeded(op) {
		return op, false, nil
	}
	if operationRetryable(op) {
		claimed, err := claimRetryableOperation(lease, op.ID, occupySlot)
		if err != nil {
			return op, false, err
		}
		if claimed {
			return op, true, nil
		}
		if updated, loadErr := loadOperation(lease.ID, op.ID); loadErr == nil {
			op = updated
		}
		op = waitIfUnfinished(lease.ID, op)
		if operationSucceeded(op) || op.FinishedAt == nil {
			return op, false, nil
		}
	}
	if op.FinishedAt == nil {
		return op, false, nil
	}
	return op, false, finishedOperationError(op)
}

func cancelLeaseOperations(leaseID string) {
	stopLeaseOperations(leaseID, true)
}

func stopLeaseOperations(leaseID string, revoke bool) {
	var ops []models.MCPOperation
	_ = database().Where("lease_id = ? AND state IN ?", leaseID, []string{opAccepted, opRunning, opCancelRequested}).Find(&ops).Error
	now := time.Now().UTC()
	agents := map[string]struct{}{}
	state := opCancelRequested
	if !revoke {
		state = opCancelled
	}
	for _, op := range ops {
		_ = database().Model(&op).Updates(map[string]any{
			"state":       state,
			"finished_at": now,
		}).Error
		agent_runtime.DispatchV2Event(op.AgentUUID, v2.MethodAgentMCPCancel, v2.MCPCancelParams{
			OperationID: op.ID,
			LeaseID:     leaseID,
		})
		agent_runtime.RemoveV2EventsByTaskID(op.AgentUUID, op.ID)
		agents[op.AgentUUID] = struct{}{}
		signalOperation(op.ID)
	}
	if revoke {
		// Include completed file/terminal operations on dynamically authorized nodes.
		var usedNodes []string
		_ = database().Model(&models.MCPOperation{}).Where("lease_id = ?", leaseID).
			Distinct("agent_uuid").Pluck("agent_uuid", &usedNodes).Error
		for _, uuid := range usedNodes {
			if uuid != "" {
				agents[uuid] = struct{}{}
			}
		}
		var lease models.MCPLease
		if err := database().Where("id = ?", leaseID).First(&lease).Error; err == nil {
			for _, uuid := range parseTargetUUIDs(lease.TargetUUIDs) {
				agents[uuid] = struct{}{}
			}
		}
		for uuid := range agents {
			agent_runtime.DispatchV2Event(uuid, v2.MethodAgentMCPRevoke, v2.MCPRevokeParams{LeaseID: leaseID})
		}
	}
	remote.CloseMCPLeaseSessions(leaseID)
}

func fileRequestID(opID string) string {
	sum := sha256.Sum256([]byte(opID))
	return hex.EncodeToString(sum[:8])
}

func loadOperation(leaseID, operationID string) (models.MCPOperation, error) {
	var op models.MCPOperation
	if err := database().Where("id = ? AND lease_id = ?", operationID, leaseID).First(&op).Error; err != nil {
		return models.MCPOperation{}, errTool("operation not found")
	}
	return op, nil
}

func outputSlice(output string, cursor, maxBytes int) (chunk string, next int, truncated bool) {
	if cursor < 0 {
		cursor = 0
	}
	if maxBytes <= 0 {
		maxBytes = DefaultReadBytes
	}
	if maxBytes > MaxReadBytes {
		maxBytes = MaxReadBytes
	}
	if cursor > len(output) {
		cursor = len(output)
	}
	end := cursor + maxBytes
	if end > len(output) {
		end = len(output)
	}
	return output[cursor:end], end, end < len(output)
}
