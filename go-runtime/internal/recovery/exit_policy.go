package recovery

// Ported from control/app/failover.py at ec620942e93edbbb567398acda4c0dffe1d8f375.
// This policy makes no network or lifecycle calls. In particular a policy
// decision is not authority to change a shared country exit.

import (
	"errors"
	"slices"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

const (
	StrikesPerNode        = 3
	FailuresBeforeReport  = 6
	SuspiciousRetransmits = 6
	ExhaustedRetry        = time.Hour
)

type ExitVerdict string

const (
	BlamesExit      ExitVerdict = "exit"
	BlamesElsewhere ExitVerdict = "elsewhere"
	ExitUnclear     ExitVerdict = "unclear"
)

type ExitAction string

const (
	ExitHold    ExitAction = "hold"
	ExitSwitch  ExitAction = "switch"
	ExitBackOff ExitAction = "back-off"
	ExitGiveUp  ExitAction = "give-up"
	ExitReport  ExitAction = "report"
	ExitPace    ExitAction = "pace"
)

type ExitLedger struct {
	LastManualRetryID string         `json:"last_manual_retry_id,omitempty"`
	Notice            *ExitNotice    `json:"-"`
	RetryAfter        time.Time      `json:"retry_after,omitempty"`
	Selection         *ExitSelection `json:"selection,omitempty"`
	LastSequence      uint64         `json:"last_sequence,omitempty"`
	LastDecision      ExitAction     `json:"last_decision,omitempty"`
	LastFailureID     string         `json:"last_failure_id,omitempty"`
	Node              string         `json:"node"`
	Strikes           int            `json:"strikes"`
	Tried             []string       `json:"tried"`
	Failures          int            `json:"failures"`
	GivenUp           bool           `json:"given_up"`
	Exhausted         bool           `json:"exhausted"`
	HeldForPeer       bool           `json:"held_for_peer"`
	Reported          bool           `json:"reported"`
	CampaignEpoch     string         `json:"campaign_epoch"`
	SampleGeneration  string         `json:"sample_generation"`
	StableCardKey     string         `json:"stable_card_key"`
	LineConfigEpoch   string         `json:"line_config_epoch"`
}

// ExitNotice is published atomically with the decision by the existing event
// store. It is an outbox value, not part of the persisted policy ledger.
type ExitNotice struct {
	ID         string    `json:"id"`
	LineID     string    `json:"line_id"`
	LineName   string    `json:"line_name"`
	CardID     string    `json:"card_id"`
	MSISDN     string    `json:"msisdn,omitempty"`
	Text       string    `json:"text"`
	OccurredAt time.Time `json:"occurred_at"`
}

type ExitSelection struct {
	AttentionRecorded bool                         `json:"attention_recorded,omitempty"`
	Request           egressconfig.RecoveryRequest `json:"request"`
	State             string                       `json:"state"`
	Attempts          int                          `json:"attempts"`
	NextAttempt       time.Time                    `json:"next_attempt,omitempty"`
	Code              string                       `json:"code,omitempty"`
}

func (selection *ExitSelection) Pending() bool {
	return selection != nil && (selection.State == "pending" || selection.State == "unknown")
}

type ExitCampaign struct {
	Epoch, SampleGeneration, StableCardKey, LineConfigEpoch string
	ControlledRebuild                                       bool
}

func BeginExitCampaign(current ExitLedger, campaign ExitCampaign) (ExitLedger, error) {
	if campaign.Epoch == "" || campaign.SampleGeneration == "" || campaign.StableCardKey == "" || campaign.LineConfigEpoch == "" {
		return current, errors.New("recovery campaign identity is incomplete")
	}
	same := current.CampaignEpoch == campaign.Epoch && current.StableCardKey == campaign.StableCardKey && current.LineConfigEpoch == campaign.LineConfigEpoch
	if !same {
		current = ExitLedger{LastManualRetryID: current.LastManualRetryID}
	} else if current.SampleGeneration != "" && current.SampleGeneration != campaign.SampleGeneration && !campaign.ControlledRebuild {
		return current, nil
	}
	current.CampaignEpoch = campaign.Epoch
	current.SampleGeneration = campaign.SampleGeneration
	current.StableCardKey = campaign.StableCardKey
	current.LineConfigEpoch = campaign.LineConfigEpoch
	current.Tried = slices.Clone(current.Tried)
	return current, nil
}

// Includes main.py:_plan_exit_failure's evidence/continuity guards, not only
// failover.py:classify. Missing counters are not zero, and a newly established
// tunnel without a healthy stretch does not attribute an IMS failure.
func ClassifyExit(tunnelConnected *bool, retransmits *int, stableFor, stableThreshold time.Duration) ExitVerdict {
	if tunnelConnected == nil || retransmits == nil || *retransmits < 0 {
		return ExitUnclear
	}
	if stableThreshold > 0 && stableFor >= stableThreshold {
		return BlamesElsewhere
	}
	if !*tunnelConnected {
		return BlamesExit
	}
	if *retransmits >= SuspiciousRetransmits || stableFor <= 0 {
		return ExitUnclear
	}
	return BlamesElsewhere
}

// Adapt the old disconnected-SWu rule to MDD's actual single-send transport.
// Only a completed failed bootstrap whose every sent request timed out is an
// exit-path candidate. This never treats candidate attempts as retransmits,
// and does not blame a path for authentication, dial or local cleanup errors.
func ClassifyProviderFailure(snapshot vowifiipc.Snapshot, stableFor, stableThreshold time.Duration) ExitVerdict {
	if snapshot.Validate() != nil || snapshot.Runtime.Condition != vowifiipc.RuntimeFailed || snapshot.Runtime.FailureID == "" {
		return ExitUnclear
	}
	ike := snapshot.Runtime.IKE
	if ike == nil || ike.RequestsSent == 0 || ike.ResponseDatagrams != 0 || ike.ResponseTimeouts != ike.RequestsSent ||
		snapshot.Runtime.Code != "swu_open_failed" || snapshot.Tunnel.Condition != vowifiipc.LayerBlocked || snapshot.Tunnel.Code != "swu_open_failed" {
		return ExitUnclear
	}
	if stableThreshold > 0 && stableFor >= stableThreshold {
		return BlamesElsewhere
	}
	return BlamesExit
}

type ExitFailure struct {
	Verdict                                                   ExitVerdict
	Node                                                      string
	Pinned                                                    bool
	Candidates                                                []string
	PeerRegistered                                            bool
	CampaignEpoch, SampleGeneration, ExpectedSampleGeneration string
	ControlledRebuild                                         bool
}

// RecordExitFailureOnce is the entry point for monotonically accepted,
// generation-fenced Provider observations. The caller supplies Runtime.FailureID,
// never the polling sequence. Unknown evidence remains reconsiderable when the
// required facts arrive; only a counted observation consumes its identity.
func RecordExitFailureOnce(current ExitLedger, failure ExitFailure, failureID string) (ExitAction, ExitLedger) {
	if failureID == "" || failureID == current.LastFailureID {
		current.Tried = slices.Clone(current.Tried)
		return ExitHold, current
	}
	action, next := RecordExitFailure(current, failure)
	if next.Failures > current.Failures {
		next.LastFailureID = failureID
	}
	return action, next
}

func RecordExitFailure(current ExitLedger, failure ExitFailure) (ExitAction, ExitLedger) {
	current.Tried = slices.Clone(current.Tried)
	if failure.Verdict != BlamesExit && failure.Verdict != BlamesElsewhere {
		return ExitHold, current
	}
	if failure.CampaignEpoch == "" || current.CampaignEpoch != failure.CampaignEpoch ||
		failure.ExpectedSampleGeneration == "" || current.SampleGeneration != failure.ExpectedSampleGeneration {
		return ExitHold, current
	}
	if failure.SampleGeneration != failure.ExpectedSampleGeneration {
		if !failure.ControlledRebuild || failure.SampleGeneration == "" {
			return ExitHold, current
		}
		current.SampleGeneration = failure.SampleGeneration
	}
	current.Failures++
	if current.GivenUp {
		if failure.Pinned {
			return ExitHold, current
		}
		current.GivenUp = false
	}
	if failure.Verdict != BlamesExit {
		if current.Exhausted {
			current.Exhausted = false
			current.Tried = nil
			current.Strikes = 0
		}
		current.HeldForPeer = false
		return reportExitFailure(current)
	}
	if failure.PeerRegistered {
		current.HeldForPeer = true
		return reportExitFailure(current)
	}
	current.HeldForPeer = false
	if current.Exhausted {
		return ExitBackOff, current
	}
	if failure.Node != current.Node {
		current.Node, current.Strikes = failure.Node, 0
	}
	current.Strikes++
	if current.Strikes < StrikesPerNode {
		return ExitHold, current
	}
	if failure.Node != "" && !slices.Contains(current.Tried, failure.Node) {
		current.Tried = append(current.Tried, failure.Node)
	}
	if failure.Pinned {
		current.GivenUp = true
		return ExitGiveUp, current
	}
	remaining := false
	for _, node := range failure.Candidates {
		if node != "" && !slices.Contains(current.Tried, node) {
			remaining = true
			break
		}
	}
	if !remaining {
		current.Exhausted = true
		return ExitBackOff, current
	}
	current.Strikes = 0
	return ExitSwitch, current
}

func reportExitFailure(current ExitLedger) (ExitAction, ExitLedger) {
	if current.Reported {
		return ExitPace, current
	}
	if current.Failures >= FailuresBeforeReport {
		current.Reported = true
		return ExitReport, current
	}
	return ExitHold, current
}
