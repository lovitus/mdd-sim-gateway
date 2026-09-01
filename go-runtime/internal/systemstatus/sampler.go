package systemstatus

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultInterval = 30 * time.Second
	defaultTimeout  = 10 * time.Second
)

type Config struct {
	Context   context.Context
	DataPath  string
	Collector Collector
	Interval  time.Duration
	Timeout   time.Duration
	Now       func() time.Time
}

type Sampler struct {
	collector  Collector
	interval   time.Duration
	timeout    time.Duration
	now        func() time.Time
	ctx        context.Context
	cancel     context.CancelFunc
	results    chan Snapshot
	collecting atomic.Bool

	mu        sync.RWMutex
	current   *Snapshot
	startOnce sync.Once
	closeOnce sync.Once
	wg        sync.WaitGroup
}

func New(config Config) (*Sampler, error) {
	if config.Context == nil {
		return nil, errors.New("system status context is required")
	}
	config.DataPath = filepath.Clean(config.DataPath)
	if !filepath.IsAbs(config.DataPath) || config.DataPath == string(filepath.Separator) {
		return nil, errors.New("system status data path must be absolute and scoped")
	}
	if config.Interval == 0 {
		config.Interval = defaultInterval
	}
	if config.Timeout == 0 {
		config.Timeout = defaultTimeout
	}
	if config.Interval <= 0 || config.Timeout <= 0 || config.Timeout >= config.Interval {
		return nil, errors.New("system status timing is invalid")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Collector == nil {
		config.Collector = newDefaultCollector(config.DataPath)
	}
	ctx, cancel := context.WithCancel(config.Context)
	return &Sampler{
		collector: config.Collector, interval: config.Interval, timeout: config.Timeout,
		now: config.Now, ctx: ctx, cancel: cancel, results: make(chan Snapshot, 1),
	}, nil
}

func (sampler *Sampler) Start() {
	if sampler == nil {
		return
	}
	if sampler.ctx.Err() != nil {
		return
	}
	sampler.startOnce.Do(func() {
		sampler.wg.Add(1)
		go sampler.run()
	})
}

func (sampler *Sampler) Close() {
	if sampler == nil {
		return
	}
	sampler.closeOnce.Do(sampler.cancel)
	sampler.wg.Wait()
}

func (sampler *Sampler) Snapshot(now time.Time) Snapshot {
	if sampler == nil {
		return unavailableSnapshot(defaultInterval)
	}
	sampler.mu.RLock()
	current := sampler.current
	if current == nil {
		sampler.mu.RUnlock()
		return unavailableSnapshot(sampler.interval)
	}
	result := cloneSnapshot(*current)
	sampler.mu.RUnlock()
	result.IntervalSeconds = int(sampler.interval.Seconds())
	if result.SampledAt == nil || now.UTC().Sub(*result.SampledAt) > 2*sampler.interval {
		result.Stale, result.State, result.Code = true, "stale", "status_stale"
	}
	return result
}

func (sampler *Sampler) run() {
	defer sampler.wg.Done()
	select {
	case <-sampler.ctx.Done():
		return
	default:
	}
	sampler.tryCollect()
	ticker := time.NewTicker(sampler.interval)
	defer ticker.Stop()
	for {
		select {
		case <-sampler.ctx.Done():
			return
		case result := <-sampler.results:
			result.SchemaVersion = SchemaVersion
			result.IntervalSeconds = int(sampler.interval.Seconds())
			if result.SampledAt != nil {
				value := result.SampledAt.UTC()
				result.SampledAt = &value
			}
			result.Stale = false
			copy := cloneSnapshot(result)
			sampler.mu.Lock()
			sampler.current = &copy
			sampler.mu.Unlock()
			sampler.collecting.Store(false)
		case <-ticker.C:
			sampler.tryCollect()
		}
	}
}

func (sampler *Sampler) tryCollect() {
	if sampler.ctx.Err() != nil {
		return
	}
	if !sampler.collecting.CompareAndSwap(false, true) {
		return
	}
	if sampler.ctx.Err() != nil {
		sampler.collecting.Store(false)
		return
	}
	collector, timeout, root, results, now := sampler.collector, sampler.timeout, sampler.ctx, sampler.results, sampler.now
	go func() {
		ctx, cancel := context.WithTimeout(root, timeout)
		result := collector.Collect(ctx)
		cancel()
		completed := now().UTC()
		result.SampledAt = &completed
		select {
		case results <- result:
		case <-root.Done():
		}
	}()
}
