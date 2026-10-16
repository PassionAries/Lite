package remote

import (
	"errors"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestMCPTerminalIORejectsNodesThatLeftTheGrant(t *testing.T) {
	mcpLeaseValidatorMu.RLock()
	previous := mcpLeaseValidator
	mcpLeaseValidatorMu.RUnlock()
	t.Cleanup(func() { SetMCPTerminalLeaseValidator(previous) })

	allowed := "kept-node"
	SetMCPTerminalLeaseValidator(func(leaseID, userUUID, nodeUUID string) bool {
		return leaseID == "lease" && userUUID == "owner" && nodeUUID == allowed
	})

	now := time.Now()
	session := &remoteSession{
		ID: "term-edit", UUID: "kept-node", UserUUID: "owner",
		mcp: true, leaseID: "lease",
		ExpiresAt: now.Add(time.Hour), LastActivity: now,
	}
	if err := putSession(session); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { deleteSession("term-edit") })

	if err := EnsureMCPTerminal("term-edit", "lease"); err != nil {
		t.Fatalf("live grant rejected terminal: %v", err)
	}
	if err := WriteMCPTerminal("term-edit", []byte("x")); err == nil || err.Error() != "terminal is not ready" {
		t.Fatalf("authorized write without agent = %v", err)
	}

	allowed = "other-node"
	if err := WriteMCPTerminal("term-edit", []byte("x")); !errors.Is(err, errMCPTerminalUnauthorized) {
		t.Fatalf("narrowed grant still accepted terminal write: %v", err)
	}
	if err := EnsureMCPTerminal("term-edit", "lease"); err == nil || err.Error() != "terminal not found" {
		t.Fatalf("unauthorized terminal was not closed: %v", err)
	}
	if CountMCPLeaseSessions("lease") != 0 {
		t.Fatal("unauthorized terminal session was not closed")
	}
}

func TestMCPTerminalReadyRejectsExpiredOrNarrowedGrant(t *testing.T) {
	mcpLeaseValidatorMu.RLock()
	previous := mcpLeaseValidator
	mcpLeaseValidatorMu.RUnlock()
	t.Cleanup(func() { SetMCPTerminalLeaseValidator(previous) })

	allowed := "kept-node"
	SetMCPTerminalLeaseValidator(func(leaseID, userUUID, nodeUUID string) bool {
		return leaseID == "lease" && userUUID == "owner" && nodeUUID == allowed
	})

	now := time.Now()
	session := &remoteSession{
		ID: "term-ready", UUID: "kept-node", UserUUID: "owner",
		mcp: true, leaseID: "lease",
		ExpiresAt: now.Add(time.Hour), LastActivity: now,
		Agent: &websocket.Conn{},
	}
	if err := putSession(session); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { deleteSession("term-ready") })

	if !MCPTerminalReady("term-ready") {
		t.Fatal("live grant reported terminal not ready")
	}
	session.mu.Lock()
	session.Agent = nil
	session.mu.Unlock()
	allowed = "other-node"
	if MCPTerminalReady("term-ready") {
		t.Fatal("narrowed grant still reported terminal ready")
	}
	if CountMCPLeaseSessions("lease") != 0 {
		t.Fatal("unauthorized ready check did not close the terminal")
	}
}
