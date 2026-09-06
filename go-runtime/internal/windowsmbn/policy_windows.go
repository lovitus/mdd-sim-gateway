//go:build windows && (amd64 || arm64)

package windowsmbn

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	win32 "github.com/deploymenttheory/go-bindings-win32/bindings/runtime/win32"
	mbn "github.com/deploymenttheory/go-bindings-win32/bindings/win32/networkmanagement/mobilebroadband"
	"github.com/deploymenttheory/go-bindings-win32/bindings/win32/system/com"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentpolicy"
	"golang.org/x/sys/windows"
)

func (*Prober) PolicyProfileMode() string { return "system" }

func (prober *Prober) PrepareSIMAPDU(ctx context.Context, target agentpolicy.Target) (bool, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	facts, err := prober.probeLocked(ctx)
	if err != nil || !exactPolicyTarget(facts, target) {
		return false, agentpolicyTargetError(err)
	}
	for _, fact := range facts {
		if fact.AttachmentID != target.AttachmentID || fact.EquipmentID != target.EquipmentID ||
			fact.SIM.ICCID != target.CardID || fact.SIM.SessionGeneration != target.SIMSessionGeneration {
			continue
		}
		if fact.Network.Data != agentmodem.DataDisconnected {
			return false, agentpolicy.ErrSIMAPDUDataActive
		}
		if fact.AT.SIMAPDU {
			return true, nil
		}
		if fact.AT.State != agentmodem.ATControlReady || !fact.AT.SIMAPDUOnDemand {
			return false, agentpolicy.ErrSIMAPDUUnavailable
		}
		return prober.at.PrepareSIMAPDU(ctx, target.EquipmentID)
	}
	return false, agentmodem.ErrOperationTargetReplaced
}

func (prober *Prober) SetPolicyRadio(ctx context.Context, target agentpolicy.Target, enabled bool) error {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	facts, err := prober.probeLocked(ctx)
	if err != nil || !exactPolicyTarget(facts, target) {
		return agentpolicyTargetError(err)
	}
	_, err = withMBNInterface(ctx, target.AttachmentID, func(value *mbn.IMbnInterface) (string, error) {
		radio, err := query[mbn.IMbnRadio](value, &mbn.IID_IMbnRadio)
		if err != nil {
			return "", err
		}
		defer radio.Release()
		wanted := mbn.MBN_RADIO_OFF
		if enabled {
			wanted = mbn.MBN_RADIO_ON
		}
		var current mbn.MBN_RADIO
		if err := radio.Get_SoftwareRadioState(&current); err != nil {
			return "", err
		}
		if current == wanted {
			return "", nil
		}
		var requestID uint32
		if err := radio.SetSoftwareRadioState(wanted, &requestID); err != nil {
			return "", err
		}
		deadline := time.Now().Add(20 * time.Second)
		for {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			if err := radio.Get_SoftwareRadioState(&current); err == nil && current == wanted {
				return "", nil
			}
			if !time.Now().Before(deadline) {
				return "", errors.New("Windows MBN radio state did not converge")
			}
			time.Sleep(200 * time.Millisecond)
		}
	})
	return err
}

func (prober *Prober) ListPolicyProfiles(ctx context.Context, target agentpolicy.Target) ([]agentpolicy.ProfileView, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	facts, err := prober.probeLocked(ctx)
	if err != nil || !exactPolicyTarget(facts, target) {
		return nil, agentpolicyTargetError(err)
	}
	var result []agentpolicy.ProfileView
	_, err = withMBNInterface(ctx, target.AttachmentID, func(value *mbn.IMbnInterface) (string, error) {
		profiles, err := connectionProfiles(value)
		if err != nil {
			return "", err
		}
		result = make([]agentpolicy.ProfileView, 0, len(profiles))
		for _, profile := range profiles {
			result = append(result, agentpolicy.ProfileView{Name: profile.Name, APN: strings.TrimSpace(profile.Context.AccessString),
				Auth: normalizeWindowsAuth(profile.Context.AuthProtocol), Username: profile.Context.Credentials.UserName,
				PasswordConfigured: profile.Context.Credentials.Password != "", System: true, Source: "system"})
		}
		return "", nil
	})
	if err == nil {
		for _, fact := range facts {
			if fact.AttachmentID == target.AttachmentID && fact.EquipmentID == target.EquipmentID &&
				fact.AT.State == agentmodem.ATControlReady {
				if payload, queryErr := prober.at.Exchange(ctx, target.EquipmentID, "AT+CGDCONT?", 3*time.Second); queryErr == nil {
					result = append(result, agentpolicy.ParsePDPContexts(payload)...)
				}
				result = append(result, agentpolicy.ProviderAPNCandidates(fact.SIM.IMSI)...)
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, err
}

func (prober *Prober) SavePolicyProfile(ctx context.Context, target agentpolicy.Target, profile agentpolicy.Profile) error {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	facts, err := prober.probeLocked(ctx)
	if err != nil || !exactPolicyTarget(facts, target) {
		return agentpolicyTargetError(err)
	}
	var subscriberID string
	for _, fact := range facts {
		if fact.AttachmentID == target.AttachmentID && fact.EquipmentID == target.EquipmentID && fact.SIM.ICCID == target.CardID {
			subscriberID = fact.SIM.IMSI
		}
	}
	if len(subscriberID) < 14 || len(subscriberID) > 15 {
		return errors.New("SIM IMSI is required to create a Windows MBN profile")
	}
	payload, err := windowsProfileXML(profile, subscriberID)
	if err != nil {
		return err
	}
	_, err = withMBNInterface(ctx, target.AttachmentID, func(value *mbn.IMbnInterface) (string, error) {
		manager, err := openProfileManager()
		if err != nil {
			return "", err
		}
		defer manager.Release()
		var existing *mbn.IMbnConnectionProfile
		getErr := manager.GetConnectionProfile(value, profile.Name, &existing)
		if getErr == nil && existing != nil {
			defer existing.Release()
			if err := existing.UpdateProfile(payload); err != nil {
				return "", err
			}
		} else if errors.Is(getErr, windows.ERROR_NOT_FOUND) {
			if existing != nil {
				existing.Release()
			}
			if err := manager.CreateConnectionProfile(payload); err != nil {
				return "", err
			}
		} else {
			if existing != nil {
				existing.Release()
			}
			return "", getErr
		}
		return "", waitPolicyProfile(ctx, value, profile)
	})
	return err
}

func waitPolicyProfile(ctx context.Context, value *mbn.IMbnInterface, desired agentpolicy.Profile) error {
	deadline := time.Now().Add(20 * time.Second)
	var last error
	for {
		profiles, err := connectionProfiles(value)
		last = err
		if err == nil {
			for _, current := range profiles {
				if current.Name == strings.TrimSpace(desired.Name) &&
					strings.TrimSpace(current.Context.AccessString) == strings.TrimSpace(desired.APN) &&
					normalizeWindowsAuth(current.Context.AuthProtocol) == strings.ToUpper(strings.TrimSpace(desired.Auth)) &&
					current.Context.Credentials.UserName == desired.Username {
					return nil
				}
			}
		}
		if !time.Now().Before(deadline) {
			return errors.Join(errors.New("Windows MBN profile postcondition was not observed"), last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func exactPolicyTarget(facts []agentmodem.Fact, target agentpolicy.Target) bool {
	matches := 0
	for _, fact := range facts {
		if fact.AttachmentID == target.AttachmentID && fact.EquipmentID == target.EquipmentID &&
			fact.SIM.ICCID == target.CardID && fact.SIM.State == agentmodem.SIMReady &&
			fact.SIM.SessionGeneration == target.SIMSessionGeneration {
			matches++
		}
	}
	return matches == 1
}

func agentpolicyTargetError(err error) error {
	if err != nil {
		return err
	}
	return agentmodem.ErrOperationTargetReplaced
}

func openProfileManager() (*mbn.IMbnConnectionProfileManager, error) {
	var root *win32.IUnknown
	if err := com.CoCreateInstance(&clsidMbnConnectionProfileManager, nil, com.CLSCTX_INPROC_SERVER,
		&mbn.IID_IMbnConnectionProfileManager, &root); err != nil {
		if root != nil {
			root.Release()
		}
		return nil, err
	}
	return win32.Cast[mbn.IMbnConnectionProfileManager](root), nil
}
