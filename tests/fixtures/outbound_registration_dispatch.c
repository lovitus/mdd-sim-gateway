#include "asterisk/res_pjsip.h"

struct sip_outbound_registration_client_state {
	int status;
	int retries;
	struct pj_timer_entry timer;
};

static int handle_client_registration(void *data)
{
	RAII_VAR(struct sip_outbound_registration_client_state *, client_state, data, ao2_cleanup);
	pjsip_tx_data *tdata;
	if (pjsip_regc_register(client_state->client, PJ_FALSE, &tdata) != PJ_SUCCESS) {
		return 0;
	}
	registration_client_send(client_state, tdata);
	return 0;
}

static void sip_outbound_registration_timer_cb(
	pj_timer_heap_t *timer_heap, struct pj_timer_entry *entry)
{
	struct sip_outbound_registration_client_state *client_state = entry->user_data;
	entry->id = 0;
	ast_sip_push_task(client_state->serializer, handle_client_registration, client_state);
}

static void schedule_registration(
	struct sip_outbound_registration_client_state *client_state, unsigned int seconds)
{
	pj_time_val delay = { .sec = seconds, };
	cancel_registration(client_state);
	pjsip_endpt_schedule_timer(ast_sip_get_pjsip_endpoint(), &client_state->timer, &delay);
}

static int registration_transport_shutdown_cb(void *data)
{
	struct sip_outbound_registration_state *state = data;
	cancel_registration(state->client_state);
	handle_client_registration(state->client_state);
	return 0;
}

static int ami_register(struct mansession *s, const struct message *m)
{
	const char *registration = astman_get_header(m, "Registration");
	return queue_registration(registration);
}
