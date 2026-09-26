package com.lovitus.mddagent;

import org.json.JSONArray;
import org.json.JSONObject;
import org.junit.Test;
import static org.junit.Assert.*;

public class MessageJournalTest {
    private JSONObject config() throws Exception {
        JSONObject config = new JSONObject();
        MessageJournal.begin(config, "owner", "operation", Json.obj(
            "id", "line-a", "card_id", "test-card", "number", "+15550100123"),
            "vowifi", "+15550100123", "original body");
        return config;
    }

    private JSONObject accepted() {
        return Json.obj("message_id", "operation", "operation_id", "operation",
            "accepted", true, "code", "sent");
    }

    private JSONObject failure() {
        return Json.obj("event_id", "receipt", "message_id", "operation",
            "line_id", "line-a", "transport", "vowifi", "part", 1,
            "kind", "delivery", "state", "failed", "sip_code", 200,
            "rp_cause", 38, "error", "RP cause 38: network out of order");
    }

    @Test public void acceptedSubmissionStillObservesExactDeliveryFailure() throws Exception {
        JSONObject config = config();
        MessageJournal.response(config, "owner", "operation", accepted());
        for (String field : new String[]{"message_id", "line_id", "transport"}) {
            JSONObject other = failure().put(field, "another-owner");
            MessageJournal.observe(config, "owner", new JSONArray().put(other));
            assertEquals("submitted", MessageJournal.find(config, "owner", "operation").getString("state"));
        }
        MessageJournal.observe(config, "other-account", new JSONArray().put(failure()));
        assertEquals("submitted", MessageJournal.find(config, "owner", "operation").getString("state"));

        MessageJournal.observe(config, "owner", new JSONArray().put(failure()));
        JSONObject receipt = MessageJournal.find(new JSONObject(config.toString()), "owner", "operation");
        assertEquals("failure_observed", receipt.getString("state"));
        assertTrue(receipt.getString("failure_detail").contains("RP cause 38"));
        assertEquals("original body", receipt.getString("body"));
        assertEquals("+15550100123", receipt.getString("recipient"));
        String retained = config.toString();
        MessageJournal.observe(config, "owner", new JSONArray().put(failure()));
        assertEquals(retained, config.toString());
    }

    @Test public void lateSubmitResponseCannotEraseObservedFailure() throws Exception {
        JSONObject config = config();
        MessageJournal.observe(config, "owner", new JSONArray().put(failure()));
        assertEquals("failure_observed", MessageJournal.find(config, "owner", "operation").getString("state"));
        MessageJournal.response(config, "owner", "operation", accepted());
        MessageJournal.observe(config, "owner", new JSONArray().put(Json.obj(
            "message_id", "operation", "line_id", "line-a", "transport", "vowifi",
            "kind", "submitted", "state", "accepted")));
        JSONObject retained = MessageJournal.find(config, "owner", "operation");
        assertEquals("failure_observed", retained.getString("state"));
        assertFalse(MessageJournal.resolved(retained));
        assertEquals(1, config.getJSONArray("message_operations").length());
    }
}
