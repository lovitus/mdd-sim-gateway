// Package linebootstrap projects live, unconfigured SIM attachments and turns
// one explicitly confirmed attachment into a disabled catalog draft. Live
// observations are never persisted and no runtime or Provider action occurs.
package linebootstrap

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

const (
	SchemaVersion = 1
	defaultMaxAge = 30 * time.Second
)

var (
	ErrCandidateStale   = errors.New("line candidate is no longer current")
	ErrCandidateBlocked = errors.New("line candidate cannot be claimed")
)

type AgentFacts interface {
	Statuses() []agentlink.ConnectionStatus
}

type ObservedIdentity struct {
	IMSI     string   `json:"imsi,omitempty"`
	MCC      string   `json:"mcc,omitempty"`
	MNC      string   `json:"mnc,omitempty"`
	IMEI     string   `json:"imei,omitempty"`
	MSISDN   string   `json:"msisdn,omitempty"`
	MSISDNs  []string `json:"msisdns,omitempty"`
	SMSC     string   `json:"smsc,omitempty"`
	PINState string   `json:"pin_state,omitempty"`
}

type RawAvailability struct {
	Available bool   `json:"available"`
	Code      string `json:"code"`
}

type Candidate struct {
	CandidateID       string           `json:"candidate_id"`
	Kind              string           `json:"kind"`
	Mode              string           `json:"mode"`
	Condition         string           `json:"condition"`
	CanClaim          bool             `json:"can_claim"`
	AgentID           string           `json:"agent_id"`
	ProcessGeneration string           `json:"process_generation"`
	ReaderName        string           `json:"reader_name,omitempty"`
	AttachmentID      string           `json:"attachment_id,omitempty"`
	SessionGeneration string           `json:"session_generation"`
	EquipmentID       string           `json:"equipment_id,omitempty"`
	CardID            string           `json:"card_id"`
	ConfiguredLineID  string           `json:"configured_line_id,omitempty"`
	ProvisionState    string           `json:"provision_state"`
	ProvisionBlockers []string         `json:"provision_blockers,omitempty"`
	Observed          ObservedIdentity `json:"observed"`
	Raw               *RawAvailability `json:"raw,omitempty"`
}

type Snapshot struct {
	SchemaVersion   int         `json:"schema_version"`
	CatalogRevision uint64      `json:"catalog_revision"`
	GeneratedAt     time.Time   `json:"generated_at"`
	Candidates      []Candidate `json:"candidates"`
}

type ClaimResult struct {
	SchemaVersion int              `json:"schema_version"`
	Revision      uint64           `json:"revision"`
	Line          linecatalog.Line `json:"line"`
	Candidate     Candidate        `json:"candidate"`
}

type Service struct {
	catalog *linecatalog.Store
	agents  AgentFacts
	now     func() time.Time
	maxAge  time.Duration
	random  io.Reader
}

func New(catalog *linecatalog.Store, agents AgentFacts, now func() time.Time) (*Service, error) {
	if catalog == nil || agents == nil {
		return nil, errors.New("line bootstrap requires catalog and Agent facts")
	}
	if now == nil {
		now = time.Now
	}
	return &Service{catalog: catalog, agents: agents, now: now, maxAge: defaultMaxAge, random: rand.Reader}, nil
}

func (service *Service) Project() (Snapshot, error) {
	now := service.now().UTC()
	// Include soft-deleted records only for CardID ownership fencing. They
	// remain hidden from the normal catalog projection but cannot be claimed
	// again while their history is retained.
	catalog, err := service.catalog.SnapshotIncludingDeleted()
	if err != nil {
		return Snapshot{}, err
	}
	configured := make(map[string]string, len(catalog.Lines))
	for _, line := range catalog.Lines {
		configured[line.CardID] = line.ID
	}

	// Count every current, authoritative ICCID occurrence before applying the
	// stricter candidate-completeness filters below. An incomplete attachment
	// must still make the identity ambiguous; otherwise the same physical card
	// could be claimed from a second Agent while facts are still converging.
	occurrences := make(map[string]int)
	statuses := service.agents.Statuses()
	for _, status := range statuses {
		if !service.fresh(status.LastReport, now) || status.Topology == nil {
			continue
		}
		topology := status.Topology
		if topology.ReaderCondition == agentlink.ReaderReady {
			for _, reader := range topology.Readers {
				if reader.CardPresent && reader.IdentityState == agentlink.CardIdentified && validDigits(reader.CardID, 4, 32) {
					occurrences[reader.CardID]++
				}
			}
		}
		if topology.ModemCondition == agentlink.ModemReady {
			for _, modem := range topology.Modems {
				if modem.SIM.State == "ready" && validDigits(modem.SIM.ICCID, 4, 32) {
					occurrences[modem.SIM.ICCID]++
				}
			}
		}
	}

	var candidates []Candidate
	for _, status := range statuses {
		if !service.fresh(status.LastReport, now) || status.Topology == nil ||
			status.AgentID == "" || status.ProcessGeneration == "" {
			continue
		}
		topology := status.Topology
		if topology.ReaderCondition == agentlink.ReaderReady {
			for _, reader := range topology.Readers {
				if !reader.CardPresent || reader.IdentityState != agentlink.CardIdentified ||
					reader.ReaderName == "" || reader.SessionGeneration == "" || !validDigits(reader.CardID, 4, 32) {
					continue
				}
				candidate := Candidate{
					Kind: "reader", Mode: "remote_card", AgentID: status.AgentID,
					ProcessGeneration: status.ProcessGeneration, ReaderName: reader.ReaderName,
					SessionGeneration: reader.SessionGeneration, CardID: reader.CardID,
				}
				candidate.CandidateID = candidateHash(candidate)
				candidates = append(candidates, candidate)
			}
		}
		if topology.ModemCondition == agentlink.ModemReady {
			for _, modem := range topology.Modems {
				if modem.AttachmentID == "" || modem.EquipmentID == "" || modem.SIM.State != "ready" ||
					modem.SIM.SessionGeneration == "" || !validDigits(modem.SIM.ICCID, 4, 32) || !typedModem(modem) {
					continue
				}
				observed := modemIdentity(modem)
				candidate := Candidate{
					Kind: "modem", Mode: "adapted", AgentID: status.AgentID,
					ProcessGeneration: status.ProcessGeneration, AttachmentID: modem.AttachmentID,
					SessionGeneration: modem.SIM.SessionGeneration, EquipmentID: modem.EquipmentID,
					CardID: modem.SIM.ICCID, Observed: observed,
					Raw: &RawAvailability{Available: false, Code: "raw_isolation_unproven"},
				}
				candidate.CandidateID = candidateHash(candidate)
				candidates = append(candidates, candidate)
			}
		}
	}

	for index := range candidates {
		candidate := &candidates[index]
		candidate.ConfiguredLineID = configured[candidate.CardID]
		switch {
		case occurrences[candidate.CardID] != 1:
			candidate.Condition, candidate.ProvisionState, candidate.ProvisionBlockers = "ambiguous_card", "blocked", []string{"identity_ambiguous"}
		case candidate.ConfiguredLineID != "":
			candidate.Condition, candidate.ProvisionState = "configured", "already_configured"
		case !identityComplete(candidate.Observed):
			candidate.Condition, candidate.CanClaim, candidate.ProvisionState, candidate.ProvisionBlockers = "identity_incomplete", true, "draft_only", []string{"identity_incomplete"}
		default:
			candidate.Condition, candidate.CanClaim, candidate.ProvisionState = "ready", true, "awaiting_catalog_commit"
		}
	}
	sort.Slice(candidates, func(left, right int) bool {
		if candidates[left].CardID != candidates[right].CardID {
			return candidates[left].CardID < candidates[right].CardID
		}
		if candidates[left].Kind != candidates[right].Kind {
			return candidates[left].Kind < candidates[right].Kind
		}
		return candidates[left].CandidateID < candidates[right].CandidateID
	})
	return Snapshot{SchemaVersion: SchemaVersion, CatalogRevision: catalog.Revision,
		GeneratedAt: now, Candidates: candidates}, nil
}

func (service *Service) Claim(candidateID, name string, expectedRevision uint64) (ClaimResult, error) {
	snapshot, err := service.Project()
	if err != nil {
		return ClaimResult{}, err
	}
	if snapshot.CatalogRevision != expectedRevision {
		return ClaimResult{Revision: snapshot.CatalogRevision}, linecatalog.ErrRevision
	}
	var selected *Candidate
	for index := range snapshot.Candidates {
		if snapshot.Candidates[index].CandidateID == candidateID {
			copy := snapshot.Candidates[index]
			selected = &copy
			break
		}
	}
	if selected == nil {
		return ClaimResult{Revision: snapshot.CatalogRevision}, ErrCandidateStale
	}
	if !selected.CanClaim {
		return ClaimResult{Revision: snapshot.CatalogRevision, Candidate: *selected}, ErrCandidateBlocked
	}

	line := linecatalog.Line{
		SchemaVersion: linecatalog.SchemaVersion, Name: strings.TrimSpace(name), Enabled: false,
		CardID: selected.CardID,
		SIM: linecatalog.SIMConfig{
			IMSI: selected.Observed.IMSI, MCC: selected.Observed.MCC, MNC: selected.Observed.MNC,
			IMEI: selected.Observed.IMEI, MSISDN: selected.Observed.MSISDN, SMSC: selected.Observed.SMSC,
		},
	}
	if line.Name == "" {
		line.Name = "未命名 SIM"
	}
	for attempt := 0; attempt < 4; attempt++ {
		line.ID, err = service.randomLineID()
		if err != nil {
			return ClaimResult{Revision: snapshot.CatalogRevision, Candidate: *selected}, err
		}
		created, revision, createErr := service.catalog.CreateExpected(line, expectedRevision)
		if errors.Is(createErr, linecatalog.ErrAlreadyExists) {
			continue
		}
		return ClaimResult{SchemaVersion: SchemaVersion, Revision: revision, Line: created, Candidate: *selected}, createErr
	}
	return ClaimResult{Revision: snapshot.CatalogRevision, Candidate: *selected}, errors.New("could not allocate a unique line identity")
}

func (service *Service) fresh(observed, now time.Time) bool {
	if observed.IsZero() || observed.After(now.Add(service.maxAge)) {
		return false
	}
	return now.Sub(observed) <= service.maxAge
}

func (service *Service) randomLineID() (string, error) {
	value := make([]byte, 12)
	if _, err := io.ReadFull(service.random, value); err != nil {
		return "", err
	}
	return "line-" + hex.EncodeToString(value), nil
}

func candidateHash(candidate Candidate) string {
	values := []string{candidate.Kind, candidate.AgentID, candidate.ProcessGeneration, candidate.ReaderName,
		candidate.AttachmentID, candidate.SessionGeneration, candidate.EquipmentID, candidate.CardID}
	hash := sha256.New()
	for _, value := range values {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func typedModem(modem agentlink.ModemFact) bool {
	return modem.AT.CallSignalling || modem.AT.SMS || modem.AT.SIMAPDU || modem.Capabilities.CellularData ||
		modem.Capabilities.SMSReceive || modem.Capabilities.SMSSend
}

func modemIdentity(modem agentlink.ModemFact) ObservedIdentity {
	identity := ObservedIdentity{PINState: strings.TrimSpace(modem.SIM.PINState)}
	if validDigits(modem.SIM.IMSI, 5, 18) {
		identity.IMSI = modem.SIM.IMSI
		identity.MCC = modem.SIM.IMSI[:3]
	}
	if validDigits(modem.EquipmentID, 14, 16) {
		identity.IMEI = modem.EquipmentID
	}
	identity.MSISDNs = cleanNumbers(modem.SIM.MSISDNs)
	if len(identity.MSISDNs) == 1 {
		identity.MSISDN = identity.MSISDNs[0]
	}
	if validNumber(modem.SIM.SMSC) {
		identity.SMSC = strings.TrimSpace(modem.SIM.SMSC)
	}
	return identity
}

func identityComplete(identity ObservedIdentity) bool {
	return validDigits(identity.IMSI, 5, 18) && validDigits(identity.MCC, 3, 3) && validDigits(identity.MNC, 2, 3)
}

func cleanNumbers(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if !validNumber(value) {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func validNumber(value string) bool {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "+") {
		value = strings.TrimPrefix(value, "+")
	}
	return validDigits(value, 1, 32)
}

func validDigits(value string, minimum, maximum int) bool {
	if len(value) < minimum || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
