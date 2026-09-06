package agentmodem

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/recovery"
)

type Worker struct {
	Prober      Prober
	PINs        PINRecoverer
	Policies    PolicyReconciler
	Coordinator BackgroundScanCoordinator
	Interval    time.Duration
	Recovery    recovery.Policy
	Observed    func(Observation)
}

func (worker Worker) Run(ctx context.Context) error {
	if worker.Prober == nil || worker.Interval <= 0 || worker.Observed == nil {
		return errors.New("invalid modem monitor configuration")
	}
	if _, err := worker.Recovery.Decide(recovery.Failure{Attempt: 1, Recoverable: true}); err != nil {
		return err
	}
	worker.Observed(Observation{Condition: ConditionStarting})
	attempt := 0
	for {
		wait := worker.Interval
		var fatal error
		cycle := func(cycleContext context.Context) error {
			facts, err := worker.Prober.Probe(cycleContext)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err == nil && worker.PINs != nil {
				err = worker.PINs.RecoverPINs(cycleContext, facts)
			}
			if err == nil && worker.Policies != nil {
				worker.Policies.ReconcilePolicies(cycleContext, facts)
			}
			if cycleContext.Err() != nil {
				return cycleContext.Err()
			}
			if err == nil {
				attempt = 0
				worker.Observed(Observation{Condition: ConditionReady, Modems: facts})
				return nil
			}
			attempt++
			decision, policyErr := worker.Recovery.Decide(recovery.Failure{Attempt: attempt, Recoverable: true, Action: recovery.ActionRetry})
			if policyErr != nil || !decision.Retry {
				fatal = errors.Join(err, policyErr)
				return nil
			}
			wait = decision.After
			worker.Observed(Observation{Condition: ConditionRecovering, Detail: boundedDetail(err)})
			return nil
		}
		var coordinateErr error
		if worker.Coordinator != nil {
			coordinateErr = worker.Coordinator.DoBackgroundScan(ctx, cycle)
		} else {
			coordinateErr = cycle(ctx)
		}
		if fatal != nil {
			return fatal
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if coordinateErr != nil {
			attempt++
			decision, policyErr := worker.Recovery.Decide(recovery.Failure{Attempt: attempt, Recoverable: true, Action: recovery.ActionRetry})
			if policyErr != nil || !decision.Retry {
				return errors.Join(coordinateErr, policyErr)
			}
			wait = decision.After
			worker.Observed(Observation{Condition: ConditionRecovering, Detail: boundedDetail(coordinateErr)})
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func boundedDetail(err error) string {
	detail := strings.ToValidUTF8(err.Error(), "?")
	if len(detail) > 1024 {
		detail = strings.ToValidUTF8(detail[:1024], "?")
	}
	return detail
}
