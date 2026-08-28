package linecatalog

import (
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
	if err != nil || snapshot.Revision != 2 || len(snapshot.Lines) != 2 || snapshot.Lines[0].ID != "line-a" {
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
    imsi: "208151234567890"
    mcc: "208"
    mnc: "15"
    imei: "123456789012345"
    msisdn: "+33123456789"
    smsc: "+33123456700"
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
    network:
      epdg_address: epdg.epc.example
      pcscf: pcscf.ims.example
    ims:
      network: udp
      expires: 600
`
	if err := os.WriteFile(source, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(source)
	lines, receipt, err := ReadLegacy(source)
	if err != nil || len(lines) != 2 || receipt.LineCount != 2 || len(receipt.SourceSHA256) != 64 {
		t.Fatalf("lines=%+v receipt=%+v err=%v", lines, receipt, err)
	}
	if lines[0].ID != "1" || lines[0].Enabled != true || lines[1].ID != "line-b" || lines[1].Enabled != false ||
		strings.Join(lines[1].Network.PCSCF, ",") != "pcscf-a.example,pcscf-b.example" {
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
	if snapshot.Revision != 0 || len(snapshot.Lines) != 0 {
		t.Fatalf("catalog changed after failed parse: %+v", snapshot)
	}
}

func TestReadOnlyCatalogHandler(t *testing.T) {
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
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"revision":1`) {
		t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/v1/catalog/lines", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("mutation status=%d", response.Code)
	}
}

func testLine(id, cardID string) Line {
	return Line{ID: id, Name: id, Enabled: true, CardID: cardID, SIM: SIMConfig{
		IMSI: "234100000000001", MCC: "234", MNC: "10", IMEI: "123456789012345",
	}}
}
