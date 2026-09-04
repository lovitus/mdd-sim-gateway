package linecatalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStorePersistsSortedLinesAndUniqueCardBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	store, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	first := testLine("line-b", "8944100000000000002")
	second := testLine("line-a", "8944100000000000001")
	if _, err := store.Put(first); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(second); err != nil {
		t.Fatal(err)
	}
	conflict := testLine("line-c", second.CardID)
	if _, err := store.Put(conflict); !errors.Is(err, ErrCardInUse) {
		t.Fatalf("duplicate card error=%v", err)
	}
	snapshot, err := store.Snapshot()
	if err != nil || snapshot.Revision != 3 || len(snapshot.Lines) != 2 || snapshot.Lines[0].ID != "line-a" {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	reopened, err := store.Get("line-b")
	if err != nil || reopened.CardID != first.CardID {
		t.Fatalf("reopened=%+v err=%v", reopened, err)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("catalog mode=%04o", info.Mode().Perm())
		}
	}
}

func TestExpectedRevisionPreventsLostUpdate(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	line := testLine("line-1", "8944100000000000001")
	if _, err := store.Put(line); err != nil {
		t.Fatal(err)
	}
	first := line
	first.Name = "first writer"
	if _, revision, err := store.PutExpected(first, 2); err != nil || revision != 3 {
		t.Fatalf("revision=%d err=%v", revision, err)
	}
	second := line
	second.Name = "stale writer"
	if _, revision, err := store.PutExpected(second, 2); !errors.Is(err, ErrRevision) || revision != 3 {
		t.Fatalf("revision=%d err=%v", revision, err)
	}
	stored, revision, err := store.GetWithRevision(line.ID)
	if err != nil || revision != 3 || stored.Name != first.Name {
		t.Fatalf("stored=%+v revision=%d err=%v", stored, revision, err)
	}
}

func TestCreateExpectedCannotOverwriteLineOrCard(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	created := testLine("line-created", "8944100000000000001")
	created.Enabled = false
	created.SIM = SIMConfig{}
	if _, revision, err := store.CreateExpected(created, 1); err != nil || revision != 2 {
		t.Fatalf("create revision=%d err=%v", revision, err)
	}
	sameID := created
	sameID.CardID = "8944100000000000002"
	if _, revision, err := store.CreateExpected(sameID, 2); !errors.Is(err, ErrAlreadyExists) || revision != 2 {
		t.Fatalf("same ID revision=%d err=%v", revision, err)
	}
	sameCard := created
	sameCard.ID = "line-other"
	if _, revision, err := store.CreateExpected(sameCard, 2); !errors.Is(err, ErrCardInUse) || revision != 2 {
		t.Fatalf("same card revision=%d err=%v", revision, err)
	}
	stale := created
	stale.ID, stale.CardID = "line-stale", "8944100000000000003"
	if _, revision, err := store.CreateExpected(stale, 1); !errors.Is(err, ErrRevision) || revision != 2 {
		t.Fatalf("stale revision=%d err=%v", revision, err)
	}
	snapshot, err := store.Snapshot()
	if err != nil || snapshot.Revision != 2 || len(snapshot.Lines) != 1 || snapshot.Lines[0].ID != created.ID {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
}

func TestRuntimeIntentIsIndependentPersistentAndNoOpStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	store, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	line := testLine("line-1", "8944100000000000001")
	if _, err := store.Put(line); err != nil {
		t.Fatal(err)
	}
	if enabled, found, revision, err := store.RuntimeIntent(line.ID); err != nil || found || enabled || revision != 1 {
		t.Fatalf("initial intent enabled=%v found=%v revision=%d err=%v", enabled, found, revision, err)
	}
	lineEnabled, changed, revision, err := store.SetRuntimeIntent(line.ID, true)
	if err != nil || !lineEnabled || !changed || revision != 2 {
		t.Fatalf("set intent line_enabled=%v changed=%v revision=%d err=%v", lineEnabled, changed, revision, err)
	}
	if _, changed, revision, err = store.SetRuntimeIntent(line.ID, true); err != nil || changed || revision != 2 {
		t.Fatalf("no-op intent changed=%v revision=%d err=%v", changed, revision, err)
	}
	if snapshot, err := store.Snapshot(); err != nil || snapshot.Revision != 2 || !snapshot.Lines[0].Enabled {
		t.Fatalf("runtime intent changed catalog snapshot=%+v err=%v", snapshot, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if enabled, found, revision, err := store.RuntimeIntent(line.ID); err != nil || !found || !enabled || revision != 2 {
		t.Fatalf("reopened intent enabled=%v found=%v revision=%d err=%v", enabled, found, revision, err)
	}
	if _, _, _, err := store.SetRuntimeIntent("missing", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing line intent error=%v", err)
	}
}

func TestLegacyImportIsAtomicReadOnlyAndCannotOverwrite(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "config.yaml")
	payload := `settings:
  ignored_secret: do-not-import
instances:
  "2":
    id: line-b
    name: France
    enabled: false
    iccid: "8933012345678901234"
    imei: "123456789012345"
    msisdn: "+33123456789"
    smsc: "+33123456700"
    proxy_country: fr
    epdg: epdg.example
    pcscf: [pcscf-b.example, pcscf-a.example]
    ports: {sip_udp: 5060}
    ami_secret: must-not-survive
  "3":
    id: deleted
    soft_deleted: true
    iccid: "8933012345678901235"
  "1":
    name: UK
    iccid: 8944100000000000001
    imsi: 234100000000001
    mcc: 234
    mnc: 10
    proxy_country: gb
    network:
      epdg_address: epdg.epc.example
      pcscf: pcscf.ims.example
    ims:
      network: udp
      expires: 600
    sip:
      pani: 'IEEE-802.11\;i-wlan-node-id="020000000001"\;country=GB'
      visited_network_id: visited.example
      access_type: wlan1
      user_eq_phone: true
      user_agent: Legacy-Handset/1.0
`
	if err := os.WriteFile(source, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(source)
	lines, receipt, err := ReadLegacy(source)
	if err != nil || len(lines) != 2 || receipt.LineCount != 2 || len(receipt.SourceSHA256) != 64 {
		t.Fatalf("lines=%+v receipt=%+v err=%v", lines, receipt, err)
	}
	desiredPath := filepath.Join(directory, "desired.json")
	desired := `{"version":1,"lines":[{"id":"1","enabled":true,"country":"gb"},{"id":"line-b","enabled":false,"country":"fr"}]}`
	if err := os.WriteFile(desiredPath, []byte(desired), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, receipt.EgressSourceSHA256, err = ApplyLegacyDesiredEgress(lines, desiredPath)
	if err != nil || len(receipt.EgressSourceSHA256) != 64 {
		t.Fatalf("apply egress lines=%+v receipt=%+v err=%v", lines, receipt, err)
	}
	if lines[0].ID != "1" || lines[0].Enabled != true || lines[1].ID != "line-b" || lines[1].Enabled != false ||
		lines[0].Network.EgressCountry != "gb" || lines[1].Network.EgressCountry != "fr" ||
		strings.Join(lines[1].Network.PCSCF, ",") != "pcscf-a.example,pcscf-b.example" ||
		lines[0].IMS.AccessNetworkInfo != `IEEE-802.11;i-wlan-node-id="020000000001";country=GB` ||
		lines[0].IMS.VisitedNetworkID != "visited.example" || lines[0].IMS.AccessType != "wlan1" ||
		lines[0].IMS.UserAgent != "Legacy-Handset/1.0" || !lines[0].IMS.UserEqualsPhone {
		t.Fatalf("unexpected imported lines: %+v", lines)
	}
	store, err := Open(filepath.Join(directory, "new", "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ImportEmpty(lines, receipt); err != nil {
		t.Fatal(err)
	}
	if err := store.ImportEmpty(lines, receipt); !errors.Is(err, ErrNotEmpty) {
		t.Fatalf("second import error=%v", err)
	}
	after, _ := os.ReadFile(source)
	if string(after) != string(before) {
		t.Fatal("legacy source changed during import")
	}
	stored, _ := store.Snapshot()
	persisted, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	encoded := strings.ToLower(strings.TrimSpace(string(persisted)))
	if strings.Contains(encoded, "ami_secret") || strings.Contains(encoded, "sip_udp") || strings.Contains(encoded, "must-not-survive") {
		t.Fatalf("legacy runtime fields leaked into catalog: %s", encoded)
	}
}

func TestDisabledLineAllowsMissingButNotInvalidSIMIdentity(t *testing.T) {
	line := testLine("line-disabled", "8944100000000000001")
	line.Enabled = false
	line.SIM.IMSI, line.SIM.MCC, line.SIM.MNC = "", "", ""
	if err := line.normalizeAndValidate(); err != nil {
		t.Fatalf("disabled placeholder rejected: %v", err)
	}
	line.SIM.MNC = "x"
	if err := line.normalizeAndValidate(); err == nil {
		t.Fatal("disabled line with invalid non-empty MNC was accepted")
	}
}

func TestOptionalIMSUserAgentIsNormalizedPersistedAndValidated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	store, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	line := testLine("line-ua", "8944100000000000001")
	line.IMS.UserAgent = "  Carrier-Handset/1.0  "
	if _, err := store.Put(line); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stored, err := store.Get(line.ID)
	if err != nil || stored.IMS.UserAgent != "Carrier-Handset/1.0" {
		t.Fatalf("stored User-Agent=%q err=%v", stored.IMS.UserAgent, err)
	}
	for _, invalid := range []string{"Carrier\r\nInjected: value", strings.Repeat("x", 513)} {
		candidate := testLine("line-invalid", "8944100000000000002")
		candidate.IMS.UserAgent = invalid
		if _, err := store.Put(candidate); err == nil {
			t.Fatalf("invalid User-Agent was accepted: %q", invalid)
		}
	}
}

func TestInvalidLegacyBatchLeavesCatalogEmpty(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "config.yaml")
	payload := `instances:
  good: {iccid: "8944100000000000001", imsi: "234100000000001", mcc: "234", mnc: "10"}
  bad: {iccid: "", imsi: "234100000000002", mcc: "234", mnc: "10"}
`
	if err := os.WriteFile(source, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadLegacy(source); err == nil {
		t.Fatal("invalid legacy batch accepted")
	}
	store, _ := Open(filepath.Join(directory, "catalog.db"), time.Second)
	defer store.Close()
	snapshot, _ := store.Snapshot()
	if snapshot.Revision != 1 || len(snapshot.Lines) != 0 {
		t.Fatalf("catalog changed after failed parse: %+v", snapshot)
	}
}

func TestLegacyDesiredEgressRejectsMismatchedSnapshot(t *testing.T) {
	directory := t.TempDir()
	line := testLine("line-1", "8944100000000000001")
	line.Network.EgressCountry = "gb"
	for name, desired := range map[string]string{
		"country": `{"version":1,"lines":[{"id":"line-1","enabled":true,"country":"fr"}]}`,
		"enabled": `{"version":1,"lines":[{"id":"line-1","enabled":false,"country":"gb"}]}`,
		"missing": `{"version":1,"lines":[{"id":"other","enabled":true,"country":"gb"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(directory, name+".json")
			if err := os.WriteFile(path, []byte(desired), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, _, err := ApplyLegacyDesiredEgress([]Line{line}, path); err == nil {
				t.Fatal("mismatched legacy snapshot was accepted")
			}
		})
	}
}

func TestConditionalCatalogHandler(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Put(testLine("line-1", "8944100000000000001")); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store)
	request := httptest.NewRequest(http.MethodGet, "/v1/catalog/lines", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"2"` || !strings.Contains(response.Body.String(), `"revision":2`) {
		t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
	}
	updated := testLine("line-1", "8944100000000000001")
	updated.Name = "updated"
	payload, _ := json.Marshal(updated)
	request = httptest.NewRequest(http.MethodPut, "/v1/catalog/lines/line-1", bytes.NewReader(payload))
	request.SetPathValue("lineID", "line-1")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match status=%d body=%s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPut, "/v1/catalog/lines/line-1", bytes.NewReader(payload))
	request.SetPathValue("lineID", "line-1")
	request.Header.Set("If-Match", `"2"`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"3"` {
		t.Fatalf("update status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPut, "/v1/catalog/lines/line-1", bytes.NewReader(payload))
	request.SetPathValue("lineID", "line-1")
	request.Header.Set("If-Match", `"2"`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPreconditionFailed || response.Header().Get("ETag") != `"3"` {
		t.Fatalf("stale update status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	managed := updated
	managed.SIM.IMEI = "123456789012346"
	payload, _ = json.Marshal(managed)
	request = httptest.NewRequest(http.MethodPut, "/v1/catalog/lines/line-1", bytes.NewReader(payload))
	request.SetPathValue("lineID", "line-1")
	request.Header.Set("If-Match", `"3"`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "imei_binding_managed") {
		t.Fatalf("managed IMEI status=%d body=%s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/v1/catalog/lines", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("mutation status=%d", response.Code)
	}
}

func TestSoftDeleteAndRestorePreserveCardOwnership(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	line := testLine("line-lifecycle", "8944100000000000001")
	line.Enabled = false
	if _, err := store.Put(line); err != nil {
		t.Fatal(err)
	}
	if _, revision, err := store.SetDeletedExpected(line.ID, true, 2); err != nil || revision != 3 {
		t.Fatalf("soft-delete revision=%d err=%v", revision, err)
	}
	active, err := store.Snapshot()
	if err != nil || len(active.Lines) != 0 {
		t.Fatalf("active snapshot=%+v err=%v", active, err)
	}
	all, err := store.SnapshotIncludingDeleted()
	if err != nil || len(all.Lines) != 1 || !all.Lines[0].Deleted {
		t.Fatalf("recycle snapshot=%+v err=%v", all, err)
	}
	if _, _, err := store.CreateExpected(testLine("line-new", line.CardID), all.Revision); !errors.Is(err, ErrCardInUse) {
		t.Fatalf("deleted card was claimable: %v", err)
	}
	if _, revision, err := store.SetDeletedExpected(line.ID, false, all.Revision); err != nil || revision != 4 {
		t.Fatalf("restore revision=%d err=%v", revision, err)
	}
	restored, err := store.Get(line.ID)
	if err != nil || restored.Deleted {
		t.Fatalf("restored=%+v err=%v", restored, err)
	}
}

func TestLifecycleHTTPRequiresRevisionAndReturnsTypedConflict(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	line := testLine("line-http-lifecycle", "8944100000000000002")
	line.Enabled = false
	if _, err := store.Put(line); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store)
	request := httptest.NewRequest(http.MethodPost, "/v1/catalog/lines/line-http-lifecycle/soft-delete", nil)
	request.SetPathValue("lineID", line.ID)
	request.SetPathValue("operation", "soft-delete")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing revision status=%d body=%s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/v1/catalog/lines/line-http-lifecycle/soft-delete", nil)
	request.SetPathValue("lineID", line.ID)
	request.SetPathValue("operation", "soft-delete")
	request.Header.Set("If-Match", `"2"`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"3"` {
		t.Fatalf("delete status=%d etag=%q body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
}

func testLine(id, cardID string) Line {
	return Line{ID: id, Name: id, Enabled: true, CardID: cardID, SIM: SIMConfig{
		IMSI: "234100000000001", MCC: "234", MNC: "10", IMEI: "123456789012345",
	}}
}
