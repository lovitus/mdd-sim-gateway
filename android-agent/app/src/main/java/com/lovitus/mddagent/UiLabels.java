package com.lovitus.mddagent;

import android.content.Context;
import org.json.JSONObject;

/** Presentation of existing machine fields; never permission or routing logic. */
final class UiLabels {
    private UiLabels(){}
    static String transport(Context context,String mode){return mode.equals("cellular")?context.getString(R.string.cellular):mode.equals("vowifi")?"VoWiFi":context.getString(R.string.unknown);}
    static String cardSuffix(Context context,String card){return context.getString(R.string.card_suffix,card.substring(Math.max(0,card.length()-4)));}
    static String line(Context context,JSONObject line){
        String name=line.optString("name"),number=line.optString("number"),card=line.optString("card_id");
        if(name.isEmpty())name=line.optString("id");
        return name+"\n"+(number.isEmpty()?context.getString(R.string.number_unavailable):number)+
            (card.isEmpty()?"":" · "+cardSuffix(context,card))+(line.optBoolean("enabled")?"":"\n"+context.getString(R.string.line_disabled));
    }
    static UiText unconfirmedCallState(String code){
        int label;
        switch(code){
            case "active":label=R.string.state_active;break;
            case "preparing":case "prepared":case "ready":label=R.string.state_preparing;break;
            case "dialing":case "ringing":case "ringing_out":label=R.string.state_dialing;break;
            case "ending":label=R.string.state_ending;break;
            case "ended":case "terminal":case "hangup_unconfirmed":case "ending_unconfirmed":label=R.string.state_end_unconfirmed;break;
            default:label=R.string.state_unknown;
        }
        return UiText.of(label);
    }
    static String messageState(Context context,String code){
        int label;
        switch(code){
            case "submitted":case "sent":label=R.string.message_submitted;break;
            case "not_dispatched":label=R.string.message_not_sent;break;
            case "submission_observed":label=R.string.message_partial;break;
            case "received":label=R.string.message_received;break;
            case "delivered":label=R.string.message_delivered;break;
            case "failed":label=R.string.message_failed;break;
            default:label=R.string.message_unknown;
        }
        return context.getString(label);
    }
    static String messageEvent(Context context,JSONObject event){
        String kind=event.optString("kind"),state=event.optString("state"),label;
        if(kind.equals("delivery"))label=context.getString(state.equals("delivered")?R.string.message_delivered:state.equals("failed")?R.string.message_failed:state.equals("pending")?R.string.message_delivery_pending:R.string.message_delivery_unknown);
        else label=messageState(context,kind);
        return event.optInt("part")>0?context.getString(R.string.message_part,event.optInt("part"),label):label;
    }
    static String readinessReason(Context context,JSONObject reason){
        String layer=reason.optString("layer"),code=reason.optString("code");int label;
        switch(layer){
            case "intent":case "vowifi_intent":label=R.string.layer_intent;break;
            case "agent_link":label=R.string.layer_agent;break;
            case "hardware":label=R.string.layer_hardware;break;
            case "card":case "card_route":label=R.string.layer_card;break;
            case "pin":label=R.string.layer_pin;break;
            case "cellular_data":label=R.string.layer_cellular_data;break;
            case "cellular_voice":label=R.string.layer_cellular_voice;break;
            case "cellular_sms":case "messaging":label=R.string.layer_messages;break;
            case "engine_process":case "vowifi_runtime":label=R.string.layer_provider;break;
            case "tunnel":label=R.string.layer_tunnel;break;
            case "ims":case "ims_voice":label=R.string.layer_ims;break;
            case "media":label=R.string.layer_media;break;
            case "admission":case "call":label=R.string.layer_call;break;
            default:label=R.string.layer_gateway;
        }
        return context.getString(label)+" · "+(code.isEmpty()?layer:code);
    }
}
