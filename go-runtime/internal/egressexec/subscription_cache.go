package egressexec

// Cache behavior is ported from ec620942 host/mdd_orchestrator.py:1854-1877:
// a feed outage preserves the last usable document. Invalid responses are not
// allowed to replace it, and the URL is never included in public errors.

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
)

type subscriptionCache struct {
	root     string
	client   *http.Client
	next     map[string]time.Time
	failures map[string]uint
}

func (cache *subscriptionCache) load(ctx context.Context, profile egressconfig.Profile, now time.Time) ([]subscriptionNode, error) {
	parsed, err := url.Parse(profile.URL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return nil, errors.New("subscription URL must be HTTP or HTTPS")
	}
	if cache.next == nil {
		cache.next = map[string]time.Time{}
		cache.failures = map[string]uint{}
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(profile.URL)))
	path := filepath.Join(cache.root, "subscription-"+key+".yaml")
	var cached []subscriptionNode
	var modified time.Time
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Size() <= 8<<20 {
		if payload, err := os.ReadFile(path); err == nil {
			cached, _ = parseSubscription(payload)
			modified = info.ModTime()
		}
	}
	refresh := time.Duration(profile.RefreshMinutes) * time.Minute
	if refresh < time.Minute {
		refresh = 30 * time.Minute
	}
	if len(cached) > 0 && now.Before(modified.Add(refresh)) {
		return cached, nil
	}
	if now.Before(cache.next[key]) {
		if len(cached) > 0 {
			return cached, nil
		}
		return nil, errors.New("subscription refresh is in backoff; no usable cache")
	}
	shift := cache.failures[key]
	if shift > 5 {
		shift = 5
	}
	delay := 30 * time.Second * time.Duration(1<<shift)
	if delay > 10*time.Minute {
		delay = 10 * time.Minute
	}
	cache.next[key] = now.Add(delay)
	cache.failures[key]++
	client := cache.client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, profile.URL, nil)
	if err != nil {
		return nil, errors.New("subscription request is invalid")
	}
	request.Header.Set("User-Agent", "mdd-sim-gateway/1")
	response, fetchErr := client.Do(request)
	var payload []byte
	if fetchErr == nil {
		payload, fetchErr = io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			fetchErr = errors.New("subscription HTTP response failed")
		}
	}
	var nodes []subscriptionNode
	if fetchErr == nil {
		nodes, fetchErr = parseSubscription(payload)
	}
	if fetchErr == nil {
		usable := false
		for _, node := range nodes {
			if node.supportsUDP() {
				usable = true
				break
			}
		}
		if !usable {
			fetchErr = errors.New("subscription contains no supported UDP nodes")
		}
	}
	if fetchErr != nil {
		if len(cached) > 0 {
			return cached, nil
		}
		return nil, errors.New("subscription fetch or parsing failed; no usable cache")
	}
	if err := atomicWrite(path, payload, 0600); err != nil {
		return nil, errors.New("subscription cache could not be saved")
	}
	cache.next[key] = now.Add(refresh)
	cache.failures[key] = 0
	return nodes, nil
}

func (runner *executor) subscriptions(ctx context.Context, config egressconfig.Config) (map[string][]subscriptionNode, error) {
	feeds := map[string][]subscriptionNode{}
	if !config.Enabled {
		return feeds, nil
	}
	if runner.feedCache == nil {
		runner.feedCache = &subscriptionCache{root: runner.settings.StateDir}
	}
	for _, country := range sortedCountries(config.Exits) {
		exit := config.Exits[country]
		profile := config.Profiles[exit.ProfileID]
		if !exit.Enabled || exit.Mode == "direct" || profile.Type != "subscription" {
			continue
		}
		if _, loaded := feeds[exit.ProfileID]; loaded {
			continue
		}
		nodes, err := runner.feedCache.load(ctx, profile, runner.now())
		if err != nil {
			return nil, err
		}
		feeds[exit.ProfileID] = nodes
	}
	return feeds, nil
}
