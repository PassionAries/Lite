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
		TargetUUIDs     []string `json:"target_uuids"`
		AllNodes        bool     `json:"all_nodes"`
		DurationMinutes any      `json:"duration_minutes"`
		LongTerm        *bool    `json:"long_term"`
		Note            string   `json:"note"`
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
	targets, err := validateTargetSelection(body.AllNodes, body.TargetUUIDs)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, err.Error())
		return
	}
	if body.DurationMinutes != nil || body.LongTerm != nil {
		minutes, err := ParseDurationMinutes(body.DurationMinutes)
		if err == nil {
			minutes, err = ValidateLeaseDuration(minutes, siteDurationSettings().MaxMinutes)
		}
		if err != nil {
			api.RespondError(c, http.StatusBadRequest, err.Error())
			return
		}
		longTerm, expiresAt, err := authorizationPolicy(body.LongTerm, now, minutes)
		if err != nil {
			api.RespondError(c, http.StatusBadRequest, err.Error())
			return
		}
		lease.LongTerm, lease.ExpiresAt = longTerm, expiresAt
	}
	note := strings.TrimSpace(body.Note)
	if utf8.RuneCountInString(note) > 200 {
		api.RespondError(c, http.StatusBadRequest, "note must be at most 200 characters")
		return
	}
	lease.TargetUUIDs, lease.AllNodes = encodeTargetUUIDs(targets), body.AllNodes
	lease.Note = note
	lease.OwnerLoginSessionHash = accounts.SessionLookupKey(loginSession)
	var saveErr error
	agent_runtime.DrainRemoteDelivery(func() []agent_runtime.RemovedV2Event {
		saveErr = saveLeasePolicy(database(), lease, time.Now().UTC())
		if saveErr == nil {
			// Do not send agent.mcp.revoke: agents remember revoked lease IDs,
			// which would also block future work under the edited policy.
			stopLeaseOperations(lease.ID, false)
		}
		return nil
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
