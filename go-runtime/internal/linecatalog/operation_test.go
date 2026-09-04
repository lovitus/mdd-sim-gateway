package linecatalog

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validReceipt() OperationReceipt {
	now := time.Unix(1_800_000_000, 0).UTC()
	return OperationReceipt{SchemaVersion: OperationSchemaVersion, OperationID: "provision-01",
		Kind: OperationProvision, State: OperationPrepared, CreatedAt: now, UpdatedAt: now,
		RequestDigest: strings.Repeat("a", 64), AttemptCount: 1}
}

func TestOperationReceiptValidation(t *testing.T) {
	receipt := validReceipt()
	if err := receipt.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*OperationReceipt){
		"secret-like operation id": func(value *OperationReceipt) { value.OperationID = "pin secret" },
		"invalid digest":           func(value *OperationReceipt) { value.RequestDigest = "not-a-digest" },
		"zero attempts":            func(value *OperationReceipt) { value.AttemptCount = 0 },
		"backwards timestamp":      func(value *OperationReceipt) { value.UpdatedAt = value.CreatedAt.Add(-time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := receipt
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatalf("receipt unexpectedly accepted: %+v", candidate)
			}
		})
	}
}

func TestOperationReceiptPersistsInCatalogDatabaseAndRejectsOverwrite(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	receipt := validReceipt()
	if err := store.PutOperation(receipt); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.GetOperation(receipt.OperationID)
	if err != nil || !found || got != receipt {
		t.Fatalf("got=%+v found=%v err=%v", got, found, err)
	}
	receipt.State = OperationUnknown
	if err := store.PutOperation(receipt); !errors.Is(err, ErrOperationExists) {
		t.Fatal("overwriting an operation receipt was accepted")
	}
	receipt.State = OperationCatalogCommitted
	receipt.UpdatedAt = receipt.CreatedAt.Add(time.Second)
	if err := store.UpdateOperationCAS(receipt, OperationPrepared, receipt.RequestDigest); err != nil {
		t.Fatal(err)
	}
	updated, found, err := store.GetOperation(receipt.OperationID)
	if err != nil || !found || updated.State != OperationCatalogCommitted {
		t.Fatalf("updated=%+v found=%v err=%v", updated, found, err)
	}
	receipt.State = OperationSucceeded
	receipt.UpdatedAt = receipt.CreatedAt.Add(2 * time.Second)
	if err := store.UpdateOperationCAS(receipt, OperationPrepared, receipt.RequestDigest); !errors.Is(err, ErrOperationStateChanged) {
		t.Fatalf("stale CAS err=%v", err)
	}
	missing, found, err := store.GetOperation("missing-operation")
	if err != nil || found || !missing.CreatedAt.IsZero() {
		t.Fatalf("missing=%+v found=%v err=%v", missing, found, err)
	}
}

func TestOperationStatusHTTPRedactsHardwareIdentity(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	receipt := validReceipt()
	receipt.CardID, receipt.AgentID, receipt.AttachmentID = "89010000000000000001", "agent-secret", "usb-secret"
	if err := store.PutOperation(receipt); err != nil {
		t.Fatal(err)
	}
	handler := NewOperationHandler(store)
	request := httptest.NewRequest(http.MethodGet, "/v1/operations/"+receipt.OperationID, nil)
	request.SetPathValue("operationID", receipt.OperationID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), receipt.CardID) ||
		strings.Contains(response.Body.String(), receipt.AgentID) || strings.Contains(response.Body.String(), receipt.AttachmentID) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
