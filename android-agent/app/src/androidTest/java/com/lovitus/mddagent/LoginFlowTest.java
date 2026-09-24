package com.lovitus.mddagent;

import android.content.*;
import android.widget.*;
import android.view.accessibility.*;
import androidx.test.core.app.*;
import androidx.test.platform.app.InstrumentationRegistry;
import androidx.test.ext.junit.runners.AndroidJUnit4;
import okhttp3.*;
import okhttp3.mockwebserver.*;
import okhttp3.tls.*;
import org.json.*;
import org.junit.Test;
import org.junit.runner.RunWith;
import java.util.concurrent.*;
import java.util.concurrent.atomic.*;
import static org.junit.Assert.*;

@RunWith(AndroidJUnit4.class)
public class LoginFlowTest {
    @Test public void unauthenticatedTabsRemainDistinctAndEncryptedDraftSurvivesRecreation()throws Exception{
        Context context=ApplicationProvider.getApplicationContext();ConfigStore store=new ConfigStore(context);store.clear();
        try(ActivityScenario<MainActivity> scene=ActivityScenario.launch(MainActivity.class)){
            await(scene,a->a.findViewById(R.id.gateway_password)!=null);form(scene,"https://fixture.test","owner","remember-this-fixture");
            int[] tabs={R.id.tab_calls,R.id.tab_messages,R.id.tab_readers,R.id.tab_settings};
            int[] views={R.id.dial_number,R.id.message_body,R.id.reader_share,R.id.forget_login};
            int[] titles={R.string.calls,R.string.messages,R.string.readers,R.string.settings};
            for(int i=0;i<tabs.length;i++){click(tabs[i]);int view=views[i],title=titles[i];await(scene,a->a.findViewById(view)!=null);scene.onActivity(a->{assertEquals("MDD · "+context.getString(title),((TextView)a.findViewById(R.id.page_title)).getText().toString());assertNull(a.findViewById(R.id.gateway_password));});}
            click(R.id.tab_home);await(scene,a->a.findViewById(R.id.gateway_password)!=null);
            scene.onActivity(a->assertEquals("remember-this-fixture",((EditText)a.findViewById(R.id.gateway_password)).getText().toString()));
            scene.recreate();await(scene,a->a.findViewById(R.id.gateway_password)!=null);
            scene.onActivity(a->{EditText secret=a.findViewById(R.id.gateway_password);assertEquals("remember-this-fixture",secret.getText().toString());assertFalse(secret.isSaveEnabled());});
            drain();assertEquals("remember-this-fixture",LoginProfile.read(store.load()).password);
            byte[] raw=java.nio.file.Files.readAllBytes(new java.io.File(context.getNoBackupFilesDir(),"private-state-v1").toPath());assertFalse(new String(raw,java.nio.charset.StandardCharsets.UTF_8).contains("remember-this-fixture"));
            click(R.id.login_remember);drain();assertEquals("",LoginProfile.read(store.load()).password);
            scene.recreate();await(scene,a->a.findViewById(R.id.gateway_password)!=null);scene.onActivity(a->assertEquals("remember-this-fixture",((EditText)a.findViewById(R.id.gateway_password)).getText().toString()));
            click(R.id.login_remember);drain();assertEquals("remember-this-fixture",LoginProfile.read(store.load()).password);
            click(R.id.tab_settings);
            java.lang.reflect.Field fault=ConfigStore.class.getDeclaredField("writeFault");fault.setAccessible(true);fault.setBoolean(null,true);
            try{dialogAfter(()->click(R.id.forget_login));assertEquals("remember-this-fixture",LoginProfile.read(store.load()).password);assertFalse(root().findAccessibilityNodeInfosByText(context.getString(R.string.login_save_failed)).isEmpty());click("android:id/button1");}finally{store.retry();}
            dialogAfter(()->click(R.id.forget_login));assertEquals("",LoginProfile.read(store.load()).password);click("android:id/button1");
            click(R.id.tab_home);await(scene,a->a.findViewById(R.id.gateway_password)!=null);scene.onActivity(a->assertEquals("",((EditText)a.findViewById(R.id.gateway_password)).getText().toString()));
        }finally{drain();store.clear();}
    }
    @Test public void selfSignedCertificateRequiresConsentThenRetainsPasswordOnAuthFailure()throws Exception{certificateFlow(false);}
    @Test public void changedCertificateRequiresExplicitReplacementBeforeCredentials()throws Exception{certificateFlow(true);}
    @Test public void logoutCancelsDelayedLoginWithoutRestartingAvailability()throws Exception{
        Context context=ApplicationProvider.getApplicationContext();ConfigStore store=new ConfigStore(context);store.clear();
        HeldCertificate cert=new HeldCertificate.Builder().addSubjectAlternativeName("localhost").build();
        CountDownLatch received=new CountDownLatch(1),release=new CountDownLatch(1);
        try(MockWebServer gateway=new MockWebServer()){
            gateway.useHttps(new HandshakeCertificates.Builder().heldCertificate(cert).build().sslSocketFactory(),false);
            gateway.setDispatcher(new okhttp3.mockwebserver.Dispatcher(){public MockResponse dispatch(RecordedRequest request)throws InterruptedException{if(request.getPath().equals("/api/auth/login")){received.countDown();assertTrue(release.await(15,TimeUnit.SECONDS));return json(Json.obj("token","late-session","csrf","late-csrf"));}return json(Json.obj("authenticated",true,"username","owner","token","late-session"));}});gateway.start();
            String origin=new Endpoint(gateway.url("/").toString(),"").origin;store.update(current->LoginProfile.acceptPin(current,origin,"",Json.sha(cert.certificate().getEncoded())));
            try(ActivityScenario<MainActivity> scene=ActivityScenario.launch(MainActivity.class)){
                await(scene,a->a.findViewById(R.id.gateway_password)!=null);form(scene,origin,"owner","fixture-password");click(R.id.connect_gateway);assertTrue(received.await(10,TimeUnit.SECONDS));
                click(R.id.tab_settings);dialogAfter(()->click(R.id.logout));click("android:id/button1");release.countDown();drainActivity(scene);drain();
                assertTrue(store.load().optString("token").isEmpty());assertFalse(store.load().optBoolean("available"));
            }finally{release.countDown();context.stopService(new Intent(context,AgentService.class));}
        }finally{release.countDown();drain();store.clear();}
    }
    private void certificateFlow(boolean changed)throws Exception{
        Context context=ApplicationProvider.getApplicationContext();ConfigStore store=new ConfigStore(context);store.clear();
        if(android.os.Build.VERSION.SDK_INT>=33)InstrumentationRegistry.getInstrumentation().getUiAutomation().executeShellCommand("pm grant "+context.getPackageName()+" android.permission.POST_NOTIFICATIONS").close();
        HeldCertificate certificate=new HeldCertificate.Builder().addSubjectAlternativeName("localhost").build();
        AtomicInteger logins=new AtomicInteger();AtomicBoolean allow=new AtomicBoolean(false);CountDownLatch authenticated=new CountDownLatch(1);
        try(MockWebServer gateway=new MockWebServer()){
            gateway.useHttps(new HandshakeCertificates.Builder().heldCertificate(certificate).build().sslSocketFactory(),false);
            gateway.setDispatcher(new okhttp3.mockwebserver.Dispatcher(){@Override public MockResponse dispatch(RecordedRequest request){String path=request.getPath();
                if(path.equals("/api/auth/login")){logins.incrementAndGet();if(!allow.get())return new MockResponse().setResponseCode(401).setBody("{\"code\":\"invalid_credentials\"}");authenticated.countDown();return json(Json.obj("token","fixture-session","csrf","fixture-csrf"));}
                if(path.equals("/api/auth/status"))return json(Json.obj("authenticated",true,"username","owner","token","fixture-session"));
                if(path.equals("/v1/mobile/ws"))return new MockResponse().withWebSocketUpgrade(new WebSocketListener(){public void onOpen(WebSocket socket,Response response){socket.send(Json.obj("type","mobile.snapshot","schema_version",1,"sequence",1,"data",Json.obj("lines",new JSONArray(),"messages",new JSONArray(),"incoming_lines",new JSONArray())).toString());}});
                if(path.startsWith("/v1/messages?"))return json(Json.obj("messages",new JSONArray(),"cursor","fixture:0","initial",true,"more",false));
                return new MockResponse().setResponseCode(404).setBody("{}");
            }});gateway.start();String origin=new Endpoint(gateway.url("/").toString(),"").origin;String old="01".repeat(32);
            if(changed)store.update(current->LoginProfile.acceptPin(current,origin,"",old));
            try(ActivityScenario<MainActivity> scene=ActivityScenario.launch(MainActivity.class)){
                await(scene,a->a.findViewById(R.id.gateway_password)!=null);form(scene,origin,"owner","fixture-password");
                dialogAfter(()->click(R.id.connect_gateway));assertEquals(0,logins.get());
                assertFalse(root().findAccessibilityNodeInfosByText(context.getString(changed?R.string.certificate_changed:R.string.certificate_first)).isEmpty());
                click("android:id/button2");drain();assertEquals(0,logins.get());assertEquals(changed?old:"",LoginProfile.pin(store.load(),origin));
                dialogAfter(()->click(R.id.connect_gateway));
                dialogAfter(()->click("android:id/button1"));
                assertEquals(1,logins.get());click("android:id/button1");
                scene.onActivity(a->assertEquals("fixture-password",((EditText)a.findViewById(R.id.gateway_password)).getText().toString()));
                assertEquals(Json.sha(certificate.certificate().getEncoded()),LoginProfile.pin(store.load(),origin));
                allow.set(true);click(R.id.connect_gateway);assertTrue(authenticated.await(10,TimeUnit.SECONDS));
                await(scene,a->a.findViewById(R.id.home_availability_toggle)!=null);
                assertEquals(2,logins.get());assertEquals("fixture-password",LoginProfile.read(store.load()).password);
            }finally{context.stopService(new Intent(context,AgentService.class));InstrumentationRegistry.getInstrumentation().waitForIdleSync();}
        }finally{drain();store.clear();}
    }
    private static MockResponse json(JSONObject value){return new MockResponse().setHeader("Content-Type","application/json").setBody(value.toString());}
    private static void form(ActivityScenario<MainActivity> scene,String server,String user,String password){scene.onActivity(a->{((EditText)a.findViewById(R.id.gateway_server)).setText(server);((EditText)a.findViewById(R.id.gateway_username)).setText(user);((EditText)a.findViewById(R.id.gateway_password)).setText(password);});}
    private static android.app.UiAutomation automation(){android.app.UiAutomation automation=InstrumentationRegistry.getInstrumentation().getUiAutomation();android.accessibilityservice.AccessibilityServiceInfo info=automation.getServiceInfo();info.flags|=android.accessibilityservice.AccessibilityServiceInfo.FLAG_REPORT_VIEW_IDS;automation.setServiceInfo(info);return automation;}
    private static AccessibilityNodeInfo root(){AccessibilityNodeInfo root=automation().getRootInActiveWindow();assertNotNull(root);assertEquals(ApplicationProvider.getApplicationContext().getPackageName(),String.valueOf(root.getPackageName()));return root;}
    private static void click(int id){click(ApplicationProvider.getApplicationContext().getResources().getResourceName(id));}
    private static void click(String id){try{automation().waitForIdle(200,5000);java.util.List<AccessibilityNodeInfo> nodes=root().findAccessibilityNodeInfosByViewId(id);assertEquals("Expected one visible native control: "+id,1,nodes.size());assertTrue(nodes.get(0).performAction(AccessibilityNodeInfo.ACTION_CLICK));InstrumentationRegistry.getInstrumentation().waitForIdleSync();automation().waitForIdle(200,5000);}catch(java.util.concurrent.TimeoutException failure){throw new AssertionError("Native window transition did not settle",failure);}}
    private static void dialogAfter(Runnable action)throws Exception{android.app.UiAutomation automation=automation();AccessibilityEvent event=automation.executeAndWaitForEvent(action,e->e.getEventType()==AccessibilityEvent.TYPE_WINDOW_STATE_CHANGED&&String.valueOf(e.getClassName()).contains("AlertDialog")&&ApplicationProvider.getApplicationContext().getPackageName().contentEquals(e.getPackageName()==null?"":e.getPackageName()),15000);event.recycle();automation.waitForIdle(200,5000);assertFalse(root().findAccessibilityNodeInfosByViewId("android:id/button1").isEmpty());}
    private static void drain()throws Exception{ConfigStore.intent(()->{}).get(10,TimeUnit.SECONDS);InstrumentationRegistry.getInstrumentation().waitForIdleSync();}
    private static void drainActivity(ActivityScenario<MainActivity> scene)throws Exception{AtomicReference<ExecutorService> executor=new AtomicReference<>();scene.onActivity(a->{try{java.lang.reflect.Field field=MainActivity.class.getDeclaredField("io");field.setAccessible(true);executor.set((ExecutorService)field.get(a));}catch(Exception e){throw new AssertionError(e);}});executor.get().submit(()->{}).get(10,TimeUnit.SECONDS);}
    private static void await(ActivityScenario<MainActivity> scene,java.util.function.Predicate<MainActivity> condition)throws Exception{AtomicBoolean ready=new AtomicBoolean();long until=android.os.SystemClock.elapsedRealtime()+15000;while(!ready.get()&&android.os.SystemClock.elapsedRealtime()<until){InstrumentationRegistry.getInstrumentation().waitForIdleSync();scene.onActivity(a->ready.set(condition.test(a)));if(!ready.get())android.os.SystemClock.sleep(100);}assertTrue("Native login state not reached",ready.get());}
}
