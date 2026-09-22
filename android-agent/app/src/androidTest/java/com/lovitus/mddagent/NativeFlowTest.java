package com.lovitus.mddagent;

import android.content.Context;
import android.widget.*;
import android.view.*;
import androidx.test.core.app.*;
import androidx.test.platform.app.InstrumentationRegistry;
import androidx.test.ext.junit.runners.AndroidJUnit4;
import okhttp3.*;
import okhttp3.mockwebserver.*;
import okhttp3.tls.*;
import org.json.*;
import org.junit.*;
import org.junit.runner.RunWith;
import java.util.concurrent.*;
import java.util.concurrent.atomic.*;
import static org.junit.Assert.*;

/** Actual native clicks against a TLS-only synthetic gateway, never a carrier. */
@RunWith(AndroidJUnit4.class)
public class NativeFlowTest {
    private static final String EVIDENCE="/data/local/tmp/mdd-native-ui-"+java.util.UUID.randomUUID();
    @Test public void allTabsRetainInputAndSmsConfirmationDispatchesOnce()throws Exception{
        Context context=ApplicationProvider.getApplicationContext();ConfigStore store=new ConfigStore(context);store.clear();
        HeldCertificate certificate=new HeldCertificate.Builder().addSubjectAlternativeName("localhost").build();
        AtomicInteger sends=new AtomicInteger();CountDownLatch submitted=new CountDownLatch(1),linked=new CountDownLatch(1);
        JSONObject line=Json.obj("id","line-1","name","Fixture line","number","+15550100123","card_id","8944100000000000001","enabled",true,"ims","registered","operations",Json.obj("vowifi_call",Json.obj("ready",true),"vowifi_sms",Json.obj("ready",true)));
        try(MockWebServer gateway=new MockWebServer()){
            gateway.useHttps(new HandshakeCertificates.Builder().heldCertificate(certificate).build().sslSocketFactory(),false);
            gateway.setDispatcher(new okhttp3.mockwebserver.Dispatcher(){@Override public MockResponse dispatch(RecordedRequest request){
                String path=request.getPath();
                if(path.equals("/v1/mobile/ws"))return new MockResponse().withWebSocketUpgrade(new WebSocketListener(){@Override public void onOpen(WebSocket socket,Response response){socket.send(Json.obj("type","mobile.snapshot","schema_version",1,"sequence",1,"data",Json.obj("lines",new JSONArray().put(line),"incoming_lines",new JSONArray(),"messages",new JSONArray(),"cellular_calls",new JSONArray())).toString());linked.countDown();}});
                if(path.equals("/api/auth/status"))return json(Json.obj("authenticated",true,"username","fixture-admin","token","fixture-session","csrf","fixture-csrf"));
                if(path.startsWith("/v1/messages?sync="))return json(Json.obj("messages",new JSONArray(),"cursor","fixture:0","initial",true,"more",false));
                if(path.equals("/v1/mobile/lines/line-1"))return json(line);
                if(path.equals("/v1/lines/line-1/vowifi/messages/send")){try{JSONObject body=new JSONObject(request.getBody().readUtf8());sends.incrementAndGet();submitted.countDown();return json(Json.obj("accepted",true,"code","sent","message_id",body.getString("message_id"),"operation_id",body.getString("operation_id")));}catch(Exception e){throw new AssertionError(e);}}
                return new MockResponse().setResponseCode(404).setBody("{}");
            }});gateway.start();
            store.save(Json.obj("server",gateway.url("/").toString().replaceAll("/$",""),"pin",Json.sha(certificate.certificate().getEncoded()),"token","fixture-session","csrf","fixture-csrf","available",true));
            try(ActivityScenario<MainActivity> scene=ActivityScenario.launch(MainActivity.class)){
                try {
                assertTrue(linked.await(10,TimeUnit.SECONDS));
                awaitView(scene,R.id.availability_toggle);capture(context,"home");
                awaitView(scene,R.id.tab_calls);scene.onActivity(a->a.findViewById(R.id.tab_calls).performClick());awaitView(scene,R.id.dial_number);
                awaitView(scene,R.id.call_dial);
                scene.onActivity(a->{View action=a.findViewById(R.id.call_dial);android.graphics.Rect visible=new android.graphics.Rect();assertTrue(action.getGlobalVisibleRect(visible));assertTrue("Primary call action must fit the first viewport",visible.height()>=action.getHeight());});
                awaitCondition(scene,a->{Spinner choices=a.findViewById(R.id.line_selector);return choices!=null&&choices.getSelectedItem()!=null&&choices.getSelectedItem().toString().contains("Fixture line");});
                scene.onActivity(a->{a.findViewById(R.id.dial_plus).performClick();EditText input=a.findViewById(R.id.dial_number);assertEquals("+",input.getText().toString());assertTrue("Landscape keyboard must not hide call controls",(input.getImeOptions()&android.view.inputmethod.EditorInfo.IME_FLAG_NO_EXTRACT_UI)!=0);});
                capture(context,"calls");
                scene.onActivity(a->{EditText input=a.findViewById(R.id.dial_number);input.setText("+15550100999");a.findViewById(R.id.tab_readers).performClick();});
                awaitView(scene,R.id.reader_share);capture(context,"readers");
                scene.onActivity(a->a.findViewById(R.id.tab_settings).performClick());awaitView(scene,R.id.gateway_manage);capture(context,"settings");
                scene.onActivity(a->a.findViewById(R.id.tab_messages).performClick());awaitView(scene,R.id.message_send);capture(context,"messages");
                scene.onActivity(a->{assertEquals("+15550100999",((EditText)a.findViewById(R.id.dial_number)).getText().toString());((EditText)a.findViewById(R.id.message_body)).setText("synthetic fixture only");});
                android.app.UiAutomation automation=InstrumentationRegistry.getInstrumentation().getUiAutomation();android.accessibilityservice.AccessibilityServiceInfo info=automation.getServiceInfo();info.flags|=android.accessibilityservice.AccessibilityServiceInfo.FLAG_REPORT_VIEW_IDS;automation.setServiceInfo(info);
                // Wait for the actual dialog window, then cancel that dialog. A
                // global Back key can reach the Activity before dialog focus transfers.
                openConfirmation(scene,automation,context);
                assertTrue(dialogButton(automation,context,"android:id/button2").performAction(android.view.accessibility.AccessibilityNodeInfo.ACTION_CLICK));
                automation.waitForIdle(200,5000);assertEquals(0,sends.get());
                openConfirmation(scene,automation,context);
                assertTrue(dialogButton(automation,context,"android:id/button1").performAction(android.view.accessibility.AccessibilityNodeInfo.ACTION_CLICK));
                assertTrue(submitted.await(10,TimeUnit.SECONDS));
                long deadline=android.os.SystemClock.elapsedRealtime()+10000;boolean persisted=false;
                while(!persisted&&android.os.SystemClock.elapsedRealtime()<deadline){JSONArray records=Json.array(store.load(),"message_operations");persisted=records.length()==1&&records.getJSONObject(0).optString("state").equals("submitted");if(!persisted)android.os.SystemClock.sleep(100);}
                assertTrue("The exact submission receipt must survive outside the UI",persisted);
                assertEquals(1,sends.get());
                scene.onActivity(a->a.findViewById(R.id.tab_home).performClick());
                }catch(Exception|AssertionError failure){try{capture(context,"failure");}catch(Exception captureFailure){failure.addSuppressed(captureFailure);}throw failure;}
            }finally{context.stopService(new android.content.Intent(context,AgentService.class));InstrumentationRegistry.getInstrumentation().waitForIdleSync();}
        }finally{store.clear();}
    }
    private static MockResponse json(JSONObject value){return new MockResponse().setHeader("Content-Type","application/json").setBody(value.toString());}
    private static void openConfirmation(ActivityScenario<MainActivity> scene,android.app.UiAutomation automation,Context context)throws Exception{
        android.view.accessibility.AccessibilityEvent event=automation.executeAndWaitForEvent(
            ()->scene.onActivity(a->a.findViewById(R.id.message_send).performClick()),
            e->e.getEventType()==android.view.accessibility.AccessibilityEvent.TYPE_WINDOW_STATE_CHANGED&&context.getPackageName().contentEquals(e.getPackageName()==null?"":e.getPackageName()),5000);
        event.recycle();automation.waitForIdle(200,5000);
    }
    private static android.view.accessibility.AccessibilityNodeInfo dialogButton(android.app.UiAutomation automation,Context context,String id){
        android.view.accessibility.AccessibilityNodeInfo root=automation.getRootInActiveWindow();assertNotNull(root);
        assertEquals("Confirmation must belong to the testbed",context.getPackageName(),String.valueOf(root.getPackageName()));
        java.util.List<android.view.accessibility.AccessibilityNodeInfo> buttons=root.findAccessibilityNodeInfosByViewId(id);
        assertEquals("Real confirmation button must be discoverable",1,buttons.size());return buttons.get(0);
    }
    private static void capture(Context context,String name)throws Exception{
        InstrumentationRegistry.getInstrumentation().waitForIdleSync();
        android.graphics.Bitmap bitmap=InstrumentationRegistry.getInstrumentation().getUiAutomation().takeScreenshot();assertNotNull(bitmap);
        bitmap.recycle();
        // Shell-owned evidence survives AGP uninstalling the isolated test package.
        shell("mkdir -p "+EVIDENCE);shell("screencap -p "+EVIDENCE+"/"+name+".png");
    }
    private static void shell(String command)throws Exception{
        android.os.ParcelFileDescriptor output=InstrumentationRegistry.getInstrumentation().getUiAutomation().executeShellCommand(command);
        try(java.io.InputStream in=new android.os.ParcelFileDescriptor.AutoCloseInputStream(output)){byte[] buffer=new byte[4096];while(in.read(buffer)!=-1){}}
    }
    private static void awaitView(ActivityScenario<MainActivity> scene,int id)throws Exception{
        long until=android.os.SystemClock.elapsedRealtime()+10000;AtomicBoolean found=new AtomicBoolean();
        while(!found.get()&&android.os.SystemClock.elapsedRealtime()<until){InstrumentationRegistry.getInstrumentation().waitForIdleSync();scene.onActivity(a->{View view=a.findViewById(id);found.set(view!=null&&view.getGlobalVisibleRect(new android.graphics.Rect()));});if(!found.get())android.os.SystemClock.sleep(100);}
        assertTrue("Native view did not become available: "+id,found.get());
    }
    private static void awaitCondition(ActivityScenario<MainActivity> scene,java.util.function.Predicate<MainActivity> predicate)throws Exception{
        long until=android.os.SystemClock.elapsedRealtime()+10000;AtomicBoolean matched=new AtomicBoolean();
        while(!matched.get()&&android.os.SystemClock.elapsedRealtime()<until){InstrumentationRegistry.getInstrumentation().waitForIdleSync();scene.onActivity(a->matched.set(predicate.test(a)));if(!matched.get())android.os.SystemClock.sleep(100);}
        assertTrue("Native UI did not reach expected state",matched.get());
    }
}
