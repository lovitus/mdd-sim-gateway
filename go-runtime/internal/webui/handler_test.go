package webui

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
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
		{"/index.html", "text/html; charset=utf-8", "保存到 catalog"},
		{"/assets/app.js", "text/javascript; charset=utf-8", `"If-Match"`},
		{"/assets/call-audio.js", "text/javascript; charset=utf-8", "browser.media.resume"},
		{"/assets/call-worklet.js", "text/javascript; charset=utf-8", "registerProcessor"},
		{"/assets/app.css", "text/css; charset=utf-8", ":root"},
		{"/assets/qr/decode.js", "text/javascript; charset=utf-8", `from "./index.js"`},
		{"/assets/qr/index.js", "text/javascript; charset=utf-8", "export class Bitmap"},
		{"/assets/qr/LICENSE", "text/plain; charset=utf-8", "Apache License"},
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

func TestEmbeddedUIQRImageContractAndPinnedAssets(t *testing.T) {
	javascript, err := content.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		`import("/assets/qr/decode.js")`,
		`createImageBitmap(file)`,
		`file.size>16*1024*1024`,
		`form.addEventListener("paste"`,
		`form.addEventListener("drop"`,
		`parseEUICCActivationCode(text)`,
		"图片只在浏览器内解析",
	} {
		if !strings.Contains(string(javascript), marker) {
			t.Errorf("embedded UI is missing local QR marker %q", marker)
		}
	}

	want := map[string]string{
		"assets/qr/decode.js": "89127c12e70e446eea634c88f3e90d719b9f15ac56def386a8809e24e9f2ee61",
		"assets/qr/index.js":  "764958030a06685bfb4678cec6ef0ec4ecf89dad563c237e9e644e7d6ef24033",
		"assets/qr/LICENSE":   "cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30",
	}
	for name, expected := range want {
		payload, err := content.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if actual := fmt.Sprintf("%x", sha256.Sum256(payload)); actual != expected {
			t.Errorf("%s SHA-256=%s want=%s", name, actual, expected)
		}
	}
}

func TestEmbeddedUICellularCallContract(t *testing.T) {
	javascript, err := content.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		`/v1/cellular/media/leases`,
		`/cellular/calls/start`,
		`/cellular/calls/hangup`,
		`/cellular/calls/status`,
		`cellularTargetForLine`,
	} {
		if !strings.Contains(string(javascript), marker) {
			t.Errorf("embedded UI is missing cellular call marker %q", marker)
		}
	}
}

func TestEmbeddedUIEUICCDownloadContract(t *testing.T) {
	javascript, err := content.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	payload := string(javascript)
	for _, marker := range []string{
		`/v1/euiccs/${encodeURIComponent(entry.euicc.eid)}/downloads`,
		`/downloads/${encodeURIComponent(operation)}`,
		`/cancel`,
		`LPA:1$${server}$${matchingID}`,
		`status_error`,
	} {
		if !strings.Contains(payload, marker) {
			t.Errorf("embedded UI is missing eUICC download marker %q", marker)
		}
	}
	start := strings.Index(payload, "function saveEUICCDownloads()")
	end := strings.Index(payload, "async function loadRuntime()")
	if start < 0 || end <= start {
		t.Fatal("embedded UI download persistence boundary is missing")
	}
	persistence := payload[start:end]
	if strings.Contains(persistence, "activation_code") || strings.Contains(persistence, "confirmation_code") {
		t.Fatal("embedded UI persists one-use eUICC download secrets")
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

func TestEmbeddedUIStaticElementReferencesExist(t *testing.T) {
	javascript, err := content.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	html, err := content.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, match := range regexp.MustCompile(`el\("([^"]+)"\)`).FindAllSubmatch(javascript, -1) {
		marker := `id="` + string(match[1]) + `"`
		if !strings.Contains(string(html), marker) {
			t.Errorf("app.js references missing static element %s", marker)
		}
	}
}
