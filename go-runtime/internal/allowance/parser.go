package allowance

import (
	"regexp"
	"strings"
)

type parserPattern struct {
	field string
	expr  *regexp.Regexp
	group int
}

// These two conservative parsers are migrated from this repository's GPL-3.0
// the retired allowance parser and retain its fixture-backed field scope.
// No code is copied from VoCat's separately licensed implementation.
var ultraPatterns = []parserPattern{
	{field: "voice_remaining", expr: regexp.MustCompile(`(?i)(本月)?剩余通话时间[：:]\s*([\d.]+\s*(分钟|min(ute)?s?))`), group: 2},
	{field: "sms_remaining", expr: regexp.MustCompile(`(?i)(本月)?剩余短信数[：:]\s*([\d.]+\s*(条|texts?|SMS))`), group: 2},
	{field: "data_remaining", expr: regexp.MustCompile(`(?i)(本月)?剩余流量[：:]\s*([\d.]+\s*(KB|MB|GB|TB))`), group: 2},
	{field: "valid_until", expr: regexp.MustCompile(`(?i)(计划到期日|到期日|有效期)[：:]\s*([^\s\n]+)`), group: 2},
	{field: "balance", expr: regexp.MustCompile(`(?i)(PayGo\s*)?(钱包余额|余额)[：:]\s*([^\s\n]+)`), group: 3},
}

var ctExcelPatterns = []parserPattern{
	{field: "balance", expr: regexp.MustCompile(`(?i)(您?当前余额为|credit\s+balance\s+is)\s*([£$€¥\d.]+)`), group: 2},
	{field: "valid_until", expr: regexp.MustCompile(`有效期至\s*([^\s,，\n]+)`), group: 1},
	{field: "data_remaining", expr: regexp.MustCompile(`你还有[：:]\s*-\s*([\d.]+\s*(GB|MB|KB))`), group: 1},
}

var ctExcelBalanceFallback = regexp.MustCompile(`(?i)(current\s+)?credit\s+balance\s+is\s+([^\s.]+(\.\d+)?)`)

func parseReplies(parser string, replies []Reply) map[string]string {
	if parser == ParserNone || len(replies) == 0 {
		return nil
	}
	parts := make([]string, 0, len(replies))
	for _, reply := range replies {
		parts = append(parts, reply.Body)
	}
	text := strings.Join(parts, "\n")
	patterns := ultraPatterns
	if parser == ParserCTExcelV1 {
		patterns = ctExcelPatterns
	}
	parsed := make(map[string]string)
	for _, pattern := range patterns {
		match := pattern.expr.FindStringSubmatch(text)
		if len(match) <= pattern.group {
			continue
		}
		value := normalizeParsedValue(match[pattern.group])
		if pattern.field == "balance" && parser == ParserCTExcelV1 {
			value = strings.TrimSuffix(value, ".")
		}
		if value != "" {
			parsed[pattern.field] = value
		}
	}
	if parser == ParserCTExcelV1 && parsed["balance"] == "" {
		match := ctExcelBalanceFallback.FindStringSubmatch(text)
		if len(match) > 2 {
			parsed["balance"] = strings.TrimSuffix(normalizeParsedValue(match[2]), ".")
		}
	}
	if len(parsed) == 0 {
		return nil
	}
	return parsed
}

func normalizeParsedValue(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}
