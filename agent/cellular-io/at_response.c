#include "at_response.h"

#include <ctype.h>
#include <stdio.h>
#include <string.h>
#include <strings.h>

static int line_equals(const char *line, const char *value) {
    return strcasecmp(line, value) == 0;
}

static int line_prefix(const char *line, const char *value) {
    size_t length = strlen(value);
    return strncasecmp(line, value, length) == 0 &&
           (line[length] == '\0' || line[length] == ' ' || line[length] == ':');
}

enum mdd_at_policy mdd_at_policy_for_command(const char *command) {
    if (!command) return MDD_AT_STANDARD;
    while (*command == ' ' || *command == '\t') ++command;
    if (strncasecmp(command, "ATD", 3) == 0 || strcasecmp(command, "ATA") == 0)
        return MDD_AT_CALL_START;
    if (strcasecmp(command, "ATH") == 0 || strcasecmp(command, "AT+CHUP") == 0)
        return MDD_AT_CALL_END;
    if (strncasecmp(command, "AT+CGDATA", 9) == 0 || strcasecmp(command, "ATO") == 0)
        return MDD_AT_DATA_CONNECT;
    return MDD_AT_STANDARD;
}

void mdd_at_parser_init(struct mdd_at_parser *parser, enum mdd_at_policy policy) {
    memset(parser, 0, sizeof(*parser));
    parser->policy = policy;
}

static enum mdd_at_result finish_line(struct mdd_at_parser *parser) {
    size_t start = 0;
    size_t end = parser->line_used;
    while (start < end && isspace((unsigned char)parser->line[start])) ++start;
    while (end > start && isspace((unsigned char)parser->line[end - 1])) --end;
    parser->line[end] = '\0';
    const char *line = parser->line + start;
    parser->line_used = 0;
    if (!*line) return MDD_AT_PENDING;

    if (line_equals(line, "OK")) return MDD_AT_OK;
    if (line_equals(line, "ERROR") || line_prefix(line, "+CME ERROR") ||
        line_prefix(line, "+CMS ERROR")) return MDD_AT_ERROR;
    if (line_prefix(line, "CONNECT"))
        return parser->policy == MDD_AT_DATA_CONNECT ? MDD_AT_CONNECT : MDD_AT_PENDING;
    if (line_equals(line, "NO CARRIER") || line_equals(line, "BUSY") ||
        line_equals(line, "NO ANSWER") || line_equals(line, "NO DIALTONE")) {
        /* These are asynchronous URCs for ordinary status/configuration transactions. */
        if (parser->policy == MDD_AT_STANDARD) return MDD_AT_PENDING;
        /* For ATH/CHUP these all prove that no call remains.  Higher layers still require
           fresh CLCC samples before clearing the durable paid-call lease. */
        if (parser->policy == MDD_AT_CALL_END) return MDD_AT_OK;
        return MDD_AT_ERROR;
    }
    return MDD_AT_PENDING;
}

enum mdd_at_result mdd_at_parser_feed(
        struct mdd_at_parser *parser, const unsigned char *data, size_t length) {
    enum mdd_at_result aggregate = MDD_AT_PENDING;
    for (size_t i = 0; i < length; ++i) {
        unsigned char value = data[i];
        if (value == '\r' || value == '\n') {
            enum mdd_at_result result = finish_line(parser);
            if (result == MDD_AT_OVERFLOW) return result;
            /* Scan the complete USB batch. A modem may emit OK followed by NO CARRIER in one
               transfer; for call/data start the rejecting terminal must outrank optimistic OK
               regardless of line order. Explicit errors outrank success for every policy. */
            if (result == MDD_AT_ERROR) aggregate = MDD_AT_ERROR;
            else if (aggregate != MDD_AT_ERROR && result == MDD_AT_CONNECT)
                aggregate = MDD_AT_CONNECT;
            else if (aggregate == MDD_AT_PENDING && result == MDD_AT_OK)
                aggregate = MDD_AT_OK;
            continue;
        }
        if (parser->line_used + 1 >= sizeof(parser->line)) return MDD_AT_OVERFLOW;
        parser->line[parser->line_used++] = (char)value;
        parser->line[parser->line_used] = '\0';
    }
    if (aggregate == MDD_AT_ERROR || aggregate == MDD_AT_OVERFLOW)
        return aggregate;
    if (parser->policy != MDD_AT_STANDARD &&
            (aggregate == MDD_AT_OK || aggregate == MDD_AT_CONNECT)) {
        parser->provisional = aggregate;
        return MDD_AT_PENDING;
    }
    return aggregate;
}

enum mdd_at_result mdd_at_parser_provisional(const struct mdd_at_parser *parser) {
    return parser ? parser->provisional : MDD_AT_PENDING;
}

void mdd_sim_event_parser_init(struct mdd_sim_event_parser *parser) {
    memset(parser, 0, sizeof(*parser));
}

static unsigned int finish_sim_event_line(struct mdd_sim_event_parser *parser) {
    size_t start = 0;
    size_t end = parser->line_used;
    while (start < end && isspace((unsigned char)parser->line[start])) ++start;
    while (end > start && isspace((unsigned char)parser->line[end - 1])) --end;
    parser->line[end] = '\0';
    unsigned int enabled = 0, inserted = 0;
    char tail = '\0';
    int fields = sscanf(parser->line + start, "+QSIMSTAT: %u,%u %c", &enabled, &inserted, &tail);
    parser->line_used = 0;
    return fields == 2 && enabled == 1U && inserted <= 2U ? 1U : 0U;
}

unsigned int mdd_sim_event_parser_feed(
        struct mdd_sim_event_parser *parser, const unsigned char *data, size_t length) {
    if (!parser || (!data && length)) return 0;
    unsigned int events = 0;
    for (size_t i = 0; i < length; ++i) {
        unsigned char value = data[i];
        if (value == '\r' || value == '\n') {
            if (parser->line_used) events += finish_sim_event_line(parser);
            continue;
        }
        if (parser->line_used + 1U < sizeof(parser->line)) {
            parser->line[parser->line_used++] = (char)value;
            parser->line[parser->line_used] = '\0';
        } else {
            parser->line_used = 0;
        }
    }
    return events;
}
