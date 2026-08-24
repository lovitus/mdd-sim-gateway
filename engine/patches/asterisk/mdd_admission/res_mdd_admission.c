/* SPDX-License-Identifier: GPL-2.0-only */
/* Fail-closed Unix-socket client shared by PJSIP MESSAGE and the dialplan. */

/*** MODULEINFO
	<support_level>extended</support_level>
 ***/

#include "asterisk.h"

#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <poll.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <time.h>
#include <unistd.h>

#include "asterisk/module.h"
#include "asterisk/pbx.h"
#include "asterisk/strings.h"
#include "asterisk/utils.h"

#define AST_API_MODULE
#include "asterisk/mdd_admission.h"

#define MDD_ADMISSION_SOCKET "/run/mdd-sim-gateway/admission-gate.sock"
#define MDD_ADMISSION_TIMEOUT_MS 150
#define MDD_ADMISSION_FRAME_MAX 512

static int operation_valid(const char *operation)
{
	return operation && (!strcmp(operation, "call_in") || !strcmp(operation, "call_out")
		|| !strcmp(operation, "media_check") || !strcmp(operation, "sms_in")
		|| !strcmp(operation, "sms_out"));
}

static long long monotonic_ms(void)
{
	struct timespec now;
	if (clock_gettime(CLOCK_MONOTONIC, &now)) {
		return -1;
	}
	return (long long) now.tv_sec * 1000LL + now.tv_nsec / 1000000LL;
}

static int remaining_ms(long long deadline)
{
	long long now = monotonic_ms();
	long long remaining;
	if (now < 0 || deadline <= now) {
		return 0;
	}
	remaining = deadline - now;
	return remaining > INT_MAX ? INT_MAX : (int) remaining;
}

static int wait_fd(int fd, short events, long long deadline)
{
	struct pollfd pfd = { .fd = fd, .events = events };
	int result;
	do {
		int timeout = remaining_ms(deadline);
		if (!timeout) {
			return -1;
		}
		result = poll(&pfd, 1, timeout);
	} while (result < 0 && errno == EINTR);
	return result == 1 && (pfd.revents & events) ? 0 : -1;
}

static int write_all(int fd, const char *data, size_t length, long long deadline)
{
	size_t offset = 0;
	while (offset < length) {
		ssize_t written;
		if (wait_fd(fd, POLLOUT, deadline)) {
			return -1;
		}
		written = write(fd, data + offset, length - offset);
		if (written < 0 && errno == EINTR) {
			continue;
		}
		if (written <= 0) {
			return -1;
		}
		offset += written;
	}
	return 0;
}

static int read_response(int fd, char *buffer, size_t size, long long deadline)
{
	size_t used = 0;
	while (used + 1 < size) {
		ssize_t count;
		if (wait_fd(fd, POLLIN, deadline)) {
			return -1;
		}
		count = read(fd, buffer + used, size - used - 1);
		if (count < 0 && errno == EINTR) {
			continue;
		}
		if (count < 0) {
			return -1;
		}
		if (!count) {
			break;
		}
		used += count;
	}
	buffer[used] = '\0';
	if (!used || used + 1 == size || buffer[used - 1] != '\n'
			|| memchr(buffer, '\0', used)) {
		return -1;
	}
	return (int) used;
}

static int lower_hex(const char *value, size_t length)
{
	size_t index;
	for (index = 0; index < length; ++index) {
		if (!((value[index] >= '0' && value[index] <= '9')
				|| (value[index] >= 'a' && value[index] <= 'f'))) {
			return 0;
		}
	}
	return value[length] == '\0';
}

static int positive_decimal(const char *value)
{
	const char *cursor = value;
	if (!cursor || !*cursor || *cursor == '0') {
		return 0;
	}
	for (; *cursor; ++cursor) {
		if (*cursor < '0' || *cursor > '9') {
			return 0;
		}
	}
	return 1;
}

int AST_OPTIONAL_API_NAME(ast_mdd_admission_check)(const char *operation)
{
	struct sockaddr_un address = { .sun_family = AF_UNIX };
	const char *path = getenv("MDD_ADMISSION_SOCKET");
	char request[128];
	char response[MDD_ADMISSION_FRAME_MAX];
	char nonce[17];
	char *save = NULL;
	char *parts[7] = { 0 };
	long long deadline;
	int fd = -1;
	int flags;
	int error = 0;
	socklen_t error_length = sizeof(error);
	int result = 0;
	int index;

	if (!operation_valid(operation)) {
		return 0;
	}
	if (ast_strlen_zero(path)) {
		path = MDD_ADMISSION_SOCKET;
	}
	if (strlen(path) >= sizeof(address.sun_path)) {
		return 0;
	}
	ast_copy_string(address.sun_path, path, sizeof(address.sun_path));
	snprintf(nonce, sizeof(nonce), "%08x%08x", (unsigned int) ast_random(),
		(unsigned int) ast_random());
	if (snprintf(request, sizeof(request), "MDD1 %s %s\n", nonce, operation)
			>= (int) sizeof(request)) {
		return 0;
	}
	deadline = monotonic_ms();
	if (deadline < 0) {
		return 0;
	}
	deadline += MDD_ADMISSION_TIMEOUT_MS;
	fd = socket(AF_UNIX, SOCK_STREAM, 0);
	if (fd < 0) {
		return 0;
	}
	flags = fcntl(fd, F_GETFL, 0);
	if (flags < 0 || fcntl(fd, F_SETFL, flags | O_NONBLOCK)) {
		goto done;
	}
	if (connect(fd, (struct sockaddr *) &address, sizeof(address)) && errno != EINPROGRESS) {
		goto done;
	}
	if (wait_fd(fd, POLLOUT, deadline)
			|| getsockopt(fd, SOL_SOCKET, SO_ERROR, &error, &error_length) || error) {
		goto done;
	}
	if (write_all(fd, request, strlen(request), deadline)) {
		goto done;
	}
	shutdown(fd, SHUT_WR);
	if (read_response(fd, response, sizeof(response), deadline) < 0) {
		goto done;
	}
	/* Exactly one newline and six space-delimited fields are required for ALLOW. */
	if (strchr(response, '\n') != strrchr(response, '\n')) {
		goto done;
	}
	response[strlen(response) - 1] = '\0';
	if (!response[0] || response[0] == ' ' || response[strlen(response) - 1] == ' '
			|| strstr(response, "  ")) {
		goto done;
	}
	for (index = 0; index < 7; ++index) {
		parts[index] = strtok_r(index ? NULL : response, " ", &save);
	}
	if (parts[6] || !parts[0] || !parts[1] || !parts[2] || !parts[3]
			|| !parts[4] || !parts[5]) {
		goto done;
	}
	if (strcmp(parts[0], "MDD1") || strcmp(parts[1], nonce)
			|| strcmp(parts[2], "ALLOW") || strlen(parts[3]) != 32
			|| !lower_hex(parts[3], 32) || !positive_decimal(parts[4])
			|| !positive_decimal(parts[5])) {
		goto done;
	}
	result = 1;

done:
	close(fd);
	return result;
}

static int admission_read(struct ast_channel *channel, const char *command, char *data,
	char *buffer, size_t length)
{
	(void) channel;
	(void) command;
	ast_copy_string(buffer, ast_mdd_admission_check(data) ? "ALLOW" : "DENY", length);
	return 0;
}

static struct ast_custom_function admission_function = {
	.name = "MDD_ADMISSION",
	.read = admission_read,
};

static int load_module(void)
{
	return ast_custom_function_register(&admission_function)
		? AST_MODULE_LOAD_DECLINE : AST_MODULE_LOAD_SUCCESS;
}

static int unload_module(void)
{
	return ast_custom_function_unregister(&admission_function);
}

AST_MODULE_INFO(ASTERISK_GPL_KEY,
	AST_MODFLAG_GLOBAL_SYMBOLS | AST_MODFLAG_LOAD_ORDER,
	"MDD monotonic admission gate client",
	.support_level = AST_MODULE_SUPPORT_EXTENDED,
	.load = load_module,
	.unload = unload_module,
	.load_pri = AST_MODPRI_APP_DEPEND - 1,
);
