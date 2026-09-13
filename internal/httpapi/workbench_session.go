package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/riba2534/herdrx/internal/store"
)

type workbenchSessionRequest struct {
	HostID      string `json:"host_id"`
	WorkspaceID string `json:"workspace_id"`
	TabID       string `json:"tab_id"`
	PaneID      string `json:"pane_id"`
	DeviceID    string `json:"device_id"`
	ClientClass string `json:"client_class"`
}

func (a *API) getWorkbenchSession(w http.ResponseWriter, r *http.Request) {
	session, err := a.store.WorkbenchSession(r.Context(), userFromContext(r.Context()).ID)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, 200, map[string]any{"session": nil})
		return
	}
	if err != nil {
		writeError(w, 503, "database_error", "无法读取上次工作台位置")
		return
	}
	writeJSON(w, 200, map[string]any{"session": session})
}

func (a *API) putWorkbenchSession(w http.ResponseWriter, r *http.Request) {
	var input workbenchSessionRequest
	if err := decodeJSON(r, &input); err != nil {
		decodeError(w, err)
		return
	}
	input.HostID = strings.TrimSpace(input.HostID)
	input.WorkspaceID = strings.TrimSpace(input.WorkspaceID)
	input.TabID = strings.TrimSpace(input.TabID)
	input.PaneID = strings.TrimSpace(input.PaneID)
	input.DeviceID = strings.TrimSpace(input.DeviceID)
	input.ClientClass = strings.TrimSpace(input.ClientClass)
	if !validWorkbenchRef(input.HostID, 1, 128) {
		writeError(w, 400, "invalid_session", "请选择要恢复的主机")
		return
	}
	for _, item := range []struct {
		value string
		label string
	}{
		{input.WorkspaceID, "工作区"},
		{input.TabID, "标签页"},
		{input.PaneID, "Pane"},
	} {
		if item.value != "" && !validWorkbenchRef(item.value, 1, 128) {
			writeError(w, 400, "invalid_session", item.label+"标识无效")
			return
		}
	}
	if input.DeviceID != "" && !validWorkbenchRef(input.DeviceID, 8, 128) {
		writeError(w, 400, "invalid_session", "设备标识无效")
		return
	}
	if input.ClientClass != "" && input.ClientClass != "desktop" && input.ClientClass != "mobile" {
		writeError(w, 400, "invalid_session", "请标明电脑或手机客户端")
		return
	}
	saved, err := a.store.PutWorkbenchSession(r.Context(), store.WorkbenchSession{
		UserID:      userFromContext(r.Context()).ID,
		HostID:      input.HostID,
		WorkspaceID: input.WorkspaceID,
		TabID:       input.TabID,
		PaneID:      input.PaneID,
		DeviceID:    input.DeviceID,
		ClientClass: input.ClientClass,
	})
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, 404, "host_not_found", "主机不存在或无权访问")
		return
	}
	if err != nil {
		writeError(w, 503, "database_error", "无法保存工作台位置")
		return
	}
	writeJSON(w, 200, map[string]any{"session": saved})
}

func validWorkbenchRef(value string, min, max int) bool {
	n := utf8.RuneCountInString(value)
	if n < min || n > max {
		return false
	}
	for _, r := range value {
		if r < 33 || r > 126 {
			return false
		}
	}
	return true
}
