package systemupdate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"
)

const cacheTTL = 5 * time.Minute

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

type Release struct {
	OK              bool      `json:"ok"`
	Current         string    `json:"current"`
	Repository      string    `json:"repository"`
	Latest          string    `json:"latest,omitempty"`
	UpdateAvailable bool      `json:"update_available"`
	ComparisonKnown bool      `json:"comparison_known"`
	ReleaseURL      string    `json:"release_url,omitempty"`
	PublishedAt     string    `json:"published_at,omitempty"`
	Notes           string    `json:"notes,omitempty"`
	CheckedAt       time.Time `json:"checked_at"`
	Error           string    `json:"error,omitempty"`
	ErrorCode       string    `json:"error_code,omitempty"`
}

type Checker struct {
	repository, current string
	client              *http.Client
	mu                  sync.Mutex
	cached              Release
	cachedAt            time.Time
}

func NewChecker(repository, current string, client *http.Client) (*Checker, error) {
	repository, current = strings.TrimSpace(repository), strings.TrimSpace(current)
	if !repositoryPattern.MatchString(repository) || current == "" {
		return nil, errors.New("invalid update checker configuration")
	}
	if client == nil {
		client = &http.Client{Timeout: 12 * time.Second}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Checker{repository: repository, current: current, client: &copyClient}, nil
}

func (checker *Checker) Check(ctx context.Context, force bool) Release {
	checker.mu.Lock()
	if !force && !checker.cachedAt.IsZero() && time.Since(checker.cachedAt) < cacheTTL {
		value := checker.cached
		checker.mu.Unlock()
		return value
	}
	checker.mu.Unlock()
	result := Release{Current: checker.current, Repository: checker.repository, CheckedAt: time.Now().UTC()}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+checker.repository+"/releases/latest", nil)
	if err == nil {
		request.Header.Set("Accept", "application/vnd.github+json")
		request.Header.Set("User-Agent", "mdd-sim-gateway/"+checker.current)
		request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	if err != nil {
		result.Error, result.ErrorCode = "update request could not be created", "update.error.request"
	} else if response, requestErr := checker.client.Do(request); requestErr != nil {
		result.Error, result.ErrorCode = "update service unavailable", "update.error.unavailable"
	} else {
		defer response.Body.Close()
		var payload struct {
			Tag       string `json:"tag_name"`
			URL       string `json:"html_url"`
			Published string `json:"published_at"`
			Body      string `json:"body"`
		}
		decodeErr := json.NewDecoder(response.Body).Decode(&payload)
		if response.StatusCode != http.StatusOK || decodeErr != nil || strings.TrimSpace(payload.Tag) == "" {
			result.Error, result.ErrorCode = "no release is available", "update.error.no_release"
		} else {
			result.OK = true
			result.Latest = strings.TrimPrefix(payload.Tag, "v")
			result.ComparisonKnown = releaseVersion(result.Current) != "" && releaseVersion(result.Latest) != ""
			if !result.ComparisonKnown {
				result.ErrorCode = "update.version_uncomparable"
			}
			result.UpdateAvailable = newer(result.Current, result.Latest)
			result.ReleaseURL, result.PublishedAt, result.Notes = payload.URL, payload.Published, truncate(payload.Body, 4000)
		}
	}
	checker.mu.Lock()
	checker.cached, checker.cachedAt = result, time.Now()
	checker.mu.Unlock()
	return result
}

func newer(current, latest string) bool {
	left, right := releaseVersion(current), releaseVersion(latest)
	return left != "" && right != "" && semver.Compare(right, left) > 0
}

func releaseVersion(value string) string {
	value = "v" + strings.TrimPrefix(strings.TrimSpace(value), "v")
	// Release tags use all three components. In particular, an all-digit
	// abbreviated commit must not be interpreted as a semver major version.
	base := strings.SplitN(strings.SplitN(value, "+", 2)[0], "-", 2)[0]
	if strings.Count(base, ".") != 2 || !semver.IsValid(value) {
		return ""
	}
	return value
}
func truncate(value string, maximum int) string {
	if len(value) > maximum {
		return value[:maximum]
	}
	return value
}

type Handler struct {
	checker *Checker
	store   *Store
}

func NewHandler(checker *Checker, stores ...*Store) (*Handler, error) {
	if checker == nil {
		return nil, errors.New("update checker is required")
	}
	if len(stores) > 1 {
		return nil, errors.New("only one update store is allowed")
	}
	var store *Store
	if len(stores) == 1 {
		store = stores[0]
	}
	return &Handler{checker: checker, store: store}, nil
}
func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	if request.Method == http.MethodGet && request.URL.Path == "/v1/system/update/progress" {
		if handler.store == nil {
			response.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(response).Encode(map[string]string{"code": "update_status_unavailable"})
			return
		}
		status, err := handler.store.Status()
		if err != nil {
			response.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(response).Encode(map[string]string{"code": "update_status_corrupt"})
			return
		}
		response.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(response).Encode(status)
		return
	}
	if request.Method == http.MethodPost && request.URL.Path == "/v1/system/update/apply" {
		if handler.store == nil {
			response.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(response).Encode(map[string]string{"code": "update_executor_unavailable"})
			return
		}
		var input struct {
			ExpectedTarget string `json:"expected_target"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 4096))
		decoder.DisallowUnknownFields()
		if request.URL.RawQuery != "" || decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || releaseVersion(input.ExpectedTarget) == "" {
			response.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(response).Encode(map[string]string{"code": "invalid_update_confirmation"})
			return
		}
		result := handler.checker.Check(request.Context(), true)
		if !result.OK || !result.UpdateAvailable {
			response.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(response).Encode(map[string]string{"code": result.ErrorCode})
			return
		}
		if input.ExpectedTarget != result.Latest {
			response.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(response).Encode(map[string]string{"code": "update_target_changed"})
			return
		}
		now := time.Now().UTC()
		op := "update-" + now.Format("20060102T150405.000000000Z")
		if err := handler.store.Request(Request{SchemaVersion: 1, OperationID: op, Repository: result.Repository, Target: result.Latest, RequestedAt: now}); err != nil {
			response.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(response).Encode(map[string]string{"code": "update_already_in_progress"})
			return
		}
		response.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(response).Encode(map[string]any{"ok": true, "operation_id": op, "target": result.Latest})
		return
	}
	if request.Method != http.MethodGet || request.URL.Path != "/v1/system/update/check" {
		response.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(response).Encode(map[string]string{"code": "method_not_allowed"})
		return
	}
	force := request.URL.Query().Get("force") == "true"
	result := handler.checker.Check(request.Context(), force)
	status := http.StatusOK
	if !result.OK {
		status = http.StatusServiceUnavailable
	}
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(result)
}
