package com.lovitus.mddagent;

import android.app.UiAutomation;
import android.content.Context;
import android.content.Intent;
import android.os.SystemClock;
import android.view.View;
import android.view.accessibility.AccessibilityNodeInfo;
import android.widget.EditText;
import android.widget.TextView;
import androidx.test.core.app.ActivityScenario;
import androidx.test.core.app.ApplicationProvider;
import androidx.test.ext.junit.runners.AndroidJUnit4;
import androidx.test.platform.app.InstrumentationRegistry;
import okhttp3.Response;
import okhttp3.WebSocket;
import okhttp3.WebSocketListener;
import okhttp3.mockwebserver.*;
import okhttp3.tls.*;
import org.json.*;
import org.junit.Test;
import org.junit.runner.RunWith;
import java.io.File;
import java.util.*;
import java.util.concurrent.*;
import java.util.concurrent.atomic.*;
import static org.junit.Assert.*;

/** PR #12 counterexamples. Runs against the unchanged baseline too; no real SIM or SMS. */
@RunWith(AndroidJUnit4.class)
public class ReviewRegressionTest {
    private final Context context=ApplicationProvider.getApplicationContext();

    @Test public void confirmedFailedSubmissionsRemainResolvedAcrossReopenAndCapacity()throws Exception{
        JSONObject line=Json.obj("id","line","card_id","card");
        char[] body=new char[2048];Arrays.fill(body,'x');
        for(boolean failureFirst:new boolean[]{false,true}){
            JSONObject config=new JSONObject();
            for(int i=0;i<128;i++){
                String id="message-"+i;
                MessageJournal.begin(config,"owner",id,line,"vowifi","+15550100123",new String(body));
                JSONArray failed=new JSONArray().put(Json.obj("line_id","line","message_id",id,"transport","vowifi","kind","delivery","part",1,"state","failed","error","RP cause 38"));
                if(failureFirst)MessageJournal.observe(config,"owner",failed);
                MessageJournal.response(config,"owner",id,Json.obj("message_id",id,"operation_id",id,"accepted",true,"code","sent"));
                MessageJournal.observe(config,"owner",failed);
            }
            config=new JSONObject(config.toString());
            JSONObject retained=MessageJournal.find(config,"owner","message-127");
            assertEquals("failure_observed",retained.getString("state"));
            assertEquals("RP cause 38",retained.getString("failure_detail"));
            assertTrue("N1: delivery failure must not erase whole-submission confirmation",MessageJournal.resolved(retained));
            MessageJournal.begin(config,"owner","new-intent",line,"vowifi","+15550100123","new body");
            assertEquals(128,config.getJSONArray("message_operations").length());
            assertFalse(MessageJournal.resolved(MessageJournal.find(config,"owner","new-intent")));
        }
        JSONObject old=Json.obj("scope","owner","operation_id","legacy","message_id","legacy","line_id","line","transport","vowifi","state","submitted");
        JSONObject migration=Json.obj("message_operations",new JSONArray().put(old));
        MessageJournal.observe(migration,"owner",new JSONArray().put(Json.obj("line_id","line","message_id","legacy","transport","vowifi","kind","delivery","state","failed")));
        assertTrue(MessageJournal.resolved(MessageJournal.find(migration,"owner","legacy")));
        for(String state:new String[]{"failure_observed","submission_observed","unknown"}){
            JSONObject unknown=new JSONObject();
            JSONObject legacy=new JSONObject(old.toString());legacy.remove("submission_state");legacy.put("state",state);
            unknown.put("message_operations",new JSONArray().put(legacy));
            MessageJournal.observe(unknown,"owner",new JSONArray().put(Json.obj("line_id","line","message_id","legacy","transport","vowifi","kind","submitted","state","accepted")));
            assertFalse("Part receipts cannot reconstruct a lost whole response",MessageJournal.resolved(MessageJournal.find(unknown,"owner","legacy")));
        }
        assertFalse(MessageJournal.resolved(Json.obj("state","submitted","submission_state","future-format")));
        JSONObject full=new JSONObject();
        for(int i=0;i<128;i++)MessageJournal.begin(full,"owner","unknown-"+i,line,"vowifi");
        try{MessageJournal.begin(full,"owner","overflow",line,"vowifi");fail("Unknown submissions must remain protected");}catch(IllegalStateException expected){assertEquals(128,full.getJSONArray("message_operations").length());}
    }

    @Test public void historyWithoutCardRequiresSelectionAndRechecksAfterConfirmation()throws Exception{
        ConfigStore store=new ConfigStore(context);store.clear();
        HeldCertificate certificate=new HeldCertificate.Builder().addSubjectAlternativeName("localhost").build();
        AtomicReference<String> card=new AtomicReference<>("card-B");AtomicInteger posts=new AtomicInteger();
        CountDownLatch linked=new CountDownLatch(1);
        JSONObject event=Json.obj("line_id","line","message_id","received-on-A","kind","received","sender","+15550100999","transport","vowifi","body","old message","received_at","2026-09-27T00:00:00Z");
        assertFalse("Keep the actual history contract",event.has("card_id"));
        try(MockWebServer gateway=new MockWebServer()){
            gateway.useHttps(new HandshakeCertificates.Builder().heldCertificate(certificate).build().sslSocketFactory(),false);
            gateway.setDispatcher(new okhttp3.mockwebserver.Dispatcher(){
                private JSONObject line(){return Json.obj("id","line","line_id","line","name","Current fixture SIM","card_id",card.get(),"enabled",true,"sim",Json.obj("msisdn","+15550100123"),"operations",Json.obj("vowifi_sms",Json.obj("ready",true)));}
                @Override public MockResponse dispatch(RecordedRequest request){
                    if(!"GET".equals(request.getMethod()))posts.incrementAndGet();
                    String path=request.getPath();
                    if(path.equals("/v1/mobile/ws"))return new MockResponse().withWebSocketUpgrade(new WebSocketListener(){@Override public void onOpen(WebSocket socket,Response response){socket.send(Json.obj("type","mobile.snapshot","schema_version",1,"sequence",1,"data",Json.obj("lines",new JSONArray().put(line()),"messages",new JSONArray().put(event),"incoming_lines",new JSONArray(),"cellular_calls",new JSONArray())).toString());linked.countDown();}});
                    if(path.equals("/api/auth/status"))return json(Json.obj("authenticated",true,"username","fixture-admin","token","fixture-session","csrf","fixture-csrf"));
                    if(path.equals("/v1/catalog/lines")||path.equals("/v1/lines"))return json(Json.obj("lines",new JSONArray().put(line())));
                    if(path.equals("/v1/catalog/lines/line")||path.equals("/v1/lines/line"))return json(line());
                    return new MockResponse().setResponseCode(404).setBody("{}");
                }
            });gateway.start();
            store.save(Json.obj("server",gateway.url("/").toString().replaceAll("/$",""),"pin",Json.sha(certificate.certificate().getEncoded()),"token","fixture-session","csrf","fixture-csrf","available",true));
            try(ActivityScenario<MainActivity> scene=ActivityScenario.launch(MainActivity.class)){
                assertTrue(linked.await(10,TimeUnit.SECONDS));
                click(scene,R.id.tab_messages);await(scene,a->a.findViewById(R.id.message_reply)!=null);
                scene.onActivity(a->{TextView identity=a.findViewById(R.id.message_history_identity);assertNotNull("N2: historical identity must be distinguished from the current SIM",identity);assertTrue(identity.getText().toString().contains(context.getString(R.string.message_historical_card_unknown)));((EditText)a.findViewById(R.id.message_body)).setText("keep my draft");});
                click(scene,R.id.message_reply);waitText(context.getString(R.string.message_reply_choose));clickText("Current fixture SIM");
                // Rebind while the user is reading the first confirmation.
                card.set("card-C");dialogButton(true);dialogButton(true);
                waitText(context.getString(R.string.message_reply_unavailable));dialogButton(true);
                scene.onActivity(a->assertEquals("keep my draft",((EditText)a.findViewById(R.id.message_body)).getText().toString()));
                assertEquals(0,posts.get());
                click(scene,R.id.message_reply);waitText(context.getString(R.string.message_reply_choose));clickText("Current fixture SIM");dialogButton(true);dialogButton(true);
                await(scene,a->((EditText)a.findViewById(R.id.message_body)).getText().length()==0);
                scene.onActivity(a->assertEquals("+15550100999",((EditText)a.findViewById(R.id.dial_number)).getText().toString()));
                assertEquals("Reply only prepares a new intent; never sends",0,posts.get());
            }finally{stopAgent();}
        }finally{new ConfigStore(context).clear();}
    }

    @Test public void interruptedFirstWriteHasCancelableArchivedPausedRecovery()throws Exception{
        AndroidStateIO files=new AndroidStateIO();File base=new File(context.getNoBackupFilesDir(),"private-state-v1");
        ConfigStore store=new ConfigStore(context);store.clear();store.save(Json.obj("fixture","original"));
        byte[] original=files.read(base);File owned=new File(base+".owned"),staged=new File(base+".new"),marker=new File(base+".reset");
        byte[] originalMarker=marker.exists()?files.read(marker):null;
        try{
            for(String shape:new String[]{"key-only","marker-only","partial-new"}){
                stopAgent();remove(base);remove(owned);remove(staged);remove(marker);
                if(!shape.equals("key-only"))files.writeSynced(owned,new byte[]{1});
                if(shape.equals("partial-new"))files.writeSynced(staged,new byte[]{1,2,3});
                assertFalse(shape+": no late writer may restore base",base.exists());
                assertEquals(shape+": ownership marker",!shape.equals("key-only"),owned.exists());
                assertEquals(shape+": staged candidate",shape.equals("partial-new"),staged.exists());
                try(ActivityScenario<MainActivity> scene=ActivityScenario.launch(MainActivity.class)){
                    await(scene,a->a.findViewById(R.id.storage_reset)!=null);
                    click(scene,R.id.storage_reset);dialogButton(false);
                    assertFalse("Cancel cannot initialize state",base.exists());
                    click(scene,R.id.storage_reset);dialogButton(true);dialogButton(false);
                    assertFalse("Second confirmation can also be cancelled",base.exists());
                    click(scene,R.id.storage_reset);dialogButton(true);dialogButton(true);
                    await(scene,a->a.findViewById(R.id.connect_gateway)!=null);
                    JSONObject fresh=new ConfigStore(context).load();
                    assertFalse(fresh.optBoolean("available",true));assertFalse(fresh.optBoolean("share",true));assertFalse(fresh.has("token"));
                    File archive=new File(context.getNoBackupFilesDir(),fresh.getString("retained_recovery_archive"));
                    assertTrue(new File(archive,"manifest.json").isFile());
                    if(shape.equals("partial-new"))assertArrayEquals(new byte[]{1,2,3},files.read(new File(archive,"1-private-state-v1.new")));
                    if(!shape.equals("key-only"))assertArrayEquals(new byte[]{1},files.read(new File(archive,"2-private-state-v1.owned")));
                    JSONObject manifest=new JSONObject(new String(files.read(new File(archive,"manifest.json")),java.nio.charset.StandardCharsets.UTF_8));
                    assertFalse(manifest.getJSONArray("files").getJSONObject(0).getBoolean("present"));
                    ConfigStore cleared=new ConfigStore(context);cleared.clear();
                    assertEquals(archive.getName(),cleared.load().getString("retained_recovery_archive"));
                }
            }
            stopAgent();remove(base);remove(owned);remove(staged);remove(marker);
            try(ActivityScenario<MainActivity> scene=ActivityScenario.launch(MainActivity.class)){
                click(scene,R.id.storage_reset);dialogButton(true);
                files.writeSynced(owned,new byte[]{7});
                dialogButton(true);await(scene,a->a.findViewById(R.id.storage_reset)!=null);
                assertFalse("Changed confirmation material must not be overwritten",base.exists());
                assertArrayEquals(new byte[]{7},files.read(owned));

                click(scene,R.id.storage_reset);dialogButton(true);
                CountDownLatch entered=new CountDownLatch(1),release=new CountDownLatch(1);
                Future<?> gate=ConfigStore.intent(()->{entered.countDown();try{assertTrue(release.await(10,TimeUnit.SECONDS));}catch(InterruptedException e){throw new AssertionError(e);}});
                assertTrue(entered.await(10,TimeUnit.SECONDS));
                Future<?> late;
                try{
                    dialogButton(true);
                    // This writer is admitted before reset, but constructs its store afterwards.
                    late=ConfigStore.intent(()->new ConfigStore(context).save(Json.obj("token","stale-login")));
                }finally{release.countDown();}
                gate.get(10,TimeUnit.SECONDS);
                try{late.get(10,TimeUnit.SECONDS);fail("Pre-reset queued writer must not resurrect credentials");}catch(ExecutionException expected){assertTrue(expected.getCause() instanceof IllegalStateException);}
                await(scene,a->a.findViewById(R.id.connect_gateway)!=null);
                assertFalse(new ConfigStore(context).load().has("token"));
                try{store.save(Json.obj("token","stale-instance"));fail("Old store must not write after reset");}catch(IllegalStateException expected){assertFalse(new ConfigStore(context).load().has("token"));}
            }
            stopAgent();
            ConfigStore current=new ConfigStore(context);
            current.update(saved->saved.put("message_operations",new JSONArray().put(Json.obj("operation_id","unknown","state","failure_observed"))));
            File backup=new File(base+".bak");files.writeSynced(backup,files.read(base));files.writeSynced(base,new byte[]{3});
            try(ActivityScenario<MainActivity> scene=ActivityScenario.launch(MainActivity.class)){
                click(scene,R.id.storage_reset);waitText(context.getString(R.string.storage_reset_blocked));dialogButton(true);
                assertArrayEquals(new byte[]{3},files.read(base));
                assertTrue("Known unresolved material must survive",backup.isFile());
            }finally{stopAgent();remove(backup);}
        }finally{stopAgent();remove(staged);remove(marker);files.writeSynced(owned,new byte[]{1});files.writeSynced(base,original);if(originalMarker!=null)files.writeSynced(marker,originalMarker);new ConfigStore(context).retry();new ConfigStore(context).clear();}
    }

    @Test public void rebuiltActivityCannotRestorePreResetCredentialDraft()throws Exception{
        stopAgent();ConfigStore store=new ConfigStore(context);store.clear();
        store.save(Json.obj("login_profile",Json.obj("server","https://gateway.example","username","old-user","password","old-fixture-password","remember",true,"enrollment_origin","https://gateway.example","agent_id","old-reader","agent_token","old-fixture-reader-token")));
        File base=new File(context.getNoBackupFilesDir(),"private-state-v1");AndroidStateIO files=new AndroidStateIO();
        byte[] original=files.read(base);File marker=new File(base+".reset");byte[] originalMarker=marker.exists()?files.read(marker):null;
        try(ActivityScenario<MainActivity> scene=ActivityScenario.launch(MainActivity.class)){
            await(scene,a->a.findViewById(R.id.gateway_password)!=null);
            scene.recreate();await(scene,a->a.findViewById(R.id.gateway_password)!=null);
            scene.onActivity(a->assertEquals("Ordinary recreation keeps the valid draft","old-fixture-password",((EditText)a.findViewById(R.id.gateway_password)).getText().toString()));
            scene.moveToState(androidx.lifecycle.Lifecycle.State.CREATED);
            ConfigStore.intent(()->{}).get(10,TimeUnit.SECONDS);
            files.writeSynced(base,new byte[]{3});
            scene.moveToState(androidx.lifecycle.Lifecycle.State.RESUMED);
            click(scene,R.id.storage_reset);dialogButton(true);
            CountDownLatch entered=new CountDownLatch(1),release=new CountDownLatch(1);
            Future<?> barrier=ConfigStore.intent(()->{entered.countDown();try{assertTrue(release.await(10,TimeUnit.SECONDS));}catch(InterruptedException e){throw new AssertionError(e);}});
            assertTrue(entered.await(10,TimeUnit.SECONDS));
            try{dialogButton(true);scene.recreate();}finally{release.countDown();}
            barrier.get(10,TimeUnit.SECONDS);ConfigStore.intent(()->{}).get(10,TimeUnit.SECONDS);
            // A read admitted before reset may be correctly rejected. Retry from the new UI owner.
            AtomicBoolean retry=new AtomicBoolean();
            await(scene,a->a.findViewById(R.id.storage_reset)!=null||a.findViewById(R.id.gateway_password)!=null);
            scene.onActivity(a->retry.set(a.findViewById(R.id.storage_reset)!=null));
            if(retry.get())clickText(context.getString(R.string.storage_retry));
            await(scene,a->a.findViewById(R.id.gateway_password)!=null);
            scene.onActivity(a->{
                assertEquals("N3: reset invalidates retained credentials","",((EditText)a.findViewById(R.id.gateway_password)).getText().toString());
                assertEquals("",((EditText)a.findViewById(R.id.gateway_username)).getText().toString());
            });
            scene.moveToState(androidx.lifecycle.Lifecycle.State.CREATED);
            ConfigStore.intent(()->{}).get(10,TimeUnit.SECONDS);
            JSONObject fresh=new ConfigStore(context).load(),draft=Json.object(fresh,"login_profile");
            assertEquals("",draft.optString("password"));assertEquals("",draft.optString("agent_token"));
            assertFalse(fresh.optBoolean("available"));assertFalse(fresh.optBoolean("share"));
        }finally{stopAgent();remove(new File(base+".new"));remove(marker);files.writeSynced(base,original);if(originalMarker!=null)files.writeSynced(marker,originalMarker);new ConfigStore(context).retry();new ConfigStore(context).clear();}
    }

    @Test public void storageIntentAdmissionDoesNotBlockTheMainThread()throws Exception{
        stopAgent();ConfigStore store=new ConfigStore(context);store.clear();
        CountDownLatch entered=new CountDownLatch(1),release=new CountDownLatch(1),uiProgress=new CountDownLatch(1);
        Future<?> writing=ConfigStore.intent(()->store.update(saved->{entered.countDown();assertTrue(release.await(10,TimeUnit.SECONDS));}));
        assertTrue(entered.await(10,TimeUnit.SECONDS));boolean responsive;
        try{
            android.os.Handler main=new android.os.Handler(android.os.Looper.getMainLooper());
            main.post(()->{new ConfigStore(context);ConfigStore.intent(()->{});main.post(uiProgress::countDown);});
            responsive=uiProgress.await(2,TimeUnit.SECONDS);
        }finally{release.countDown();}
        writing.get(10,TimeUnit.SECONDS);assertTrue(uiProgress.await(10,TimeUnit.SECONDS));
        ConfigStore.intent(()->{}).get(10,TimeUnit.SECONDS);
        assertTrue("N3: snapshot/admission must not wait for the disk writer lock",responsive);
    }

    private void stopAgent()throws Exception{
        context.stopService(new Intent(context,AgentService.class));
        InstrumentationRegistry.getInstrumentation().waitForIdleSync();
        // Activity.onStop persists the setup draft asynchronously. Drain it before corrupting fixtures.
        ConfigStore.intent(()->{}).get(10,TimeUnit.SECONDS);
        InstrumentationRegistry.getInstrumentation().waitForIdleSync();
    }
    private static void remove(File file)throws Exception{if(file.exists()&&!file.delete())throw new java.io.IOException("Cannot remove fixture "+file.getName());}
    private static MockResponse json(JSONObject value){return new MockResponse().setHeader("Content-Type","application/json").setBody(value.toString());}
    private static void click(ActivityScenario<MainActivity> scene,int id)throws Exception{await(scene,a->a.findViewById(id)!=null);scene.onActivity(a->a.findViewById(id).performClick());}
    private static void await(ActivityScenario<MainActivity> scene,java.util.function.Predicate<MainActivity> predicate)throws Exception{
        long end=SystemClock.elapsedRealtime()+10000;AtomicBoolean found=new AtomicBoolean();
        while(!found.get()&&SystemClock.elapsedRealtime()<end){InstrumentationRegistry.getInstrumentation().waitForIdleSync();scene.onActivity(a->found.set(predicate.test(a)));if(!found.get())SystemClock.sleep(100);}
        assertTrue("Native review regression did not reach its required state",found.get());
    }
    private static UiAutomation automation(){UiAutomation ui=InstrumentationRegistry.getInstrumentation().getUiAutomation();android.accessibilityservice.AccessibilityServiceInfo info=ui.getServiceInfo();info.flags|=android.accessibilityservice.AccessibilityServiceInfo.FLAG_REPORT_VIEW_IDS;ui.setServiceInfo(info);return ui;}
    private static AccessibilityNodeInfo waitNode(String query,boolean id)throws Exception{
        UiAutomation ui=automation();long end=SystemClock.elapsedRealtime()+10000;
        while(SystemClock.elapsedRealtime()<end){ui.waitForIdle(100,5000);AccessibilityNodeInfo root=ui.getRootInActiveWindow();if(root!=null){List<AccessibilityNodeInfo> found=id?root.findAccessibilityNodeInfosByViewId(query):root.findAccessibilityNodeInfosByText(query);for(AccessibilityNodeInfo node:found)if(node.isVisibleToUser())return node;}SystemClock.sleep(100);}
        throw new AssertionError("Native dialog missing: "+query);
    }
    private static void dialogButton(boolean positive)throws Exception{assertTrue(waitNode("android:id/button"+(positive?"1":"2"),true).performAction(AccessibilityNodeInfo.ACTION_CLICK));automation().waitForIdle(200,5000);}
    private static void clickText(String value)throws Exception{AccessibilityNodeInfo node=waitNode(value,false);while(node!=null&&!node.isClickable())node=node.getParent();assertNotNull(node);assertTrue(node.performAction(AccessibilityNodeInfo.ACTION_CLICK));automation().waitForIdle(200,5000);}
    private static void waitText(String value)throws Exception{assertNotNull(waitNode(value,false));}
}
