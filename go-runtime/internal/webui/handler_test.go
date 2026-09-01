package webui

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedReactRoutesAndSecurityHeaders(t *testing.T) {
	handler, err := New()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	for _, test := range []struct {
		path, contentType, contains string
	}{
		{"/", "text/html; charset=utf-8", "MDD Sim Gateway"},
		{"/index.html", "text/html; charset=utf-8", "/assets/app.js"},
		{"/devices", "text/html; charset=utf-8", `<div id="root"></div>`},
		{"/assets/app.js", "text/javascript; charset=utf-8", "/v1/devices"},
		{"/assets/app.css", "text/css; charset=utf-8", ".u-sidebar"},
		{"/logo.svg", "image/svg+xml", "<svg"},
		{"/licenses/jsqr-Apache-2.0.txt", "text/plain; charset=utf-8", "Apache License"},
	} {
		response, err := http.Get(server.URL + test.path)
		if err != nil {
			t.Fatal(err)
		}
		payload, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != test.contentType ||
			!strings.Contains(string(payload), test.contains) {
			t.Fatalf("path=%s status=%d type=%q body=%q", test.path, response.StatusCode,
				response.Header.Get("Content-Type"), payload)
		}
		assertSecurityHeaders(t, response.Header)
	}
}

func TestEmbeddedReactGeneratedAssetsAreReachableAndWorkletIsExternal(t *testing.T) {
	handler, err := New()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	foundWorklet, foundReact, foundFont := false, false, false
	err = fs.WalkDir(handler.assets, "assets", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		response, err := http.Get(server.URL + "/" + name)
		if err != nil {
			return err
		}
		payload, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK || len(payload) == 0 {
			t.Fatalf("generated asset %s status=%d bytes=%d", name, response.StatusCode, len(payload))
		}
		base := filepath.Base(name)
		if strings.HasPrefix(base, "browserMediaWorklet-") {
			foundWorklet = strings.Contains(string(payload), "registerProcessor")
		}
		if strings.HasPrefix(base, "react-") {
			foundReact = true
		}
		if strings.HasSuffix(base, ".ttf") {
			foundFont = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !foundWorklet || !foundReact || !foundFont {
		t.Fatalf("generated assets worklet=%v react=%v font=%v", foundWorklet, foundReact, foundFont)
	}
	application, err := fs.ReadFile(handler.assets, "assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		"/v1/browser/ws", "/v1/devices", "/v1/egress/config", "/cellular/calls/hangup",
		"X-MDD-CSRF-Token", "browser.media.evidence", "mdd.go.pendingMessage",
	} {
		if !strings.Contains(string(application), marker) {
			t.Errorf("React bundle is missing contract marker %q", marker)
		}
	}
	if strings.Contains(string(application), "data:text/javascript;base64") {
		t.Fatal("AudioWorklet was inlined as a data URL and would violate the production CSP")
	}
}

func TestEmbeddedReactRejectsReservedAndUnknownAssetRoutes(t *testing.T) {
	handler, err := New()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	for _, route := range []string{
		"/api", "/api/unknown", "/v1", "/v1/unknown", "/ws", "/healthz/extra",
		"/assets/missing.js", "/assets/missing", "/unknown.json", "/licenses/missing.txt",
	} {
		response, err := http.Get(server.URL + route)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Errorf("route %s status=%d want=404", route, response.StatusCode)
		}
		assertSecurityHeaders(t, response.Header)
	}
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status=%d", response.Code)
	}
}

func TestEmbeddedReactHEADHasExactLengthAndNoBody(t *testing.T) {
	handler, err := New()
	if err != nil {
		t.Fatal(err)
	}
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	head := httptest.NewRecorder()
	handler.ServeHTTP(head, httptest.NewRequest(http.MethodHead, "/assets/app.js", nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 ||
		head.Header().Get("Content-Length") != get.Header().Get("Content-Length") {
		t.Fatalf("HEAD status=%d bytes=%d length=%q get=%q", head.Code, head.Body.Len(),
			head.Header().Get("Content-Length"), get.Header().Get("Content-Length"))
	}
}

func TestEmbeddedReactIndexUsesExternalAssetsOnly(t *testing.T) {
	html, err := content.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	value := string(html)
	if !strings.Contains(value, `src="/assets/app.js"`) ||
		!strings.Contains(value, `href="/assets/app.css"`) ||
		strings.Contains(value, "<script>") {
		t.Fatalf("index does not match external-only asset contract: %s", value)
	}
}

func assertSecurityHeaders(t *testing.T, header http.Header) {
	t.Helper()
	csp := header.Get("Content-Security-Policy")
	if csp == "" || strings.Contains(csp, "'unsafe-inline'") || strings.Contains(csp, "script-src 'self' data:") ||
		header.Get("X-Content-Type-Options") != "nosniff" || header.Get("Cache-Control") != "no-store" {
		t.Fatalf("invalid security headers: CSP=%q nosniff=%q cache=%q", csp,
			header.Get("X-Content-Type-Options"), header.Get("Cache-Control"))
	}
}
