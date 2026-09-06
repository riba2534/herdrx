package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/store"
)

func (a *API) hostFolderRoutes(r chi.Router) {
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		folders, err := a.store.ListHostFolders(r.Context(), userFromContext(r.Context()).ID)
		if err != nil {
			writeError(w, 500, "database_error", "无法读取文件夹，请重试")
			return
		}
		writeJSON(w, 200, map[string]any{"folders": folders})
	})
	r.With(a.requireCSRF).Post("/", a.saveHostFolder)
	r.With(a.requireCSRF).Patch("/{folderID}", a.saveHostFolder)
	r.With(a.requireCSRF).Delete("/{folderID}", func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "folderID")
		if err := a.store.DeleteHostFolder(r.Context(), userFromContext(r.Context()).ID, id); folderError(w, err) {
			return
		}
		a.audit(r, "host_folder.deleted", "host_folder", id, nil)
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
}

func folderError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, 404, "folder_not_found", "文件夹或主机不存在，或无权操作")
	} else if errors.Is(err, store.ErrFolderCycle) {
		writeError(w, 400, "folder_cycle", "不能将文件夹移入自己或自己的子文件夹")
	} else {
		writeError(w, 500, "database_error", "无法保存文件夹，请重试")
	}
	return true
}

func (a *API) saveHostFolder(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name     string `json:"name"`
		ParentID string `json:"parent_id"`
	}
	if decodeJSON(r, &input) != nil {
		writeError(w, 400, "invalid_request", "请填写有效的文件夹信息")
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if !validResourceName(input.Name) {
		writeError(w, 400, "invalid_name", "文件夹名称须为 1–80 个字符")
		return
	}
	id := chi.URLParam(r, "folderID")
	update := id != ""
	if !update {
		token, err := secure.Token(12)
		if err != nil {
			writeError(w, 500, "folder_error", "无法创建文件夹，请重试")
			return
		}
		id = "fld_" + token
	}
	f := store.HostFolder{ID: id, OwnerID: userFromContext(r.Context()).ID, Name: input.Name, ParentID: input.ParentID}
	if folderError(w, a.store.SaveHostFolder(r.Context(), f, update)) {
		return
	}
	action, status := "host_folder.created", 201
	if update {
		action, status = "host_folder.updated", 200
	}
	a.audit(r, action, "host_folder", id, map[string]string{"name": f.Name, "parent_id": f.ParentID})
	writeJSON(w, status, map[string]any{"folder": f})
}

func (a *API) moveHost(w http.ResponseWriter, r *http.Request) {
	var input struct {
		FolderID string `json:"folder_id"`
	}
	if decodeJSON(r, &input) != nil {
		writeError(w, 400, "invalid_request", "请选择文件夹")
		return
	}
	id := chi.URLParam(r, "hostID")
	owner := userFromContext(r.Context()).ID
	if folderError(w, a.store.MoveHost(r.Context(), owner, id, input.FolderID)) {
		return
	}
	host, err := a.store.HostByID(r.Context(), owner, id)
	if folderError(w, err) {
		return
	}
	a.audit(r, "host.moved", "host", id, map[string]string{"folder_id": input.FolderID})
	writeJSON(w, 200, map[string]any{"host": host})
}
