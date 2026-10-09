package com.lovitus.mddagent;

import android.content.*;
import android.os.*;
import android.view.*;
import android.view.accessibility.AccessibilityNodeInfo;
import android.widget.*;
import androidx.test.core.app.*;
import androidx.test.ext.junit.runners.AndroidJUnit4;
import androidx.test.platform.app.InstrumentationRegistry;
import okhttp3.*;
import okhttp3.mockwebserver.*;
import okhttp3.tls.*;
import okio.ByteString;
import org.json.*;
import org.junit.Test;
import org.junit.runner.RunWith;
import java.util.*;
import java.util.concurrent.*;
import java.util.concurrent.atomic.*;
import static org.junit.Assert.*;

/** Full native client against a TLS gateway. No reader, carrier or paid operation. */
@RunWith(AndroidJUnit4.class)
public class CommunicationUxTest {
    private final Context context=ApplicationProvider.getApplicationContext();
    private final String card="8944100000000000001",peer="+15550100999";
    private JSONObject line(){return Json.obj("id","line","line_id","line","name","Server SIM","number","+15550100123","sim",Json.obj("msisdn","+15550100123"),"card_id",card,"enabled",true,
        "operations",Json.obj("vowifi_call",Json.obj("ready",false),"cellular_call",Json.obj("ready",true),"vowifi_sms",Json.obj("ready",true),"cellular_sms",Json.obj("ready",true)));}
    private JSONObject message(String fingerprint,String body){return Json.obj("line_id","line","kind","received","transport","cellular","sender",peer,"message_id",fingerprint,
        "event_id","cellular-"+Json.sha((card+"\u0000"+fingerprint).getBytes(java.nio.charset.StandardCharsets.UTF_8)),"body",body,"observed_at","2026-09-20T00:00:00Z","received_at","2026-09-20T00:00:01Z");}

    @Test public void homeAndLineDotsDescribeServerCommunicationWithoutReader()throws Exception{
        try(Fixture fixture=new Fixture();ActivityScenario<MainActivity> scene=fixture.launch()){
            await(scene,a->a.findViewById(R.id.home_line_state)!=null&&((TextView)a.findViewById(R.id.home_line_state)).getText().toString().contains("1"));
            scene.onActivity(a->{
                assertTrue("Home availability must be an intent switch",a.findViewById(R.id.home_availability_toggle) instanceof CompoundButton);
                assertFalse("A communication-only device never enables reader sharing",fixture.service.sharing());
                a.findViewById(R.id.tab_calls).performClick();
            });
            await(scene,a->a.findViewById(R.id.line_selector)!=null);
            scene.onActivity(a->{
                TextView wifi=a.findViewById(id("line_vowifi_state")),cell=a.findViewById(id("line_cellular_state"));
                assertNotNull("Calls needs an independent VoWiFi state",wifi);assertNotNull("Calls needs an independent cellular state",cell);
                assertTrue(wifi.getText().toString().contains("Unavailable"));assertTrue(cell.getText().toString().contains("Ready"));
                assertEquals(UiLabels.routeColor(R.string.route_unavailable),wifi.getCurrentTextColor());
                assertEquals(UiLabels.routeColor(R.string.route_ready),cell.getCurrentTextColor());
                a.findViewById(R.id.route_cellular).performClick();assertTrue(a.findViewById(R.id.call_dial).isEnabled());
            });
            fixture.service.pause();await(scene,a->!a.findViewById(R.id.call_dial).isEnabled());
            scene.onActivity(a->{assertTrue(((TextView)a.findViewById(id("line_cellular_state"))).getText().toString().contains("Offline"));});
            fixture.assertOnlyMutations();
        }
    }

    @Test public void lineAvailabilitySummaryAndFullTextFollowCallAndSmsStates(){
        InstrumentationRegistry.getInstrumentation().runOnMainSync(()->{
            LineStatusView row=new LineStatusView(context);
            JSONObject item=line();
            row.bind(item,true,false);
            TextView wifi=row.findViewById(R.id.line_vowifi_state),cell=row.findViewById(R.id.line_cellular_state);
            assertEquals("Entire ready status must be colored, not only a tiny dot",UiLabels.routeColor(R.string.route_ready),cell.getCurrentTextColor());
            TextView summary=row.findViewById(R.id.line_availability_summary);
            assertNotNull("Line title needs a visible availability summary",summary);
            assertEquals(context.getString(R.string.line_cellular_available),summary.getText().toString());
            assertTrue(summary.getTypeface().isBold());
            row.bind(item,true,true);
            assertEquals(context.getString(R.string.line_both_available),summary.getText().toString());
            Json.object(item,"operations").remove("cellular_sms");
            row.bind(item,true,true);
            assertEquals(context.getString(R.string.line_vowifi_available),summary.getText().toString());
            assertEquals(UiLabels.routeColor(R.string.route_unknown),cell.getCurrentTextColor());
            for(int state:new int[]{R.string.route_disabled,R.string.route_unknown,R.string.route_connecting,R.string.route_unavailable,R.string.route_busy}){
                JSONObject readiness=Json.obj("ready",false);
                if(state==R.string.route_unknown)readiness=new JSONObject();
                if(state==R.string.route_disabled)readiness=Json.obj("ready",false,"facts",new JSONArray().put(Json.obj("code","vowifi_disabled")));
                if(state==R.string.route_connecting)readiness=Json.obj("ready",false,"facts",new JSONArray().put(Json.obj("code","registering")));
                JSONObject blocked=Json.obj("name","Test SIM","enabled",state!=R.string.route_disabled,
                    "operations",Json.obj("vowifi_call",readiness,"cellular_call",readiness));
                if(state==R.string.route_busy)try{blocked.put("active",Json.obj("call_id","existing-call"));}catch(JSONException e){throw new AssertionError(e);}
                row.bind(blocked,true,false);
                assertEquals(context.getString(state),summary.getText().toString());
                assertEquals(UiLabels.routeColor(state),wifi.getCurrentTextColor());
                assertEquals(UiLabels.routeColor(state),summary.getCurrentTextColor());
            }
            row.bind(item,false,false);
            assertEquals(context.getString(R.string.route_offline),summary.getText().toString());
            assertEquals(UiLabels.routeColor(R.string.route_offline),wifi.getCurrentTextColor());
            assertNotEquals(UiLabels.routeColor(R.string.route_disabled),UiLabels.routeColor(R.string.route_ready));
        });
    }

    @Test public void completeInboxPagesOlderMessagesAndPreselectsProvenOriginalCard()throws Exception{
        try(Fixture fixture=new Fixture();ActivityScenario<MainActivity> scene=fixture.launch()){
            scene.onActivity(a->a.findViewById(R.id.tab_messages).performClick());
            await(scene,a->fixture.conversationReads.get()>0&&contains(a.getWindow().getDecorView(),"Archived preview"),"Default inbox did not read retained conversations");
            assertTrue("The default inbox must read retained conversations, not only mobile recent events",fixture.conversationReads.get()>0);
            clickText("Archived preview");waitText("first history page");
            clickText(context.getString(R.string.older_messages));waitText("older retained SMS");
            assertTrue("Uses the exact immutable cursor",fixture.olderRead.get());
            // The first visible received row is older, still from the same proven card.
            clickText(context.getString(R.string.message_reply));waitText(context.getString(R.string.message_reply_choose));
            AccessibilityNodeInfo list=waitNode(context.getPackageName()+":id/reply_line_list",true);
            assertTrue("Original cellular card must already be checked",hasChecked(list));
            dialog(true);dialog(true);
            await(scene,a->a.findViewById(R.id.message_body)!=null);
            scene.onActivity(a->{
                assertEquals(peer,((EditText)a.findViewById(R.id.dial_number)).getText().toString());
                assertEquals("",((EditText)a.findViewById(R.id.message_body)).getText().toString());
                assertTrue(((Spinner)a.findViewById(R.id.line_selector)).getSelectedItem().toString().contains("Server SIM"));
                assertTrue(a.findViewById(R.id.message_send).isEnabled());
            });
            fixture.assertOnlyMutations();
        }
    }

    @Test public void losingIncomingClientReleasesOnlyItsLeaseEvenAfterCleanupFailure()throws Exception{
        InstrumentationRegistry.getInstrumentation().getUiAutomation().executeShellCommand("pm grant "+context.getPackageName()+" android.permission.RECORD_AUDIO").close();
        try(Fixture fixture=new Fixture();ActivityScenario<MainActivity> scene=fixture.launch()){
            fixture.failFirstRelease=true;
            AtomicReference<RemoteCall> loser=new AtomicReference<>();
            scene.onActivity(a->{try{fixture.service.begin(new CallPlan(line(),"vowifi","",Json.obj("call_id","shared-incoming","caller",peer)));loser.set(fixture.service.call);}catch(Exception e){throw new AssertionError(e);}});
            assertTrue("Real canary must precede Answer",fixture.answer.await(15,TimeUnit.SECONDS));
            await(scene,a->loser.get().state.render(context).contains(context.getString(R.string.call_prepare_cleanup_unknown)),"Rejected admission did not retain its own cleanup failure");
            scene.onActivity(a->loser.get().hangup());
            await(scene,a->fixture.service.call==null);
            assertEquals("Only one answer attempt",1,fixture.paid.get());
            assertEquals("The losing client must never end the winning call",0,fixture.ends.get());
            assertTrue(fixture.releases.get()>=2);
            assertTrue("Original conflict code is retained",fixture.service.notice.render(context).contains("call_busy"));
            fixture.assertOnlyMutations("POST /v1/media/leases","POST /v1/lines/line/vowifi/calls/incoming/answer","DELETE /v1/media/leases");
        }
    }

    @Test public void unknownIncomingAnswerCannotEndAnotherClient()throws Exception{
        InstrumentationRegistry.getInstrumentation().getUiAutomation().executeShellCommand("pm grant "+context.getPackageName()+" android.permission.RECORD_AUDIO").close();
        try(Fixture fixture=new Fixture();ActivityScenario<MainActivity> scene=fixture.launch()){
            fixture.answerStatus=503;fixture.answerCode="call_start_failed";
            AtomicReference<RemoteCall> owner=new AtomicReference<>();
            scene.onActivity(a->{try{fixture.service.begin(new CallPlan(line(),"vowifi","",Json.obj("call_id","shared-incoming","caller",peer)));owner.set(fixture.service.call);}catch(Exception e){throw new AssertionError(e);}});
            assertTrue(fixture.answer.await(15,TimeUnit.SECONDS));
            await(scene,a->owner.get().audio!=null&&owner.get().audio.closed);
            scene.onActivity(a->owner.get().hangup());
            fixture.service.controlIO.submit(()->{}).get(10,TimeUnit.SECONDS);
            assertEquals("Shared call ID and an uncertain response grant no ownership",0,fixture.ends.get());
            assertEquals("Only the local lease is released",1,fixture.releases.get());
            assertSame("An unknown answer is not declared rejected or ended",owner.get(),fixture.service.call);
            assertEquals(1,fixture.paid.get());
            fixture.assertOnlyMutations("POST /v1/media/leases","POST /v1/lines/line/vowifi/calls/incoming/answer","DELETE /v1/media/leases");
        }
    }

    @Test public void acceptedIncomingClientKeepsNormalHangup()throws Exception{
        InstrumentationRegistry.getInstrumentation().getUiAutomation().executeShellCommand("pm grant "+context.getPackageName()+" android.permission.RECORD_AUDIO").close();
        try(Fixture fixture=new Fixture();ActivityScenario<MainActivity> scene=fixture.launch()){
            fixture.answerStatus=200;
            scene.onActivity(a->{try{fixture.service.begin(new CallPlan(line(),"vowifi","",Json.obj("call_id","shared-incoming","caller",peer)));}catch(Exception e){throw new AssertionError(e);}});
            assertTrue(fixture.answer.await(15,TimeUnit.SECONDS));
            await(scene,a->fixture.service.call!=null&&fixture.service.call.canSendTone());
            scene.onActivity(a->fixture.service.hangup());await(scene,a->fixture.service.call==null);
            assertEquals(1,fixture.paid.get());assertEquals(1,fixture.ends.get());assertEquals(1,fixture.releases.get());
            fixture.assertOnlyMutations("POST /v1/media/leases","POST /v1/lines/line/vowifi/calls/incoming/answer","DELETE /v1/media/leases","POST /v1/lines/line/vowifi/calls/end");
        }
    }

    @Test public void activeObservationOutranksFailedSharedHistory()throws Exception{
        InstrumentationRegistry.getInstrumentation().getUiAutomation().executeShellCommand("pm grant "+context.getPackageName()+" android.permission.RECORD_AUDIO").close();
        try(Fixture fixture=new Fixture();ActivityScenario<MainActivity> scene=fixture.launch()){
            fixture.answerStatus=503;fixture.answerCode="call_start_failed";fixture.failedHistory=true;
            AtomicReference<RemoteCall> owner=new AtomicReference<>();
            scene.onActivity(a->{try{fixture.service.begin(new CallPlan(line(),"vowifi","",Json.obj("call_id","shared-incoming","caller",peer)));owner.set(fixture.service.call);}catch(Exception e){throw new AssertionError(e);}});
            assertTrue(fixture.answer.await(15,TimeUnit.SECONDS));await(scene,a->!owner.get().busy());
            java.lang.reflect.Field checking=RemoteCall.class.getDeclaredField("checking");checking.setAccessible(true);
            AtomicBoolean reconciling=(AtomicBoolean)checking.get(owner.get());
            owner.get().reconcile();await(scene,a->fixture.statusReads.get()>0&&!reconciling.get());
            scene.onActivity(a->assertSame("Failed shared history cannot retire a still-active call",owner.get(),fixture.service.call));
            assertEquals(1,fixture.paid.get());assertEquals(0,fixture.ends.get());assertEquals(0,fixture.releases.get());
            fixture.assertOnlyMutations("POST /v1/media/leases","POST /v1/lines/line/vowifi/calls/incoming/answer");
        }
    }

    @Test public void lateAcceptedAnswerHonorsCancellationWithoutReopeningAudio()throws Exception{
        InstrumentationRegistry.getInstrumentation().getUiAutomation().executeShellCommand("pm grant "+context.getPackageName()+" android.permission.RECORD_AUDIO").close();
        CountDownLatch receipt=new CountDownLatch(1),release=new CountDownLatch(1);
        AtomicReference<String> barrierFailure=new AtomicReference<>("");
        try(Fixture fixture=new Fixture();ActivityScenario<MainActivity> scene=fixture.launch()){
            fixture.answerStatus=200;
            // Pause the real HTTP lifecycle after all receipt bytes arrive but
            // before GatewayApi returns them to RemoteCall. No call/API mock.
            java.lang.reflect.Field apiField=AgentService.class.getDeclaredField("api");apiField.setAccessible(true);
            GatewayApi api=(GatewayApi)apiField.get(fixture.service);
            java.lang.reflect.Field httpField=GatewayApi.class.getDeclaredField("http");httpField.setAccessible(true);
            httpField.set(api,api.http.newBuilder().eventListener(new okhttp3.EventListener(){
                @Override public void callEnd(Call call){if(call.request().url().encodedPath().endsWith("/incoming/answer")){receipt.countDown();try{if(!release.await(15,TimeUnit.SECONDS))barrierFailure.set("Receipt barrier timed out");}catch(InterruptedException e){Thread.currentThread().interrupt();barrierFailure.set("Receipt barrier interrupted");}}}
            }).build());
            try{
            AtomicReference<RemoteCall> owner=new AtomicReference<>();
            scene.onActivity(a->{try{fixture.service.begin(new CallPlan(line(),"vowifi","",Json.obj("call_id","shared-incoming","caller",peer)));owner.set(fixture.service.call);}catch(Exception e){throw new AssertionError(e);}});
            assertTrue("Exact answer receipt reached transport barrier",receipt.await(15,TimeUnit.SECONDS));
            scene.onActivity(a->owner.get().hangup());fixture.service.controlIO.submit(()->{}).get(10,TimeUnit.SECONDS);
            assertEquals("Unaccepted in-flight answer must not end the shared call",0,fixture.ends.get());
            assertTrue(owner.get().audio.closed);assertFalse(owner.get().audio.active);
            release.countDown();await(scene,a->fixture.service.call==null);
            assertFalse("Late receipt must not reactivate the microphone",owner.get().audio.active);
            assertEquals(1,fixture.paid.get());assertEquals(1,fixture.ends.get());assertEquals(1,fixture.releases.get());
            fixture.assertOnlyMutations("POST /v1/media/leases","POST /v1/lines/line/vowifi/calls/incoming/answer","DELETE /v1/media/leases","POST /v1/lines/line/vowifi/calls/end");
            assertEquals("",barrierFailure.get());
            }finally{release.countDown();}
        }
    }

    private final class Fixture implements AutoCloseable {
        final ConfigStore store=new ConfigStore(context);final MockWebServer gateway=new MockWebServer();
        final AtomicInteger conversationReads=new AtomicInteger(),paid=new AtomicInteger(),ends=new AtomicInteger(),releases=new AtomicInteger();
        final AtomicInteger statusReads=new AtomicInteger();final List<JSONObject> mutations=new CopyOnWriteArrayList<>();
        final AtomicBoolean olderRead=new AtomicBoolean();final CountDownLatch online=new CountDownLatch(1),bound=new CountDownLatch(1),answer=new CountDownLatch(1);
        volatile AgentService service;volatile boolean failFirstRelease,failedHistory;volatile int answerStatus=409;volatile String answerCode="call_busy";
        final ServiceConnection connection=new ServiceConnection(){public void onServiceConnected(ComponentName name,IBinder binder){service=((AgentService.LocalBinder)binder).service();bound.countDown();}public void onServiceDisconnected(ComponentName name){}};
        Fixture()throws Exception{
            store.clear();HeldCertificate certificate=new HeldCertificate.Builder().addSubjectAlternativeName("localhost").build();
            gateway.useHttps(new HandshakeCertificates.Builder().heldCertificate(certificate).build().sslSocketFactory(),false);
            gateway.setDispatcher(new okhttp3.mockwebserver.Dispatcher(){@Override public MockResponse dispatch(RecordedRequest request){
                String path=request.getPath(),method=request.getMethod();
                if(!method.equals("GET"))mutations.add(Json.obj("method",method,"path",path,"body",request.getBody().clone().readUtf8()));
                if(path.equals("/api/auth/status"))return json(Json.obj("authenticated",true,"username","fixture-admin","token","fixture-session","csrf","fixture-csrf"));
                if(path.equals("/v1/mobile/ws"))return new MockResponse().withWebSocketUpgrade(new WebSocketListener(){@Override public void onOpen(WebSocket ws,Response r){ws.send(Json.obj("type","mobile.snapshot","schema_version",1,"sequence",1,"data",Json.obj("lines",new JSONArray().put(line()),"messages",new JSONArray(),"cellular_calls",new JSONArray())).toString());online.countDown();}});
                if(path.equals("/v1/catalog/lines")||path.equals("/v1/lines"))return json(Json.obj("lines",new JSONArray().put(line())));
                if(path.equals("/v1/catalog/lines/line")||path.equals("/v1/lines/line"))return json(line());
                if(path.equals("/v1/messages/conversations?all=true")){conversationReads.incrementAndGet();return json(Json.obj("conversations",new JSONArray().put(Json.obj("line_id","line","transport","cellular","peer",peer,"count",75,"last",message("a".repeat(64),"Archived preview")))));}
                if(path.startsWith("/v1/messages?page=true")){
                    boolean older="50".equals(request.getRequestUrl().queryParameter("before"));if(older)olderRead.set(true);
                    JSONArray rows=new JSONArray();for(int n=older?1:26;n<=(older?25:75);n++)rows.put(message(String.format(java.util.Locale.ROOT,"%064x",n),older?"older retained SMS "+n:"first history page "+n));
                    return json(Json.obj("messages",rows,"next_before",older?"":"50"));
                }
                if(path.equals("/v1/media/leases")&&method.equals("POST"))return json(Json.obj("session_id","loser-only","ws_path","/fixture-media"));
                if(path.equals("/fixture-media"))return new MockResponse().withWebSocketUpgrade(new WebSocketListener(){@Override public void onMessage(WebSocket ws,String value){try{
                    JSONObject q=new JSONObject(value);
                    if(q.optString("type").equals("browser.media.hello")){ws.send(Json.obj("type","browser.media.claimed","challenge","canary","resume_ticket","ticket","connection_epoch",1).toString());ws.send(Json.obj("type","browser.media.started","purpose","canary").toString());for(int n=0;n<8;n++)ws.send(ByteString.of(new byte[320]));}
                    if(q.optString("type").equals("browser.media.evidence")&&q.optLong("capture_callbacks")>0&&q.optLong("played_frames")>0)ws.send(Json.obj("type","browser.media.ready","ready",true).toString());
                }catch(Exception e){throw new AssertionError(e);}}});
                if(path.equals("/v1/lines/line/vowifi/calls/incoming/answer")){paid.incrementAndGet();answer.countDown();try{JSONObject body=new JSONObject(request.getBody().readUtf8());return answerStatus==200?json(Json.obj("accepted",true,"code","active","call_id","shared-incoming","operation_id",body.getString("operation_id"))):json(Json.obj("kind","conflict","code",answerCode,"layer","call")).setResponseCode(answerStatus);}catch(Exception e){throw new AssertionError(e);}}
                if(path.equals("/v1/media/leases")&&method.equals("DELETE")){try{assertEquals("loser-only",new JSONObject(request.getBody().readUtf8()).getString("session_id"));}catch(Exception e){throw new AssertionError(e);}int n=releases.incrementAndGet();return n==1&&failFirstRelease?json(Json.obj("code","fixture_release_failed")).setResponseCode(503):json(new JSONObject());}
                if(path.endsWith("/calls/end")){ends.incrementAndGet();try{JSONObject body=new JSONObject(request.getBody().readUtf8());return json(Json.obj("accepted",true,"code","ended","call_id","shared-incoming","operation_id",body.getString("operation_id")));}catch(Exception e){throw new AssertionError(e);}}
                if(path.endsWith("/vowifi/status")){statusReads.incrementAndGet();return json(Json.obj("active_call",Json.obj("call_id","shared-incoming","condition","active")));}
                if(path.startsWith("/v1/calls"))return json(Json.obj("calls",failedHistory?new JSONArray().put(Json.obj("line_id","line","transport","vowifi","call_id","shared-incoming","status","failed","ended_at","2026-09-20T00:00:01Z")):new JSONArray()));
                return new MockResponse().setResponseCode(404).setBody("{}");
            }});gateway.start();
            store.save(Json.obj("server",gateway.url("/").toString().replaceAll("/$",""),"pin",Json.sha(certificate.certificate().getEncoded()),"token","fixture-session","csrf","fixture-csrf","available",true,"share",false));
            assertTrue(context.bindService(new Intent(context,AgentService.class),connection,Context.BIND_AUTO_CREATE));
        }
        ActivityScenario<MainActivity> launch()throws Exception{ActivityScenario<MainActivity> scene=ActivityScenario.launch(MainActivity.class);assertTrue(bound.await(10,TimeUnit.SECONDS));assertTrue(online.await(10,TimeUnit.SECONDS));await(scene,a->service.online());return scene;}
        void assertOnlyMutations(String... permitted)throws Exception{
            Set<String> allow=new HashSet<>(Arrays.asList(permitted));
            for(JSONObject request:mutations){
                String key=request.getString("method")+" "+request.getString("path");
                assertTrue("Unexpected mutation: "+key,allow.contains(key));JSONObject body=new JSONObject(request.getString("body"));
                if(key.equals("DELETE /v1/media/leases"))assertEquals("loser-only",body.getString("session_id"));
                else{assertEquals("shared-incoming",body.getString("call_id"));if(key.endsWith("/incoming/answer")){assertEquals("loser-only",body.getString("media_session_id"));assertFalse(body.getString("operation_id").isEmpty());}}
            }
        }
        public void close()throws Exception{context.unbindService(connection);context.stopService(new Intent(context,AgentService.class));InstrumentationRegistry.getInstrumentation().waitForIdleSync();ConfigStore.intent(()->{}).get(10,TimeUnit.SECONDS);gateway.close();store.clear();}
    }
    private int id(String name){return context.getResources().getIdentifier(name,"id",context.getPackageName());}
    private static boolean contains(View view,String value){if(view instanceof TextView&&((TextView)view).getText().toString().contains(value))return true;if(view instanceof ViewGroup)for(int i=0;i<((ViewGroup)view).getChildCount();i++)if(contains(((ViewGroup)view).getChildAt(i),value))return true;return false;}
    private static MockResponse json(JSONObject body){return new MockResponse().setHeader("Content-Type","application/json").setBody(body.toString());}
    private static void await(ActivityScenario<MainActivity> scene,java.util.function.Predicate<MainActivity> condition)throws Exception{await(scene,condition,"Required communication UI state was not reached");}
    private static void await(ActivityScenario<MainActivity> scene,java.util.function.Predicate<MainActivity> condition,String message)throws Exception{long end=SystemClock.elapsedRealtime()+12000;AtomicBoolean matched=new AtomicBoolean();while(!matched.get()&&SystemClock.elapsedRealtime()<end){InstrumentationRegistry.getInstrumentation().waitForIdleSync();scene.onActivity(a->matched.set(condition.test(a)));if(!matched.get())SystemClock.sleep(100);}assertTrue(message,matched.get());}
    private static AccessibilityNodeInfo waitNode(String value,boolean id)throws Exception{android.app.UiAutomation ui=InstrumentationRegistry.getInstrumentation().getUiAutomation();android.accessibilityservice.AccessibilityServiceInfo info=ui.getServiceInfo();info.flags|=android.accessibilityservice.AccessibilityServiceInfo.FLAG_REPORT_VIEW_IDS;ui.setServiceInfo(info);long end=SystemClock.elapsedRealtime()+10000;while(SystemClock.elapsedRealtime()<end){ui.waitForIdle(100,5000);AccessibilityNodeInfo root=ui.getRootInActiveWindow();if(root!=null)for(AccessibilityNodeInfo node:id?root.findAccessibilityNodeInfosByViewId(value):root.findAccessibilityNodeInfosByText(value))if(node.isVisibleToUser())return node;SystemClock.sleep(100);}throw new AssertionError("Native control missing: "+value);}
    private static boolean hasChecked(AccessibilityNodeInfo node){if(node.isChecked())return true;for(int i=0;i<node.getChildCount();i++){AccessibilityNodeInfo child=node.getChild(i);if(child!=null&&hasChecked(child))return true;}return false;}
    private static void clickText(String text)throws Exception{AccessibilityNodeInfo node=waitNode(text,false);while(node!=null&&!node.isClickable())node=node.getParent();assertNotNull(node);assertTrue(node.performAction(AccessibilityNodeInfo.ACTION_CLICK));}
    private static void waitText(String text)throws Exception{assertNotNull(waitNode(text,false));}
    private static void dialog(boolean yes)throws Exception{assertTrue(waitNode("android:id/button"+(yes?"1":"2"),true).performAction(AccessibilityNodeInfo.ACTION_CLICK));InstrumentationRegistry.getInstrumentation().getUiAutomation().waitForIdle(200,5000);}
}
