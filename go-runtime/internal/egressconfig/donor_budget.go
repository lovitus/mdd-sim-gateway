package egressconfig

import (
	"encoding/json"
	"errors"
	"time"

	bolt "go.etcd.io/bbolt"
)

const DefaultDonorLimit = uint64(1 << 40)

var donorBucket = []byte("donor_budgets_v1")

var (
	ErrDonorOwner     = errors.New("donor still has an active or unconfirmed owner")
	ErrDonorCommitted = errors.New("stop borrowing before lowering the limit below committed usage and reservation")
	ErrDonorExhausted = errors.New("donor budget exhausted")
)

type DonorGrant struct {
	SessionID string          `json:"session_id"`
	LineID    string          `json:"line_id"`
	Purpose   string          `json:"purpose"`
	Target    json.RawMessage `json:"target"`
	MaxBytes  uint64          `json:"max_bytes"`
	UsedBytes uint64          `json:"used_bytes"`
}

// A grant is reserved before Agent admission. Unknown remote stops retain the
// reservation. After a Core crash, unobserved bytes remain conservatively
// charged, separately from measured usage, even after the owner is stopped.
type DonorBudget struct {
	CardID         string      `json:"card_id"`
	Revision       uint64      `json:"revision"`
	LimitBytes     uint64      `json:"limit_bytes"`
	UsedBytes      uint64      `json:"used_bytes"`
	UncertainBytes uint64      `json:"uncertain_bytes"`
	StartedAt      time.Time   `json:"started_at"`
	Configured     bool        `json:"configured"`
	Grant          *DonorGrant `json:"-"`
}

type storedDonorBudget struct {
	DonorBudget
	Reservation *DonorGrant `json:"reservation,omitempty"`
}

func validDonorCard(card string) bool {
	if len(card) < 18 || len(card) > 22 {
		return false
	}
	for _, c := range card {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func loadDonor(bucket *bolt.Bucket, card string) (DonorBudget, error) {
	budget := DonorBudget{CardID: card, Revision: 1, LimitBytes: DefaultDonorLimit}
	if bucket == nil || bucket.Get([]byte(card)) == nil {
		return budget, nil
	}
	var stored storedDonorBudget
	if json.Unmarshal(bucket.Get([]byte(card)), &stored) != nil {
		return budget, errors.New("donor budget is corrupt")
	}
	budget = stored.DonorBudget
	budget.Grant = stored.Reservation
	if budget.CardID != card || budget.Revision == 0 || budget.LimitBytes > DefaultDonorLimit ||
		budget.UsedBytes > budget.LimitBytes || budget.UncertainBytes > budget.LimitBytes-budget.UsedBytes {
		return budget, errors.New("donor budget is invalid")
	}
	if g := budget.Grant; g != nil && (g.SessionID == "" || g.MaxBytes < g.UsedBytes ||
		g.MaxBytes-g.UsedBytes > budget.LimitBytes-budget.UsedBytes-budget.UncertainBytes) {
		return budget, errors.New("donor reservation is invalid")
	}
	return budget, nil
}

func (store *Store) DonorBudget(card string) (DonorBudget, error) {
	if !validDonorCard(card) {
		return DonorBudget{}, errors.New("invalid donor ICCID")
	}
	var budget DonorBudget
	err := store.db.View(func(tx *bolt.Tx) (err error) { budget, err = loadDonor(tx.Bucket(donorBucket), card); return err })
	return budget, err
}

func (store *Store) updateDonor(card string, change func(*DonorBudget) error) (DonorBudget, error) {
	if !validDonorCard(card) {
		return DonorBudget{}, errors.New("invalid donor ICCID")
	}
	var budget DonorBudget
	err := store.db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(donorBucket)
		if err != nil {
			return err
		}
		budget, err = loadDonor(bucket, card)
		if err != nil {
			return err
		}
		if err := change(&budget); err != nil {
			return err
		}
		if budget.StartedAt.IsZero() {
			budget.StartedAt = time.Now().UTC()
		}
		payload, err := json.Marshal(storedDonorBudget{DonorBudget: budget, Reservation: budget.Grant})
		if err != nil {
			return err
		}
		return bucket.Put([]byte(card), payload)
	})
	return budget, err
}

func (store *Store) ConfigureDonor(card string, revision, limit uint64, reset bool) (DonorBudget, error) {
	if limit > DefaultDonorLimit {
		return DonorBudget{}, errors.New("donor limit exceeds 1 TiB")
	}
	return store.updateDonor(card, func(b *DonorBudget) error {
		if b.Revision != revision {
			return ErrRevision
		}
		if reset && b.Grant != nil {
			return ErrDonorOwner
		}
		committed := b.UsedBytes + b.UncertainBytes
		if b.Grant != nil {
			committed += b.Grant.MaxBytes - b.Grant.UsedBytes
		}
		if !reset && limit < committed {
			return ErrDonorCommitted
		}
		if reset {
			b.UsedBytes, b.UncertainBytes = 0, 0
			b.StartedAt = time.Now().UTC()
		}
		b.LimitBytes, b.Configured = limit, true
		b.Revision++
		return nil
	})
}

func (store *Store) ReserveDonor(card string, grant DonorGrant) (DonorBudget, error) {
	if grant.SessionID == "" || grant.LineID == "" || len(grant.Target) == 0 || grant.MaxBytes < 1024 || grant.MaxBytes > DefaultDonorLimit {
		return DonorBudget{}, errors.New("invalid donor reservation")
	}
	return store.updateDonor(card, func(b *DonorBudget) error {
		if b.Grant != nil {
			return ErrDonorOwner
		}
		remaining := b.LimitBytes - b.UsedBytes - b.UncertainBytes
		if remaining < 1024 {
			return ErrDonorExhausted
		}
		if grant.MaxBytes > remaining {
			grant.MaxBytes = remaining
		}
		grant.UsedBytes = 0
		b.Grant = &grant
		return nil
	})
}

func (store *Store) RecordDonor(card, session string, used uint64, stopped, interrupted bool) error {
	_, err := store.updateDonor(card, func(b *DonorBudget) error {
		g := b.Grant
		if g == nil || g.SessionID != session {
			return errors.New("donor reservation changed")
		}
		if used < g.UsedBytes || used > g.MaxBytes {
			return errors.New("donor usage is not monotonic")
		}
		b.UsedBytes += used - g.UsedBytes
		g.UsedBytes = used
		if stopped {
			if interrupted {
				b.UncertainBytes += g.MaxBytes - used
			}
			b.Grant = nil
		}
		return nil
	})
	return err
}
