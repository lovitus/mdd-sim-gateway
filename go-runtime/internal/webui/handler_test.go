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

func TestEmbeddedUIShellNavigationThemeNoticeAndSecurityCoexist(t *testing.T) {
	javascript, err := content.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	html, err := content.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	css, err := content.ReadFile("assets/app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		`location.hash.replace(/^#\/?/`,
		`history.replaceState(null,"",wanted)`,
		`window.addEventListener("hashchange"`,
		`localStorage.getItem("theme")`,
		`document.querySelectorAll(".theme-switch [data-theme]")`,
		`document.documentElement.dataset.theme=theme`,
		`setTimeout(()=>showNotice(""),15000)`,
		`headers.set("X-MDD-CSRF-Token",state.csrf)`,
		`credentials:"same-origin"`,
	} {
		if !strings.Contains(string(javascript), marker) {
			t.Errorf("embedded UI shell is missing marker %q", marker)
		}
	}
	if strings.Contains(string(javascript), "function render(snapshot){\n  showNotice(\"\")") {
		t.Error("live snapshots must not dismiss a user notice before its own timeout")
	}
	for _, color := range []string{"#344054", "#b42318", "#067647", "#925c00"} {
		if strings.Contains(string(javascript), `style.color="`+color+`"`) {
			t.Errorf("inline status color %s bypasses dark and auto theme contrast", color)
		}
	}
	for _, marker := range []string{
		`id="page-title"`, `id="page-subtitle"`, `id="notice-dismiss"`,
		`id="menu-toggle"`, `data-theme="auto"`, `data-view="overview"`,
	} {
		if !strings.Contains(string(html), marker) {
			t.Errorf("embedded UI shell is missing HTML marker %q", marker)
		}
	}
	for _, marker := range []string{`:root[data-theme="dark"]`, `--status-danger:#ff938a`, `color:var(--status-danger)`, `.sidebar.open`, `.page-header .header-actions>span{display:inline-flex}`, `.euicc-modal-backdrop{z-index:120}`, `@media(max-width:800px)`} {
		if !strings.Contains(string(css), marker) {
			t.Errorf("embedded UI shell is missing CSS marker %q", marker)
		}
	}
	if strings.Count(string(css), `--status-danger:#ff938a`) != 2 {
		t.Error("explicit dark and auto-dark themes must both define readable semantic status colors")
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
		`/cellular/calls/dtmf`,
		`/cellular/calls/hangup`,
		`/cellular/calls/status`,
		`operationReadyForLine(line.id,"cellular_call")`,
		`expected_card_id`,
	} {
		if !strings.Contains(string(javascript), marker) {
			t.Errorf("embedded UI is missing cellular call marker %q", marker)
		}
	}
	for _, marker := range []string{`/v1/calls?limit=100`, `/vowifi/calls/dtmf`} {
		if !strings.Contains(string(javascript), marker) {
			t.Errorf("embedded UI is missing call keypad/history marker %q", marker)
		}
	}
	html, err := content.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{`data-dial-key`, `data-dtmf-key`, `id="call-history"`} {
		if !strings.Contains(string(html), marker) {
			t.Errorf("embedded UI is missing call keypad/history marker %q", marker)
		}
	}
	if strings.Contains(string(javascript), `function cellularTargetForLine`) {
		t.Fatal("embedded UI must consume Core cellular_call admission instead of reimplementing Agent routing")
	}
}

func TestEmbeddedUILineCatalogPreservesIMSPresentation(t *testing.T) {
	javascript, err := content.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{`user_agent`, `access_network_info`, `visited_network_id`, `access_type`, `user_equals_phone`} {
		if !strings.Contains(string(javascript), marker) {
			t.Errorf("embedded UI is missing IMS presentation marker %q", marker)
		}
	}
	html, err := content.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), `id="line-config-user-agent"`) {
		t.Error("embedded UI is missing the IMS User-Agent editor")
	}
}

func TestEmbeddedUILiveCardBootstrapIsExplicitAndSideEffectFree(t *testing.T) {
	javascript, err := content.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	html, err := content.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		`jsonRequest("/v1/line-candidates")`,
		`/claim`,
		`headers:{"If-Match"`,
		`schema_version:1,name:`,
		`创建禁用草稿`,
		`不会启动、注册、拨号、发短信或应用 Provider`,
		`同一 ICCID 当前有多个附件`,
	} {
		if !strings.Contains(string(javascript), marker) {
			t.Errorf("embedded UI is missing line-bootstrap marker %q", marker)
		}
	}
	for _, marker := range []string{`id="refresh-line-candidates"`, `id="line-candidates"`, `创建仅写入禁用草稿`} {
		if !strings.Contains(string(html), marker) {
			t.Errorf("embedded UI is missing line-bootstrap HTML marker %q", marker)
		}
	}
	if strings.Contains(string(html), `value="raw"`) {
		t.Fatal("embedded UI must not expose raw modem passthrough before isolation is proved")
	}
}

func TestEmbeddedUICountryEgressDiagnosticContract(t *testing.T) {
	javascript, err := content.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	html, err := content.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		`/v1/egress/exits`, `/test`, `任一目标通过即成功`, `country_egress_udp`,
	} {
		if !strings.Contains(string(javascript), marker) {
			t.Errorf("embedded UI is missing country-egress diagnostic marker %q", marker)
		}
	}
	if !strings.Contains(string(html), "这里只证明 UDP 出口链路，不代表 VoWiFi 注册、短信或通话健康") {
		t.Fatal("embedded UI conflates UDP egress with business health")
	}
}

func TestEmbeddedUICountryEgressConfigurationSeparatesSaveAndApply(t *testing.T) {
	javascript, err := content.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	html, err := content.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		`jsonRequest("/v1/egress/config",{method:"PUT",headers:{"If-Match"`,
		`jsonRequest("/v1/egress/config/apply",{method:"POST"`,
		`不会改变 sing-box、路由或 Provider`,
		`配置仍可保存；应用服务恢复前不会改变运行网络`,
		`error.status===412`,
	} {
		if !strings.Contains(string(javascript), marker) {
			t.Errorf("embedded UI is missing country-egress configuration marker %q", marker)
		}
	}
	for _, marker := range []string{"国家出口配置", "保存不会改变运行网络", "应用已保存配置"} {
		if !strings.Contains(string(html), marker) {
			t.Errorf("embedded UI is missing country-egress configuration text %q", marker)
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

func TestEmbeddedUIEUICCNicknameContract(t *testing.T) {
	javascript, err := content.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	payload := string(javascript)
	for _, marker := range []string{
		`/profiles/${encodeURIComponent(profile.iccid)}/nickname`,
		`expected_nickname:profile.nickname||""`,
		`new TextEncoder().encode(nickname).length>64`,
		`openEUICCNicknameDialog(profile)`,
		`aria-modal`,
	} {
		if !strings.Contains(payload, marker) {
			t.Errorf("embedded UI is missing eUICC nickname marker %q", marker)
		}
	}
	if strings.Contains(payload, `prompt(`) {
		t.Fatal("embedded UI depends on an unsupported native prompt")
	}
}

func TestEmbeddedUIEUICCDiscoveryContract(t *testing.T) {
	javascript, err := content.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	payload := string(javascript)
	for _, marker := range []string{
		`/v1/euiccs/${encodeURIComponent(entry.euicc.eid)}/discovery`,
		`profile_discovery`,
		`lpa.ds.gsma.com`,
		`不会下载、写卡、保存参数或自动重试`,
		`/^\d{15}$/`,
	} {
		if !strings.Contains(payload, marker) {
			t.Errorf("embedded UI is missing eUICC discovery marker %q", marker)
		}
	}
	start := strings.Index(payload, "function showEUICCDiscoveryForm")
	end := strings.Index(payload, "function showEUICCDownloadForm")
	if start < 0 || end <= start {
		t.Fatal("embedded UI discovery boundary is missing")
	}
	discovery := payload[start:end]
	if strings.Contains(discovery, "localStorage") || strings.Contains(discovery, "setTimeout(") {
		t.Fatal("embedded UI persists or automatically retries SM-DS discovery")
	}
}

func TestEmbeddedUIEUICCNotificationDeliveryIsExplicitAndNeverRetried(t *testing.T) {
	javascript, err := content.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	payload := string(javascript)
	for _, marker := range []string{
		`/v1/euiccs/${encodeURIComponent(entry.euicc.eid)}/notifications`,
		`notification_inventory`, `notification_delivery`, `notification_removal`, `查看卡内通知`, `sequence_number`,
		`发送并确认移除`, `confirmed:true`, `/deliver`,
		`euicc_notification_acknowledged_not_removed`, `仅移除已确认记录`,
		`receiver_acknowledged:true`, `/remove`,
	} {
		if !strings.Contains(payload, marker) {
			t.Errorf("embedded UI is missing eUICC notification marker %q", marker)
		}
	}
	start := strings.Index(payload, "async function loadEUICCNotifications")
	if start < 0 {
		t.Fatal("embedded UI notification boundary is missing")
	}
	end := strings.Index(payload[start:], "function parseEUICCActivationCode")
	if end < 0 {
		t.Fatal("embedded UI notification boundary is missing")
	}
	notifications := payload[start : start+end]
	if strings.Contains(notifications, "localStorage") || strings.Contains(notifications, "setTimeout(") ||
		strings.Contains(notifications, `method:"DELETE"`) {
		t.Fatal("embedded UI persists, retries, or uses DELETE for notifications")
	}
	inventoryEnd := strings.Index(notifications, "async function deliverEUICCNotification")
	if inventoryEnd < 0 || strings.Contains(notifications[:inventoryEnd], "/remove") {
		t.Fatal("notification inventory exposes removal without an acknowledged delivery failure")
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
