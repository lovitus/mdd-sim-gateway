#include "at_response.h"

#include <assert.h>
#include <string.h>

static enum mdd_at_result feed(struct mdd_at_parser *parser, const char *text) {
    return mdd_at_parser_feed(parser, (const unsigned char *)text, strlen(text));
}

int main(void) {
    struct mdd_at_parser parser;

    assert(mdd_at_policy_for_command("AT+CLCC") == MDD_AT_STANDARD);
    assert(mdd_at_policy_for_command("ATD123;") == MDD_AT_CALL_START);
    assert(mdd_at_policy_for_command("ATA") == MDD_AT_CALL_START);
    assert(mdd_at_policy_for_command("AT+CGDATA=\"PPP\",1") == MDD_AT_DATA_CONNECT);

    mdd_at_parser_init(&parser, MDD_AT_STANDARD);
    assert(feed(&parser, "NO CARRIER\r\n+CR") == MDD_AT_PENDING);
    assert(feed(&parser, "EG: 5\r\nO") == MDD_AT_PENDING);
    assert(feed(&parser, "K\r\n") == MDD_AT_OK);

    mdd_at_parser_init(&parser, MDD_AT_STANDARD);
    assert(feed(&parser, "+COPS: \"OK MOBILE\"\r\n") == MDD_AT_PENDING);
    assert(feed(&parser, "OK\r\n") == MDD_AT_OK);

    mdd_at_parser_init(&parser, MDD_AT_CALL_START);
    assert(feed(&parser, "\r\nNO CARRIER\r\n") == MDD_AT_ERROR);
    mdd_at_parser_init(&parser, MDD_AT_CALL_START);
    assert(feed(&parser, "OK\r\nNO CARRIER\r\n") == MDD_AT_ERROR);
    mdd_at_parser_init(&parser, MDD_AT_CALL_START);
    assert(feed(&parser, "NO CARRIER\r\nOK\r\n") == MDD_AT_ERROR);
    mdd_at_parser_init(&parser, MDD_AT_CALL_START);
    assert(feed(&parser, "OK\r\nNO CAR") == MDD_AT_PENDING);
    assert(mdd_at_parser_provisional(&parser) == MDD_AT_OK);
    assert(feed(&parser, "RIER\r\n") == MDD_AT_ERROR);
    mdd_at_parser_init(&parser, MDD_AT_CALL_END);
    assert(feed(&parser, "OK\r\nNO CARRIER\r\n") == MDD_AT_PENDING);
    assert(mdd_at_parser_provisional(&parser) == MDD_AT_OK);
    mdd_at_parser_init(&parser, MDD_AT_CALL_END);
    assert(feed(&parser, "NO CARRIER\r\n") == MDD_AT_PENDING);
    assert(mdd_at_parser_provisional(&parser) == MDD_AT_OK);
    mdd_at_parser_init(&parser, MDD_AT_DATA_CONNECT);
    assert(feed(&parser, "\r\nCONNECT 115200\r\n") == MDD_AT_PENDING);
    assert(mdd_at_parser_provisional(&parser) == MDD_AT_CONNECT);
    mdd_at_parser_init(&parser, MDD_AT_STANDARD);
    assert(feed(&parser, "+CME ERROR: 30\r\n") == MDD_AT_ERROR);
    return 0;
}
