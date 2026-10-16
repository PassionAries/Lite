package mcp

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/nuomiiiii/lite/database/accounts"
	"github.com/nuomiiiii/lite/database/auditlog"
	"github.com/nuomiiiii/lite/database/models"
	agent_runtime "github.com/nuomiiiii/lite/web/agent"
	"github.com/nuomiiiii/lite/web/api"
	"github.com/nuomiiiii/lite/web/api/remote"
	"github.com/nuomiiiii/lite/web/passkey"
	"github.com/nuomiiiii/lite/web/remotectl"
	"gorm.io/gorm"
)

type adminReauthorization struct {
	Password   string          `json:"password"`
	OTP        string          `json:"otp"`
	TwoFA      string          `json:"2fa_code"`
	CeremonyID string          `json:"ceremony_id"`
	Credential json.RawMessage `json:"credential"`
}

func reauthorizeAdmin(c *gin.Context, userUUID string, body adminReauthorization) bool {
	otp := firstNonEmpty(body.OTP, body.TwoFA)
	var err error
	if len(body.Credential) > 0 || body.CeremonyID != "" {
		err = remotectl.ReauthorizePasskey(userUUID, c.ClientIP(), func() error {
			return passkey.FinishUserAssertion(c, userUUID, body.CeremonyID, body.Credential)
		})
	} else {
		err = remotectl.Reauthorize(userUUID, body.Password, otp, c.ClientIP())
	}
	if err != nil {
		status := http.StatusForbidden
		if remotectl.IsRateLimited(err) {
			status = http.StatusTooManyRequests
		}
		api.RespondError(c, status, err.Error())
		return false
	}
	return true
}

func updateLease(c *gin.Context) {
	principal, loginSession, ok := requireAdminSession(c)
	if !ok {
		return
	}
	var existing models.MCPLease
	if err := database().Where("id = ?", c.Param("id")).First(&existing).Error; err != nil {
		api.RespondError(c, http.StatusNotFound, "authorization not found")
		return
	}
	if existing.OwnerUserUUID != principal.UserUUID {
		api.RespondError(c, http.StatusForbidden, "only the authorization owner can edit it")
		return
	}
	var body struct {
		adminReauthorization
		TargetUUIDs     *[]string `json:"target_uuids"`
		AllNodes        *bool     `json:"all_nodes"`
		DurationMinutes any       `json:"duration_minutes"`
		LongTerm        *bool     `json:"long_term"`
		Note            *string   `json:"note"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		api.RespondError(c, http.StatusBadRequest, "invalid authorization policy")
		return
	}
	if !reauthorizeAdmin(c, principal.UserUUID, body.adminReauthorization) {
		return
	}
	if !mcpEnabled() || !remote.RemoteManagementEnabled() {
		api.RespondError(c, http.StatusConflict, ErrMCPDisabled.Error())
		return
	}
	now := time.Now().UTC()
	lease, err := loadLiveLease(existing.ID, now)
	if err != nil {
		api.RespondError(c, http.StatusConflict, "only active authorizations can be edited")
		return
	}
	before := lease
	lease, err = applyLeaseEdit(lease, leaseEditRequest{
		TargetUUIDs:     body.TargetUUIDs,
		AllNodes:        body.AllNodes,
		DurationMinutes: body.DurationMinutes,
		LongTerm:        body.LongTerm,
		Note:            body.Note,
	}, now)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, err.Error())
		return
	}
	lease.OwnerLoginSessionHash = accounts.SessionLookupKey(loginSession)
	interrupt := leaseWorkMustStop(before, lease)
	var saveErr error
	agent_runtime.DrainRemoteDelivery(func() []agent_runtime.RemovedV2Event {
		saveErr = saveLeasePolicy(database(), lease, time.Now().UTC())
		if saveErr != nil || !interrupt {
			return nil
		}
		// Do not send agent.mcp.revoke: agents remember revoked lease IDs,
		// which would also block future work under the edited policy.
		return interruptLeaseWork(lease.ID)
	})
	if saveErr != nil {
		status := http.StatusInternalServerError
		if errors.Is(saveErr, ErrLeaseInactive) {
			status = http.StatusConflict
		}
		api.RespondError(c, status, saveErr.Error())
		return
	}
	auditlog.Event(c.ClientIP(), principal.UserUUID, "warn", "audit.mcp_update", map[string]string{
		"id":         lease.ID,
		"all_nodes":  strconv.FormatBool(lease.AllNodes),
		"targets":    lease.TargetUUIDs,
		"long_term":  strconv.FormatBool(lease.LongTerm),
		"expires_at": lease.ExpiresAt.UTC().Format(time.RFC3339),
	})
	getLease(c)
}

type leaseEditRequest struct {
	TargetUUIDs     *[]string
	AllNodes        *bool
	DurationMinutes any
	LongTerm        *bool
	Note            *string
}

func boolPtr(value bool) *bool { return &value }

func stringSlicePtr(ids []string) *[]string { return &ids }

func applyLeaseEdit(lease models.MCPLease, req leaseEditRequest, now time.Time) (models.MCPLease, error) {
	if req.AllNodes != nil || req.TargetUUIDs != nil {
		allNodes := lease.AllNodes
		if req.AllNodes != nil {
			allNodes = *req.AllNodes
		} else {
			allNodes = false
		}
		if allNodes {
			lease.TargetUUIDs, lease.AllNodes = encodeTargetUUIDs(nil), true
		} else if req.TargetUUIDs != nil {
			targets, err := validateTargetSelection(false, *req.TargetUUIDs)
			if err != nil {
				return lease, err
			}
			lease.TargetUUIDs, lease.AllNodes = encodeTargetUUIDs(targets), false
		} else if lease.AllNodes {
			return lease, errTool("select at least one node")
		} else {
			lease.AllNodes = false
		}
	}
	if req.DurationMinutes != nil || req.LongTerm != nil {
		longTerm := lease.LongTerm
		if req.LongTerm != nil {
			longTerm = *req.LongTerm
		}
		minutes := 0
		if !longTerm {
			parsed, err := ParseDurationMinutes(req.DurationMinutes)
			if err == nil {
				parsed, err = ValidateLeaseDuration(parsed, siteDurationSettings().MaxMinutes)
			}
			if err != nil {
				return lease, err
			}
			minutes = parsed
		}
		nextLong, expiresAt, err := authorizationPolicy(boolPtr(longTerm), now, minutes)
		if err != nil {
			return lease, err
		}
		lease.LongTerm, lease.ExpiresAt = nextLong, expiresAt
	}
	if req.Note != nil {
		note := strings.TrimSpace(*req.Note)
		if utf8.RuneCountInString(note) > 200 {
			return lease, errors.New("note must be at most 200 characters")
		}
		lease.Note = note
	}
	return lease, nil
}

func leaseWorkMustStop(before, after models.MCPLease) bool {
	if before.AllNodes != after.AllNodes || before.LongTerm != after.LongTerm {
		return true
	}
	if !before.ExpiresAt.Equal(after.ExpiresAt) {
		return true
	}
	if before.AllNodes && after.AllNodes {
		return false
	}
	return !sameTargetSet(before.TargetUUIDs, after.TargetUUIDs)
}

func sameTargetSet(left, right string) bool {
	a, b := parseTargetUUIDs(left), parseTargetUUIDs(right)
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, id := range a {
		seen[id]++
	}
	for _, id := range b {
		seen[id]--
		if seen[id] < 0 {
			return false
		}
	}
	return true
}

func interruptLeaseWork(leaseID string) []agent_runtime.RemovedV2Event {
	removed := agent_runtime.RemoveMCPWorkEventsByLeaseID(leaseID)
	stopLeaseOperations(leaseID, false)
	return removed
}

func saveLeasePolicy(db *gorm.DB, lease models.MCPLease, now time.Time) error {
	return db.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&models.MCPLease{}).
			Where("id = ? AND owner_user_uuid = ? AND status = ? AND revoked_at IS NULL AND expires_at > ?",
				lease.ID, lease.OwnerUserUUID, statusActive, now).
			Updates(map[string]any{
				"target_uuids":             lease.TargetUUIDs,
				"all_nodes":                lease.AllNodes,
				"long_term":                lease.LongTerm,
				"expires_at":               lease.ExpiresAt,
				"note":                     lease.Note,
				"owner_login_session_hash": lease.OwnerLoginSessionHash,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrLeaseInactive
		}
		// Extend/shorten usable refresh tokens with the lease, but never resurrect
		// an expired or consumed credential. Access tokens remain short lived.
		return tx.Model(&models.MCPToken{}).
			Where("lease_id = ? AND family_id = ? AND kind = ? AND used = ? AND expires_at > ?",
				lease.ID, lease.TokenFamilyID, kindRefresh, false, now).
			Update("expires_at", lease.ExpiresAt).Error
	})
}
