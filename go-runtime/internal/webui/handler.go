// Package webui serves the dependency-free browser adapter embedded in the
// mdd-core executable. Business state and recovery remain in typed Go APIs.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strconv"
)

//go:embed assets/*
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
	var name, contentType string
	switch request.URL.Path {
	case "/", "/index.html":
		name, contentType = "index.html", "text/html; charset=utf-8"
	case "/assets/app.js":
		name, contentType = "app.js", "text/javascript; charset=utf-8"
	case "/assets/app.css":
		name, contentType = "app.css", "text/css; charset=utf-8"
	default:
		http.NotFound(response, request)
		return
	}
	payload, err := fs.ReadFile(handler.assets, name)
	if err != nil {
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

func securityHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self' ws: wss:; img-src 'self' data:; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
}
