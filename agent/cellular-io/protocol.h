#ifndef MDD_CELLULAR_IO_PROTOCOL_H
#define MDD_CELLULAR_IO_PROTOCOL_H

#include <stdint.h>

#define MDD_IO_VERSION 1
#define MDD_IO_MAX_FRAME (1024U * 1024U)

enum mdd_io_message_type {
    MDD_IO_HELLO = 1,
    MDD_IO_RESOLVE = 2,
    MDD_IO_TCP_OPEN = 3,
    MDD_IO_TCP_WRITE = 4,
    MDD_IO_TCP_CLOSE = 5,
    MDD_IO_UDP_OPEN = 6,
    MDD_IO_UDP_SEND = 7,
    MDD_IO_UDP_CLOSE = 8,
    MDD_IO_AT_COMMAND = 9,
    MDD_IO_SHUTDOWN = 10,
    MDD_IO_DATA_ENABLE = 11,
    MDD_IO_DATA_DISABLE = 12,
    MDD_IO_DNS_SERVER = 13,
    MDD_IO_RAW_EXCHANGE = 14,
    MDD_IO_ISOLATION_CHECK = 15,
    /* uint32 timeout_ms (network byte order), followed by the ASCII command. */
    MDD_IO_AT_COMMAND_V2 = 16,
    MDD_IO_RESPONSE = 0x80,
    MDD_IO_TCP_DATA = 0x90,
    MDD_IO_TCP_EOF = 0x91,
    MDD_IO_UDP_DATA = 0x92,
    MDD_IO_LINK_STATE = 0x93,
};

struct mdd_io_header {
    uint8_t version;
    uint8_t type;
    uint16_t flags_be;
    uint32_t request_id_be;
    uint32_t length_be;
} __attribute__((packed));

#endif
