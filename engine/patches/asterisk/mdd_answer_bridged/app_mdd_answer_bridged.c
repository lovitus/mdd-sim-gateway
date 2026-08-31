/*
 * SPDX-License-Identifier: GPL-2.0-only
 *
 * MDD exact bridged inbound answer primitive.
 *
 * This module intentionally exposes one AMI action rather than a dialplan application.  Control
 * must first put the still-unanswered IMS channel and an answered media-WebSocket winner into an
 * exact two-party bridge.  The action only queues a copied POD request onto the IMS bridge-channel
 * thread.  That callback revalidates every identity, consumes MDD_INBOUND_ARMED exactly once, and
 * only then invokes ast_raw_answer().
 *
 * Runtime unload is deliberately refused.  A queued bridge callback frame can be discarded when
 * its channel dies, so no pending-counter scheme can prove that all copied function pointers have
 * left bridge queues.  The process lifetime is the module lifetime.
 */

/*** MODULEINFO
	<support_level>extended</support_level>
 ***/

#include "asterisk.h"

#include <ctype.h>

#include "asterisk/astobj2.h"
#include "asterisk/bridge.h"
#include "asterisk/bridge_channel.h"
#include "asterisk/channel.h"
#include "asterisk/manager.h"
#include "asterisk/mdd_admission.h"
#include "asterisk/module.h"
#include "asterisk/pbx.h"

#define MDD_CHANNEL_MAX 240
#define MDD_ID_MAX 160
#define MDD_BRIDGE_MAX 96
#define MDD_OPERATION_MAX 64
#define MDD_EPOCH_MAX 64

struct mdd_answer_request {
	char ims_channel[MDD_CHANNEL_MAX + 1];
	char ims_uniqueid[MDD_ID_MAX + 1];
	char winner_channel[MDD_CHANNEL_MAX + 1];
	char winner_uniqueid[MDD_ID_MAX + 1];
	char bridge_uniqueid[MDD_BRIDGE_MAX + 1];
	char operation_id[MDD_OPERATION_MAX + 1];
	char media_epoch[MDD_EPOCH_MAX + 1];
};

static int bounded_visible(const char *value, size_t maximum)
{
	size_t length;

	if (!value || !(length = strlen(value)) || length > maximum) {
		return 0;
	}
	for (; *value; ++value) {
		if ((unsigned char) *value < 0x21 || (unsigned char) *value > 0x7e) {
			return 0;
		}
	}
	return 1;
}

static int bounded_identifier(const char *value, size_t minimum, size_t maximum)
{
	size_t length;

	if (!value || (length = strlen(value)) < minimum || length > maximum) {
		return 0;
	}
	for (; *value; ++value) {
		if (!isalnum((unsigned char) *value) && *value != '_' && *value != '-'
				&& *value != '.' && *value != ':') {
			return 0;
		}
	}
	return 1;
}

static int lower_hex_operation(const char *value)
{
	size_t index;

	if (!value || strlen(value) != 32) {
		return 0;
	}
	for (index = 0; index < 32; ++index) {
		if (!isdigit((unsigned char) value[index])
				&& (value[index] < 'a' || value[index] > 'f')) {
			return 0;
		}
	}
	return 1;
}

static int opaque_epoch(const char *value)
{
	size_t length;

	if (!value || (length = strlen(value)) < 20 || length > MDD_EPOCH_MAX) {
		return 0;
	}
	for (; *value; ++value) {
		if (!isalnum((unsigned char) *value) && *value != '_' && *value != '-') {
			return 0;
		}
	}
	return 1;
}

static int media_websocket_channel(const char *value)
{
	static const char prefix[] = "WebSocket/mdd_control_media/0x";
	const char *cursor;

	if (!value || strncmp(value, prefix, sizeof(prefix) - 1)) {
		return 0;
	}
	cursor = value + sizeof(prefix) - 1;
	if (!*cursor || strlen(value) > MDD_CHANNEL_MAX) {
		return 0;
	}
	for (; *cursor; ++cursor) {
		if (!isxdigit((unsigned char) *cursor)) {
			return 0;
		}
	}
	return 1;
}

static int variable_equals(struct ast_channel *channel, const char *name, const char *expected)
{
	const char *observed = pbx_builtin_getvar_helper(channel, name);
	return observed && !strcmp(observed, expected);
}

static int validate_headers(const struct message *message)
{
	static const char * const allowed[] = {
		"Action", "ActionID", "Channel", "IMSUniqueid", "WinnerChannel",
		"WinnerUniqueid", "BridgeUniqueid", "OperationID", "MediaEpoch",
	};
	unsigned int seen[ARRAY_LEN(allowed)] = { 0 };
	unsigned int index;

	for (index = 0; index < message->hdrcount; ++index) {
		const char *header = message->headers[index];
		const char *separator = strchr(header, ':');
		size_t name_length;
		unsigned int allowed_index;
		int matched = 0;

		if (!separator || !(name_length = separator - header)) {
			return 0;
		}
		for (allowed_index = 0; allowed_index < ARRAY_LEN(allowed); ++allowed_index) {
			if (strlen(allowed[allowed_index]) == name_length
					&& !strncasecmp(header, allowed[allowed_index], name_length)) {
				if (++seen[allowed_index] != 1) {
					return 0;
				}
				matched = 1;
				break;
			}
		}
		if (!matched) {
			return 0;
		}
	}

	/* ActionID is optional. Every other header is required exactly once. */
	for (index = 0; index < ARRAY_LEN(allowed); ++index) {
		if (index != 1 && seen[index] != 1) {
			return 0;
		}
	}
	return 1;
}

static void publish_result(struct ast_channel *ims, const struct mdd_answer_request *request,
	const char *result, const char *reason)
{
	ast_manager_event(ims, EVENT_FLAG_CALL, "MddAnswerBridgedResult",
		"OperationID: %s\r\n"
		"MediaEpoch: %s\r\n"
		"IMSUniqueid: %s\r\n"
		"WinnerUniqueid: %s\r\n"
		"BridgeUniqueid: %s\r\n"
		"Result: %s\r\n"
		"Reason: %s\r\n",
		request->operation_id, request->media_epoch, request->ims_uniqueid,
		request->winner_uniqueid, request->bridge_uniqueid, result, reason);
}

static void answer_callback(struct ast_bridge_channel *ims_bridge_channel,
	const void *payload, size_t payload_size)
{
	const struct mdd_answer_request *request = payload;
	RAII_VAR(struct ast_bridge_channel *, winner_bridge_channel, NULL, ao2_cleanup);
	RAII_VAR(struct ast_channel *, ims, NULL, ao2_cleanup);
	RAII_VAR(struct ast_channel *, winner, NULL, ao2_cleanup);
	struct ast_bridge *bridge = NULL;
	const char *reason = "identity_mismatch";
	const char *event_result = "failed";
	int admission_allowed;
	int bridge_valid = 0;
	int core_owner_matches = 0;
	int owner_matches = 0;
	int full_matches = 0;
	int consumed = 0;
	int answer_result = -1;
	int answered = 0;

	/* This is the final admission check and intentionally precedes every bridge/channel lock. */
	admission_allowed = ast_mdd_admission_check("call_in");
	if (!request || payload_size != sizeof(*request)) {
		reason = "payload_invalid";
		goto done;
	}
	ims = ast_bridge_channel_get_chan(ims_bridge_channel);
	if (!ims) {
		reason = "channel_missing";
		goto done;
	}

	/* The public helper returns with the exact current bridge locked. */
	ast_bridge_channel_lock_bridge(ims_bridge_channel);
	bridge = ims_bridge_channel->bridge;
	bridge_valid = bridge && !bridge->dissolved && bridge->num_channels == 2
		&& !strcmp(bridge->uniqueid, request->bridge_uniqueid);
	if (bridge_valid) {
		winner_bridge_channel = ast_bridge_channel_peer(ims_bridge_channel);
		if (winner_bridge_channel) {
			ao2_ref(winner_bridge_channel, +1);
			winner = ast_bridge_channel_get_chan(winner_bridge_channel);
		}
	}

	if (winner) {
		ast_channel_lock_both(ims, winner);
	} else {
		ast_channel_lock(ims);
	}
	core_owner_matches = !strcmp(ast_channel_name(ims), request->ims_channel)
		&& !strcmp(ast_channel_uniqueid(ims), request->ims_uniqueid)
		&& variable_equals(ims, "MDD_INBOUND_ARMED", "1")
		&& variable_equals(ims, "MDD_INBOUND_OPERATION", request->operation_id)
		&& variable_equals(ims, "MDD_MEDIA_EPOCH", request->media_epoch)
		&& variable_equals(ims, "MDD_INBOUND_WINNER_ID", request->winner_uniqueid);
	owner_matches = core_owner_matches
		&& variable_equals(ims, "MDD_INBOUND_ATTACH", "1")
		&& variable_equals(ims, "MDD_INBOUND_SOURCE_ID", request->ims_uniqueid)
		&& variable_equals(ims, "MDD_INBOUND_WINNER_CHANNEL", request->winner_channel);
	full_matches = owner_matches && admission_allowed && bridge_valid && winner
		&& !strcmp(ast_channel_name(winner), request->winner_channel)
		&& !strcmp(ast_channel_uniqueid(winner), request->winner_uniqueid)
		&& ast_channel_internal_bridge_channel(ims) == ims_bridge_channel
		&& ast_channel_internal_bridge_channel(winner) == winner_bridge_channel
		&& ast_channel_state(ims) != AST_STATE_UP
		&& ast_channel_state(winner) == AST_STATE_UP
		&& variable_equals(winner, "MDD_INBOUND_WINNER", "1")
		&& variable_equals(winner, "MDD_INBOUND_OPERATION", request->operation_id)
		&& variable_equals(winner, "MDD_MEDIA_EPOCH", request->media_epoch)
		&& variable_equals(winner, "MDD_INBOUND_SOURCE_ID", request->ims_uniqueid);

	if (core_owner_matches) {
		/* Every owned callback consumes the arm, including final admission/identity failures. */
		pbx_builtin_setvar_helper(ims, "MDD_INBOUND_ARMED", "0");
		consumed = 1;
		if (!admission_allowed) {
			pbx_builtin_setvar_helper(ims, "MDD_INBOUND_ANSWER_RESULT", "denied");
			event_result = "denied";
			reason = "admission_denied";
		} else if (!full_matches) {
			pbx_builtin_setvar_helper(ims, "MDD_INBOUND_ANSWER_RESULT", "failed");
			event_result = "failed";
			reason = bridge_valid ? "identity_mismatch" : "bridge_mismatch";
		} else {
			pbx_builtin_setvar_helper(ims, "MDD_INBOUND_ANSWER_RESULT", "pending");
			event_result = "pending";
			reason = "answer_failed";
		}
	}
	if (winner) {
		ast_channel_unlock(winner);
	}
	ast_channel_unlock(ims);

	ast_bridge_unlock(bridge);
	bridge = NULL;

	/* ast_raw_answer() locks the channel and synchronously enters the PJSIP serializer. */
	if (full_matches) {
		answer_result = ast_raw_answer(ims);
		ast_channel_lock(ims);
		if (answer_result == 0 && ast_channel_state(ims) == AST_STATE_UP) {
			pbx_builtin_setvar_helper(ims, "MDD_INBOUND_ANSWER_RESULT", "answered");
			pbx_builtin_setvar_helper(ims, "DIALSTATUS", "ANSWER");
			reason = "answered";
			event_result = "answered";
			answered = 1;
		} else {
			pbx_builtin_setvar_helper(ims, "MDD_INBOUND_ANSWER_RESULT", "failed");
			event_result = "failed";
			reason = "answer_failed";
		}
		ast_channel_unlock(ims);
	}

done:
	if (ims && consumed) {
		publish_result(ims, request, answered ? "answered" : event_result, reason);
	}
	ast_module_unref(AST_MODULE_SELF);
	return;
}

static int manager_answer_bridged(struct mansession *session, const struct message *message)
{
	struct mdd_answer_request request = { { 0 } };
	RAII_VAR(struct ast_channel *, ims, NULL, ao2_cleanup);
	RAII_VAR(struct ast_bridge_channel *, bridge_channel, NULL, ao2_cleanup);
	const char *channel = astman_get_header(message, "Channel");
	const char *ims_uniqueid = astman_get_header(message, "IMSUniqueid");
	const char *winner_channel = astman_get_header(message, "WinnerChannel");
	const char *winner_uniqueid = astman_get_header(message, "WinnerUniqueid");
	const char *bridge_uniqueid = astman_get_header(message, "BridgeUniqueid");
	const char *operation_id = astman_get_header(message, "OperationID");
	const char *media_epoch = astman_get_header(message, "MediaEpoch");
	int admission_allowed;
	int core_owner_matches;
	int full_owner_matches;

	if (!validate_headers(message)
			|| !bounded_visible(channel, MDD_CHANNEL_MAX)
			|| !bounded_identifier(ims_uniqueid, 1, MDD_ID_MAX)
			|| !media_websocket_channel(winner_channel)
			|| !bounded_identifier(winner_uniqueid, 1, MDD_ID_MAX)
			|| !bounded_identifier(bridge_uniqueid, 1, MDD_BRIDGE_MAX)
			|| !lower_hex_operation(operation_id)
			|| !opaque_epoch(media_epoch)) {
		astman_send_error(session, message, "Invalid exact bridged-answer request");
		return 0;
	}
	ast_copy_string(request.ims_channel, channel, sizeof(request.ims_channel));
	ast_copy_string(request.ims_uniqueid, ims_uniqueid, sizeof(request.ims_uniqueid));
	ast_copy_string(request.winner_channel, winner_channel, sizeof(request.winner_channel));
	ast_copy_string(request.winner_uniqueid, winner_uniqueid, sizeof(request.winner_uniqueid));
	ast_copy_string(request.bridge_uniqueid, bridge_uniqueid, sizeof(request.bridge_uniqueid));
	ast_copy_string(request.operation_id, operation_id, sizeof(request.operation_id));
	ast_copy_string(request.media_epoch, media_epoch, sizeof(request.media_epoch));

	ims = ast_channel_get_by_name(request.ims_channel);
	if (!ims) {
		astman_send_error(session, message, "IMS channel is unavailable");
		return 0;
	}
	admission_allowed = ast_mdd_admission_check("call_in");
	ast_channel_lock(ims);
	core_owner_matches = !strcmp(ast_channel_uniqueid(ims), request.ims_uniqueid)
		&& ast_channel_state(ims) != AST_STATE_UP
		&& variable_equals(ims, "MDD_INBOUND_ARMED", "1")
		&& variable_equals(ims, "MDD_INBOUND_OPERATION", request.operation_id)
		&& variable_equals(ims, "MDD_MEDIA_EPOCH", request.media_epoch)
		&& variable_equals(ims, "MDD_INBOUND_WINNER_ID", request.winner_uniqueid);
	full_owner_matches = core_owner_matches
		&& variable_equals(ims, "MDD_INBOUND_ATTACH", "1")
		&& variable_equals(ims, "MDD_INBOUND_SOURCE_ID", request.ims_uniqueid)
		&& variable_equals(ims, "MDD_INBOUND_WINNER_CHANNEL", request.winner_channel);
	if (!core_owner_matches) {
		ast_channel_unlock(ims);
		astman_send_error(session, message, "IMS answer identity is stale");
		return 0;
	}
	if (!full_owner_matches) {
		pbx_builtin_setvar_helper(ims, "MDD_INBOUND_ARMED", "0");
		pbx_builtin_setvar_helper(ims, "MDD_INBOUND_ANSWER_RESULT", "failed");
		ast_channel_unlock(ims);
		publish_result(ims, &request, "failed", "owner_revoked");
		astman_send_error(session, message, "IMS answer owner was revoked");
		return 0;
	}
	if (!admission_allowed) {
		pbx_builtin_setvar_helper(ims, "MDD_INBOUND_ARMED", "0");
		pbx_builtin_setvar_helper(ims, "MDD_INBOUND_ANSWER_RESULT", "denied");
		ast_channel_unlock(ims);
		publish_result(ims, &request, "denied", "admission_denied");
		astman_send_error(session, message, "MDD admission denied bridged answer");
		return 0;
	}
	bridge_channel = ast_channel_get_bridge_channel(ims);
	ast_channel_unlock(ims);
	if (!bridge_channel) {
		astman_send_error(session, message, "IMS channel is not bridged");
		return 0;
	}

	/* The callback pointer remains valid for process life; runtime unload is always refused. */
	ast_module_ref(AST_MODULE_SELF);
	if (ast_bridge_channel_queue_callback(
			bridge_channel, 0, answer_callback, &request, sizeof(request))) {
		ast_module_unref(AST_MODULE_SELF);
		astman_send_error(session, message, "Could not queue exact bridged answer");
		return 0;
	}
	astman_send_ack(session, message, "Exact bridged answer queued");
	return 0;
}

static int unload_module(void)
{
	/* See file header: queued callback function pointers make hot unload unverifiable. */
	return -1;
}

static int load_module(void)
{
	if (ast_manager_register(
			"MddAnswerBridged", EVENT_FLAG_CALL, manager_answer_bridged,
			"Queue one exact IMS answer on its bridge-channel thread")) {
		return AST_MODULE_LOAD_DECLINE;
	}
	return AST_MODULE_LOAD_SUCCESS;
}

AST_MODULE_INFO(
	ASTERISK_GPL_KEY,
	AST_MODFLAG_LOAD_ORDER,
	"MDD exact bridged inbound answer",
	.support_level = AST_MODULE_SUPPORT_EXTENDED,
	.load = load_module,
	.unload = unload_module,
	.load_pri = AST_MODPRI_APP_DEPEND,
	.requires = "res_mdd_admission",
);
