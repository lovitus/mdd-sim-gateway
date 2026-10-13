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
	dir := t.TempDir()
	if err := os.Chmod(filepath.Dir(dir), 0711); err != nil {
		t.Fatal(err)
	}
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
	// CI places this test binary in a service-readable runner temp directory.
	command := exec.Command(executable, "-test.run=^TestUpdateRootStatusReadableByCoreWithoutOpeningPrivateDirectory$", "-test.v")
	command.Env = append(os.Environ(), "MDD_TEST_UPDATE_STATUS_CONSUMER="+filepath.Join(dir, "update"))
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("ROOT_STATUS_READ: %v\n%s", err, output)
	}
}
