package com.lovitus.mddagent;

import java.nio.charset.StandardCharsets;
import java.util.Locale;
import org.json.JSONObject;

/** Reuses Core providermessages.CellularEventID; never infers a card from its name or number. */
final class MessageIdentity {
    private MessageIdentity(){}
    static String historicalCard(JSONObject event,JSONObject current){
        String recorded=event.optString("card_id");
        if(!recorded.isEmpty())return recorded;
        if(!event.optString("kind").equals("received")||!event.optString("transport").equals("cellular")||
            !event.optString("line_id").equals(current.optString("id")))return "";
        String card=current.optString("card_id"),fingerprint=event.optString("message_id").trim().toLowerCase(Locale.ROOT);
        if(!card.matches("[0-9]{4,32}")||!fingerprint.matches("[0-9a-f]{64}"))return "";
        String expected="cellular-"+Json.sha((card+"\u0000"+fingerprint).getBytes(StandardCharsets.UTF_8));
        return expected.equals(event.optString("event_id"))?card:"";
    }
}
