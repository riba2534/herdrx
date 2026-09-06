package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5"
)

var safePaneID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:._-]{0,127}$`)

const (
	maxUploadSize = 25 << 20 // 25MB for multipart form envelope
	maxImageSize  = 20 << 20 // 20MB for image file
)

func (a *API) pasteImage(writer http.ResponseWriter, request *http.Request) {
	host, err := a.ownedHost(request)
	if err != nil {
		writeError(writer, http.StatusNotFound, "host_not_found", "host not found")
		return
	}

	// chi can preserve escaped path parameters (real Pane IDs contain ':').
	paneID := chi.URLParam(request, "paneID")
	if request.URL.RawPath != "" {
		paneID, err = url.PathUnescape(paneID)
	}
	if err != nil || !safePaneID.MatchString(paneID) {
		writeError(writer, http.StatusBadRequest, "invalid_pane_id", "invalid pane id")
		return
	}

	request.Body = http.MaxBytesReader(writer, request.Body, maxUploadSize)
	if err := request.ParseMultipartForm(10 << 20); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_form", "could not parse form or payload exceeds limit")
		return
	}
	if request.MultipartForm != nil {
		defer request.MultipartForm.RemoveAll()
	}

	file, header, err := request.FormFile("file")
	if err != nil {
		file, header, err = request.FormFile("image")
	}
	if err != nil {
		writeError(writer, http.StatusBadRequest, "missing_file", "image file is required in 'file' form field")
		return
	}
	defer file.Close()

	if header.Size > maxImageSize {
		writeError(writer, http.StatusBadRequest, "image_too_large", "image file exceeds maximum limit of 20MB")
		return
	}

	headerBytes := make([]byte, 512)
	n, err := io.ReadFull(file, headerBytes)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		writeError(writer, http.StatusBadRequest, "read_error", "could not read image data")
		return
	}
	headerBytes = headerBytes[:n]
	if len(headerBytes) == 0 {
		writeError(writer, http.StatusBadRequest, "empty_file", "image file is empty")
		return
	}

	ext, ok := detectImageExt(headerBytes)
	if !ok {
		writeError(writer, http.StatusBadRequest, "invalid_image_type", "only png, jpeg, webp and gif images are allowed")
		return
	}

	combinedReader := io.MultiReader(bytes.NewReader(headerBytes), file)

	ctx, cancel := context.WithTimeout(request.Context(), 30*time.Second)
	defer cancel()

	endpoint, err := a.hosts.Open(ctx, host)
	if err != nil {
		writeHostConnectionError(writer, err)
		return
	}
	defer endpoint.Close()

	remotePath, err := endpoint.StageImage(ctx, ext, combinedReader)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "stage_image_failed", err.Error())
		return
	}

	inject := request.URL.Query().Get("inject") != "false"
	if inject {
		// Herdr must encode this as one paste, using the target's negotiated
		// bracketed-paste mode. Raw keystrokes do not reliably attach images in
		// agents. Do not append a space or Enter: the path is the entire paste.
		_, err = endpoint.Call(ctx, "pane.send_input", map[string]any{
			"pane_id": paneID,
			"text":    remotePath,
			"keys":    []string{},
		})
		if err != nil {
			writeError(writer, http.StatusInternalServerError, "inject_failed", fmt.Sprintf("staged to %s but injection failed: %v", remotePath, err))
			return
		}
	}

	a.audit(request, "pane.paste_image", "pane", paneID, map[string]any{
		"host_id":  host.ID,
		"path":     remotePath,
		"ext":      ext,
		"injected": inject,
	})

	writeJSON(writer, http.StatusOK, map[string]any{
		"ok":       true,
		"path":     remotePath,
		"injected": inject,
	})
}

func detectImageExt(header []byte) (string, bool) {
	if bytes.HasPrefix(header, []byte("\x89PNG\r\n\x1a\n")) {
		return "png", true
	}
	if bytes.HasPrefix(header, []byte("\xff\xd8\xff")) {
		return "jpg", true
	}
	if bytes.HasPrefix(header, []byte("GIF87a")) || bytes.HasPrefix(header, []byte("GIF89a")) {
		return "gif", true
	}
	if len(header) >= 12 && string(header[0:4]) == "RIFF" && string(header[8:12]) == "WEBP" {
		return "webp", true
	}
	// Fallback to standard sniff
	contentType := http.DetectContentType(header)
	switch contentType {
	case "image/png":
		return "png", true
	case "image/jpeg":
		return "jpg", true
	case "image/gif":
		return "gif", true
	case "image/webp":
		return "webp", true
	default:
		return "", false
	}
}
