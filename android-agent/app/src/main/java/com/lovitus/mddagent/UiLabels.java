package com.lovitus.mddagent;

import android.content.Context;
import org.json.JSONObject;

/** Presentation of existing machine fields; never permission or routing logic. */
final class UiLabels {
    static final int OK=0xff0c6b64, WARNING=0xff8a5100, ERROR=0xffb3261e, NEUTRAL=0xff52616b;
    private UiLabels(){}
    static int statusColor(UiText text){
        int id=text.resource;
        if(id==R.string.dtmf_accepted)return OK;
        if(id==R.string.dtmf_failed||id==R.string.dtmf_unavailable)return ERROR;
        if(id==R.string.dtmf_sending||id==R.string.dtmf_unconfirmed)return WARNING;
        if(id==R.string.link_online||id==R.string.link_reader_online||id==R.string.availability_on||id==R.string.reader_count||id==R.string.reader_identity_ready||id==R.string.call_request_accepted||id==R.string.call_ended||id==R.string.call_remote_ended||id==R.string.call_original_ended||id==R.string.call_still_active||id==R.string.audio_connected||id==R.string.audio_live_description||id==R.string.audio_reconnected||id==R.string.audio_focus_resumed||id==R.string.message_received||id==R.string.message_submitted||id==R.string.message_delivered||id==R.string.activity_reader_connected||id==R.string.activity_call_active)return OK;
        if(id==R.string.call_preflight_failed||id==R.string.call_reason||id==R.string.call_rejected||id==R.string.call_check_failed||id==R.string.call_end_unknown||id==R.string.call_prepare_cleanup_unknown||id==R.string.link_schema||id==R.string.link_auth_required||id==R.string.link_upgrade||id==R.string.link_auth_tls||id==R.string.reader_usb_unavailable||id==R.string.reader_usb_write_failed||id==R.string.reader_identity_unavailable||id==R.string.reader_omapi_blocked||id==R.string.settings_unavailable||id==R.string.settings_save_failed||id==R.string.availability_failed||id==R.string.audio_check_timeout||id==R.string.audio_capture_stopped||id==R.string.audio_playback_stopped||id==R.string.audio_invalid_frame||id==R.string.audio_invalid_handshake||id==R.string.audio_lost||id==R.string.audio_focus_lost||id==R.string.audio_focus_pause_failed||id==R.string.audio_reconnect_expired||id==R.string.history_failed||id==R.string.message_failed||id==R.string.message_not_dispatched||id==R.string.enrollment_failed)return ERROR;
        if(id==R.string.link_connecting||id==R.string.link_interrupted||id==R.string.link_wait_network||id==R.string.link_handshake_timeout||id==R.string.link_network_changed||id==R.string.link_heartbeat_missed||id==R.string.link_backpressure||id==R.string.call_preparing||id==R.string.call_result_unknown||id==R.string.call_dispatching||id==R.string.call_end_unconfirmed||id==R.string.call_no_evidence||id==R.string.call_no_active_evidence||id==R.string.audio_checking||id==R.string.audio_reconnecting||id==R.string.audio_focus_suspended||id==R.string.reader_scan_details||id==R.string.reader_identity_pin||id==R.string.reader_identity_partial||id==R.string.reader_link_pending||id==R.string.message_unknown_id||id==R.string.message_saving||id==R.string.activity_reader_disconnected)return WARNING;
        return NEUTRAL;
    }
    static int messageColor(JSONObject message){
        String code=message.optString("kind").equals("delivery")?message.optString("state"):message.optString("kind");
        return code.equals("failed")||code.equals("not_dispatched")?ERROR:code.equals("received")||code.equals("delivered")||code.equals("sent")||code.equals("submitted")?OK:WARNING;
    }
    static String messagePeer(JSONObject message){
        for(String key:new String[]{"sender","recipient","peer"}){String value=message.optString(key).trim();if(!value.isEmpty())return value;}
        return "";
    }
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
