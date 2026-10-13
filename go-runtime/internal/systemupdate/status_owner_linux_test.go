//go:build linux

package systemupdate

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestUpdateRootStatusReadableByCoreWithoutOpeningPrivateDirectory(t *testing.T) {
	if path := os.Getenv("MDD_TEST_UPDATE_STATUS_CONSUMER"); path != "" {
		store, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		status, err := store.Status()
		if err != nil || status.OperationID != "update-one" || status.State != StateRunning {
			t.Fatalf("ROOT_STATUS_READ: %+v %v", status, err)
		}
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("separate hosted root publication check exercises the service UID")
	}
	dir, err := os.MkdirTemp("/var/tmp", "mdd-update-status-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chown(dir, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	store, err := Open(filepath.Join(dir, "update"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkerStatusReaderGroup(65534); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStatus(Status{OperationID: "update-one", State: StateRunning, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("state directory broadened: %v", err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Runner ancestors may be private to the runner UID. Keep the lowered-UID
	// executable in its own private fixture, without changing runner permissions.
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	consumer := filepath.Join(dir, "status-reader.test")
	if err := os.WriteFile(consumer, binary, 0o555); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(consumer, "-test.run=^TestUpdateRootStatusReadableByCoreWithoutOpeningPrivateDirectory$", "-test.v")
	command.Env = append(os.Environ(), "MDD_TEST_UPDATE_STATUS_CONSUMER="+filepath.Join(dir, "update"))
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("ROOT_STATUS_READ: %v\n%s", err, output)
	}
}
