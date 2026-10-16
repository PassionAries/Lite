package mcp

import (
	"time"

	"github.com/nuomiiiii/lite/database/models"
	v2 "github.com/nuomiiiii/lite/protocol/v2"
	agent_runtime "github.com/nuomiiiii/lite/web/agent"
	"github.com/nuomiiiii/lite/web/api/remote"
)

func dispatchMCPExec(uuid string, params v2.MCPExecParams) bool {
	queued := false
	accepted := agent_runtime.GuardRemoteDelivery(func() bool {
		return mcpDispatchAllowed(params.LeaseID, uuid, params.ExpiresAt)
	}, func() {
		event := agent_runtime.EnqueueV2Event(uuid, v2.MethodAgentMCPExec, params)
		queued = event.ID != ""
		if queued {
			if conn := agent_runtime.GetConnectedClient(uuid); conn != nil {
				payload := v2.Request{JSONRPC: v2.Version, Method: event.Method, Params: event.Params, ID: event.ID}
				_ = conn.WriteJSON(payload)
			}
		}
	})
	return accepted && queued
}

func dispatchMCPFile(uuid string, params v2.MCPFileParams) bool {
	queued := false
	accepted := agent_runtime.GuardRemoteDelivery(func() bool {
		return mcpDispatchAllowed(params.LeaseID, uuid, params.ExpiresAt)
	}, func() {
		event := agent_runtime.EnqueueV2Event(uuid, v2.MethodAgentMCPFile, params)
		queued = event.ID != ""
		if queued {
			if conn := agent_runtime.GetConnectedClient(uuid); conn != nil {
				payload := v2.Request{JSONRPC: v2.Version, Method: event.Method, Params: event.Params, ID: event.ID}
				_ = conn.WriteJSON(payload)
			}
		}
	})
	return accepted && queued
}

func mcpDispatchAllowed(leaseID, uuid string, expiresAt time.Time) bool {
	if !remote.RemoteManagementEnabled() || !mcpEnabled() {
		return false
	}
	lease, err := loadLiveLease(leaseID, time.Now().UTC())
	if err != nil {
		return false
	}
	return mcpDispatchMatchesLease(lease, uuid, expiresAt)
}

func mcpDispatchMatchesLease(lease models.MCPLease, uuid string, expiresAt time.Time) bool {
	if expiresAt.IsZero() || !leaseContainsNode(lease, uuid) {
		return false
	}
	// In-flight params may still carry an earlier expiry after an extension.
	// Reject only work that claims to outlive the current grant.
	return !expiresAt.After(lease.ExpiresAt)
}

func mcpMethods() []string {
	return []string{
		v2.MethodAgentMCPExec,
		v2.MethodAgentMCPCancel,
		v2.MethodAgentMCPRenew,
		v2.MethodAgentMCPRevoke,
		v2.MethodAgentMCPFile,
	}
}

func DrainMCPDelivery() []agent_runtime.RemovedV2Event {
	return agent_runtime.DrainRemoteDelivery(func() []agent_runtime.RemovedV2Event {
		_ = RevokeAllActive(reasonMCPOff)
		return agent_runtime.RemoveAllV2EventsByMethods(mcpMethods()...)
	})
}
