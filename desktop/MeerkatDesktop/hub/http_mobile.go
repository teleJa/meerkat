package hub

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

func (h *Hub) handleCreateMobilePairingToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Label      string `json:"label"`
		TTLSeconds int    `json:"ttl_seconds"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	ttl := body.TTLSeconds
	if ttl == 0 {
		ttl = int(MobilePairingTTL / time.Second)
	}
	if ttl < 60 {
		ttl = 60
	}
	if ttl > 3600 {
		ttl = 3600
	}
	var label *string
	if value := strings.TrimSpace(body.Label); value != "" {
		if len(value) > 120 {
			value = value[:120]
		}
		label = &value
	}
	code, expiresAt, err := h.createMobilePairingCode(label, time.Duration(ttl)*time.Second, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "生成移动设备配对码失败")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"code": code, "expires_at": expiresAt, "label": label,
		"scopes": MobileScopes, "ttl_seconds": ttl,
	})
}

func (h *Hub) handleRegisterMobile(w http.ResponseWriter, r *http.Request) {
	var input mobileRegistration
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json")
		return
	}
	if normalizeMobilePairingCode(input.Code) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "缺少配对码")
		return
	}
	credential, token, err := h.registerMobileCredential(input, time.Now())
	if err != nil {
		if errors.Is(err, ErrMobilePairingInvalid) {
			writeError(w, http.StatusUnauthorized, "mobile_pairing_invalid", "配对码无效、已使用或已过期")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "注册移动设备失败")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"credential_id": credential.ID,
		"token":         token,
		"scopes":        credential.Scopes,
		"expires_at":    credential.ExpiresAt,
		"credential":    credential,
		"hub_time":      nowISO(time.Now()),
	})
}

func (h *Hub) handleListMobileCredentials(w http.ResponseWriter, _ *http.Request) {
	credentials, err := h.listMobileCredentials()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "读取移动设备列表失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"credentials": credentials,
		"server_time": nowISO(time.Now()),
	})
}

func (h *Hub) handleRevokeMobileCredential(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(pathID(r))
	if !strings.HasPrefix(id, mobileCredentialID) || len(id) > 64 {
		writeError(w, http.StatusBadRequest, "invalid_credential_id", "无效的移动设备凭证 id")
		return
	}
	credential, err := h.getMobileCredential(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "读取移动设备凭证失败")
		return
	}
	if credential == nil {
		writeError(w, http.StatusNotFound, "mobile_credential_not_found", "移动设备凭证不存在")
		return
	}
	revoked, err := h.revokeMobileCredential(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "撤销移动设备凭证失败")
		return
	}
	if !revoked {
		// 撤销是硬删除：删除影响 0 行说明这一行已经不在了（被别处的撤销抢先删掉）。
		// 语义从「已经撤销」变成「不存在」，用 404 与上面的前置检查保持一致。
		writeError(w, http.StatusNotFound, "mobile_credential_not_found", "移动设备凭证不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "credential_id": id, "revoked": true})
}

func (h *Hub) handleRotateMobileCredential(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireAuth(w, r, false)
	if !ok {
		return
	}
	if auth.Via != "mobile" || auth.CredentialID == "" {
		writeError(w, http.StatusForbidden, "mobile_credential_required", "只有移动设备凭证可以轮换自己")
		return
	}
	token, err := h.rotateMobileCredential(auth.CredentialID, time.Now())
	if err != nil {
		switch {
		case errors.Is(err, ErrMobileCredentialExpired):
			writeError(w, http.StatusUnauthorized, "mobile_credential_expired", "移动设备凭证已过期，请重新配对")
		case errors.Is(err, ErrMobileCredentialInvalid):
			writeError(w, http.StatusUnauthorized, "mobile_credential_invalid", "移动设备凭证无效，请重新配对")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "轮换移动设备凭证失败")
		}
		return
	}
	credential, _ := h.getMobileCredential(auth.CredentialID)
	dto := MobileCredential{ID: auth.CredentialID}
	if credential != nil {
		dto = mobileCredentialToDTO(*credential, time.Now())
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"credential_id": auth.CredentialID,
		"token":         token,
		"credential":    dto,
		"grace_seconds": int(MobileRotationGrace / time.Second),
		"expires_at":    dto.ExpiresAt,
	})
}
