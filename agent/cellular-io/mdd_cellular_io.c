#include <libusb.h>
#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <poll.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

#include "lwip/dns.h"
#include "lwip/init.h"
#include "lwip/pbuf.h"
#include "lwip/tcp.h"
#include "lwip/timeouts.h"
#include "lwip/udp.h"
#include "netif/ppp/ppp.h"
#include "netif/ppp/pppos.h"
#include "protocol.h"
#include "at_response.h"

#define MAX_AT_CHANNELS 16
#define MAX_TCP_HANDLES 24
#define MAX_UDP_HANDLES 24
#define IPC_BUFFER_SIZE (MDD_IO_MAX_FRAME + sizeof(struct mdd_io_header))

struct at_channel {
    int interface_number;
    uint8_t input_endpoint;
    uint8_t output_endpoint;
};

struct tcp_handle {
    uint32_t handle;
    uint32_t open_request;
    struct tcp_pcb *pcb;
    int connected;
};

struct udp_handle {
    uint32_t handle;
    struct udp_pcb *pcb;
};

enum pending_kind { PENDING_RESOLVE, PENDING_TCP, PENDING_UDP };

struct pending_dns {
    enum pending_kind kind;
    uint32_t request_id;
    uint32_t handle;
    uint16_t port;
    size_t data_length;
    unsigned char *data;
};

struct companion_state {
    int ipc_fd;
    int watch_fd;
    uint16_t vid;
    uint16_t pid;
    uint8_t bus;
    uint8_t address;
    char serial[256];
    libusb_context *usb_context;
    libusb_device_handle *usb_handle;
    struct at_channel channels[MAX_AT_CHANNELS];
    size_t channel_count;
    int control_index;
    int data_index;
    int control_claimed;
    int data_claimed;
    ppp_pcb *ppp;
    struct netif ppp_netif;
    int ppp_up;
    int ppp_error;
    uint32_t enable_request;
    uint32_t enable_deadline;
    struct tcp_handle tcp[MAX_TCP_HANDLES];
    struct udp_handle udp[MAX_UDP_HANDLES];
    uint32_t next_handle;
    unsigned char ipc_buffer[IPC_BUFFER_SIZE];
    size_t ipc_used;
    int stopping;
};

static struct companion_state state;
static volatile sig_atomic_t signal_stop;

u32_t sys_now(void) {
    struct timespec now;
    clock_gettime(CLOCK_MONOTONIC, &now);
    return (u32_t)((uint64_t)now.tv_sec * 1000ULL + (uint64_t)now.tv_nsec / 1000000ULL);
}

u32_t sys_jiffies(void) { return sys_now(); }
u32_t lwip_port_rand(void) { return (u32_t)arc4random(); }

static void request_stop(int number) {
    (void)number;
    signal_stop = 1;
}

static int write_all(int fd, const void *data, size_t length) {
    const unsigned char *cursor = data;
    while (length) {
        ssize_t written = write(fd, cursor, length);
        if (written < 0 && errno == EINTR) continue;
        if (written <= 0) return -1;
        cursor += (size_t)written;
        length -= (size_t)written;
    }
    return 0;
}

static int send_frame(uint8_t type, uint32_t request_id,
                      const void *payload, uint32_t length) {
    struct mdd_io_header header = {
        .version = MDD_IO_VERSION,
        .type = type,
        .flags_be = 0,
        .request_id_be = htonl(request_id),
        .length_be = htonl(length),
    };
    if (write_all(state.ipc_fd, &header, sizeof(header)) < 0) return -1;
    return length ? write_all(state.ipc_fd, payload, length) : 0;
}

static int send_response(uint32_t request_id, int32_t status,
                         const void *payload, uint32_t length) {
    unsigned char *value = malloc((size_t)length + 4U);
    if (!value) return -1;
    uint32_t encoded = htonl((uint32_t)status);
    memcpy(value, &encoded, 4);
    if (length) memcpy(value + 4, payload, length);
    int result = send_frame(MDD_IO_RESPONSE, request_id, value, length + 4U);
    free(value);
    return result;
}

static int send_error(uint32_t request_id, int32_t status, const char *detail) {
    return send_response(request_id, status, detail, (uint32_t)strlen(detail));
}

static int send_link_state(const char *value) {
    return send_frame(MDD_IO_LINK_STATE, 0, value, (uint32_t)strlen(value));
}

static int usb_write_channel(const struct at_channel *channel, const void *data,
                             int length, unsigned int timeout_ms) {
    const unsigned char *cursor = data;
    int remaining = length;
    while (remaining > 0) {
        int transferred = 0;
        int rc = libusb_bulk_transfer(state.usb_handle, channel->output_endpoint,
                                      (unsigned char *)cursor, remaining,
                                      &transferred, timeout_ms);
        if (rc != LIBUSB_SUCCESS || transferred <= 0) return -1;
        cursor += transferred;
        remaining -= transferred;
    }
    return length;
}

static unsigned int deadline_remaining(uint32_t deadline, unsigned int ceiling_ms);

static int usb_write_channel_until(const struct at_channel *channel, const void *data,
                                   int length, uint32_t deadline) {
    const unsigned char *cursor = data;
    int remaining = length;
    while (remaining > 0) {
        int transferred = 0;
        unsigned int timeout_ms = deadline_remaining(deadline, 1000U);
        if (!timeout_ms) return -1;
        int rc = libusb_bulk_transfer(state.usb_handle, channel->output_endpoint,
                                      (unsigned char *)cursor, remaining,
                                      &transferred, timeout_ms);
        if (rc != LIBUSB_SUCCESS || transferred <= 0) return -1;
        cursor += transferred;
        remaining -= transferred;
    }
    return length;
}

static unsigned int deadline_remaining(uint32_t deadline, unsigned int ceiling_ms) {
    int32_t remaining = (int32_t)(deadline - sys_now());
    if (remaining <= 0) return 0;
    return (unsigned int)remaining < ceiling_ms ? (unsigned int)remaining : ceiling_ms;
}

static int drain_at_input(const struct at_channel *channel, uint32_t transaction_deadline) {
    uint32_t drain_deadline = sys_now() + 250U;
    if ((int32_t)(transaction_deadline - drain_deadline) < 0)
        drain_deadline = transaction_deadline;
    size_t drained = 0;
    unsigned int quiet_windows = 0;
    while ((int32_t)(drain_deadline - sys_now()) > 0) {
        unsigned char buffer[1024];
        int transferred = 0;
        unsigned int transfer_timeout = deadline_remaining(drain_deadline, 60U);
        if (!transfer_timeout) break;
        int rc = libusb_bulk_transfer(state.usb_handle, channel->input_endpoint,
                                      buffer, sizeof(buffer), &transferred, transfer_timeout);
        if (rc == LIBUSB_ERROR_TIMEOUT) {
            if (++quiet_windows >= 2U) {
                if (drained && drained <= 8192U)
                    fprintf(stderr, "[mdd-cellular-io] drained %zu bytes of pending AT notifications\n",
                            drained);
                return 0;
            }
            continue;
        }
        if (rc != LIBUSB_SUCCESS) return -1;
        if (transferred > 0) {
            quiet_windows = 0;
            drained += (size_t)transferred;
            if (drained > 8192U) return -1;
        }
    }
    return drained ? -1 : 0;
}

static int at_command(const struct at_channel *channel, const char *command,
                      char *response, size_t response_size, unsigned int timeout_ms) {
    char wire[512];
    int wire_length = snprintf(wire, sizeof(wire), "%s\r", command);
    if (wire_length <= 0 || (size_t)wire_length >= sizeof(wire) || response_size < 2)
        return -1;
    /* One caller deadline covers drain, USB write and response read.  Starting the clock after
       the write let Python time out while this single-owner AT lane was still occupied. */
    const uint32_t deadline = sys_now() + timeout_ms;
    response[0] = '\0';
    if (drain_at_input(channel, deadline) < 0) return -1;
    if (usb_write_channel_until(channel, wire, wire_length, deadline) < 0)
        return -1;
    struct mdd_at_parser parser;
    mdd_at_parser_init(&parser, mdd_at_policy_for_command(command));
    size_t used = 0;
    uint32_t terminal_settle_deadline = 0;
    while ((int32_t)(deadline - sys_now()) > 0) {
        unsigned char buffer[2048];
        int transferred = 0;
        if (terminal_settle_deadline &&
                (int32_t)(terminal_settle_deadline - sys_now()) <= 0)
            return (mdd_at_parser_provisional(&parser) == MDD_AT_CONNECT) ? 1 : 0;
        uint32_t effective_deadline = deadline;
        if (terminal_settle_deadline &&
                (int32_t)(effective_deadline - terminal_settle_deadline) > 0)
            effective_deadline = terminal_settle_deadline;
        uint32_t remaining = effective_deadline - sys_now();
        unsigned int transfer_timeout = remaining < 100U ? remaining : 100U;
        if (!transfer_timeout) break;
        int rc = libusb_bulk_transfer(state.usb_handle, channel->input_endpoint,
                                      buffer, sizeof(buffer), &transferred, transfer_timeout);
        if (rc == LIBUSB_ERROR_TIMEOUT) continue;
        if (rc != LIBUSB_SUCCESS) return -1;
        if (transferred <= 0) continue;
        if ((size_t)transferred > response_size - used - 1U) return -1;
        memcpy(response + used, buffer, (size_t)transferred);
        used += (size_t)transferred;
        response[used] = '\0';
        enum mdd_at_result result = mdd_at_parser_feed(
            &parser, buffer, (size_t)transferred);
        if (result == MDD_AT_OK) return 0;
        if (result == MDD_AT_CONNECT) return 1;
        if (result == MDD_AT_ERROR || result == MDD_AT_OVERFLOW) return -1;
        if (mdd_at_parser_provisional(&parser) != MDD_AT_PENDING &&
                !terminal_settle_deadline)
            terminal_settle_deadline = sys_now() + 120U;
    }
    if (mdd_at_parser_provisional(&parser) == MDD_AT_OK) return 0;
    if (mdd_at_parser_provisional(&parser) == MDD_AT_CONNECT) return 1;
    return -1;
}

static int raw_exchange(const struct at_channel *channel, const unsigned char *wire,
                        size_t wire_length, char *response, size_t response_size,
                        unsigned int timeout_ms) {
    if (usb_write_channel(channel, wire, (int)wire_length, 2000) < 0) return -1;
    response[0] = '\0';
    size_t used = 0;
    for (unsigned int elapsed = 0; elapsed < timeout_ms; elapsed += 100) {
        unsigned char buffer[2048];
        int transferred = 0;
        int rc = libusb_bulk_transfer(state.usb_handle, channel->input_endpoint,
                                      buffer, sizeof(buffer), &transferred, 100);
        if (rc == LIBUSB_ERROR_TIMEOUT) continue;
        if (rc != LIBUSB_SUCCESS) return -1;
        size_t available = response_size > used ? response_size - used - 1 : 0;
        size_t copy = (size_t)transferred < available ? (size_t)transferred : available;
        if (copy) {
            memcpy(response + used, buffer, copy);
            used += copy;
            response[used] = '\0';
        }
        if (strstr(response, "\r\nOK") || strstr(response, "\r\nERROR") ||
            strstr(response, "+CMS ERROR:") || strstr(response, "+CME ERROR:")) return 0;
    }
    return -1;
}

static void set_control_line_state(const struct at_channel *channel, int enabled) {
    /* CDC SET_CONTROL_LINE_STATE is also implemented by many vendor-class USB modem
     * functions.  Unsupported requests fail harmlessly; the guarded escape below remains
     * the portable fallback.  Dropping DTR is the only deterministic way to recover a data
     * function when its PPP peer or the parent process disappeared mid-session. */
    (void)libusb_control_transfer(
        state.usb_handle,
        LIBUSB_ENDPOINT_OUT | LIBUSB_REQUEST_TYPE_CLASS | LIBUSB_RECIPIENT_INTERFACE,
        0x22, enabled ? 1 : 0, channel->interface_number, NULL, 0, 1000);
}

static int guarded_hangup(const struct at_channel *channel) {
    char response[512];
    set_control_line_state(channel, 0);
    usleep(500000);
    set_control_line_state(channel, 1);
    usleep(500000);
    if (at_command(channel, "AT", response, sizeof(response), 1500) == 0) return 0;
    usleep(1100000);
    (void)usb_write_channel(channel, "+++", 3, 1000);
    usleep(1100000);
    unsigned char discard[1024];
    int transferred = 0;
    (void)libusb_bulk_transfer(state.usb_handle, channel->input_endpoint,
                               discard, sizeof(discard), &transferred, 250);
    (void)usb_write_channel(channel, "ATH\r", 4, 1000);
    (void)libusb_bulk_transfer(state.usb_handle, channel->input_endpoint,
                               discard, sizeof(discard), &transferred, 500);
    usleep(500000);
    return at_command(channel, "AT", response, sizeof(response), 2000);
}

static int is_network_function(const struct libusb_interface_descriptor *interface) {
    if (interface->bInterfaceClass == LIBUSB_CLASS_COMM ||
        interface->bInterfaceClass == LIBUSB_CLASS_DATA) return 1;
    return interface->bInterfaceClass == LIBUSB_CLASS_WIRELESS &&
           interface->bInterfaceSubClass == 1;
}

static int inspect_device(libusb_device *device) {
    struct libusb_config_descriptor *config = NULL;
    int rc = libusb_get_active_config_descriptor(device, &config);
    if (rc != LIBUSB_SUCCESS) return rc;
    for (uint8_t i = 0; i < config->bNumInterfaces; ++i) {
        const struct libusb_interface *entry = &config->interface[i];
        for (int a = 0; a < entry->num_altsetting; ++a) {
            const struct libusb_interface_descriptor *interface = &entry->altsetting[a];
            if (is_network_function(interface)) {
                libusb_free_config_descriptor(config);
                return LIBUSB_ERROR_NOT_SUPPORTED;
            }
            if (interface->bAlternateSetting != 0 ||
                interface->bInterfaceClass != LIBUSB_CLASS_VENDOR_SPEC) continue;
            struct at_channel channel = {.interface_number = interface->bInterfaceNumber};
            for (uint8_t e = 0; e < interface->bNumEndpoints; ++e) {
                const struct libusb_endpoint_descriptor *endpoint = &interface->endpoint[e];
                if ((endpoint->bmAttributes & LIBUSB_TRANSFER_TYPE_MASK) !=
                    LIBUSB_TRANSFER_TYPE_BULK) continue;
                if ((endpoint->bEndpointAddress & LIBUSB_ENDPOINT_DIR_MASK) ==
                    LIBUSB_ENDPOINT_IN)
                    channel.input_endpoint = endpoint->bEndpointAddress;
                else
                    channel.output_endpoint = endpoint->bEndpointAddress;
            }
            if (channel.input_endpoint && channel.output_endpoint &&
                state.channel_count < MAX_AT_CHANNELS)
                state.channels[state.channel_count++] = channel;
        }
    }
    libusb_free_config_descriptor(config);
    return state.channel_count ? LIBUSB_SUCCESS : LIBUSB_ERROR_NOT_FOUND;
}

static int isolation_still_proven(void) {
    if (!state.usb_handle) return LIBUSB_ERROR_NO_DEVICE;
    libusb_device *device = libusb_get_device(state.usb_handle);
    struct libusb_device_descriptor descriptor;
    if (!device || libusb_get_device_descriptor(device, &descriptor) != LIBUSB_SUCCESS ||
        descriptor.idVendor != state.vid || descriptor.idProduct != state.pid ||
        libusb_get_bus_number(device) != state.bus ||
        libusb_get_device_address(device) != state.address)
        return LIBUSB_ERROR_NO_DEVICE;
    struct libusb_config_descriptor *config = NULL;
    int result = libusb_get_active_config_descriptor(device, &config);
    if (result != LIBUSB_SUCCESS) return result;
    for (uint8_t i = 0; i < config->bNumInterfaces; ++i) {
        const struct libusb_interface *entry = &config->interface[i];
        for (int a = 0; a < entry->num_altsetting; ++a) {
            if (is_network_function(&entry->altsetting[a])) {
                libusb_free_config_descriptor(config);
                return LIBUSB_ERROR_NOT_SUPPORTED;
            }
        }
    }
    libusb_free_config_descriptor(config);
    /* The claims below were obtained without detaching a kernel driver.  On
     * macOS libusb_kernel_driver_active() can nevertheless return 1 for an
     * interface owned by this same process, so it is not a valid continuity
     * probe.  A claim cannot be concurrently transferred while this handle is
     * alive; live IPC plus the exact-device and descriptor checks above are
     * the continuing ownership proof. */
    if (!state.control_claimed || state.control_index < 0)
        return LIBUSB_ERROR_BUSY;
    if (state.ppp_up && (!state.data_claimed || state.data_index < 0))
        return LIBUSB_ERROR_BUSY;
    return LIBUSB_SUCCESS;
}

static int open_exact_device(void) {
    libusb_device **devices = NULL;
    ssize_t count = libusb_get_device_list(state.usb_context, &devices);
    if (count < 0) return (int)count;
    int result = LIBUSB_ERROR_NOT_FOUND;
    for (ssize_t i = 0; i < count; ++i) {
        struct libusb_device_descriptor descriptor;
        if (libusb_get_bus_number(devices[i]) != state.bus ||
            libusb_get_device_address(devices[i]) != state.address ||
            libusb_get_device_descriptor(devices[i], &descriptor) != LIBUSB_SUCCESS ||
            descriptor.idVendor != state.vid || descriptor.idProduct != state.pid)
            continue;
        result = inspect_device(devices[i]);
        if (result == LIBUSB_SUCCESS)
            result = libusb_open(devices[i], &state.usb_handle);
        if (result == LIBUSB_SUCCESS && descriptor.iSerialNumber) {
            int serial_length = libusb_get_string_descriptor_ascii(
                state.usb_handle, descriptor.iSerialNumber,
                (unsigned char *)state.serial, sizeof(state.serial) - 1);
            if (serial_length > 0) state.serial[serial_length] = '\0';
        }
        break;
    }
    libusb_free_device_list(devices, 1);
    return result;
}

static int discover_control_channel(void) {
    char response[512];
    state.control_index = -1;
    state.data_index = -1;
    for (size_t i = 0; i < state.channel_count; ++i) {
        struct at_channel *channel = &state.channels[i];
        if (libusb_kernel_driver_active(state.usb_handle, channel->interface_number) == 1)
            continue;
        if (libusb_claim_interface(state.usb_handle, channel->interface_number) !=
            LIBUSB_SUCCESS) continue;
        int result = at_command(channel, "AT", response, sizeof(response), 1200);
        if (result == 0) {
            state.control_index = (int)i;
            state.control_claimed = 1;
            return 0;
        }
        libusb_release_interface(state.usb_handle, channel->interface_number);
    }
    return -1;
}

static u32_t ppp_output(ppp_pcb *pcb, const void *data, u32_t length, void *context) {
    (void)pcb;
    (void)context;
    if (state.data_index < 0) return 0;
    int written = usb_write_channel(&state.channels[state.data_index], data,
                                    (int)length, 2000);
    return written < 0 ? 0U : (u32_t)written;
}

static void ppp_status(ppp_pcb *pcb, int error_code, void *context) {
    (void)pcb;
    (void)context;
    if (error_code == PPPERR_NONE) {
        state.ppp_up = 1;
        state.ppp_error = 0;
        send_link_state("up");
        if (state.enable_request) {
            send_response(state.enable_request, 0, NULL, 0);
            state.enable_request = 0;
        }
    } else if (error_code != PPPERR_USER) {
        state.ppp_error = error_code;
        state.ppp_up = 0;
        send_link_state("down");
        if (state.enable_request) {
            send_error(state.enable_request, -error_code, "PPP negotiation failed");
            state.enable_request = 0;
        }
    }
}

static void pump_ppp(unsigned int timeout_ms) {
    if (!state.ppp || state.data_index < 0) {
        usleep(timeout_ms * 1000U);
        sys_check_timeouts();
        return;
    }
    unsigned char buffer[4096];
    int transferred = 0;
    int rc = libusb_bulk_transfer(
        state.usb_handle, state.channels[state.data_index].input_endpoint,
        buffer, sizeof(buffer), &transferred, timeout_ms);
    if (rc == LIBUSB_SUCCESS && transferred > 0)
        pppos_input(state.ppp, buffer, transferred);
    else if (rc != LIBUSB_SUCCESS && rc != LIBUSB_ERROR_TIMEOUT)
        state.ppp_error = PPPERR_DEVICE;
    sys_check_timeouts();
}

static void release_data_channel(void) {
    if (state.ppp) {
        ppp_close(state.ppp, 0);
        uint32_t deadline = sys_now() + 1200U;
        while ((int32_t)(deadline - sys_now()) > 0) pump_ppp(20);
        ppp_free(state.ppp);
        state.ppp = NULL;
    }
    if (state.data_claimed && state.data_index >= 0) {
        guarded_hangup(&state.channels[state.data_index]);
        libusb_release_interface(state.usb_handle,
                                 state.channels[state.data_index].interface_number);
    }
    state.data_claimed = 0;
    state.data_index = -1;
    state.ppp_up = 0;
    state.ppp_error = 0;
    send_link_state("down");
}

static int start_data_channel(uint32_t request_id) {
    if (state.ppp_up) return send_response(request_id, 0, NULL, 0);
    if (state.enable_request) return send_error(request_id, -EBUSY, "PPP start is pending");
    /* A failed negotiation must not consume the single lwIP PPP control
     * block forever.  Normally the event loop reaps it immediately; this is
     * a defensive boundary for a retry received in the same poll turn. */
    if (state.ppp) release_data_channel();
    char response[1024];
    for (size_t offset = 0; offset < state.channel_count; ++offset) {
        size_t i = state.channel_count - offset - 1;
        if ((int)i == state.control_index) continue;
        struct at_channel *channel = &state.channels[i];
        if (libusb_kernel_driver_active(state.usb_handle, channel->interface_number) == 1 ||
            libusb_claim_interface(state.usb_handle, channel->interface_number) !=
                LIBUSB_SUCCESS) continue;
        int ready = at_command(channel, "AT", response, sizeof(response), 1500);
        if (ready != 0) {
            guarded_hangup(channel);
            ready = at_command(channel, "AT", response, sizeof(response), 1500);
        }
        int dial = ready == 0 ? at_command(
            channel, "AT+CGDATA=\"PPP\",1", response, sizeof(response), 15000) : -1;
        if (dial == 1) {
            state.data_index = (int)i;
            state.data_claimed = 1;
            break;
        }
        libusb_release_interface(state.usb_handle, channel->interface_number);
    }
    if (state.data_index < 0)
        return send_error(request_id, -ENODEV, "no private PPP data function accepted the dial");
    state.ppp = pppos_create(&state.ppp_netif, ppp_output, ppp_status, &state);
    if (!state.ppp) {
        release_data_channel();
        return send_error(request_id, -ENOMEM, "cannot create private PPP state");
    }
    ppp_set_default(state.ppp);
    ppp_set_usepeerdns(state.ppp, 1);
    ppp_set_auth(state.ppp, PPPAUTHTYPE_ANY, "", "");
    if (ppp_connect(state.ppp, 0) != ERR_OK) {
        release_data_channel();
        return send_error(request_id, -EIO, "cannot start private PPP negotiation");
    }
    state.enable_request = request_id;
    state.enable_deadline = sys_now() + 60000U;
    send_link_state("connecting");
    return 0;
}

static struct tcp_handle *find_tcp(uint32_t handle) {
    for (size_t i = 0; i < MAX_TCP_HANDLES; ++i)
        if (state.tcp[i].handle == handle) return &state.tcp[i];
    return NULL;
}

static struct udp_handle *find_udp(uint32_t handle) {
    for (size_t i = 0; i < MAX_UDP_HANDLES; ++i)
        if (state.udp[i].handle == handle) return &state.udp[i];
    return NULL;
}

static void clear_tcp(struct tcp_handle *entry) {
    if (!entry) return;
    entry->handle = 0;
    entry->open_request = 0;
    entry->pcb = NULL;
    entry->connected = 0;
}

static err_t tcp_connected_callback(void *argument, struct tcp_pcb *pcb, err_t error) {
    struct tcp_handle *entry = argument;
    if (!entry || entry->pcb != pcb) return ERR_ARG;
    if (error != ERR_OK) {
        send_error(entry->open_request, error, "private TCP connect failed");
        clear_tcp(entry);
        return error;
    }
    entry->connected = 1;
    uint32_t handle_be = htonl(entry->handle);
    send_response(entry->open_request, 0, &handle_be, sizeof(handle_be));
    entry->open_request = 0;
    return ERR_OK;
}

static err_t tcp_received_callback(void *argument, struct tcp_pcb *pcb,
                                   struct pbuf *packet, err_t error) {
    struct tcp_handle *entry = argument;
    if (!entry || entry->pcb != pcb) {
        if (packet) pbuf_free(packet);
        return ERR_ARG;
    }
    if (!packet) {
        uint32_t handle_be = htonl(entry->handle);
        send_frame(MDD_IO_TCP_EOF, 0, &handle_be, sizeof(handle_be));
        tcp_close(pcb);
        clear_tcp(entry);
        return ERR_OK;
    }
    if (error == ERR_OK && packet->tot_len) {
        size_t length = (size_t)packet->tot_len + 4U;
        unsigned char *payload = malloc(length);
        if (!payload) {
            pbuf_free(packet);
            return ERR_MEM;
        }
        uint32_t handle_be = htonl(entry->handle);
        memcpy(payload, &handle_be, 4);
        pbuf_copy_partial(packet, payload + 4, packet->tot_len, 0);
        if (send_frame(MDD_IO_TCP_DATA, 0, payload, (uint32_t)length) == 0)
            tcp_recved(pcb, packet->tot_len);
        free(payload);
    }
    pbuf_free(packet);
    return ERR_OK;
}

static void tcp_error_callback(void *argument, err_t error) {
    struct tcp_handle *entry = argument;
    if (!entry) return;
    if (entry->open_request)
        send_error(entry->open_request, error, "private TCP connection failed");
    else {
        unsigned char payload[8];
        uint32_t handle_be = htonl(entry->handle);
        uint32_t error_be = htonl((uint32_t)error);
        memcpy(payload, &handle_be, 4);
        memcpy(payload + 4, &error_be, 4);
        send_frame(MDD_IO_TCP_EOF, 0, payload, sizeof(payload));
    }
    clear_tcp(entry);
}

static int connect_tcp_resolved(struct pending_dns *pending, const ip_addr_t *address) {
    struct tcp_handle *entry = find_tcp(pending->handle);
    if (!entry || !entry->pcb) return -1;
    err_t rc = tcp_connect(entry->pcb, address, pending->port, tcp_connected_callback);
    if (rc != ERR_OK) {
        send_error(pending->request_id, rc, "private TCP connect could not start");
        tcp_abort(entry->pcb);
        clear_tcp(entry);
        return -1;
    }
    return 0;
}

static int udp_send_resolved(struct pending_dns *pending, const ip_addr_t *address) {
    struct udp_handle *entry = find_udp(pending->handle);
    if (!entry || !entry->pcb) {
        send_error(pending->request_id, -ENOENT, "unknown private UDP handle");
        return -1;
    }
    struct pbuf *packet = pbuf_alloc(PBUF_TRANSPORT, (u16_t)pending->data_length, PBUF_RAM);
    if (!packet || pbuf_take(packet, pending->data, pending->data_length) != ERR_OK) {
        if (packet) pbuf_free(packet);
        send_error(pending->request_id, -ENOMEM, "cannot allocate private UDP datagram");
        return -1;
    }
    /* Exactly one submission: generic UDP traffic is never transparently replayed. */
    err_t rc = udp_sendto(entry->pcb, packet, address, pending->port);
    pbuf_free(packet);
    if (rc != ERR_OK) {
        send_error(pending->request_id, rc, "private UDP send failed");
        return -1;
    }
    send_response(pending->request_id, 0, NULL, 0);
    return 0;
}

static void dns_callback(const char *hostname, const ip_addr_t *address, void *argument) {
    (void)hostname;
    struct pending_dns *pending = argument;
    if (!pending) return;
    if (!address) {
        send_error(pending->request_id, -EHOSTUNREACH, "private DNS resolution failed");
    } else if (pending->kind == PENDING_RESOLVE) {
        send_response(pending->request_id, 0, ip_2_ip4(address), 4);
    } else if (pending->kind == PENDING_TCP) {
        connect_tcp_resolved(pending, address);
    } else {
        udp_send_resolved(pending, address);
    }
    free(pending->data);
    free(pending);
}

static int begin_dns(struct pending_dns *pending, const char *hostname) {
    ip_addr_t address;
    err_t result = dns_gethostbyname(hostname, &address, dns_callback, pending);
    if (result == ERR_OK) {
        dns_callback(hostname, &address, pending);
        return 0;
    }
    if (result != ERR_INPROGRESS) {
        send_error(pending->request_id, result, "private DNS query could not start");
        free(pending->data);
        free(pending);
        return -1;
    }
    return 0;
}

static int parse_target(const unsigned char *payload, size_t length,
                        char *host, size_t host_size, uint16_t *port, size_t *offset) {
    if (length < 4) return -1;
    uint16_t host_length_be, port_be;
    memcpy(&host_length_be, payload, 2);
    memcpy(&port_be, payload + 2, 2);
    size_t host_length = ntohs(host_length_be);
    if (!host_length || host_length >= host_size || length < 4U + host_length) return -1;
    memcpy(host, payload + 4, host_length);
    host[host_length] = '\0';
    *port = ntohs(port_be);
    *offset = 4U + host_length;
    return *port ? 0 : -1;
}

static void udp_received_callback(void *argument, struct udp_pcb *pcb, struct pbuf *packet,
                                  const ip_addr_t *source, u16_t port) {
    struct udp_handle *entry = argument;
    if (!entry || entry->pcb != pcb || !packet || !IP_IS_V4(source)) {
        if (packet) pbuf_free(packet);
        return;
    }
    size_t length = (size_t)packet->tot_len + 10U;
    unsigned char *payload = malloc(length);
    if (payload) {
        uint32_t handle_be = htonl(entry->handle);
        uint16_t port_be = htons(port);
        memcpy(payload, &handle_be, 4);
        memcpy(payload + 4, &port_be, 2);
        memcpy(payload + 6, ip_2_ip4(source), 4);
        pbuf_copy_partial(packet, payload + 10, packet->tot_len, 0);
        send_frame(MDD_IO_UDP_DATA, 0, payload, (uint32_t)length);
        free(payload);
    }
    pbuf_free(packet);
}

static void handle_request(uint8_t type, uint32_t request_id,
                           const unsigned char *payload, size_t length) {
    if (type == MDD_IO_HELLO) {
        char hello[384];
        int hello_length = snprintf(
            hello, sizeof(hello), "version=1;at_transactions=2;vid=%04x;pid=%04x;bus=%u;address=%u;serial=%s",
            state.vid, state.pid, state.bus, state.address, state.serial);
        send_response(request_id, 0, hello, (uint32_t)hello_length);
        return;
    }
    if (type == MDD_IO_SHUTDOWN) {
        send_response(request_id, 0, NULL, 0);
        state.stopping = 1;
        return;
    }
    if (type == MDD_IO_DATA_ENABLE) {
        start_data_channel(request_id);
        return;
    }
    if (type == MDD_IO_DATA_DISABLE) {
        release_data_channel();
        send_response(request_id, 0, NULL, 0);
        return;
    }
    if (type == MDD_IO_AT_COMMAND || type == MDD_IO_AT_COMMAND_V2) {
        size_t command_offset = 0;
        unsigned int command_timeout = 8000;
        if (type == MDD_IO_AT_COMMAND_V2) {
            if (length < 5) {
                send_error(request_id, -EINVAL, "invalid bounded AT command");
                return;
            }
            uint32_t timeout_be;
            memcpy(&timeout_be, payload, sizeof(timeout_be));
            command_timeout = ntohl(timeout_be);
            command_offset = sizeof(timeout_be);
            if (command_timeout < 100U || command_timeout > 30000U) {
                send_error(request_id, -EINVAL, "invalid AT command deadline");
                return;
            }
        }
        size_t command_length = length - command_offset;
        if (state.control_index < 0 || !state.control_claimed || !command_length ||
            command_length >= 500) {
            send_error(request_id, -ENODEV, "private AT control function is unavailable");
            return;
        }
        char command[512], response[8192];
        memcpy(command, payload + command_offset, command_length);
        command[command_length] = '\0';
        if (strncmp(command, "AT", 2) || strchr(command, '\r') || strchr(command, '\n')) {
            send_error(request_id, -EINVAL, "invalid AT command");
            return;
        }
        int result = at_command(&state.channels[state.control_index], command,
                                response, sizeof(response), command_timeout);
        if (result < 0) {
            /* Preserve the modem's bounded CME/CMS diagnostic.  The Python side already
             * redacts secrets from logs; replacing every device error with a generic string
             * made standards-based recovery impossible to distinguish from USB failure. */
            send_error(request_id, -EIO,
                       response[0] ? response : "AT command failed without a modem response");
        }
        else send_response(request_id, 0, response, (uint32_t)strlen(response));
        return;
    }
    if (type == MDD_IO_RAW_EXCHANGE) {
        if (state.control_index < 0 || !state.control_claimed || !length || length > 65535) {
            send_error(request_id, -ENODEV, "private raw AT control is unavailable");
            return;
        }
        char response[8192];
        if (raw_exchange(&state.channels[state.control_index], payload, length,
                         response, sizeof(response), 190000) < 0)
            send_error(request_id, -EIO, "raw modem exchange failed");
        else
            send_response(request_id, 0, response, (uint32_t)strlen(response));
        return;
    }
    if (type == MDD_IO_ISOLATION_CHECK) {
        int result = isolation_still_proven();
        if (result == LIBUSB_SUCCESS)
            send_response(request_id, 0, NULL, 0);
        else
            send_error(request_id, result,
                       "isolation_not_proven: USB ownership or descriptor changed");
        return;
    }
    if (!state.ppp_up) {
        send_error(request_id, -ENETDOWN, "private cellular link is down");
        return;
    }
    if (type == MDD_IO_DNS_SERVER) {
        const ip_addr_t *server = dns_getserver(0);
        if (!server || ip_addr_isany(server) || !IP_IS_V4(server))
            send_error(request_id, -ENOENT, "PPP did not negotiate an IPv4 DNS server");
        else
            send_response(request_id, 0, ip_2_ip4(server), 4);
        return;
    }
    if (type == MDD_IO_RESOLVE) {
        if (!length || length > 253) {
            send_error(request_id, -EINVAL, "invalid DNS name");
            return;
        }
        char hostname[254];
        memcpy(hostname, payload, length);
        hostname[length] = '\0';
        struct pending_dns *pending = calloc(1, sizeof(*pending));
        if (!pending) { send_error(request_id, -ENOMEM, "out of memory"); return; }
        pending->kind = PENDING_RESOLVE;
        pending->request_id = request_id;
        begin_dns(pending, hostname);
        return;
    }
    if (type == MDD_IO_TCP_OPEN) {
        char hostname[254];
        uint16_t port;
        size_t offset;
        if (parse_target(payload, length, hostname, sizeof(hostname), &port, &offset) < 0 ||
            offset != length) {
            send_error(request_id, -EINVAL, "invalid private TCP target");
            return;
        }
        struct tcp_handle *entry = NULL;
        for (size_t i = 0; i < MAX_TCP_HANDLES; ++i)
            if (!state.tcp[i].handle) { entry = &state.tcp[i]; break; }
        if (!entry) { send_error(request_id, -ENOSPC, "private TCP handle limit reached"); return; }
        entry->handle = ++state.next_handle;
        entry->open_request = request_id;
        entry->pcb = tcp_new_ip_type(IPADDR_TYPE_V4);
        if (!entry->pcb) { clear_tcp(entry); send_error(request_id, -ENOMEM, "out of memory"); return; }
        tcp_arg(entry->pcb, entry);
        tcp_recv(entry->pcb, tcp_received_callback);
        tcp_err(entry->pcb, tcp_error_callback);
        struct pending_dns *pending = calloc(1, sizeof(*pending));
        if (!pending) {
            tcp_abort(entry->pcb); clear_tcp(entry);
            send_error(request_id, -ENOMEM, "out of memory"); return;
        }
        pending->kind = PENDING_TCP;
        pending->request_id = request_id;
        pending->handle = entry->handle;
        pending->port = port;
        begin_dns(pending, hostname);
        return;
    }
    if (type == MDD_IO_TCP_WRITE) {
        if (length < 4) { send_error(request_id, -EINVAL, "short TCP write"); return; }
        uint32_t handle_be;
        memcpy(&handle_be, payload, 4);
        struct tcp_handle *entry = find_tcp(ntohl(handle_be));
        if (!entry || !entry->connected) { send_error(request_id, -ENOENT, "unknown TCP handle"); return; }
        err_t rc = tcp_write(entry->pcb, payload + 4, length - 4, TCP_WRITE_FLAG_COPY);
        if (rc == ERR_OK) rc = tcp_output(entry->pcb);
        if (rc != ERR_OK) send_error(request_id, rc, "private TCP write failed");
        else send_response(request_id, 0, NULL, 0);
        return;
    }
    if (type == MDD_IO_TCP_CLOSE) {
        if (length != 4) { send_error(request_id, -EINVAL, "invalid TCP close"); return; }
        uint32_t handle_be;
        memcpy(&handle_be, payload, 4);
        struct tcp_handle *entry = find_tcp(ntohl(handle_be));
        if (entry && entry->pcb) {
            tcp_arg(entry->pcb, NULL);
            if (tcp_close(entry->pcb) != ERR_OK) tcp_abort(entry->pcb);
            clear_tcp(entry);
        }
        send_response(request_id, 0, NULL, 0);
        return;
    }
    if (type == MDD_IO_UDP_OPEN) {
        struct udp_handle *entry = NULL;
        for (size_t i = 0; i < MAX_UDP_HANDLES; ++i)
            if (!state.udp[i].handle) { entry = &state.udp[i]; break; }
        if (!entry) { send_error(request_id, -ENOSPC, "private UDP handle limit reached"); return; }
        entry->pcb = udp_new_ip_type(IPADDR_TYPE_V4);
        if (!entry->pcb) { send_error(request_id, -ENOMEM, "out of memory"); return; }
        entry->handle = ++state.next_handle;
        udp_recv(entry->pcb, udp_received_callback, entry);
        uint32_t handle_be = htonl(entry->handle);
        send_response(request_id, 0, &handle_be, 4);
        return;
    }
    if (type == MDD_IO_UDP_SEND) {
        if (length < 8) { send_error(request_id, -EINVAL, "short UDP send"); return; }
        uint32_t handle_be;
        memcpy(&handle_be, payload, 4);
        uint32_t handle = ntohl(handle_be);
        char hostname[254];
        uint16_t port;
        size_t offset;
        if (!find_udp(handle) || parse_target(payload + 4, length - 4, hostname,
                                               sizeof(hostname), &port, &offset) < 0) {
            send_error(request_id, -EINVAL, "invalid private UDP target"); return;
        }
        offset += 4;
        if (length - offset > UINT16_MAX) {
            send_error(request_id, -EMSGSIZE, "private UDP datagram is too large"); return;
        }
        struct pending_dns *pending = calloc(1, sizeof(*pending));
        if (!pending) { send_error(request_id, -ENOMEM, "out of memory"); return; }
        pending->kind = PENDING_UDP;
        pending->request_id = request_id;
        pending->handle = handle;
        pending->port = port;
        pending->data_length = length - offset;
        pending->data = malloc(pending->data_length);
        if (pending->data_length && !pending->data) {
            free(pending); send_error(request_id, -ENOMEM, "out of memory"); return;
        }
        if (pending->data_length) memcpy(pending->data, payload + offset, pending->data_length);
        begin_dns(pending, hostname);
        return;
    }
    if (type == MDD_IO_UDP_CLOSE) {
        if (length != 4) { send_error(request_id, -EINVAL, "invalid UDP close"); return; }
        uint32_t handle_be;
        memcpy(&handle_be, payload, 4);
        struct udp_handle *entry = find_udp(ntohl(handle_be));
        if (entry) {
            if (entry->pcb) udp_remove(entry->pcb);
            entry->pcb = NULL;
            entry->handle = 0;
        }
        send_response(request_id, 0, NULL, 0);
        return;
    }
    send_error(request_id, -ENOTSUP, "unsupported private cellular request");
}

static int read_ipc(void) {
    if (state.ipc_used == sizeof(state.ipc_buffer)) return -1;
    ssize_t count = read(state.ipc_fd, state.ipc_buffer + state.ipc_used,
                         sizeof(state.ipc_buffer) - state.ipc_used);
    if (count < 0 && (errno == EAGAIN || errno == EINTR)) return 0;
    if (count <= 0) return -1;
    state.ipc_used += (size_t)count;
    size_t consumed = 0;
    while (state.ipc_used - consumed >= sizeof(struct mdd_io_header)) {
        struct mdd_io_header header;
        memcpy(&header, state.ipc_buffer + consumed, sizeof(header));
        uint32_t length = ntohl(header.length_be);
        if (header.version != MDD_IO_VERSION || length > MDD_IO_MAX_FRAME) return -1;
        size_t frame_length = sizeof(header) + (size_t)length;
        if (state.ipc_used - consumed < frame_length) break;
        handle_request(header.type, ntohl(header.request_id_be),
                       state.ipc_buffer + consumed + sizeof(header), length);
        consumed += frame_length;
    }
    if (consumed) {
        memmove(state.ipc_buffer, state.ipc_buffer + consumed, state.ipc_used - consumed);
        state.ipc_used -= consumed;
    }
    return 0;
}

static void close_network_handles(void) {
    for (size_t i = 0; i < MAX_TCP_HANDLES; ++i) {
        if (state.tcp[i].pcb) {
            tcp_arg(state.tcp[i].pcb, NULL);
            tcp_abort(state.tcp[i].pcb);
        }
        clear_tcp(&state.tcp[i]);
    }
    for (size_t i = 0; i < MAX_UDP_HANDLES; ++i) {
        if (state.udp[i].pcb) udp_remove(state.udp[i].pcb);
        state.udp[i].pcb = NULL;
        state.udp[i].handle = 0;
    }
}

static void cleanup(void) {
    close_network_handles();
    release_data_channel();
    if (state.control_claimed && state.control_index >= 0)
        libusb_release_interface(state.usb_handle,
                                 state.channels[state.control_index].interface_number);
    state.control_claimed = 0;
    if (state.usb_handle) libusb_close(state.usb_handle);
    if (state.usb_context) libusb_exit(state.usb_context);
    if (state.ipc_fd >= 0) close(state.ipc_fd);
    if (state.watch_fd >= 0) close(state.watch_fd);
}

static int parse_number(const char *value, long minimum, long maximum, long *result) {
    char *end = NULL;
    errno = 0;
    long number = strtol(value, &end, 0);
    if (errno || !end || *end || number < minimum || number > maximum) return -1;
    *result = number;
    return 0;
}

static int list_excludes(int argc, char **argv, uint8_t bus, uint8_t address) {
    for (int i = 2; i + 1 < argc; i += 2) {
        unsigned int excluded_bus = 0, excluded_address = 0;
        char tail = '\0';
        if (strcmp(argv[i], "--exclude") ||
            sscanf(argv[i + 1], "%u:%u%c", &excluded_bus, &excluded_address, &tail) != 2 ||
            excluded_bus > 255 || excluded_address > 255)
            return -1;
        if (bus == excluded_bus && address == excluded_address) return 1;
    }
    return 0;
}

static int list_devices(int argc, char **argv) {
    if ((argc - 2) % 2) return 2;
    if (libusb_init(&state.usb_context) != LIBUSB_SUCCESS) return 3;
    libusb_device **devices = NULL;
    ssize_t count = libusb_get_device_list(state.usb_context, &devices);
    if (count < 0) { libusb_exit(state.usb_context); return 3; }
    for (ssize_t i = 0; i < count; ++i) {
        struct libusb_device_descriptor descriptor;
        if (libusb_get_device_descriptor(devices[i], &descriptor) != LIBUSB_SUCCESS)
            continue;
        int excluded = list_excludes(
            argc, argv, libusb_get_bus_number(devices[i]),
            libusb_get_device_address(devices[i]));
        if (excluded < 0) {
            libusb_free_device_list(devices, 1);
            libusb_exit(state.usb_context);
            state.usb_context = NULL;
            return 2;
        }
        if (excluded) continue;
        state.channel_count = 0;
        state.control_index = -1;
        state.control_claimed = 0;
        state.usb_handle = NULL;
        state.serial[0] = '\0';
        if (inspect_device(devices[i]) != LIBUSB_SUCCESS ||
            libusb_open(devices[i], &state.usb_handle) != LIBUSB_SUCCESS)
            continue;
        if (descriptor.iSerialNumber) {
            int serial_length = libusb_get_string_descriptor_ascii(
                state.usb_handle, descriptor.iSerialNumber,
                (unsigned char *)state.serial, sizeof(state.serial) - 1);
            if (serial_length > 0) state.serial[serial_length] = '\0';
        }
        if (discover_control_channel() == 0) {
            for (char *cursor = state.serial; *cursor; ++cursor)
                if (*cursor == '\n' || *cursor == '\r' || *cursor == ';') *cursor = '_';
            printf("device vid=%04x pid=%04x bus=%u address=%u serial=%s\n",
                   descriptor.idVendor, descriptor.idProduct,
                   libusb_get_bus_number(devices[i]), libusb_get_device_address(devices[i]),
                   state.serial);
            fflush(stdout);
        }
        if (state.control_claimed)
            libusb_release_interface(state.usb_handle,
                                     state.channels[state.control_index].interface_number);
        libusb_close(state.usb_handle);
        state.usb_handle = NULL;
    }
    libusb_free_device_list(devices, 1);
    libusb_exit(state.usb_context);
    state.usb_context = NULL;
    return 0;
}

int main(int argc, char **argv) {
    memset(&state, 0, sizeof(state));
    state.ipc_fd = -1;
    state.watch_fd = -1;
    state.control_index = -1;
    state.data_index = -1;
    state.next_handle = 100;
    if (argc >= 2 && !strcmp(argv[1], "--list")) return list_devices(argc, argv);
    long value;
    for (int i = 1; i + 1 < argc; i += 2) {
        if (parse_number(argv[i + 1], 0, 65535, &value) < 0) return 2;
        if (!strcmp(argv[i], "--ipc-fd")) state.ipc_fd = (int)value;
        else if (!strcmp(argv[i], "--watch-fd")) state.watch_fd = (int)value;
        else if (!strcmp(argv[i], "--vid")) state.vid = (uint16_t)value;
        else if (!strcmp(argv[i], "--pid")) state.pid = (uint16_t)value;
        else if (!strcmp(argv[i], "--bus")) state.bus = (uint8_t)value;
        else if (!strcmp(argv[i], "--address")) state.address = (uint8_t)value;
        else return 2;
    }
    if (state.ipc_fd < 0 || state.watch_fd < 0 || !state.vid || !state.pid) return 2;
    signal(SIGINT, request_stop);
    signal(SIGTERM, request_stop);
    signal(SIGPIPE, SIG_IGN);
    lwip_init();
    if (libusb_init(&state.usb_context) != LIBUSB_SUCCESS) return 3;
    int rc = open_exact_device();
    if (rc != LIBUSB_SUCCESS) {
        send_error(0, rc, rc == LIBUSB_ERROR_NOT_SUPPORTED ?
                   "isolation_not_proven: USB network function is present" :
                   "requested USB modem attachment is unavailable");
        cleanup();
        return 4;
    }
    if (discover_control_channel() < 0) {
        send_error(0, -ENODEV, "no raw USB AT control function was discovered");
        cleanup();
        return 5;
    }
    send_link_state("down");

    while (!signal_stop && !state.stopping) {
        struct pollfd descriptors[2] = {
            {.fd = state.ipc_fd, .events = POLLIN},
            {.fd = state.watch_fd, .events = POLLIN | POLLHUP},
        };
        int polled = poll(descriptors, 2, 0);
        if (polled < 0 && errno != EINTR) break;
        if (descriptors[1].revents & (POLLIN | POLLHUP | POLLERR)) break;
        if (descriptors[0].revents & (POLLIN | POLLHUP | POLLERR))
            if (read_ipc() < 0) break;
        pump_ppp(20);
        if (state.enable_request && (int32_t)(sys_now() - state.enable_deadline) >= 0) {
            uint32_t request_id = state.enable_request;
            state.enable_request = 0;
            send_error(request_id, -ETIMEDOUT, "private PPP negotiation timed out");
            release_data_channel();
        }
        if (state.ppp_error && state.ppp) {
            close_network_handles();
            release_data_channel();
        }
    }
    cleanup();
    return 0;
}
