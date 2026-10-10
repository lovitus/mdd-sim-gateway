package linecatalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

const newIMEICard = "8944100000000002420"

func cardBindingRequest(t *testing.T, store *Store, method, entry, card string, pool, catalog uint64, want int) map[string]any {
	t.Helper()
	handler := NewIMEIPoolHandler(store)
	mux := http.NewServeMux()
	mux.Handle("PUT /v1/imei-pool/{entryID}/cards/{cardID}", handler)
	mux.Handle("DELETE /v1/imei-pool/{entryID}/cards/{cardID}", handler)
	body, _ := json.Marshal(map[string]any{"expected_catalog_revision": catalog, "expected_card_id": card})
	req := httptest.NewRequest(method, "/v1/imei-pool/"+entry+"/cards/"+card, bytes.NewReader(body))
	req.Header.Set("If-Match", revisionETag(pool))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)
	if response.Code != want {
		t.Fatalf("card binding HTTP %d, want %d: %s", response.Code, want, response.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestIMEICardBindingLifecycle(t *testing.T) {
	for _, creation := range []string{"create", "claim", "public-put", "internal-put", "hardware-provision"} {
		t.Run(creation, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "catalog.db")
			store, err := Open(path, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { store.Close() }()
			entry := IMEIPoolEntry{ID: "phone", Name: "Fixture phone", IMEI: "862547055201716"}
			if _, _, _, err := store.PutIMEIPoolEntryExpected(entry, 1); err != nil {
				t.Fatal(err)
			}
			result := cardBindingRequest(t, store, http.MethodPut, entry.ID, newIMEICard, 2, 1, http.StatusOK)
			if result["changed"] != true || result["line"] != nil {
				t.Fatalf("binding created a line: %v", result)
			}
			catalog, err := store.Snapshot()
			if err != nil || len(catalog.Lines) != 0 || catalog.Revision != 2 {
				t.Fatalf("binding changed lines: %+v %v", catalog, err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = Open(path, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			pool, err := store.IMEIPoolSnapshot()
			if err != nil || len(pool.Bindings) != 1 || pool.Bindings[0].LineID != "" || pool.Bindings[0].IMEI != entry.IMEI {
				t.Fatalf("saved binding not restored: %+v %v", pool, err)
			}
			result = cardBindingRequest(t, store, http.MethodPut, entry.ID, newIMEICard, 3, 2, http.StatusOK)
			if result["changed"] != false || result["revision"] != float64(3) {
				t.Fatalf("replay changed binding: %v", result)
			}
			if _, err := store.DeleteIMEIPoolEntryExpected(entry.ID, 3); !errors.Is(err, ErrIMEIValueInUse) {
				t.Fatalf("pending entry deleted: %v", err)
			}
			replacement := entry
			replacement.IMEI = "862547055201717"
			if _, _, _, err := store.PutIMEIPoolEntryExpected(replacement, 3); !errors.Is(err, ErrIMEIValueInUse) {
				t.Fatalf("pending IMEI changed: %v", err)
			}
			line := imeiTestLine("new-line", newIMEICard)
			line.HardwareProvisionState = "draft"
			if _, _, err := store.CreateExpected(line, 1); !errors.Is(err, ErrRevision) {
				t.Fatalf("stale claim accepted: %v", err)
			}
			switch creation {
			case "create":
				line, _, err = store.CreateExpected(line, 2)
			case "public-put":
				line, _, err = store.PutExpectedManaged(line, 2)
			case "internal-put":
				line, _, err = store.PutExpected(line, 2)
			default:
				receipt := validReceipt()
				receipt.LineID, receipt.CardID, receipt.ExpectedCatalogRevision = line.ID, line.CardID, 2
				if creation == "claim" {
					receipt.Kind = OperationClaim
					// Observed equipment defaults must not replace the saved presentation identity.
					line.SIM.IMEI = "862547055201717"
				} else {
					if _, _, err := store.CreateExpectedWithOperation(line, 2, receipt); !errors.Is(err, ErrIMEIBinding) {
						t.Fatalf("mismatched command accepted: %v", err)
					}
					line.SIM.IMEI = entry.IMEI
				}
				line, _, err = store.CreateExpectedWithOperation(line, 2, receipt)
				if _, found, readErr := store.GetOperation(receipt.OperationID); readErr != nil || !found {
					t.Fatalf("missing committed receipt: %v", readErr)
				}
			}
			if err != nil || line.SIM.IMEI != entry.IMEI || line.Enabled || line.HardwareProvisionState != "draft" {
				t.Fatalf("creation did not preserve binding and draft: %+v %v", line, err)
			}
			pool, err = store.IMEIPoolSnapshot()
			if err != nil || len(pool.Bindings) != 1 || pool.Bindings[0].LineID != line.ID {
				t.Fatalf("binding not transferred: %+v %v", pool, err)
			}
			if creation == "hardware-provision" {
				result = cardBindingRequest(t, store, http.MethodDelete, entry.ID, newIMEICard, pool.Revision, pool.CatalogRevision, http.StatusConflict)
				if result["code"] != "line_operation_active" {
					t.Fatalf("active operation lost protection: %v", result)
				}
				return
			}
			cardBindingRequest(t, store, http.MethodDelete, entry.ID, newIMEICard, 3, 2, http.StatusPreconditionFailed)
			cardBindingRequest(t, store, http.MethodDelete, entry.ID, newIMEICard, pool.Revision, pool.CatalogRevision, http.StatusOK)
			got, err := store.Get(line.ID)
			if err != nil || got.SIM.IMEI != "" || got.Enabled {
				t.Fatalf("unbind failed: %+v %v", got, err)
			}
		})
	}
}

func TestIMEICardBindingConflicts(t *testing.T) {
	for _, scenario := range []string{"stale-pool", "stale-catalog", "wrong-entry", "pending-unbind", "invalid-card", "existing-line", "active-provision", "deletion", "new-card-provision"} {
		t.Run(scenario, func(t *testing.T) {
			store, err := Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			for i, e := range []IMEIPoolEntry{{ID: "one", Name: "One", IMEI: "862547055201716"}, {ID: "two", Name: "Two", IMEI: "862547055201717"}} {
				if _, _, _, err := store.PutIMEIPoolEntryExpected(e, uint64(i+1)); err != nil {
					t.Fatal(err)
				}
			}
			cardBindingRequest(t, store, http.MethodPut, "one", newIMEICard, 3, 1, http.StatusOK)
			switch scenario {
			case "stale-pool":
				cardBindingRequest(t, store, http.MethodPut, "two", newIMEICard, 3, 2, http.StatusPreconditionFailed)
			case "stale-catalog":
				cardBindingRequest(t, store, http.MethodPut, "two", newIMEICard, 4, 1, http.StatusPreconditionFailed)
			case "wrong-entry":
				cardBindingRequest(t, store, http.MethodDelete, "two", newIMEICard, 4, 2, http.StatusConflict)
			case "invalid-card":
				cardBindingRequest(t, store, http.MethodPut, "one", "bad-"+newIMEICard, 4, 2, http.StatusBadRequest)
			case "pending-unbind":
				cardBindingRequest(t, store, http.MethodDelete, "one", newIMEICard, 4, 2, http.StatusOK)
				pool, err := store.IMEIPoolSnapshot()
				if err != nil || len(pool.Bindings) != 0 {
					t.Fatalf("unbind retained pending: %+v %v", pool, err)
				}
				if _, err := store.DeleteIMEIPoolEntryExpected("one", 5); err != nil {
					t.Fatal(err)
				}
				return
			default:
				line := imeiTestLine("line", newIMEICard)
				if _, _, err := store.CreateExpected(line, 2); err != nil {
					t.Fatal(err)
				}
				if scenario == "existing-line" {
					cardBindingRequest(t, store, http.MethodPut, "two", newIMEICard, 4, 3, http.StatusOK)
					got, err := store.Get(line.ID)
					if err != nil || got.SIM.IMEI != "862547055201717" {
						t.Fatalf("existing bind: %+v %v", got, err)
					}
					return
				}
				if scenario == "deletion" {
					if _, _, err := store.SetDeletedExpected(line.ID, true, 3); err != nil {
						t.Fatal(err)
					}
					if _, _, err := store.PrepareDeletionExpected(line.ID, "delete-one", true, 4, time.Now()); err != nil {
						t.Fatal(err)
					}
					cardBindingRequest(t, store, http.MethodPut, "two", newIMEICard, 4, 4, http.StatusConflict)
				} else {
					receipt := validReceipt()
					receipt.LineID, receipt.CardID, receipt.ExpectedCatalogRevision, receipt.ExistingLine = line.ID, line.CardID, 3, true
					if scenario == "new-card-provision" {
						receipt.CardID = "8944100000000002421"
					}
					if _, _, err := store.BeginExistingProvisionOperation(line.ID, 3, receipt); err != nil {
						t.Fatal(err)
					}
					cardBindingRequest(t, store, http.MethodPut, "two", receipt.CardID, 4, 4, http.StatusConflict)
				}
			}
			pool, err := store.IMEIPoolSnapshot()
			if err != nil || len(pool.Bindings) != 1 || pool.Bindings[0].EntryID != "one" {
				t.Fatalf("conflict changed binding: %+v %v", pool, err)
			}
		})
	}
}
