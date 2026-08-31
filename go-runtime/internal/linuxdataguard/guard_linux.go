//go:build linux

// Package linuxdataguard keeps every MDD-owned cellular interface unusable by
// the Linux host unless an explicit, bounded MDD data session installs a more
// specific permit. It deliberately delegates USB/IP, NetworkManager and
// netfilter mechanics to the platform tools instead of reimplementing them.
package linuxdataguard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moby/sys/atomicwriter"
)

const (
	InterfaceGroup = 0x4d4444
	agentCGroup    = "system.slice/mdd-agent.service"

	networkManagerSnippetName = "90-mdd-cellular-guard.conf"
	udevRulesName             = "90-mdd-cellular-guard.rules"
	persistenceMarkerName     = "cellular-guard-enabled"
	nftTableName              = "mdd_cellular_guard"
	guardUnitName             = "mdd-cellular-guard.service"
	maximumCommandOutput      = 64 << 10
)

var cellularDrivers = map[string]struct{}{
	"bam-dmux": {}, "cdc_mbim": {}, "ipa": {}, "mhi_net": {},
	"qmi_wwan": {}, "rmnet": {}, "rmnet_ipa": {}, "wwan": {},
}

var networkManagerSnippet = []byte(`[keyfile]
# MDD owns cellular lifecycle. This strict rule cannot be overridden by
# "nmcli device set managed yes"; explicit MDD borrowing uses its own path.
unmanaged-devices+=type:gsm
`)

var persistenceMarker = []byte("mdd-cellular-guard-v1\n")

var udevRules = []byte(`# MDD whole-Modem imports and supported cellular net devices are quarantined
# before NetworkManager sees them. The nftables devgroup rule is the hard
# boundary; NM_UNMANAGED prevents accidental address and route creation.
ACTION=="add|change", SUBSYSTEM=="net", KERNELS=="vhci_hcd.*", ENV{NM_UNMANAGED}="1", RUN+="/usr/libexec/mdd/mdd-agent cellular-guard protect-netdev %k"
ACTION=="add|change", SUBSYSTEM=="net", DRIVERS=="qmi_wwan", ENV{NM_UNMANAGED}="1", RUN+="/usr/libexec/mdd/mdd-agent cellular-guard protect-netdev %k"
ACTION=="add|change", SUBSYSTEM=="net", DRIVERS=="cdc_mbim", ENV{NM_UNMANAGED}="1", RUN+="/usr/libexec/mdd/mdd-agent cellular-guard protect-netdev %k"
ACTION=="add|change", SUBSYSTEM=="net", DRIVERS=="wwan", ENV{NM_UNMANAGED}="1", RUN+="/usr/libexec/mdd/mdd-agent cellular-guard protect-netdev %k"
ACTION=="add|change", SUBSYSTEM=="net", DRIVERS=="mhi_net", ENV{NM_UNMANAGED}="1", RUN+="/usr/libexec/mdd/mdd-agent cellular-guard protect-netdev %k"
ACTION=="add|change", SUBSYSTEM=="net", DRIVERS=="rmnet", ENV{NM_UNMANAGED}="1", RUN+="/usr/libexec/mdd/mdd-agent cellular-guard protect-netdev %k"
ACTION=="add|change", SUBSYSTEM=="net", DRIVERS=="rmnet_ipa", ENV{NM_UNMANAGED}="1", RUN+="/usr/libexec/mdd/mdd-agent cellular-guard protect-netdev %k"
ACTION=="add|change", SUBSYSTEM=="net", DRIVERS=="ipa", ENV{NM_UNMANAGED}="1", RUN+="/usr/libexec/mdd/mdd-agent cellular-guard protect-netdev %k"
ACTION=="add|change", SUBSYSTEM=="net", DRIVERS=="bam-dmux", ENV{NM_UNMANAGED}="1", RUN+="/usr/libexec/mdd/mdd-agent cellular-guard protect-netdev %k"
`)

type DeviceIdentity struct {
	VendorID, ProductID uint16
	Serial              string
}

type commandFunc func(context.Context, []byte, string, ...string) ([]byte, error)

type Guard struct {
	sysRoot   string
	etcRoot   string
	stateRoot string
	run       commandFunc
	now       func() time.Time

	importMu sync.Mutex
	imports  map[string]*importAuthorizationLease
	policyMu sync.Mutex
	permits  map[uint32]DataPermit
}

type DataPermit struct {
	Mark      uint32
	Owner     string
	Interface string
}

type importMarker struct {
	SchemaVersion uint64    `json:"schema_version"`
	PhysicalID    string    `json:"physical_id"`
	VendorID      uint16    `json:"vendor_id"`
	ProductID     uint16    `json:"product_id"`
	Serial        string    `json:"serial,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type importAuthorizationLease struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func Activate(ctx context.Context) (*Guard, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("managed Linux modem mode requires root")
	}
	guard := newGuard("/sys", "/etc", "/var/lib/mdd-agent", runCommand)
	unitPath := filepath.Join(guard.etcRoot, "systemd", "system", guardUnitName)
	if info, err := os.Lstat(unitPath); err != nil || info.Mode()&os.ModeSymlink == 0 {
		return nil, errors.New("persistent MDD cellular guard unit is not installed")
	}
	// Do not start the oneshot unit from the Agent that it orders before. The
	// current process applies and verifies the same contract directly; enabling
	// the unit makes that contract precede NetworkManager and ModemManager on
	// every later boot without a recursive systemd transaction.
	if _, err := guard.run(ctx, nil, "systemctl", "enable", guardUnitName); err != nil {
		return nil, fmt.Errorf("enable persistent MDD cellular guard: %w", err)
	}
	if err := guard.Apply(ctx); err != nil {
		return nil, err
	}
	if err := guard.VerifyContract(ctx); err != nil {
		return nil, err
	}
	return guard, nil
}

func Apply(ctx context.Context) error {
	if os.Geteuid() != 0 {
		return errors.New("cellular guard apply requires root")
	}
	guard := newGuard("/sys", "/etc", "/var/lib/mdd-agent", runCommand)
	return guard.Apply(ctx)
}

func ProtectNetdev(ctx context.Context, name string) error {
	if os.Geteuid() != 0 {
		return errors.New("cellular netdev protection requires root")
	}
	return newGuard("/sys", "/etc", "/var/lib/mdd-agent", runCommand).ProtectNetdev(ctx, name)
}

func newGuard(sysRoot, etcRoot, stateRoot string, run commandFunc) *Guard {
	return &Guard{sysRoot: filepath.Clean(sysRoot), etcRoot: filepath.Clean(etcRoot),
		stateRoot: filepath.Clean(stateRoot), run: run, now: time.Now,
		imports: make(map[string]*importAuthorizationLease), permits: make(map[uint32]DataPermit)}
}

func (guard *Guard) Apply(ctx context.Context) error {
	if guard == nil || guard.run == nil || !filepath.IsAbs(guard.sysRoot) || !filepath.IsAbs(guard.etcRoot) || !filepath.IsAbs(guard.stateRoot) {
		return errors.New("invalid Linux cellular guard configuration")
	}
	if err := guard.armPersistence(); err != nil {
		return err
	}
	if err := guard.installNetfilter(ctx); err != nil {
		return err
	}
	if err := guard.writePolicyFiles(); err != nil {
		return err
	}
	if _, err := guard.run(ctx, nil, "modprobe", "vhci_hcd"); err != nil {
		return fmt.Errorf("load vhci_hcd: %w", err)
	}
	if err := guard.setVHCIDefaultAuthorization(false); err != nil {
		return err
	}
	if _, err := guard.run(ctx, nil, "udevadm", "control", "--reload"); err != nil {
		return fmt.Errorf("reload MDD cellular udev policy: %w", err)
	}
	// NetworkManager may be absent on a small service host. A present daemon
	// must reload the strict snippet, while an absent service is already safe.
	if _, err := guard.run(ctx, nil, "systemctl", "is-active", "--quiet", "NetworkManager.service"); err == nil {
		if _, err := guard.run(ctx, nil, "systemctl", "reload", "NetworkManager.service"); err != nil {
			return fmt.Errorf("reload NetworkManager cellular policy: %w", err)
		}
	}
	if err := guard.protectExistingNetdevs(ctx); err != nil {
		return err
	}
	if err := guard.recoverMarkers(); err != nil {
		return err
	}
	return guard.VerifyContract(ctx)
}

func (guard *Guard) VerifyContract(ctx context.Context) error {
	if err := exactFile(filepath.Join(guard.stateRoot, persistenceMarkerName), persistenceMarker, 0o600); err != nil {
		return fmt.Errorf("verify durable cellular guard marker: %w", err)
	}
	if err := exactFile(filepath.Join(guard.etcRoot, "NetworkManager", "conf.d", networkManagerSnippetName), networkManagerSnippet, 0o644); err != nil {
		return fmt.Errorf("verify NetworkManager cellular policy: %w", err)
	}
	if err := exactFile(filepath.Join(guard.etcRoot, "udev", "rules.d", udevRulesName), udevRules, 0o644); err != nil {
		return fmt.Errorf("verify udev cellular policy: %w", err)
	}
	udevPath := filepath.Join(guard.etcRoot, "udev", "rules.d", udevRulesName)
	if err := guard.verifyUdevRule(ctx, udevPath); err != nil {
		return err
	}
	if err := guard.verifyNetworkManagerConfig(ctx); err != nil {
		return err
	}
	defaults, err := guard.vhciAuthorizationDefaults()
	if err != nil {
		return err
	}
	if len(defaults) == 0 {
		return errors.New("vhci_hcd has no USB interface authorization boundary")
	}
	for _, path := range defaults {
		payload, readErr := os.ReadFile(path)
		if readErr != nil || strings.TrimSpace(string(payload)) != "0" {
			return fmt.Errorf("VHCI interface authorization default is not denied: %s", path)
		}
	}
	parents, err := guard.vhciParents()
	if err != nil {
		return err
	}
	markers, err := guard.loadMarkers()
	if err != nil {
		return err
	}
	for physicalID := range parents {
		if err := guard.quarantineInterfaces(physicalID); err != nil {
			return err
		}
	}
	if err := guard.verifyTrackedParents(parents, markers); err != nil {
		return err
	}
	guard.policyMu.Lock()
	output, err := guard.run(ctx, nil, "nft", "--numeric", "--numeric-priority", "--json",
		"list", "table", "inet", nftTableName)
	permits := make(map[uint32]string, len(guard.permits))
	for mark, permit := range guard.permits {
		if permit.Interface != "" {
			permits[mark] = permit.Interface
		}
	}
	guard.policyMu.Unlock()
	if err != nil {
		return fmt.Errorf("read MDD cellular nftables table: %w", err)
	}
	if err := verifyNFTContract(output, permits); err != nil {
		return fmt.Errorf("verify MDD cellular nftables quarantine: %w", err)
	}
	return nil
}

func (guard *Guard) OpenDataPermit(ctx context.Context, owner string) (DataPermit, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return DataPermit{}, errors.New("cellular data permit owner is empty")
	}
	digest := sha256.Sum256([]byte("mdd-cellular-data\x00" + owner))
	mark := uint32(0x4d000000) | uint32(digest[0])<<16 | uint32(digest[1])<<8 | uint32(digest[2])
	guard.policyMu.Lock()
	defer guard.policyMu.Unlock()
	existing := guard.permits[mark]
	if existing.Owner != "" && existing.Owner != owner {
		return DataPermit{}, errors.New("cellular data socket mark collision")
	}
	if existing.Owner == owner {
		return existing, nil
	}
	permit := DataPermit{Mark: mark, Owner: owner}
	guard.permits[mark] = permit
	return permit, nil
}

func (guard *Guard) CloseDataPermit(ctx context.Context, permit DataPermit) error {
	guard.policyMu.Lock()
	defer guard.policyMu.Unlock()
	if permit.Mark == 0 || strings.TrimSpace(permit.Owner) == "" {
		return errors.New("cellular data permit identity was replaced")
	}
	current := guard.permits[permit.Mark]
	if current.Owner == "" {
		return nil
	}
	if current.Owner != permit.Owner || permit.Interface != "" && current.Interface != permit.Interface {
		return errors.New("cellular data permit identity was replaced")
	}
	delete(guard.permits, permit.Mark)
	if err := guard.installNetfilterLocked(ctx); err != nil {
		guard.permits[permit.Mark] = current
		return err
	}
	return nil
}

func (guard *Guard) verifyUdevRule(ctx context.Context, path string) error {
	output, err := guard.run(ctx, nil, "udevadm", "--version")
	if err != nil {
		return fmt.Errorf("read udev version: %w", err)
	}
	version, err := leadingUint(strings.TrimSpace(string(output)))
	if err != nil {
		return errors.New("udev returned an invalid version")
	}
	// udevadm verify was added in systemd 254. Older supported hosts still
	// receive the exact compiled-in rule file and a successful daemon reload;
	// newer hosts additionally validate udev's own parsed rule contract.
	if version < 254 {
		return nil
	}
	if _, err := guard.run(ctx, nil, "udevadm", "verify", "--no-summary", "--no-style", path); err != nil {
		return fmt.Errorf("verify loaded udev cellular policy syntax: %w", err)
	}
	return nil
}

func (guard *Guard) verifyNetworkManagerConfig(ctx context.Context) error {
	output, err := guard.run(ctx, nil, "systemctl", "show", "--property=LoadState", "--value", "NetworkManager.service")
	if err != nil {
		return fmt.Errorf("inspect NetworkManager availability: %w", err)
	}
	loadState := strings.TrimSpace(string(output))
	if loadState == "not-found" {
		return nil
	}
	if loadState == "" {
		return errors.New("NetworkManager load state is empty")
	}
	output, err = guard.run(ctx, nil, "NetworkManager", "--print-config")
	if err != nil {
		return fmt.Errorf("read merged NetworkManager configuration: %w", err)
	}
	if !mergedConfigHasStrictGSMGuard(output) {
		return errors.New("merged NetworkManager configuration does not strictly quarantine GSM devices")
	}
	return nil
}

func (guard *Guard) ProtectNetdev(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, " \t\r\n/\"\\") {
		return errors.New("invalid cellular network interface name")
	}
	if _, err := os.Lstat(filepath.Join(guard.sysRoot, "class", "net", name)); err != nil {
		return err
	}
	// Down precedes the devgroup change so an already-configured interface
	// cannot emit a packet in the first-install window.
	_, downErr := guard.run(ctx, nil, "ip", "link", "set", "dev", name, "down")
	if _, err := guard.run(ctx, nil, "ip", "link", "set", "dev", name, "group", strconv.FormatUint(uint64(InterfaceGroup), 10)); err != nil {
		return fmt.Errorf("assign MDD cellular devgroup: %w", errors.Join(downErr, err))
	}
	output, err := guard.run(ctx, nil, "ip", "-j", "link", "show", "dev", name)
	if err != nil {
		return fmt.Errorf("verify MDD cellular devgroup: %w", err)
	}
	var links []struct {
		IfName string          `json:"ifname"`
		Group  json.RawMessage `json:"group"`
	}
	if err := json.Unmarshal(output, &links); err != nil || len(links) != 1 || links[0].IfName != name ||
		!exactInterfaceGroup(links[0].Group) {
		return errors.New("cellular network interface did not retain the MDD devgroup")
	}
	return nil
}

func exactInterfaceGroup(raw json.RawMessage) bool {
	var number uint32
	if json.Unmarshal(raw, &number) == nil {
		return number == InterfaceGroup
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return false
	}
	parsed, err := strconv.ParseUint(text, 10, 32)
	return err == nil && uint32(parsed) == InterfaceGroup && strconv.FormatUint(parsed, 10) == text
}

func (guard *Guard) VerifyProtected(ctx context.Context, physicalID string, netPorts []string) error {
	if err := guard.VerifyContract(ctx); err != nil {
		return err
	}
	for _, name := range netPorts {
		if err := guard.ProtectNetdev(ctx, name); err != nil {
			return err
		}
		path, err := filepath.EvalSymlinks(filepath.Join(guard.sysRoot, "class", "net", name, "device"))
		if err != nil || !sameUSBTree(path, physicalID) {
			return errors.New("cellular network interface escaped its exact USB parent")
		}
	}
	return nil
}

// StartImport is the only path that may turn VHCI interfaces from the
// persistent unauthorized default into active interfaces. The short
// observation window is serialized so identical, serial-less devices are
// distinguished by the before/after set, not by a guessed name.
func (guard *Guard) StartImport(ctx context.Context, identity DeviceIdentity, start, detach func() error) (string, error) {
	if start == nil || detach == nil || identity.VendorID == 0 || identity.ProductID == 0 {
		return "", errors.New("invalid guarded raw USB import")
	}
	guard.importMu.Lock()
	defer guard.importMu.Unlock()
	if err := guard.VerifyContract(ctx); err != nil {
		return "", err
	}
	before, err := guard.vhciParents()
	if err != nil {
		return "", err
	}
	markers, err := guard.loadMarkers()
	if err != nil {
		return "", err
	}
	if err := guard.verifyTrackedParents(before, markers); err != nil {
		return "", err
	}
	if err := start(); err != nil {
		return "", errors.Join(err, guard.rollbackUnknownImport(before, detach))
	}
	parent, err := guard.awaitImportedParent(ctx, before, identity)
	if err != nil {
		return "", errors.Join(err, guard.rollbackUnknownImport(before, detach))
	}
	physicalID := parent.physicalID
	marker := importMarker{SchemaVersion: 1, PhysicalID: physicalID, VendorID: parent.vendorID,
		ProductID: parent.productID, Serial: parent.serial, CreatedAt: guard.now().UTC()}
	if err := guard.writeMarker(marker); err != nil {
		return "", errors.Join(err, guard.rollbackFailedImport(physicalID, detach))
	}
	if err := guard.authorizeInterfaces(ctx, physicalID); err != nil {
		return "", errors.Join(err, guard.rollbackFailedImport(physicalID, detach))
	}
	if err := guard.protectNetdevsUnder(ctx, physicalID); err != nil {
		return "", errors.Join(err, guard.rollbackFailedImport(physicalID, detach))
	}
	after, err := guard.vhciParents()
	if err != nil {
		return "", errors.Join(err, guard.rollbackFailedImport(physicalID, detach))
	}
	markers, err = guard.loadMarkers()
	if err != nil {
		return "", errors.Join(err, guard.rollbackFailedImport(physicalID, detach))
	}
	if err := guard.verifyTrackedParents(after, markers); err != nil {
		return "", errors.Join(err, guard.rollbackFailedImport(physicalID, detach))
	}
	if err := guard.startImportAuthorizationLease(ctx, physicalID, detach); err != nil {
		return "", errors.Join(err, guard.rollbackFailedImport(physicalID, detach))
	}
	return physicalID, nil
}

// StopImport first removes driver access, then invokes sing-usbip's native
// detach, and clears the durable marker only after the local VHCI parent is
// gone. A failed detach therefore remains quarantined across Agent exit.
func (guard *Guard) StopImport(ctx context.Context, physicalID string, detach func() error) error {
	physicalID = filepath.Clean(strings.TrimSpace(physicalID))
	if detach == nil || !guard.scopedSysfsPath(physicalID) {
		return errors.New("invalid guarded raw USB detach")
	}
	guard.importMu.Lock()
	defer guard.importMu.Unlock()
	return guard.stopImportLocked(ctx, physicalID, detach)
}

func (guard *Guard) stopImportLocked(ctx context.Context, physicalID string, detach func() error) error {
	guard.stopImportAuthorizationLease(physicalID)
	quarantineErr := guard.quarantineInterfaces(physicalID)
	detachErr := detach()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, statErr := os.Lstat(physicalID)
		if errors.Is(statErr, os.ErrNotExist) {
			return errors.Join(quarantineErr, detachErr, guard.removeMarker(physicalID))
		}
		if statErr != nil {
			return errors.Join(quarantineErr, detachErr, statErr)
		}
		if !time.Now().Before(deadline) {
			return errors.Join(quarantineErr, detachErr, errors.New("VHCI parent remains after raw USB detach"))
		}
		select {
		case <-ctx.Done():
			return errors.Join(quarantineErr, detachErr, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (guard *Guard) startImportAuthorizationLease(ctx context.Context, physicalID string, detach func() error) error {
	if guard.imports[physicalID] != nil {
		return errors.New("USB interface authorization lease already exists")
	}
	leaseContext, cancel := context.WithCancel(ctx)
	lease := &importAuthorizationLease{cancel: cancel, done: make(chan struct{})}
	guard.imports[physicalID] = lease
	go func() {
		defer close(lease.done)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-leaseContext.Done():
				return
			case <-ticker.C:
				if err := guard.reauthorizeImportedInterfaces(physicalID); err != nil {
					if !errors.Is(err, os.ErrNotExist) {
						log.Printf("mdd-agent: imported USB interface authorization lease failed: %v", err)
						_ = guard.quarantineInterfaces(physicalID)
						_ = detach()
					}
					return
				}
			}
		}
	}()
	return nil
}

func (guard *Guard) stopImportAuthorizationLease(physicalID string) {
	lease := guard.imports[physicalID]
	if lease == nil {
		return
	}
	delete(guard.imports, physicalID)
	lease.cancel()
	<-lease.done
}

func (guard *Guard) reauthorizeImportedInterfaces(physicalID string) error {
	interfaces, err := guard.interfacePaths(physicalID)
	if err != nil {
		return err
	}
	for _, path := range interfaces {
		authorized, err := os.ReadFile(filepath.Join(path, "authorized"))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		switch strings.TrimSpace(string(authorized)) {
		case "1":
			continue
		case "0":
		default:
			return errors.New("imported USB interface has an invalid authorization state")
		}
		if err := os.WriteFile(filepath.Join(path, "authorized"), []byte("1\n"), 0); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if err := os.WriteFile(filepath.Join(guard.sysRoot, "bus", "usb", "drivers_probe"),
			[]byte(filepath.Base(path)+"\n"), 0); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (guard *Guard) rollbackFailedImport(physicalID string, detach func() error) error {
	cleanupContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if physicalID == "" {
		return detach()
	}
	return guard.stopImportLocked(cleanupContext, physicalID, detach)
}

// rollbackUnknownImport handles the only failure interval where sing-usbip
// may have attached a device but its exact VHCI parent has not yet been
// selected. Every parent added since the serialized before-snapshot is first
// quarantined and durably marked; one native detach is then attempted. Markers
// are removed only for parents proven absent, so a failed detach remains safe
// across Agent exit and reboot instead of becoming an untracked USB device.
func (guard *Guard) rollbackUnknownImport(before map[string]usbParent, detach func() error) error {
	cleanupContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	parents, inventoryErr := guard.vhciParents()
	added := make([]usbParent, 0)
	if inventoryErr == nil {
		for path, parent := range parents {
			if _, existed := before[path]; !existed {
				added = append(added, parent)
			}
		}
		sort.Slice(added, func(left, right int) bool { return added[left].physicalID < added[right].physicalID })
	}
	var failures []error
	for _, parent := range added {
		failures = append(failures, guard.quarantineInterfaces(parent.physicalID))
		failures = append(failures, guard.writeMarker(importMarker{
			SchemaVersion: 1, PhysicalID: parent.physicalID, VendorID: parent.vendorID,
			ProductID: parent.productID, Serial: parent.serial, CreatedAt: guard.now().UTC(),
		}))
	}
	detachErr := detach()
	deadline := time.Now().Add(5 * time.Second)
	for _, parent := range added {
		for {
			_, statErr := os.Lstat(parent.physicalID)
			if errors.Is(statErr, os.ErrNotExist) {
				failures = append(failures, guard.removeMarker(parent.physicalID))
				break
			}
			if statErr != nil {
				failures = append(failures, statErr)
				break
			}
			if !time.Now().Before(deadline) {
				failures = append(failures, errors.New("unidentified VHCI parent remains after raw USB rollback"))
				break
			}
			select {
			case <-cleanupContext.Done():
				failures = append(failures, cleanupContext.Err())
				return errors.Join(append(failures, inventoryErr, detachErr)...)
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	return errors.Join(append(failures, inventoryErr, detachErr)...)
}

func (guard *Guard) installNetfilter(ctx context.Context) error {
	guard.policyMu.Lock()
	defer guard.policyMu.Unlock()
	return guard.installNetfilterLocked(ctx)
}

func (guard *Guard) installNetfilterLocked(ctx context.Context) error {
	_, listErr := guard.run(ctx, nil, "nft", "list", "table", "inet", nftTableName)
	var script string
	if listErr == nil {
		script = "delete table inet " + nftTableName + "\n"
	}
	script += "add table inet " + nftTableName + "\n"
	group := strconv.FormatUint(uint64(InterfaceGroup), 10)
	script += "add chain inet " + nftTableName + " output { type filter hook output priority 0; policy accept; }\n" +
		"add chain inet " + nftTableName + " input { type filter hook input priority 0; policy accept; }\n" +
		"add chain inet " + nftTableName + " forward { type filter hook forward priority 0; policy accept; }\n"
	permitMarks := make([]uint32, 0, len(guard.permits))
	for mark, permit := range guard.permits {
		if permit.Interface != "" {
			permitMarks = append(permitMarks, mark)
		}
	}
	sort.Slice(permitMarks, func(left, right int) bool { return permitMarks[left] < permitMarks[right] })
	for _, mark := range permitMarks {
		permit := guard.permits[mark]
		script += "add rule inet " + nftTableName + " output meta oifgroup " + group +
			" meta oifname \"" + permit.Interface + "\" socket cgroupv2 level 2 \"" + agentCGroup +
			"\" meta mark " + strconv.FormatUint(uint64(mark), 10) + " counter accept\n"
	}
	script += "add rule inet " + nftTableName + " output meta oifgroup " + group + " counter drop\n" +
		"add rule inet " + nftTableName + " input meta iifgroup " + group + " ct state established,related counter accept\n" +
		"add rule inet " + nftTableName + " input meta iifgroup " + group + " counter drop\n" +
		"add rule inet " + nftTableName + " forward meta iifgroup " + group + " counter drop\n" +
		"add rule inet " + nftTableName + " forward meta oifgroup " + group + " counter drop\n"
	if _, err := guard.run(ctx, []byte(script), "nft", "-f", "-"); err != nil {
		return fmt.Errorf("install MDD cellular nftables quarantine: %w", err)
	}
	output, err := guard.run(ctx, nil, "nft", "--numeric", "--numeric-priority", "--json",
		"list", "table", "inet", nftTableName)
	if err != nil {
		return fmt.Errorf("read installed MDD cellular nftables quarantine: %w", err)
	}
	activePermits := make(map[uint32]string, len(guard.permits))
	for mark, permit := range guard.permits {
		if permit.Interface != "" {
			activePermits[mark] = permit.Interface
		}
	}
	if err := verifyNFTContract(output, activePermits); err != nil {
		return fmt.Errorf("verify installed MDD cellular nftables quarantine: %w", err)
	}
	return nil
}

func (guard *Guard) writePolicyFiles() error {
	files := []struct {
		path    string
		payload []byte
	}{
		{filepath.Join(guard.etcRoot, "NetworkManager", "conf.d", networkManagerSnippetName), networkManagerSnippet},
		{filepath.Join(guard.etcRoot, "udev", "rules.d", udevRulesName), udevRules},
	}
	for _, file := range files {
		if err := os.MkdirAll(filepath.Dir(file.path), 0o755); err != nil {
			return err
		}
		if current, err := os.ReadFile(file.path); err == nil && bytes.Equal(current, file.payload) {
			if err := os.Chmod(file.path, 0o644); err != nil {
				return err
			}
			continue
		}
		if err := atomicwriter.WriteFile(file.path, file.payload, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (guard *Guard) armPersistence() error {
	if err := os.MkdirAll(guard.stateRoot, 0o700); err != nil {
		return fmt.Errorf("create durable cellular guard state: %w", err)
	}
	if err := os.Chmod(guard.stateRoot, 0o700); err != nil {
		return fmt.Errorf("protect durable cellular guard state: %w", err)
	}
	path := filepath.Join(guard.stateRoot, persistenceMarkerName)
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, persistenceMarker) {
		if err := os.Chmod(path, 0o600); err != nil {
			return err
		}
		return nil
	}
	if err := atomicwriter.WriteFile(path, persistenceMarker, 0o600); err != nil {
		return fmt.Errorf("persist cellular guard activation: %w", err)
	}
	return nil
}

func (guard *Guard) setVHCIDefaultAuthorization(allow bool) error {
	paths, err := guard.vhciAuthorizationDefaults()
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return errors.New("vhci_hcd did not expose interface authorization defaults")
	}
	value := "0\n"
	if allow {
		value = "1\n"
	}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte(value), 0); err != nil {
			return fmt.Errorf("set VHCI interface authorization default: %w", err)
		}
	}
	return nil
}

func (guard *Guard) vhciAuthorizationDefaults() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(guard.sysRoot, "bus", "usb", "devices"))
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "usb") {
			continue
		}
		link := filepath.Join(guard.sysRoot, "bus", "usb", "devices", entry.Name())
		real, err := filepath.EvalSymlinks(link)
		if err != nil || !isVHCIPath(real) {
			continue
		}
		path := filepath.Join(link, "interface_authorized_default")
		if _, err := os.Lstat(path); err == nil {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

type usbParent struct {
	physicalID string
	vendorID   uint16
	productID  uint16
	serial     string
}

func (guard *Guard) vhciParents() (map[string]usbParent, error) {
	entries, err := os.ReadDir(filepath.Join(guard.sysRoot, "bus", "usb", "devices"))
	if err != nil {
		return nil, err
	}
	result := make(map[string]usbParent)
	for _, entry := range entries {
		name := entry.Name()
		if strings.Contains(name, ":") || strings.HasPrefix(name, "usb") {
			continue
		}
		link := filepath.Join(guard.sysRoot, "bus", "usb", "devices", name)
		real, err := filepath.EvalSymlinks(link)
		if err != nil || !isVHCIPath(real) {
			continue
		}
		vendor, vendorErr := readHex16(filepath.Join(link, "idVendor"))
		product, productErr := readHex16(filepath.Join(link, "idProduct"))
		if vendorErr != nil || productErr != nil {
			return nil, fmt.Errorf("read VHCI parent USB identity %s: %w", real, errors.Join(vendorErr, productErr))
		}
		serial, _ := os.ReadFile(filepath.Join(link, "serial"))
		result[real] = usbParent{physicalID: real, vendorID: vendor, productID: product, serial: strings.TrimSpace(string(serial))}
	}
	return result, nil
}

func (guard *Guard) awaitImportedParent(ctx context.Context, before map[string]usbParent, identity DeviceIdentity) (usbParent, error) {
	deadline := time.Now().Add(12 * time.Second)
	for {
		parents, err := guard.vhciParents()
		if err != nil {
			return usbParent{}, err
		}
		matches := make([]usbParent, 0, 1)
		for path, parent := range parents {
			if _, existed := before[path]; existed || parent.vendorID != identity.VendorID || parent.productID != identity.ProductID {
				continue
			}
			if strings.TrimSpace(identity.Serial) != "" && parent.serial != strings.TrimSpace(identity.Serial) {
				continue
			}
			matches = append(matches, parent)
		}
		sort.Slice(matches, func(left, right int) bool { return matches[left].physicalID < matches[right].physicalID })
		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) > 1 {
			return usbParent{}, errors.New("multiple new VHCI parents match the imported modem")
		}
		if !time.Now().Before(deadline) {
			return usbParent{}, errors.New("imported modem did not appear under the guarded VHCI controller")
		}
		select {
		case <-ctx.Done():
			return usbParent{}, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (guard *Guard) authorizeInterfaces(ctx context.Context, physicalID string) error {
	interfaces, err := guard.interfacePaths(physicalID)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(interfaces) == 0 && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
		interfaces, err = guard.interfacePaths(physicalID)
		if err != nil {
			return err
		}
	}
	if len(interfaces) == 0 {
		return errors.New("imported modem exposed no USB interfaces")
	}
	for _, path := range interfaces {
		if err := os.WriteFile(filepath.Join(path, "authorized"), []byte("1\n"), 0); err != nil {
			return err
		}
	}
	for _, path := range interfaces {
		if err := os.WriteFile(filepath.Join(guard.sysRoot, "bus", "usb", "drivers_probe"), []byte(filepath.Base(path)+"\n"), 0); err != nil {
			return err
		}
	}
	return nil
}

func (guard *Guard) quarantineInterfaces(physicalID string) error {
	interfaces, err := guard.interfacePaths(physicalID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var failures []error
	for _, path := range interfaces {
		failures = append(failures, os.WriteFile(filepath.Join(path, "authorized"), []byte("0\n"), 0))
	}
	return errors.Join(failures...)
}

func (guard *Guard) interfacePaths(physicalID string) ([]string, error) {
	if !guard.scopedSysfsPath(physicalID) {
		return nil, errors.New("USB parent escaped sysfs")
	}
	entries, err := os.ReadDir(physicalID)
	if err != nil {
		return nil, err
	}
	prefix := filepath.Base(physicalID) + ":"
	result := make([]string, 0)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			result = append(result, filepath.Join(physicalID, entry.Name()))
		}
	}
	sort.Strings(result)
	return result, nil
}

func (guard *Guard) protectExistingNetdevs(ctx context.Context) error {
	entries, err := os.ReadDir(filepath.Join(guard.sysRoot, "class", "net"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		cellular, err := guard.cellularNetdev(entry.Name())
		if err != nil {
			return err
		}
		if cellular {
			if err := guard.ProtectNetdev(ctx, entry.Name()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (guard *Guard) protectNetdevsUnder(ctx context.Context, physicalID string) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		entries, err := os.ReadDir(filepath.Join(guard.sysRoot, "class", "net"))
		if err != nil {
			return err
		}
		for _, entry := range entries {
			path, err := filepath.EvalSymlinks(filepath.Join(guard.sysRoot, "class", "net", entry.Name(), "device"))
			if err == nil && sameUSBTree(path, physicalID) {
				if err := guard.ProtectNetdev(ctx, entry.Name()); err != nil {
					return err
				}
			}
		}
		// A modem may not expose a network interface at all. The persistent
		// udev+nft contract still protects one that appears later.
		if time.Now().After(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (guard *Guard) cellularNetdev(name string) (bool, error) {
	device := filepath.Join(guard.sysRoot, "class", "net", name, "device")
	real, err := filepath.EvalSymlinks(device)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if isVHCIPath(real) {
		return true, nil
	}
	driver, err := filepath.EvalSymlinks(filepath.Join(device, "driver"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, cellular := cellularDrivers[filepath.Base(driver)]
	return cellular, nil
}

func (guard *Guard) writeMarker(marker importMarker) error {
	if marker.SchemaVersion != 1 || !guard.scopedSysfsPath(marker.PhysicalID) || marker.VendorID == 0 || marker.ProductID == 0 || marker.CreatedAt.IsZero() {
		return errors.New("invalid raw import quarantine marker")
	}
	directory := filepath.Join(guard.stateRoot, "raw-import-quarantine")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return err
	}
	payload, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return atomicwriter.WriteFile(filepath.Join(directory, markerName(marker.PhysicalID)), payload, 0o600)
}

func (guard *Guard) removeMarker(physicalID string) error {
	path := filepath.Join(guard.stateRoot, "raw-import-quarantine", markerName(physicalID))
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (guard *Guard) loadMarkers() (map[string]importMarker, error) {
	directory := filepath.Join(guard.stateRoot, "raw-import-quarantine")
	info, statErr := os.Lstat(directory)
	if errors.Is(statErr, os.ErrNotExist) {
		return map[string]importMarker{}, nil
	}
	if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return nil, errors.New("raw import quarantine path is not an exact protected directory")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	markers := make(map[string]importMarker, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			return nil, errors.New("raw import quarantine directory contains an unexpected entry")
		}
		path := filepath.Join(directory, entry.Name())
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("raw import quarantine marker is not an exact protected regular file")
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var marker importMarker
		if json.Unmarshal(payload, &marker) != nil || marker.SchemaVersion != 1 || !guard.scopedSysfsPath(marker.PhysicalID) ||
			marker.VendorID == 0 || marker.ProductID == 0 || marker.CreatedAt.IsZero() ||
			entry.Name() != markerName(marker.PhysicalID) {
			return nil, errors.New("invalid durable raw import quarantine marker")
		}
		marker.Serial = strings.TrimSpace(marker.Serial)
		marker.PhysicalID = filepath.Clean(marker.PhysicalID)
		if _, exists := markers[marker.PhysicalID]; exists {
			return nil, errors.New("duplicate durable raw import quarantine marker")
		}
		markers[marker.PhysicalID] = marker
	}
	return markers, nil
}

func (guard *Guard) recoverMarkers() error {
	markers, err := guard.loadMarkers()
	if err != nil {
		return err
	}
	parents, inventoryErr := guard.vhciParents()
	if inventoryErr != nil {
		return inventoryErr
	}
	for physicalID := range parents {
		if err := guard.quarantineInterfaces(physicalID); err != nil {
			return err
		}
	}
	for physicalID, marker := range markers {
		parent, exists := parents[physicalID]
		if !exists {
			if _, err := os.Lstat(physicalID); errors.Is(err, os.ErrNotExist) {
				if err := guard.removeMarker(marker.PhysicalID); err != nil {
					return err
				}
				continue
			} else if err != nil {
				return err
			}
			_ = guard.quarantineInterfaces(physicalID)
			return errors.New("durable raw import marker does not identify a readable VHCI parent")
		}
		if err := guard.quarantineInterfaces(physicalID); err != nil {
			return err
		}
		if !markerMatchesParent(marker, parent) {
			return errors.New("durable raw import marker identity does not match its VHCI parent")
		}
	}
	return guard.verifyTrackedParents(parents, markers)
}

func (guard *Guard) verifyTrackedParents(parents map[string]usbParent, markers map[string]importMarker) error {
	for physicalID, parent := range parents {
		marker, exists := markers[physicalID]
		if !exists {
			return errors.New("existing VHCI parent has no durable raw import quarantine marker")
		}
		if !markerMatchesParent(marker, parent) {
			return errors.New("existing VHCI parent identity differs from its durable quarantine marker")
		}
	}
	return nil
}

func markerMatchesParent(marker importMarker, parent usbParent) bool {
	return filepath.Clean(marker.PhysicalID) == filepath.Clean(parent.physicalID) &&
		marker.VendorID == parent.vendorID && marker.ProductID == parent.productID &&
		strings.TrimSpace(marker.Serial) == strings.TrimSpace(parent.serial)
}

func mergedConfigHasStrictGSMGuard(payload []byte) bool {
	section := ""
	for _, line := range strings.Split(string(payload), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		if section != "keyfile" {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found || strings.TrimSuffix(strings.TrimSpace(key), "+") != "unmanaged-devices" {
			continue
		}
		for device := range strings.FieldsFuncSeq(value, func(current rune) bool { return current == ';' || current == ',' }) {
			if strings.TrimSpace(device) == "type:gsm" {
				return true
			}
		}
	}
	return false
}

type nftDocument struct {
	NFTables []map[string]json.RawMessage `json:"nftables"`
}

type nftTable struct {
	Family string `json:"family"`
	Name   string `json:"name"`
}

type nftChain struct {
	Family string          `json:"family"`
	Table  string          `json:"table"`
	Name   string          `json:"name"`
	Type   string          `json:"type"`
	Hook   string          `json:"hook"`
	Prio   json.RawMessage `json:"prio"`
	Policy string          `json:"policy"`
}

type nftRule struct {
	Family string            `json:"family"`
	Table  string            `json:"table"`
	Chain  string            `json:"chain"`
	Expr   []json.RawMessage `json:"expr"`
}

func verifyNFTContract(payload []byte, permits map[uint32]string) error {
	var document nftDocument
	if err := json.Unmarshal(payload, &document); err != nil || len(document.NFTables) == 0 {
		return errors.New("nftables JSON is invalid or empty")
	}
	tables := 0
	chains := map[string]bool{}
	drops := map[string]bool{}
	accepts := map[uint32]bool{}
	outputDropSeen := false
	inputEstablished := false
	for _, item := range document.NFTables {
		if len(item) != 1 {
			return errors.New("nftables JSON contains an ambiguous object")
		}
		for kind, raw := range item {
			switch kind {
			case "metainfo":
				continue
			case "table":
				var table nftTable
				if json.Unmarshal(raw, &table) != nil || table.Family != "inet" || table.Name != nftTableName {
					return errors.New("nftables table identity changed")
				}
				tables++
			case "chain":
				var chain nftChain
				if json.Unmarshal(raw, &chain) != nil || chain.Family != "inet" || chain.Table != nftTableName ||
					chain.Type != "filter" || chain.Policy != "accept" || !rawIntegerEquals(chain.Prio, 0) ||
					(chain.Name != "output" && chain.Name != "input" && chain.Name != "forward") || chain.Hook != chain.Name || chains[chain.Name] {
					return errors.New("nftables base chain contract changed")
				}
				chains[chain.Name] = true
			case "rule":
				var rule nftRule
				if json.Unmarshal(raw, &rule) != nil || rule.Family != "inet" || rule.Table != nftTableName ||
					(rule.Chain != "output" && rule.Chain != "input" && rule.Chain != "forward") {
					return errors.New("nftables drop rule contract changed")
				}
				if groupKey, ok := validNFTDropRule(rule.Expr); ok {
					dropKey := rule.Chain + ":" + groupKey
					validDrop := rule.Chain == "output" && groupKey == "oifgroup" ||
						rule.Chain == "input" && groupKey == "iifgroup" ||
						rule.Chain == "forward" && (groupKey == "iifgroup" || groupKey == "oifgroup")
					if !validDrop || drops[dropKey] {
						return errors.New("nftables contains a duplicate drop rule")
					}
					drops[dropKey] = true
					if rule.Chain == "output" {
						outputDropSeen = true
					}
					continue
				}
				if rule.Chain == "input" && !inputEstablished && validNFTEstablishedInputRule(rule.Expr) {
					inputEstablished = true
					continue
				}
				mark, ok := validNFTPermitRule(rule.Expr, permits)
				if rule.Chain != "output" || outputDropSeen || !ok || accepts[mark] {
					return errors.New("nftables permit rule contract changed")
				}
				accepts[mark] = true
			default:
				return fmt.Errorf("unexpected nftables object %q", kind)
			}
		}
	}
	if tables != 1 || len(chains) != 3 || len(drops) != 4 || !inputEstablished || len(accepts) != len(permits) {
		return errors.New("nftables quarantine table is incomplete")
	}
	for mark := range permits {
		if !accepts[mark] {
			return errors.New("nftables quarantine is missing an exact data permit")
		}
	}
	return nil
}

func validNFTDropRule(expressions []json.RawMessage) (string, bool) {
	if len(expressions) != 3 {
		return "", false
	}
	groupKey, ok := nftGroupMatch(expressions[0])
	if !ok {
		return "", false
	}
	var counterItem, dropItem map[string]json.RawMessage
	if json.Unmarshal(expressions[1], &counterItem) != nil || len(counterItem) != 1 {
		return "", false
	}
	if _, exists := counterItem["counter"]; !exists {
		return "", false
	}
	if json.Unmarshal(expressions[2], &dropItem) != nil || len(dropItem) != 1 {
		return "", false
	}
	_, exists := dropItem["drop"]
	return groupKey, exists
}

func validNFTPermitRule(expressions []json.RawMessage, permits map[uint32]string) (uint32, bool) {
	if len(expressions) != 6 {
		return 0, false
	}
	groupKey, groupOK := nftGroupMatch(expressions[0])
	if !groupOK || groupKey != "oifgroup" {
		return 0, false
	}
	interfaceName, ok := nftMetaStringMatch(expressions[1], "oifname")
	if !ok || !validNFTAgentCGroupMatch(expressions[2]) {
		return 0, false
	}
	mark, ok := nftMetaMarkMatch(expressions[3])
	if !ok {
		return 0, false
	}
	var counterItem, acceptItem map[string]json.RawMessage
	if json.Unmarshal(expressions[4], &counterItem) != nil || len(counterItem) != 1 {
		return 0, false
	}
	if _, exists := counterItem["counter"]; !exists {
		return 0, false
	}
	if json.Unmarshal(expressions[5], &acceptItem) != nil || len(acceptItem) != 1 {
		return 0, false
	}
	_, exists := acceptItem["accept"]
	return mark, exists && permits[mark] == interfaceName
}

func nftMetaStringMatch(raw json.RawMessage, key string) (string, bool) {
	var item map[string]json.RawMessage
	if json.Unmarshal(raw, &item) != nil || len(item) != 1 {
		return "", false
	}
	var match struct {
		Op   string `json:"op"`
		Left struct {
			Meta struct {
				Key string `json:"key"`
			} `json:"meta"`
		} `json:"left"`
		Right string `json:"right"`
	}
	if json.Unmarshal(item["match"], &match) != nil || match.Op != "==" || match.Left.Meta.Key != key || match.Right == "" {
		return "", false
	}
	return match.Right, true
}

func validNFTAgentCGroupMatch(raw json.RawMessage) bool {
	var item map[string]json.RawMessage
	if json.Unmarshal(raw, &item) != nil || len(item) != 1 {
		return false
	}
	var match struct {
		Op   string `json:"op"`
		Left struct {
			Socket struct {
				Key   string `json:"key"`
				Level uint32 `json:"level"`
			} `json:"socket"`
		} `json:"left"`
		Right string `json:"right"`
	}
	return json.Unmarshal(item["match"], &match) == nil && match.Op == "==" &&
		match.Left.Socket.Key == "cgroupv2" && match.Left.Socket.Level == 2 && match.Right == agentCGroup
}

func nftGroupMatch(raw json.RawMessage) (string, bool) {
	var item map[string]json.RawMessage
	if json.Unmarshal(raw, &item) != nil || len(item) != 1 {
		return "", false
	}
	var match struct {
		Op   string `json:"op"`
		Left struct {
			Meta struct {
				Key string `json:"key"`
			} `json:"meta"`
		} `json:"left"`
		Right json.RawMessage `json:"right"`
	}
	if json.Unmarshal(item["match"], &match) != nil || match.Op != "==" ||
		(match.Left.Meta.Key != "iifgroup" && match.Left.Meta.Key != "oifgroup") ||
		!rawIntegerEquals(match.Right, InterfaceGroup) {
		return "", false
	}
	return match.Left.Meta.Key, true
}

func validNFTEstablishedInputRule(expressions []json.RawMessage) bool {
	if len(expressions) != 4 {
		return false
	}
	groupKey, ok := nftGroupMatch(expressions[0])
	if !ok || groupKey != "iifgroup" {
		return false
	}
	var stateItem map[string]json.RawMessage
	if json.Unmarshal(expressions[1], &stateItem) != nil || len(stateItem) != 1 {
		return false
	}
	var match struct {
		Op   string `json:"op"`
		Left struct {
			CT struct {
				Key string `json:"key"`
			} `json:"ct"`
		} `json:"left"`
		Right json.RawMessage `json:"right"`
	}
	if json.Unmarshal(stateItem["match"], &match) != nil || match.Op != "in" || match.Left.CT.Key != "state" ||
		!validNFTEstablishedStates(match.Right) {
		return false
	}
	var counterItem, acceptItem map[string]json.RawMessage
	if json.Unmarshal(expressions[2], &counterItem) != nil || len(counterItem) != 1 {
		return false
	}
	if _, exists := counterItem["counter"]; !exists {
		return false
	}
	if json.Unmarshal(expressions[3], &acceptItem) != nil || len(acceptItem) != 1 {
		return false
	}
	_, exists := acceptItem["accept"]
	return exists
}

func validNFTEstablishedStates(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return false
	}
	// nftables 1.0.2 emits a bare numeric array under --numeric, while the
	// schema-shaped representation used by other supported versions wraps the
	// symbolic values in {"set": ...}. Both must still identify exactly the
	// established (2) and related (4) conntrack states and nothing else.
	if raw[0] == '{' {
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil || len(object) != 1 {
			return false
		}
		var exists bool
		raw, exists = object["set"]
		if !exists {
			return false
		}
	}
	var numeric []uint32
	if json.Unmarshal(raw, &numeric) == nil {
		return exactUint32Pair(numeric, 2, 4)
	}
	var symbolic []string
	if json.Unmarshal(raw, &symbolic) != nil || len(symbolic) != 2 {
		return false
	}
	seen := map[string]bool{}
	for _, value := range symbolic {
		if value != "established" && value != "related" || seen[value] {
			return false
		}
		seen[value] = true
	}
	return seen["established"] && seen["related"]
}

func exactUint32Pair(values []uint32, first, second uint32) bool {
	if len(values) != 2 || values[0] == values[1] {
		return false
	}
	return values[0] == first && values[1] == second || values[0] == second && values[1] == first
}

func nftMetaMarkMatch(raw json.RawMessage) (uint32, bool) {
	var item map[string]json.RawMessage
	if json.Unmarshal(raw, &item) != nil || len(item) != 1 {
		return 0, false
	}
	var match struct {
		Op   string `json:"op"`
		Left struct {
			Meta struct {
				Key string `json:"key"`
			} `json:"meta"`
		} `json:"left"`
		Right json.RawMessage `json:"right"`
	}
	if json.Unmarshal(item["match"], &match) != nil || match.Op != "==" || match.Left.Meta.Key != "mark" {
		return 0, false
	}
	value, err := strconv.ParseUint(strings.TrimSpace(string(match.Right)), 10, 32)
	return uint32(value), err == nil && value != 0
}

func rawIntegerEquals(raw json.RawMessage, expected uint32) bool {
	value, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 32)
	return err == nil && uint32(value) == expected
}

func leadingUint(value string) (uint64, error) {
	end := 0
	for end < len(value) && value[end] >= '0' && value[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, errors.New("value has no leading integer")
	}
	return strconv.ParseUint(value[:end], 10, 64)
}

func (guard *Guard) scopedSysfsPath(path string) bool {
	path = filepath.Clean(path)
	relative, err := filepath.Rel(guard.sysRoot, path)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && isVHCIPath(path)
}

func markerName(physicalID string) string {
	digest := sha256.Sum256([]byte(filepath.Clean(physicalID)))
	return hex.EncodeToString(digest[:16]) + ".json"
}

func exactFile(path string, expected []byte, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != mode || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("policy path is not an exact protected regular file")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(payload, expected) {
		return errors.New("policy file contents changed")
	}
	return nil
}

func readHex16(path string) (uint16, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseUint(strings.TrimSpace(string(payload)), 16, 16)
	return uint16(value), err
}

func isVHCIPath(path string) bool {
	path = filepath.ToSlash(filepath.Clean(path))
	return strings.Contains(path, "/vhci_hcd.")
}

func sameUSBTree(path, physical string) bool {
	path, physical = filepath.Clean(path), filepath.Clean(physical)
	return path == physical || strings.HasPrefix(path, physical+string(filepath.Separator)) || strings.HasPrefix(path, physical+":")
}

func runCommand(ctx context.Context, input []byte, name string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	if input != nil {
		command.Stdin = bytes.NewReader(input)
	}
	output, err := command.CombinedOutput()
	if len(output) > maximumCommandOutput {
		output = output[:maximumCommandOutput]
	}
	if err != nil {
		detail := strings.TrimSpace(strings.ToValidUTF8(string(output), "?"))
		if detail != "" {
			return output, fmt.Errorf("%s: %w: %s", name, err, detail)
		}
		return output, fmt.Errorf("%s: %w", name, err)
	}
	return output, nil
}
