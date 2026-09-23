package com.lovitus.mddagent;
import org.json.*;
import org.junit.Test;
import static org.junit.Assert.*;
public class ReaderObservationTest {
    @Test public void snapshotIsImmutableAndBoundToOwnerEpochAndFreshness()throws Exception {
        Object owner=new Object();JSONObject facts=Json.obj("reader_condition","ready","readers",new JSONArray().put(Json.obj("card_id","original")));
        ReaderObservation view=new ReaderObservation(owner,7,1000,facts);facts.getJSONArray("readers").getJSONObject(0).put("card_id","changed");
        JSONObject first=view.snapshot(owner,7,2000);assertEquals("original",first.getJSONArray("readers").getJSONObject(0).getString("card_id"));first.put("reader_condition","mutated");
        assertEquals("ready",view.snapshot(owner,7,2000).getString("reader_condition"));
        for(JSONObject bad:new JSONObject[]{view.snapshot(new Object(),7,2000),view.snapshot(owner,8,2000),view.snapshot(owner,7,22000),view.snapshot(owner,7,999)}){
            assertEquals("recovering",bad.getString("reader_condition"));assertEquals(0,bad.getJSONArray("readers").length());
        }
    }
}
