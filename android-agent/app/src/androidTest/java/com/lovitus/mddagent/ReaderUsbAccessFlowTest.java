package com.lovitus.mddagent;

import android.app.PendingIntent;
import android.content.*;
import android.content.pm.PackageManager;
import android.content.res.XmlResourceParser;
import android.hardware.usb.UsbManager;
import android.os.*;
import androidx.test.core.app.*;
import androidx.test.ext.junit.runners.AndroidJUnit4;
import androidx.test.platform.app.InstrumentationRegistry;
import org.junit.Test;
import org.junit.runner.RunWith;
import org.xmlpull.v1.XmlPullParser;
import java.lang.reflect.Field;
import java.util.*;
import java.util.concurrent.*;
import java.util.concurrent.atomic.*;
import static org.junit.Assert.*;

/** Real Service, Activity, packaged manifest and PendingIntent; only USB hardware is synthetic. */
@RunWith(AndroidJUnit4.class)
public class ReaderUsbAccessFlowTest {
    private static Field field(String name)throws Exception {Field f=AgentService.class.getDeclaredField(name);f.setAccessible(true);return f;}
    private static void main(Runnable action){InstrumentationRegistry.getInstrumentation().runOnMainSync(action);InstrumentationRegistry.getInstrumentation().waitForIdleSync();}
    private static final class Request {
        final String key;final PendingIntent result;
        Request(String key,PendingIntent result){this.key=key;this.result=result;}
    }
    private static final class USB implements ReaderUsbAccess.Platform {
        final Map<String,Boolean> devices=new LinkedHashMap<>();
        final List<Request> requests=new ArrayList<>();
        boolean brokenInventory,brokenRequest;
        public Map<String,Boolean> attached(){if(brokenInventory)throw new SecurityException("synthetic inventory failure");return new LinkedHashMap<>(devices);}
        public void request(String key,PendingIntent result){requests.add(new Request(key,result));if(brokenRequest)throw new IllegalStateException("synthetic dialog failure");}
    }
    private static final class Fixture implements AutoCloseable {
        final Context context=ApplicationProvider.getApplicationContext();
        final ConfigStore store=new ConfigStore(context);final USB usb=new USB();
        final AgentService service;final ServiceConnection binding;final ReaderUsbAccess access;
        final GatewayApi api=new GatewayApi(new Endpoint("https://example.invalid",""),"","");
        long now;
        Fixture()throws Exception{
            store.clear();store.save(Json.obj("available",false,"share",true));
            AtomicReference<AgentService> found=new AtomicReference<>();CountDownLatch ready=new CountDownLatch(1);
            binding=new ServiceConnection(){public void onServiceConnected(ComponentName name,IBinder binder){found.set(((AgentService.LocalBinder)binder).service());ready.countDown();}public void onServiceDisconnected(ComponentName name){}};
            assertTrue(context.bindService(new Intent(context,AgentService.class),binding,Context.BIND_AUTO_CREATE));assertTrue(ready.await(5,TimeUnit.SECONDS));service=found.get();
            ConfigStore.intent(()->{}).get(5,TimeUnit.SECONDS);InstrumentationRegistry.getInstrumentation().waitForIdleSync();
            access=new ReaderUsbAccess(context,usb,()->now);field("usbAccess").set(service,access);
            field("available").set(service,true);field("sharing").set(service,true);
        }
        void tick(){main(service::refreshReaders);}
        void event(){main(service::readerEnvironmentChanged);}
        void expect(int count){assertEquals("AUTO_USB_ACCESS: expected OS requests from the real Service flow",count,usb.requests.size());}
        Request last(){assertFalse("AUTO_USB_ACCESS: no system request",usb.requests.isEmpty());return usb.requests.get(usb.requests.size()-1);}
        Intent answer(boolean actualPermission,boolean claimedPermission)throws Exception{
            Request request=last();usb.devices.put(request.key,actualPermission);
            CountDownLatch done=new CountDownLatch(1);AtomicReference<Intent> delivered=new AtomicReference<>();
            request.result.send(context,0,new Intent().putExtra(UsbManager.EXTRA_PERMISSION_GRANTED,claimedPermission),
                (pending,intent,code,data,extras)->{delivered.set(intent);done.countDown();},new Handler(Looper.getMainLooper()));
            assertTrue(done.await(5,TimeUnit.SECONDS));InstrumentationRegistry.getInstrumentation().waitForIdleSync();return delivered.get();
        }
        public void close()throws Exception{
            main(()->{service.call=null;try{field("available").set(service,false);}catch(Exception e){throw new AssertionError(e);}access.cancel();});
            context.unbindService(binding);context.stopService(new Intent(context,AgentService.class));
            InstrumentationRegistry.getInstrumentation().waitForIdleSync();ConfigStore.intent(()->{}).get(5,TimeUnit.SECONDS);store.clear();api.close();
        }
    }

    @Test public void automaticPermissionUsesRealCallbackAndRefusalDoesNotLoop()throws Exception{
        try(Fixture f=new Fixture()){
            f.usb.devices.put("CCID/A",false);f.event();f.expect(1);
            PendingIntent first=f.last().result;
            for(int i=0;i<6;i++){f.tick();f.event();}f.expect(1);
            ((AtomicBoolean)field("readerRecoveryEvent").get(f.service)).set(false);
            Intent old=f.answer(false,true);
            assertTrue("Mutable result delivered the system extras",old.getBooleanExtra(UsbManager.EXTRA_PERMISSION_GRANTED,false));
            assertEquals(R.string.reader_usb_permission_retry,f.access.status("CCID/A"));
            assertFalse(((AtomicBoolean)field("readerRecoveryEvent").get(f.service)).get());
            for(int i=0;i<6;i++)f.tick();f.expect(1);
            main(()->f.access.reconcile(true,"CCID/A"));f.expect(2);
            assertNotEquals("Retries need distinct PendingIntent identities",first,f.last().result);
            assertFalse(f.access.result(old));
            f.answer(true,false);f.tick();assertFalse(f.access.waiting());f.expect(2);
            assertTrue(f.service.sharing());assertTrue(f.service.available());
            assertNull("Permission acquisition must not create a reader owner",field("hub").get(f.service));
            assertFalse(((AtomicBoolean)field("readerRecoveryEvent").get(f.service)).get());
        }
    }

    @Test public void attachmentsSerializeAndMissingCallbackNeedsExplicitRetry()throws Exception{
        try(Fixture f=new Fixture()){
            f.usb.devices.put("healthy",true);f.usb.devices.put("A",false);f.usb.devices.put("B",false);f.tick();f.expect(1);assertEquals("A",f.last().key);
            Request old=f.last();f.now=120001;f.tick();f.expect(1);
            assertEquals(R.string.reader_usb_permission_retry,f.access.status("A"));
            assertFalse(f.access.canRequest("B"));assertTrue(f.access.canRequest("A"));
            main(()->f.access.reconcile(true,"A"));f.expect(2);
            try{old.result.send();fail("Superseded pending result must be cancelled");}catch(PendingIntent.CanceledException expected){}
            f.answer(false,false);f.tick();f.expect(3);assertEquals("B",f.last().key);
            f.answer(true,true);f.tick();f.expect(3);
            f.usb.devices.remove("A");f.tick();f.usb.devices.put("A",false);f.tick();f.expect(4);
            Request stopped=f.last();main(()->f.service.shareReaders(false));
            try{stopped.result.send();fail("Sharing-off cancels the exact pending callback");}catch(PendingIntent.CanceledException expected){}
            f.usb.devices.put("A",true);f.event();f.expect(4);assertFalse(f.service.sharing());
        }
    }

    @Test public void queuedChecksRespectIntentAndCommunicationThenRecoverWhenAllowed()throws Exception{
        for(String gate:new String[]{"available","sharing","destroyed","intentSaving","smsPending","call"}){
            try(Fixture f=new Fixture()){
                f.usb.devices.put("A",false);
                RemoteCall call=new RemoteCall(f.service,f.api,new CallPlan(Json.obj("id","test-line","card_id","1234","enabled",true),"vowifi","+15550100999",null),f.service.io);
                main(()->{
                    f.service.refreshReaders();
                    try{if(gate.equals("smsPending"))((AtomicBoolean)field(gate).get(f.service)).set(true);
                    else if(gate.equals("call"))f.service.call=call;
                    else field(gate).set(f.service,!gate.equals("available")&&!gate.equals("sharing"));}
                    catch(Exception failure){throw new AssertionError(failure);}
                });
                f.expect(0);
                if(gate.equals("smsPending"))((AtomicBoolean)field(gate).get(f.service)).set(false);
                else if(gate.equals("call"))f.service.call=null;
                else field(gate).set(f.service,gate.equals("available")||gate.equals("sharing"));
                f.tick();f.expect(1);
                main(f.service::pause);f.usb.devices.put("A",true);f.event();f.expect(1);assertFalse(f.service.available());
            }
        }
    }

    @Test public void activityAttachUsesExistingServiceAndPackagedCcidFilter()throws Exception{
        try(Fixture f=new Fixture()){
            f.usb.devices.put("A",false);
            Intent attach=new Intent(UsbManager.ACTION_USB_DEVICE_ATTACHED).setPackage(f.context.getPackageName());
            assertEquals(1,f.context.getPackageManager().queryIntentActivities(attach,PackageManager.GET_META_DATA).size());
            android.content.pm.ActivityInfo info=f.context.getPackageManager().getActivityInfo(new ComponentName(f.context,MainActivity.class),PackageManager.GET_META_DATA);
            try(XmlResourceParser xml=info.loadXmlMetaData(f.context.getPackageManager(),UsbManager.ACTION_USB_DEVICE_ATTACHED)){
                assertNotNull(xml);int filters=0;
                while(xml.next()!=XmlPullParser.END_DOCUMENT)if(xml.getEventType()==XmlPullParser.START_TAG&&xml.getName().equals("usb-device")){
                    filters++;assertEquals("11",xml.getAttributeValue(null,"class"));assertEquals(1,xml.getAttributeCount());
                }
                assertEquals(1,filters);
            }
            try(ActivityScenario<MainActivity> scene=ActivityScenario.launch(attach.setComponent(new ComponentName(f.context,MainActivity.class)).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))){
                InstrumentationRegistry.getInstrumentation().waitForIdleSync();f.expect(1);
                scene.onActivity(a->a.onNewIntent(new Intent(UsbManager.ACTION_USB_DEVICE_ATTACHED)));
                InstrumentationRegistry.getInstrumentation().waitForIdleSync();f.expect(1);
                assertNull(field("hub").get(f.service));assertNull(field("agent").get(f.service));
                assertFalse("The USB entry must not persist an implicit START",f.store.load().optBoolean("available"));
            }
        }
    }

    @Test public void platformFailuresRemainBoundedAndManualRetryWorks()throws Exception{
        try(Fixture f=new Fixture()){
            f.usb.devices.put("A",false);f.usb.brokenInventory=true;f.event();f.expect(0);
            f.usb.brokenInventory=false;f.usb.brokenRequest=true;f.tick();f.expect(1);
            for(int i=0;i<6;i++)f.tick();f.expect(1);
            f.usb.brokenRequest=false;main(()->f.access.reconcile(true,"A"));f.expect(2);
            f.answer(true,true);f.tick();assertFalse(f.access.waiting());
        }
    }
}
