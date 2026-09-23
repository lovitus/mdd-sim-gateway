package com.lovitus.mddagent;

import org.json.JSONObject;

/** Immutable, owner-scoped observation. Mutable JSON never crosses owner boundaries. */
final class ReaderObservation {
    final Object owner;
    final long epoch, observedAt;
    private final String payload;
    ReaderObservation(Object owner,long epoch,long observedAt,JSONObject topology){
        this.owner=owner;this.epoch=epoch;this.observedAt=observedAt;this.payload=topology.toString();
    }
    JSONObject snapshot(Object current,long generation,long now){
        if(owner==null||owner!=current||epoch!=generation||now<observedAt||now-observedAt>20000)return recovering();
        try{return new JSONObject(payload);}catch(Exception invalid){return recovering();}
    }
    static JSONObject recovering(){return Json.obj("reader_condition","recovering","reader_detail","Fresh reader observation required","readers",new org.json.JSONArray(),"modem_condition","disabled");}
}
