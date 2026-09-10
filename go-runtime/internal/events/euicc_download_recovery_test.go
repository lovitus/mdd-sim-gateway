package events

import (
	"encoding/json"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDownloadRecoveryOptInAndRedactedSummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	store, err := OpenBoltStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	command := agentlink.EUICCDownloadCommand{EID: "89049032000000000000000000000001", OperationID: "download-retain", Action: agentlink.EUICCDownloadStart, ActivationCode: "LPA:1$example.com$private-activation", ConfirmationCode: "private-confirmation"}
	if err := store.SaveEUICCDownloadRecovery(command, true); err != nil {
		t.Fatal(err)
	}
	metadata := agentlink.EUICCDownloadMetadata{ICCID: "8944000000000000001", ProfileName: "Original profile", ServiceProviderName: "Original provider"}
	if err := store.BindEUICCDownloadRecovery(command.EID, command.OperationID, metadata, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveEUICCDownloadRecovery(command, true); err != nil {
		t.Fatal(err)
	}
	changed := command
	changed.ActivationCode = "LPA:1$example.com$changed"
	if err := store.SaveEUICCDownloadRecovery(changed, true); err == nil {
		t.Fatal("changed recovery code accepted")
	}
	store.Close()
	store, err = OpenBoltStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record, found, err := store.EUICCProfileRecovery(command.EID, metadata.ICCID)
	if err != nil || !found || record.ActivationCode != command.ActivationCode || record.ConfirmationCode != command.ConfirmationCode {
		t.Fatal("recovery data lost")
	}
	public, _ := json.Marshal(record.Summary())
	if strings.Contains(string(public), "private-") || strings.Contains(string(public), "request_hash") {
		t.Fatal("summary leaked sensitive data")
	}
	if !record.Summary().ActivationCodeSaved || !record.Summary().ConfirmationCodeSaved {
		t.Fatal("redacted presence flags missing")
	}
	metadata.ProfileName = "changed name"
	if err := store.BindEUICCDownloadRecovery(command.EID, command.OperationID, metadata, time.Now()); err == nil {
		t.Fatal("original profile metadata overwritten")
	}
	metadata.ProfileName = "Original profile"
	command.OperationID = "download-no-retain"
	metadata.ICCID = "8944000000000000002"
	if err := store.SaveEUICCDownloadRecovery(command, false); err != nil {
		t.Fatal(err)
	}
	if err := store.BindEUICCDownloadRecovery(command.EID, command.OperationID, metadata, time.Now()); err != nil {
		t.Fatal(err)
	}
	record, found, err = store.EUICCProfileRecovery(command.EID, metadata.ICCID)
	if err != nil || !found || record.ActivationCode != "" || record.ConfirmationCode != "" {
		t.Fatal("opt-out retained codes")
	}
	legacy := command
	legacy.OperationID = "legacy-download"
	metadata.ICCID = "8944000000000000003"
	if err := store.BindEUICCDownloadRecovery(legacy.EID, legacy.OperationID, metadata, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.RestoreEUICCRecoveryCodes(legacy, metadata.ICCID); err != nil {
		t.Fatal(err)
	}
	record, found, err = store.EUICCProfileRecovery(legacy.EID, metadata.ICCID)
	if err != nil || !found || record.CodeSource != "operator_recovered" || record.ActivationCode != legacy.ActivationCode {
		t.Fatal("legacy code recovery failed")
	}
}
