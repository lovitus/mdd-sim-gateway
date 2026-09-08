package core

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/provideradmin"
)

type defaultApplyStub struct {
	calls int
	fail  bool
	line  string
}

func (stub *defaultApplyStub) ApplyAdded(_ context.Context, revision uint64, line string) (provideradmin.ApplyResult, error) {
	stub.calls++
	stub.line = line
	if stub.fail {
		return provideradmin.ApplyResult{}, errors.New("response lost")
	}
	return provideradmin.ApplyResult{CatalogRevision: revision, State: "applied"}, nil
}

func TestDefaultProviderApplyRequiresSuccessAndDoesNotRepeatUnknown(t *testing.T) {
	for _, fail := range []bool{false, true} {
		store, err := linecatalog.Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		line := linecatalog.Line{SchemaVersion: 1, ID: "line-default", CardID: "8944100000000000001", Enabled: true, HardwareProvisionState: "provisioned",
			SIM: linecatalog.SIMConfig{IMSI: "234100000000001", MCC: "234", MNC: "10", IMEI: "862547055201716", SMSC: "+441234567890"}}
		if _, err := store.Put(line); err != nil {
			t.Fatal(err)
		}
		handler := &ProvisionHandler{store: store, now: time.Now}
		stub := &defaultApplyStub{fail: fail}
		if err := handler.ApplyDefaultProvider(context.Background(), line.ID, "default-claim", stub); err != nil || stub.calls != 0 {
			t.Fatal("unproven line applied", err)
		}
		now := time.Now().UTC()
		enabled := true
		if err := store.PutOperation(linecatalog.OperationReceipt{SchemaVersion: 1, OperationID: "default-claim-provision", Kind: linecatalog.OperationProvision,
			State: linecatalog.OperationSucceeded, CreatedAt: now, UpdatedAt: now, RequestDigest: strings.Repeat("a", 64), LineID: line.ID, CardID: line.CardID,
			EnableAfterSuccess: &enabled, AttemptCount: 1}); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := handler.ApplyDefaultProvider(context.Background(), line.ID, "default-claim", stub); err != nil {
				t.Fatal(err)
			}
		}
		if stub.calls != 1 || stub.line != line.ID {
			t.Fatal("scoped apply repeated or selected wrong line")
		}
		receipt, found, err := store.GetOperation("default-claim-apply")
		want := linecatalog.OperationSucceeded
		if fail {
			want = linecatalog.OperationUnknown
		}
		if err != nil || !found || receipt.State != want {
			t.Fatal("apply result not durably recorded", err)
		}
	}
}
