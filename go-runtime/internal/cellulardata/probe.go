package cellulardata

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressprobe"
)

// ProbeCard borrows only a new bounded lease. Existing borrowers are never
// reused or stopped, and Agent policy requires both saved switches to be on.
func (service *Service) ProbeCard(ctx context.Context, cardID string) (result egressprobe.Result, err error) {
	lineID, err := service.lineForCard(cardID)
	if err != nil {
		return result, err
	}
	operation, err := randomID("probe")
	if err != nil {
		return result, err
	}
	current, err := service.create(ctx, lineID, "probe:"+operation, "", time.Minute, 64<<10)
	if err != nil {
		return result, err
	}
	defer func() {
		if stopErr := current.stop("probe_complete"); stopErr != nil {
			err = errors.Join(err, fmt.Errorf("cellular data probe cleanup unconfirmed: %w", stopErr))
		}
	}()
	probeContext, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	return egressprobe.ProbeDatagram(probeContext, current.dial)
}
