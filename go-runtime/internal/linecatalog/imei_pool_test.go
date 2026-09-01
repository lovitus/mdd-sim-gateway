package linecatalog

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestIMEIPoolCRUDAndAtomicBindings(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := imeiTestLine("line-a", "8944100000000000001")
	second := imeiTestLine("line-b", "8944100000000000002")
	if _, err := store.Put(first); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(second); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.IMEIPoolSnapshot()
	if err != nil || snapshot.Revision != 1 || snapshot.CatalogRevision != 3 {
		t.Fatalf("initial snapshot=%+v err=%v", snapshot, err)
	}
	entry := IMEIPoolEntry{SchemaVersion: 1, ID: "device-a", Name: "Shared phone identity", IMEI: "862547055201716"}
	created, revision, changed, err := store.PutIMEIPoolEntryExpected(entry, 1)
	if err != nil || !changed || revision != 2 || created != entry {
		t.Fatalf("create=%+v revision=%d changed=%t err=%v", created, revision, changed, err)
	}
	if replay, replayRevision, replayChanged, err := store.PutIMEIPoolEntryExpected(entry, 2); err != nil || replayChanged || replayRevision != 2 || replay != entry {
		t.Fatalf("replay=%+v revision=%d changed=%t err=%v", replay, replayRevision, replayChanged, err)
	}
	duplicate := entry
	duplicate.ID = "device-b"
	if _, _, _, err := store.PutIMEIPoolEntryExpected(duplicate, 2); !errors.Is(err, ErrIMEIValueExists) {
		t.Fatalf("duplicate IMEI error=%v", err)
	}

	bound, poolRevision, catalogRevision, changed, err := store.BindIMEIExpected(
		entry.ID, first.ID, first.CardID, 2, 3)
	if err != nil || !changed || poolRevision != 3 || catalogRevision != 4 || bound.SIM.IMEI != entry.IMEI {
		t.Fatalf("first bind=%+v pool=%d catalog=%d changed=%t err=%v", bound, poolRevision, catalogRevision, changed, err)
	}
	if _, poolRevision, catalogRevision, changed, err = store.BindIMEIExpected(
		entry.ID, first.ID, first.CardID, 3, 4); err != nil || changed || poolRevision != 3 || catalogRevision != 4 {
		t.Fatalf("no-op bind pool=%d catalog=%d changed=%t err=%v", poolRevision, catalogRevision, changed, err)
	}
	if _, poolRevision, catalogRevision, changed, err = store.BindIMEIExpected(
		entry.ID, second.ID, second.CardID, 3, 4); err != nil || !changed || poolRevision != 4 || catalogRevision != 5 {
		t.Fatalf("second bind pool=%d catalog=%d changed=%t err=%v", poolRevision, catalogRevision, changed, err)
	}
	snapshot, err = store.IMEIPoolSnapshot()
	if err != nil || len(snapshot.Bindings) != 2 || len(snapshot.Unpooled) != 0 ||
		snapshot.Bindings[0].EntryID != entry.ID || snapshot.Bindings[1].EntryID != entry.ID {
		t.Fatalf("bound snapshot=%+v err=%v", snapshot, err)
	}

	metadata := entry
	metadata.Name = "Updated label"
	metadata.Notes = "Used by two SIM profiles"
	if _, revision, changed, err = store.PutIMEIPoolEntryExpected(metadata, 4); err != nil || !changed || revision != 5 {
		t.Fatalf("metadata revision=%d changed=%t err=%v", revision, changed, err)
	}
	replaced := metadata
	replaced.IMEI = "862547055201717"
	if _, _, _, err := store.PutIMEIPoolEntryExpected(replaced, 5); !errors.Is(err, ErrIMEIValueInUse) {
		t.Fatalf("in-use replacement error=%v", err)
	}
	if _, err := store.DeleteIMEIPoolEntryExpected(entry.ID, 5); !errors.Is(err, ErrIMEIValueInUse) {
		t.Fatalf("in-use delete error=%v", err)
	}

	if _, poolRevision, catalogRevision, changed, err = store.UnbindIMEIExpected(
		entry.ID, first.ID, first.CardID, 5, 5); err != nil || !changed || poolRevision != 6 || catalogRevision != 6 {
		t.Fatalf("first unbind pool=%d catalog=%d changed=%t err=%v", poolRevision, catalogRevision, changed, err)
	}
	if _, poolRevision, catalogRevision, changed, err = store.UnbindIMEIExpected(
		entry.ID, second.ID, second.CardID, 6, 6); err != nil || !changed || poolRevision != 7 || catalogRevision != 7 {
		t.Fatalf("second unbind pool=%d catalog=%d changed=%t err=%v", poolRevision, catalogRevision, changed, err)
	}
	if revision, err = store.DeleteIMEIPoolEntryExpected(entry.ID, 7); err != nil || revision != 8 {
		t.Fatalf("delete revision=%d err=%v", revision, err)
	}
}

func TestPresentationIMEIIsManagedOnlyAtPublicCatalogBoundary(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	line := imeiTestLine("line-a", "8944100000000000001")
	line.SIM.IMEI = "862547055201716"
	if _, err := store.Put(line); err != nil {
		t.Fatal(err)
	}
	unchanged := line
	unchanged.Name = "renamed"
	if _, revision, err := store.PutExpectedManaged(unchanged, 2); err != nil || revision != 3 {
		t.Fatalf("unchanged IMEI update revision=%d err=%v", revision, err)
	}
	changed := unchanged
	changed.SIM.IMEI = "862547055201717"
	if _, _, err := store.PutExpectedManaged(changed, 3); !errors.Is(err, ErrIMEIBindingManaged) {
		t.Fatalf("managed IMEI change error=%v", err)
	}
	newLine := imeiTestLine("line-b", "8944100000000000002")
	newLine.SIM.IMEI = "862547055201718"
	if _, _, err := store.PutExpectedManaged(newLine, 3); !errors.Is(err, ErrIMEIBindingManaged) {
		t.Fatalf("managed IMEI create error=%v", err)
	}
}

func TestIMEIPoolSnapshotPreservesLegacyUnpooledValue(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	line := imeiTestLine("legacy-line", "8944100000000000001")
	line.SIM.IMEI = "86254705520171"
	if _, err := store.Put(line); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.IMEIPoolSnapshot()
	if err != nil || len(snapshot.Unpooled) != 1 || snapshot.Unpooled[0].IMEI != line.SIM.IMEI ||
		len(snapshot.Entries) != 0 || len(snapshot.Bindings) != 0 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
}

func imeiTestLine(id, cardID string) Line {
	return Line{SchemaVersion: SchemaVersion, ID: id, Name: id, CardID: cardID}
}
