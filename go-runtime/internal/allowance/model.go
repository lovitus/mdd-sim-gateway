// Package allowance persists administrator-maintained balance/allowance data
// and correlates only explicitly requested carrier SMS replies. It never sends
// or retries an SMS itself.
package allowance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	SchemaVersion              = 1
	QueryWindow                = 120 * time.Second
	MaximumDispatchUncertainty = 150 * time.Second
	MaximumValueRunes          = 160
	MaximumRuleBodyRunes       = 500
	MaximumReplyBytes          = 256 << 10
	MaximumWindowRecords       = 500
)

const (
	ParserNone      = "none"
	ParserUltraV1   = "ultramobile_v1"
	ParserCTExcelV1 = "ctexcel_v1"
	SourceManual    = "manual"
	SourceSMS       = "sms"
	QueryPrepared   = "prepared"
	QuerySent       = "sent"
	QueryClosed     = "closed"
	QueryStale      = "stale"
	QueryReplied    = "replied"
)

var (
	ErrRevision            = errors.New("allowance revision does not match")
	ErrQueryConflict       = errors.New("allowance query identity conflict")
	ErrQueryActive         = errors.New("allowance query or correlation quarantine is active")
	ErrQueryChanged        = errors.New("allowance query changed")
	ErrRouteUnavailable    = errors.New("allowance message route is unavailable")
	ErrRuleUnavailable     = errors.New("allowance query rule is not configured")
	ErrReplyWindowTooLarge = errors.New("allowance reply window is too large")
	serviceNumber          = regexp.MustCompile(`^\+?[0-9]{1,32}$`)
)

type Values struct {
	Balance        string `json:"balance"`
	SMSRemaining   string `json:"sms_remaining"`
	DataRemaining  string `json:"data_remaining"`
	VoiceRemaining string `json:"voice_remaining"`
	ValidUntil     string `json:"valid_until"`
	ActivatedAt    string `json:"activated_at"`
}

type Snapshot struct {
	SchemaVersion int       `json:"schema_version"`
	LineID        string    `json:"line_id"`
	Revision      uint64    `json:"revision"`
	Values        Values    `json:"values"`
	Source        string    `json:"source,omitempty"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
}

type QueryRule struct {
	SchemaVersion int       `json:"schema_version"`
	LineID        string    `json:"line_id"`
	Revision      uint64    `json:"revision"`
	Recipient     string    `json:"recipient,omitempty"`
	Body          string    `json:"body,omitempty"`
	Parser        string    `json:"parser"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
}

type Query struct {
	SchemaVersion        int       `json:"schema_version"`
	QueryID              string    `json:"query_id"`
	RequestSHA256        string    `json:"request_sha256"`
	LineID               string    `json:"line_id"`
	ExpectedCardID       string    `json:"expected_card_id"`
	Transport            string    `json:"transport"`
	RuleRevision         uint64    `json:"rule_revision"`
	Recipient            string    `json:"recipient"`
	Body                 string    `json:"body"`
	Parser               string    `json:"parser"`
	OperationID          string    `json:"operation_id"`
	MessageID            string    `json:"message_id"`
	State                string    `json:"state"`
	CreatedAt            time.Time `json:"created_at"`
	DispatchAuthorizedAt time.Time `json:"dispatch_authorized_at,omitempty"`
	SentAt               time.Time `json:"sent_at,omitempty"`
	CorrelationUntil     time.Time `json:"correlation_until"`
	ReplyCount           int       `json:"reply_count"`
	ReplyCode            string    `json:"reply_code,omitempty"`
}

type QueryRequest struct {
	QueryID        string `json:"query_id"`
	ExpectedCardID string `json:"expected_card_id"`
	Transport      string `json:"transport"`
}

type Dispatch struct {
	Method string         `json:"method"`
	Path   string         `json:"path"`
	Body   map[string]any `json:"body"`
}

type Reply struct {
	EventID    string    `json:"event_id"`
	Sender     string    `json:"sender"`
	Body       string    `json:"body"`
	ObservedAt time.Time `json:"observed_at"`
	ReceivedAt time.Time `json:"received_at"`
}

type QueryView struct {
	Query    *Query    `json:"query,omitempty"`
	Dispatch *Dispatch `json:"dispatch,omitempty"`
	Replies  []Reply   `json:"replies"`
	Expired  bool      `json:"expired"`
	Code     string    `json:"code,omitempty"`
}

func defaultSnapshot(lineID string) Snapshot {
	return Snapshot{SchemaVersion: SchemaVersion, LineID: lineID, Revision: 1}
}

func defaultRule(lineID string) QueryRule {
	return QueryRule{SchemaVersion: SchemaVersion, LineID: lineID, Revision: 1, Parser: ParserNone}
}

func cleanValues(input Values) (Values, error) {
	values := Values{
		Balance: strings.TrimSpace(input.Balance), SMSRemaining: strings.TrimSpace(input.SMSRemaining),
		DataRemaining: strings.TrimSpace(input.DataRemaining), VoiceRemaining: strings.TrimSpace(input.VoiceRemaining),
		ValidUntil: strings.TrimSpace(input.ValidUntil), ActivatedAt: strings.TrimSpace(input.ActivatedAt),
	}
	for _, value := range []string{values.Balance, values.SMSRemaining, values.DataRemaining,
		values.VoiceRemaining, values.ValidUntil, values.ActivatedAt} {
		if utf8.RuneCountInString(value) > MaximumValueRunes {
			return Values{}, errors.New("allowance value is too long")
		}
	}
	if values.ActivatedAt != "" {
		parsed, err := time.Parse("2006-01-02", values.ActivatedAt)
		if err != nil || parsed.Format("2006-01-02") != values.ActivatedAt {
			return Values{}, errors.New("activated_at must use YYYY-MM-DD")
		}
	}
	return values, nil
}

func cleanRule(lineID string, input QueryRule) (QueryRule, error) {
	rule := defaultRule(lineID)
	rule.Recipient = strings.TrimSpace(input.Recipient)
	rule.Body = strings.TrimSpace(input.Body)
	rule.Parser = strings.TrimSpace(input.Parser)
	if rule.Parser == "" {
		rule.Parser = ParserNone
	}
	if !serviceNumber.MatchString(rule.Recipient) || rule.Body == "" ||
		utf8.RuneCountInString(rule.Body) > MaximumRuleBodyRunes || !validParser(rule.Parser) {
		return QueryRule{}, errors.New("invalid allowance query rule")
	}
	return rule, nil
}

func validParser(value string) bool {
	return value == ParserNone || value == ParserUltraV1 || value == ParserCTExcelV1
}

func validID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 200 {
		return false
	}
	for _, character := range value {
		if character <= 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validCardID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 4 || len(value) > 32 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func queryRequestHash(lineID string, request QueryRequest) (string, error) {
	request.QueryID = strings.TrimSpace(request.QueryID)
	request.ExpectedCardID = strings.TrimSpace(request.ExpectedCardID)
	request.Transport = strings.TrimSpace(strings.ToLower(request.Transport))
	if !validID(lineID) || !validID(request.QueryID) || !validCardID(request.ExpectedCardID) ||
		(request.Transport != "vowifi" && request.Transport != "cellular") {
		return "", errors.New("invalid allowance query request")
	}
	wire, err := json.Marshal([]string{lineID, request.QueryID, request.ExpectedCardID, request.Transport})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(wire)
	return hex.EncodeToString(digest[:]), nil
}

func sameValues(left, right Values) bool { return left == right }

func sameRule(left, right QueryRule) bool {
	return left.LineID == right.LineID && left.Recipient == right.Recipient &&
		left.Body == right.Body && left.Parser == right.Parser
}

func queryFenceEqual(left, right Query) bool {
	return left.QueryID == right.QueryID && left.RequestSHA256 == right.RequestSHA256 &&
		left.LineID == right.LineID && left.ExpectedCardID == right.ExpectedCardID &&
		left.Transport == right.Transport && left.OperationID == right.OperationID &&
		left.MessageID == right.MessageID && left.State == right.State && left.SentAt.Equal(right.SentAt) &&
		left.DispatchAuthorizedAt.Equal(right.DispatchAuthorizedAt) &&
		left.CorrelationUntil.Equal(right.CorrelationUntil) && left.RuleRevision == right.RuleRevision &&
		left.Recipient == right.Recipient && left.Body == right.Body && left.Parser == right.Parser &&
		left.ReplyCount == right.ReplyCount && left.ReplyCode == right.ReplyCode
}
