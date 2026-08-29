package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedUIRoutesAndSecurityHeaders(t *testing.T) {
	handler, err := New()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	for _, test := range []struct {
		path, contentType, contains string
	}{
		{"/", "text/html; charset=utf-8", "MDD Go Console"},
		{"/index.html", "text/html; charset=utf-8", "发送短信"},
		{"/assets/app.js", "text/javascript; charset=utf-8", "media_session_id"},
		{"/assets/call-audio.js", "text/javascript; charset=utf-8", "browser.media.resume"},
		{"/assets/call-worklet.js", "text/javascript; charset=utf-8", "registerProcessor"},
		{"/assets/app.css", "text/css; charset=utf-8", ":root"},
	} {
		response, err := http.Get(server.URL + test.path)
		if err != nil {
			t.Fatal(err)
		}
		payload, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != test.contentType ||
			!strings.Contains(string(payload), test.contains) {
			t.Fatalf("path=%s status=%d type=%q body=%q", test.path, response.StatusCode, response.Header.Get("Content-Type"), payload)
		}
		if response.Header.Get("Content-Security-Policy") == "" || response.Header.Get("X-Content-Type-Options") != "nosniff" ||
			response.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("path=%s missing security headers", test.path)
		}
	}
}

func TestEmbeddedUIDoesNotCatchUnknownRoutes(t *testing.T) {
	handler, _ := New()
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/not-a-route", nil),
		httptest.NewRequest(http.MethodGet, "/assets/missing.js", nil),
		httptest.NewRequest(http.MethodPost, "/", nil),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		want := http.StatusNotFound
		if request.Method == http.MethodPost {
			want = http.StatusMethodNotAllowed
		}
		if response.Code != want {
			t.Fatalf("%s %s status=%d want=%d", request.Method, request.URL.Path, response.Code, want)
		}
	}
}

func TestEmbeddedUIHeadHasNoBody(t *testing.T) {
	handler, _ := New()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodHead, "/assets/app.js", nil))
	if response.Code != http.StatusOK || response.Body.Len() != 0 || response.Header().Get("Content-Length") == "" {
		t.Fatalf("status=%d len=%d headers=%v", response.Code, response.Body.Len(), response.Header())
	}
}
