#ifndef MDD_AT_RESPONSE_H
#define MDD_AT_RESPONSE_H

#include <stddef.h>

enum mdd_at_policy {
    MDD_AT_STANDARD = 0,
    MDD_AT_CALL_START,
    MDD_AT_CALL_END,
    MDD_AT_DATA_CONNECT,
};

enum mdd_at_result {
    MDD_AT_PENDING = 0,
    MDD_AT_OK,
    MDD_AT_CONNECT,
    MDD_AT_ERROR,
    MDD_AT_OVERFLOW,
};

struct mdd_at_parser {
    enum mdd_at_policy policy;
    enum mdd_at_result provisional;
    char line[1024];
    size_t line_used;
};

enum mdd_at_policy mdd_at_policy_for_command(const char *command);
void mdd_at_parser_init(struct mdd_at_parser *parser, enum mdd_at_policy policy);
enum mdd_at_result mdd_at_parser_feed(
    struct mdd_at_parser *parser, const unsigned char *data, size_t length);
enum mdd_at_result mdd_at_parser_provisional(const struct mdd_at_parser *parser);

#endif
