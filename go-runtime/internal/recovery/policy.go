package recovery

import (
	"errors"
	"time"
)

type Action string

const (
	ActionNone           Action = "none"
	ActionProbe          Action = "probe"
	ActionRetry          Action = "retry_same_session"
	ActionReauthenticate Action = "reauthenticate"
	ActionReconnect      Action = "reconnect_transport"
	ActionReopenDevice   Action = "reopen_device"
)

type Policy struct {
	Base time.Duration
	Cap  time.Duration
}

type Failure struct {
	Attempt       int
	Recoverable   bool
	ProviderDelay *time.Duration
	Action        Action
}

type Decision struct {
	Retry  bool
	After  time.Duration
	Action Action
}

var ErrInvalidPolicy = errors.New("invalid recovery policy")

func (p Policy) Decide(failure Failure) (Decision, error) {
	if p.Base <= 0 || p.Cap < p.Base {
		return Decision{}, ErrInvalidPolicy
	}
	if !failure.Recoverable {
		return Decision{Action: ActionNone}, nil
	}
	action := failure.Action
	if action == "" || action == ActionNone {
		action = ActionRetry
	}
	if !validAction(action) {
		return Decision{}, ErrInvalidPolicy
	}
	if failure.ProviderDelay != nil {
		delay := *failure.ProviderDelay
		if delay < 0 {
			return Decision{}, ErrInvalidPolicy
		}
		return Decision{Retry: true, After: delay, Action: action}, nil
	}
	attempt := failure.Attempt
	if attempt < 1 {
		attempt = 1
	}
	delay := p.Base
	for index := 1; index < attempt && delay < p.Cap; index++ {
		if delay > p.Cap/2 {
			delay = p.Cap
			break
		}
		delay *= 2
	}
	if delay > p.Cap {
		delay = p.Cap
	}
	return Decision{Retry: true, After: delay, Action: action}, nil
}

func validAction(action Action) bool {
	switch action {
	case ActionProbe, ActionRetry, ActionReauthenticate, ActionReconnect, ActionReopenDevice:
		return true
	default:
		return false
	}
}
