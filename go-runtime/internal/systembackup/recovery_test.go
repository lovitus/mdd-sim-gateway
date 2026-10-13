package systembackup

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/cellularmessages"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providermessages"
)

func TestBackupRetainsCatalogRecoveryAndSMSBarriersWithoutCredentials(t *testing.T) {
	root, now := t.TempDir(), time.Now().UTC()
	catalog, err := linecatalog.Open(filepath.Join(root, "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	line := linecatalog.Line{SchemaVersion: 1, ID: "archived-line", CardID: "8944100000000000001",
		SIM:     linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10"},
		Network: linecatalog.NetworkConfig{APNProfiles: []linecatalog.APNProfile{{ID: "internet", Name: "Internet", APN: "internet", Auth: "PAP", Username: "subscriber", Password: "secret-not-for-export", PasswordSet: true}}}}
	if _, err := catalog.Put(line); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := catalog.SetRuntimeIntent(line.ID, false); err != nil {
		t.Fatal(err)
	}
	binding, rawRevision, _, err := catalog.PutRawModemBindingExpected(linecatalog.RawModemBinding{
		LineID: line.ID, CardID: line.CardID, SourceAgentID: "source-agent", EquipmentID: "123456789012345", ImporterAgentID: "importer-agent", Enabled: true}, 1)
	if err != nil {
		t.Fatal(err)
	}
	binding.Enabled = false
	if _, _, _, err := catalog.PutRawModemBindingExpected(binding, rawRevision); err != nil {
		t.Fatal(err)
	}
	if _, _, err := catalog.SetDeletedExpected(line.ID, true, 2); err != nil {
		t.Fatal(err)
	}
	entry := linecatalog.IMEIPoolEntry{ID: "phone", Name: "Fixture phone", IMEI: "862547055201716"}
	if _, _, _, err := catalog.PutIMEIPoolEntryExpected(entry, 1); err != nil {
		t.Fatal(err)
	}
	pendingCard := "8944100000000002420"
	if _, _, _, _, err := catalog.BindIMEICardExpected(entry.ID, pendingCard, 2, 3); err != nil {
		t.Fatal(err)
	}
	if _, _, err := catalog.PrepareDeletionExpected(line.ID, "deletion-operation", false, 4, now); err != nil {
		t.Fatal(err)
	}
	receipt := linecatalog.OperationReceipt{SchemaVersion: 1, OperationID: "provision-evidence", Kind: linecatalog.OperationProvision,
		State: linecatalog.OperationUnknown, RequestDigest: strings.Repeat("a", 64), AttemptCount: 1, CreatedAt: now, UpdatedAt: now}
	if err := catalog.PutOperation(receipt); err != nil {
		t.Fatal(err)
	}
	sms, err := cellularmessages.OpenOperationStore(filepath.Join(root, "sms.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer sms.Close()
	history, err := providermessages.OpenStore(filepath.Join(root, "history.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer history.Close()
	if err := sms.PrepareLineDeletion(line.ID, history); err != nil {
		t.Fatal(err)
	}
	unknown := cellularmessages.OperationRecord{OperationID: "unknown-sms", MessageID: "message", LineID: "other-line", CreatedAt: now}
	if _, _, err := sms.Begin(unknown); err != nil {
		t.Fatal(err)
	}
	if _, err := sms.Mark(unknown.OperationID, "uncertain", nil); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler([]Source{
		{Name: "catalog.json", Read: catalog.Backup, Recovery: "export_v2; reconciliation only; no automatic operation replay"},
		{Name: "cellular-sms-operations.db", Read: sms.Backup, Recovery: "operations and deletion barriers; never a send queue"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/system/backups", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("backup status=%d %s", response.Code, response.Body.String())
	}
	archive, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, file := range archive.File {
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		files[file.Name], err = io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	var exported linecatalog.BackupExport
	if err := json.Unmarshal(files["catalog.json"], &exported); err != nil {
		t.Fatal(err)
	}
	if exported.ExportVersion != 2 || exported.Revision != 4 || len(exported.Lines) != 1 || !exported.Lines[0].Deleted ||
		exported.PendingIMEIBindings[pendingCard] != entry.ID || len(exported.IMEIEntries) != 1 || exported.IMEIEntries[0].IMEI != entry.IMEI ||
		len(exported.Operations) != 1 || exported.Operations[0].State != linecatalog.OperationUnknown || len(exported.Deletions) != 1 ||
		len(exported.RawModemBindings) != 1 || exported.RawModemRevision != 3 || exported.IMEIPoolRevision != 3 || exported.RuntimeIntentRevision != 2 {
		t.Fatalf("RECOVERY_EXPORT_INCOMPLETE: %+v", exported)
	}
	if enabled, found := exported.RuntimeIntents[line.ID]; !found || enabled {
		t.Fatal("explicit disabled intent lost")
	}
	if bytes.Contains(files["catalog.json"], []byte("secret-not-for-export")) || bytes.Contains(files["catalog.json"], []byte("operation_digest_key")) {
		t.Fatal("credential exported")
	}
	if current, err := catalog.Get(line.ID); err != nil || current.Network.APNProfiles[0].Password != "secret-not-for-export" {
		t.Fatal("export changed source", err)
	}
	var manifest struct {
		SchemaVersion int            `json:"schema_version"`
		Consistency   string         `json:"consistency"`
		Recovery      string         `json:"recovery"`
		Entries       []FileEvidence `json:"entries"`
	}
	if err := json.Unmarshal(files["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != 2 || manifest.Consistency != "per_source_not_cross_database_atomic" ||
		!strings.Contains(manifest.Recovery, "no online restore") || len(manifest.Entries) != 2 || manifest.Entries[0].Recovery == "" || manifest.Entries[1].Recovery == "" {
		t.Fatalf("manifest=%+v", manifest)
	}
	path := filepath.Join(root, "offline-sms.db")
	if err := os.WriteFile(path, files["cellular-sms-operations.db"], 0600); err != nil {
		t.Fatal(err)
	}
	restored, err := cellularmessages.OpenOperationStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if got, found, err := restored.Get(unknown.OperationID); err != nil || !found || got.State != "uncertain" {
		t.Fatal("unknown operation missing", err)
	}
	unknown.LineID, unknown.OperationID = line.ID, "late-operation"
	if _, _, err := restored.Begin(unknown); err == nil {
		t.Fatal("backup lost deletion barrier")
	}
}
