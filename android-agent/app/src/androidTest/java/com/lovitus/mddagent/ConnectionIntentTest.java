package com.lovitus.mddagent;

import android.content.*;
import android.os.IBinder;
import androidx.test.core.app.*;
import androidx.test.platform.app.InstrumentationRegistry;
import androidx.test.ext.junit.runners.AndroidJUnit4;
import org.junit.Test;
import org.junit.runner.RunWith;
import java.util.concurrent.*;
import java.util.concurrent.atomic.AtomicReference;
import static org.junit.Assert.*;

/** Delays the real durable writer while user actions arrive in their original order. */
@RunWith(AndroidJUnit4.class)
public class ConnectionIntentTest {
    @Test public void sharingOffSurvivesImmediatePauseAndResume()throws Exception{
        exercise(service->{service.shareReaders(false);service.pause();service.onStartCommand(new Intent(AgentService.START),0,1);},false);
    }
    @Test public void delayedPauseCannotOverwriteLaterResume()throws Exception{
        exercise(service->{service.pause();service.onStartCommand(new Intent(AgentService.START),0,1);},true);
    }
    @Test public void restorePublishesPausedIntentAfterSupersedingInitialLoad()throws Exception{
        Context context=ApplicationProvider.getApplicationContext();ConfigStore store=new ConfigStore(context);store.clear();
        store.save(Json.obj("available",false,"share",true));
        CountDownLatch entered=new CountDownLatch(1),release=new CountDownLatch(1);
        ConfigStore.intent(()->{entered.countDown();try{if(!release.await(10,TimeUnit.SECONDS))throw new AssertionError("Writer not released");}catch(InterruptedException e){Thread.currentThread().interrupt();throw new AssertionError(e);}});
        assertTrue(entered.await(5,TimeUnit.SECONDS));
        try(ActivityScenario<MainActivity> scene=ActivityScenario.launch(MainActivity.class);Bound bound=new Bound(context)){
            main(()->bound.service.onStartCommand(new Intent(AgentService.RESTORE),0,1));
            release.countDown();drain();
            assertFalse(bound.service.available());assertFalse(bound.service.intentSaving());assertTrue(bound.service.sharing());
            assertEquals(R.string.paused,bound.service.connection.resource);assertEquals(R.string.sharing_paused,bound.service.readerStatus.resource);
            assertFalse(store.load().optBoolean("available"));assertTrue(store.load().optBoolean("share"));
        }finally{release.countDown();context.stopService(new Intent(context,AgentService.class));drain();store.clear();}
    }
    @Test public void pausedReaderIntentCanBeCancelledWithoutConnecting()throws Exception{
        Context context=ApplicationProvider.getApplicationContext();ConfigStore store=new ConfigStore(context);store.clear();
        store.save(Json.obj("available",false,"share",true));
        try(ActivityScenario<MainActivity> scene=ActivityScenario.launch(MainActivity.class);Bound bound=new Bound(context)){
            drain();assertTrue(bound.service.sharing());assertFalse(bound.service.available());
            main(()->bound.service.shareReaders(false));drain();
            assertFalse(store.load().optBoolean("share"));assertFalse(bound.service.sharing());assertFalse(bound.service.available());
            assertEquals(R.string.sharing_off,bound.service.readerStatus.resource);
            assertTrue(bound.service.notice.empty());
        }finally{context.stopService(new Intent(context,AgentService.class));drain();store.clear();}
    }
    private void exercise(java.util.function.Consumer<AgentService> actions,boolean expectedSharing)throws Exception{
        Context context=ApplicationProvider.getApplicationContext();ConfigStore store=new ConfigStore(context);store.clear();
        store.save(Json.obj("available",false,"share",true));
        CountDownLatch entered=new CountDownLatch(1),release=new CountDownLatch(1);
        try(ActivityScenario<MainActivity> scene=ActivityScenario.launch(MainActivity.class);Bound bound=new Bound(context)){
            drain();
            ConfigStore.intent(()->{entered.countDown();try{if(!release.await(10,TimeUnit.SECONDS))throw new AssertionError("Writer not released");}catch(InterruptedException e){Thread.currentThread().interrupt();throw new AssertionError(e);}});
            assertTrue(entered.await(5,TimeUnit.SECONDS));
            main(()->actions.accept(bound.service));
            assertFalse("Intent must not pretend an in-flight write completed",store.load().optBoolean("available"));
            release.countDown();drain();
            assertTrue("Last availability choice must be durable",store.load().optBoolean("available"));
            assertEquals(expectedSharing,store.load().optBoolean("share"));
            assertEquals(expectedSharing,bound.service.sharing());assertTrue(bound.service.available());
            main(bound.service::pause);drain();
        }finally{release.countDown();context.stopService(new Intent(context,AgentService.class));drain();store.clear();}
    }
    private static void main(Runnable action){InstrumentationRegistry.getInstrumentation().runOnMainSync(action);}
    private static void drain()throws Exception{ConfigStore.intent(()->{}).get(10,TimeUnit.SECONDS);InstrumentationRegistry.getInstrumentation().waitForIdleSync();}
    private static final class Bound implements AutoCloseable {
        final Context context;final AgentService service;final ServiceConnection connection;
        Bound(Context context)throws Exception{
            this.context=context;CountDownLatch ready=new CountDownLatch(1);AtomicReference<AgentService> owner=new AtomicReference<>();
            connection=new ServiceConnection(){public void onServiceConnected(ComponentName name,IBinder binder){owner.set(((AgentService.LocalBinder)binder).service());ready.countDown();}public void onServiceDisconnected(ComponentName name){}};
            assertTrue(context.bindService(new Intent(context,AgentService.class),connection,Context.BIND_AUTO_CREATE));
            if(!ready.await(5,TimeUnit.SECONDS)){context.unbindService(connection);throw new AssertionError("Service binding failed");}
            service=owner.get();
        }
        public void close(){context.unbindService(connection);}
    }
}
