// Package webui serves the dependency-free browser adapter embedded in the
// mdd-core executable. Business state and recovery remain in typed Go APIs.
package webui

import (
	"embed"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
)

//go:embed assets
var content embed.FS

type Handler struct{ assets fs.FS }

func New() (*Handler, error) {
	assets, err := fs.Sub(content, "assets")
	if err != nil {
		return nil, err
	}
	return &Handler{assets: assets}, nil
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	securityHeaders(response.Header())
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	name, contentType, ok := assetName(request.URL.Path)
	if !ok {
		http.NotFound(response, request)
		return
	}
	payload, err := fs.ReadFile(handler.assets, name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			http.NotFound(response, request)
			return
		}
		http.Error(response, "embedded asset unavailable", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", contentType)
	response.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	if request.Method == http.MethodHead {
		return
	}
	_, _ = response.Write(payload)
}

func assetName(requestPath string) (string, string, bool) {
	if requestPath == "" || strings.ContainsAny(requestPath, "\\\x00") || reservedPath(requestPath) {
		return "", "", false
	}
	if requestPath == "/" || requestPath == "/index.html" {
		return "index.html", "text/html; charset=utf-8", true
	}
	clean := path.Clean(requestPath)
	if clean != requestPath || !strings.HasPrefix(clean, "/") || strings.Contains(clean, "..") {
		return "", "", false
	}
	name := strings.TrimPrefix(clean, "/")
	if strings.HasPrefix(name, "assets/") || strings.HasPrefix(name, "licenses/") || name == "logo.svg" {
		contentType, ok := assetContentType(name)
		return name, contentType, ok
	}
	// React uses hash navigation, but clean extensionless routes are also safe
	// bookmarks. API, WebSocket and unknown extension-bearing paths never fall
	// through to HTML.
	if path.Ext(name) == "" {
		return "index.html", "text/html; charset=utf-8", true
	}
	return "", "", false
}

func reservedPath(value string) bool {
	for _, prefix := range []string{"/api", "/v1", "/ws", "/healthz"} {
		if value == prefix || strings.HasPrefix(value, prefix+"/") {
			return true
		}
	}
	return false
}

func assetContentType(name string) (string, bool) {
	switch strings.ToLower(path.Ext(name)) {
	case ".js":
		return "text/javascript; charset=utf-8", true
	case ".css":
		return "text/css; charset=utf-8", true
	case ".svg":
		return "image/svg+xml", true
	case ".png":
		return "image/png", true
	case ".ico":
		return "image/x-icon", true
	case ".ttf":
		return "font/ttf", true
	case ".woff":
		return "font/woff", true
	case ".woff2":
		return "font/woff2", true
	case ".txt":
		return "text/plain; charset=utf-8", true
	default:
		if detected := mime.TypeByExtension(path.Ext(name)); detected != "" {
			return detected, false
		}
		return "", false
	}
}

func securityHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self' ws: wss:; img-src 'self' data:; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
}
