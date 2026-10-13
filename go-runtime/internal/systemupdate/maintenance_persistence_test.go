package systemupdate

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

func TestUpdateUnpublishedInitialMaintenanceRestoresAdmission(t *testing.T) {
	owner, runtime, store := updateTestOwner(t, 1)
	store.rename = func(from, to string) error {
		if to == store.maintenancePath {
			return errors.New("rename unavailable")
		}
		return os.Rename(from, to)
	}
	view, err := owner.Begin(t.Context(), "update-one", updateTestTarget())
	if err == nil || view.Record != nil || view.GuardInstalled || len(runtime.leases) != 0 {
		t.Fatalf("UNPUBLISHED_GATE: %+v %v", view, err)
	}
	store.rename = os.Rename
	view, err = owner.Begin(t.Context(), "update-one", updateTestTarget())
	if err != nil {
		t.Fatal("same Core could not admit after storage repair", err)
	}
	if _, err := owner.Cleanup(t.Context(), "update-one", view.Record.Revision); err != nil {
		t.Fatal(err)
	}
}

func TestUpdatePublishedSyncFailureRetainsRevisionAndRecoversWithoutRestart(t *testing.T) {
	for _, phase := range []string{MaintenancePreparing, MaintenanceReady, MaintenanceActivating, MaintenanceReleased} {
		t.Run(phase, func(t *testing.T) {
			owner, runtime, store := updateTestOwner(t, 1)
			fail := true
			store.syncDirectory = func(path string) error {
				payload, err := os.ReadFile(store.maintenancePath)
				var record Maintenance
				if err == nil && json.Unmarshal(payload, &record) == nil && record.Phase == phase && fail {
					return errors.New("directory sync unavailable after rename")
				}
				return syncUpdateDirectory(path)
			}
			view, err := owner.Begin(t.Context(), "update-one", updateTestTarget())
			if phase == MaintenanceActivating || phase == MaintenanceReleased {
				if err != nil {
					t.Fatal(err)
				}
				if phase == MaintenanceActivating {
					view, err = owner.Activate("update-one", view.Record.Revision)
				} else {
					view, err = owner.Cleanup(t.Context(), "update-one", view.Record.Revision)
				}
			}
			if err == nil || !view.PersistencePending || !view.GuardInstalled || view.Record == nil || view.Record.Phase != phase {
				t.Fatalf("PUBLISHED_REVISION: %+v %v", view, err)
			}
			actual, err := store.Maintenance()
			if err != nil || actual.Revision != view.Record.Revision {
				t.Fatalf("PUBLISHED_REVISION: disk=%+v view=%+v err=%v", actual, view.Record, err)
			}
			ended, held := len(runtime.ended), len(runtime.leases)
			if _, err := owner.Activate("update-one", view.Record.Revision); err == nil {
				t.Fatal("pending persistence allowed activation")
			}
			if _, err := owner.Restarted("update-one", view.Record.Revision, FixedServices[0]); err == nil {
				t.Fatal("pending persistence accepted restart receipt")
			}
			if err := store.Request(Request{OperationID: "other", Repository: "example/project", Target: "4.0.0", RequestedAt: time.Now()}); err == nil {
				t.Fatal("pending persistence allowed new request")
			}
			if phase == MaintenanceReleased {
				_, err = owner.Finish(t.Context(), "update-one", view.Record.Revision)
			} else {
				_, err = owner.Cleanup(t.Context(), "update-one", view.Record.Revision)
			}
			if err == nil || len(runtime.ended) != ended || len(runtime.leases) != held || !owner.View().PersistencePending {
				t.Fatal("unconfirmed save dispatched more effects")
			}
			fail = false
			if phase == MaintenanceReleased || phase == MaintenanceActivating {
				view, err = owner.Finish(t.Context(), "update-one", view.Record.Revision)
			} else {
				view, err = owner.Cleanup(t.Context(), "update-one", view.Record.Revision)
			}
			if phase == MaintenanceActivating {
				if err == nil || !view.GuardInstalled || view.Record.Phase != MaintenanceActivating || view.PersistencePending || len(runtime.ended) != ended {
					t.Fatal("uncertain activation was reopened or cancelled")
				}
				return
			}
			if err != nil || view.GuardInstalled || view.PersistencePending || view.Record.Phase != MaintenanceReleased || len(runtime.leases) != 0 {
				t.Fatalf("PERSISTENCE_RECOVERY: %+v %v", view, err)
			}
		})
	}
}
