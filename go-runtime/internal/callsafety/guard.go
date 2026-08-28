package callsafety

import "time"

type Phase string

const (
	PhasePreparing Phase = "preparing"
	PhaseDialing   Phase = "dialing"
	PhaseRinging   Phase = "ringing"
	PhaseActive    Phase = "active"
	PhaseEnding    Phase = "ending"
	PhaseEnded     Phase = "ended"
)

type Action string

const (
	ActionNone        Action = "none"
	ActionHangupExact Action = "hangup_exact_call"
)

type Call struct {
	ID               string
	Phase            Phase
	BrowserLastSeen  time.Time
	BrowserConnected bool
}

type Decision struct {
	Action Action
	CallID string
	Reason string
}

type Guard struct {
	HeartbeatTimeout time.Duration
}

func (g Guard) Evaluate(call Call, now time.Time) Decision {
	if call.ID == "" || g.HeartbeatTimeout <= 0 ||
		call.Phase == PhasePreparing || call.Phase == PhaseEnding || call.Phase == PhaseEnded {
		return Decision{Action: ActionNone}
	}
	if call.BrowserConnected && !call.BrowserLastSeen.IsZero() &&
		now.Sub(call.BrowserLastSeen) <= g.HeartbeatTimeout {
		return Decision{Action: ActionNone}
	}
	if call.BrowserLastSeen.IsZero() || now.Sub(call.BrowserLastSeen) > g.HeartbeatTimeout {
		return Decision{Action: ActionHangupExact, CallID: call.ID,
			Reason: "browser heartbeat absent"}
	}
	return Decision{Action: ActionNone}
}
