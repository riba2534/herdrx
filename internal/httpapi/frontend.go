package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var inlineScriptPattern = regexp.MustCompile(`(?is)<script(\s[^>]*)?>(.*?)</script>`)
var scriptTypePattern = regexp.MustCompile(`(?i)type\s*=\s*["']?([^"'\s>]+)`)

func (a *API) loadInlineScriptHashes() {
	raw, err := fs.ReadFile(a.assets, "index.html")
	if err != nil {
		return
	}
	a.scriptHashes = inlineScriptHashes(raw)
}

func inlineScriptHashes(html []byte) string {
	var hashes []string
	for _, match := range inlineScriptPattern.FindAllSubmatch(html, -1) {
		attrs := string(match[1])
		if strings.Contains(strings.ToLower(attrs), "src=") || nonJavaScriptType(attrs) {
			continue
		}
		sum := sha256.Sum256(match[2])
		hashes = append(hashes, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
	return strings.Join(hashes, " ")
}

func nonJavaScriptType(attrs string) bool {
	match := scriptTypePattern.FindStringSubmatch(attrs)
	if match == nil {
		return false
	}
	switch strings.ToLower(match[1]) {
	case "text/javascript", "application/javascript", "module", "text/ecmascript":
		return false
	default:
		return true
	}
}

func (a *API) frontend() http.Handler {
	if a.assets == nil {
		return http.NotFoundHandler()
	}
	fileServer := http.FileServer(http.FS(a.assets))
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		path := strings.TrimPrefix(request.URL.Path, "/")
		if path != "" {
			if strings.HasPrefix(path, "assets/") && (strings.HasSuffix(path, ".br") || strings.HasSuffix(path, ".gz")) {
				http.NotFound(writer, request)
				return
			}
			if compressibleAsset(path) && a.serveCompressedAsset(writer, request, path) {
				return
			}
			if _, err := fs.Stat(a.assets, path); err == nil {
				if strings.HasPrefix(path, "assets/") {
					writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else if path == "sw.js" || path == "manifest.webmanifest" {
					writer.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
				}
				fileServer.ServeHTTP(writer, request)
				return
			}
			if strings.HasPrefix(path, "assets/") || filepath.Ext(path) != "" {
				http.NotFound(writer, request)
				return
			}
		}
		writer.Header().Set("Cache-Control", "no-store")
		request.URL.Path = "/"
		fileServer.ServeHTTP(writer, request)
	})
}

func compressibleAsset(path string) bool {
	if !strings.HasPrefix(path, "assets/") {
		return false
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".js", ".css":
		return true
	default:
		return false
	}
}

func (a *API) serveCompressedAsset(writer http.ResponseWriter, request *http.Request, path string) bool {
	if _, err := fs.Stat(a.assets, path); err != nil {
		return false
	}
	_, hasBR := fs.Stat(a.assets, path+".br")
	_, hasGZ := fs.Stat(a.assets, path+".gz")
	encoding := negotiateAssetEncoding(request.Header.Get("Accept-Encoding"), hasBR == nil, hasGZ == nil)
	filePath := path
	switch encoding {
	case "br":
		filePath = path + ".br"
	case "gzip":
		filePath = path + ".gz"
	}
	file, err := a.assets.Open(filePath)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return false
	}
	writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	writer.Header().Set("Vary", "Accept-Encoding")
	writer.Header().Set("Content-Type", assetContentType(path))
	if encoding != "" {
		writer.Header().Set("Content-Encoding", encoding)
	}
	reader, ok := file.(io.ReadSeeker)
	if !ok {
		data, err := io.ReadAll(file)
		if err != nil {
			return false
		}
		reader = bytes.NewReader(data)
	}
	http.ServeContent(writer, request, path, info.ModTime(), reader)
	return true
}

func assetContentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	default:
		if ctype := mime.TypeByExtension(filepath.Ext(path)); ctype != "" {
			return ctype
		}
		return "application/octet-stream"
	}
}

func negotiateAssetEncoding(header string, hasBR, hasGZ bool) string {
	if hasBR && acceptedEncoding(header, "br") {
		return "br"
	}
	if hasGZ && acceptedEncoding(header, "gzip") {
		return "gzip"
	}
	return ""
}

func acceptedEncoding(header, coding string) bool {
	if strings.TrimSpace(header) == "" {
		return false
	}
	coding = strings.ToLower(coding)
	starQ := -1.0
	codingQ := -1.0
	for _, part := range strings.Split(header, ",") {
		name, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		q := 1.0
		if raw, ok := params["q"]; ok {
			parsed, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				continue
			}
			q = parsed
		}
		switch strings.ToLower(name) {
		case "*":
			starQ = q
		case coding:
			codingQ = q
		}
	}
	if codingQ >= 0 {
		return codingQ > 0
	}
	return starQ > 0
}
