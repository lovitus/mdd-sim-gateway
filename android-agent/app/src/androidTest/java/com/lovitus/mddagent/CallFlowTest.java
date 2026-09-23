package com.lovitus.mddagent;

import android.app.*;
import android.content.*;
import android.os.*;
import android.service.notification.StatusBarNotification;
import android.view.*;
import android.view.accessibility.*;
import android.widget.*;
import androidx.test.core.app.*;
import androidx.test.platform.app.InstrumentationRegistry;
import androidx.test.ext.junit.runners.AndroidJUnit4;
import com.google.android.material.button.MaterialButton;
import org.json.JSONObject;
import java.io.File;
import java.io.IOException;
import org.junit.Test;
import org.junit.runner.RunWith;
import java.util.concurrent.*;
import java.util.concurrent.atomic.*;
import static org.junit.Assert.*;

/** Native buttons, real Android audio callbacks, and a TLS-only synthetic gateway. */
@RunWith(AndroidJUnit4.class)
public class CallFlowTest {
    private static final String EVIDENCE="/data/local/tmp/mdd-native-ui-call-"+java.util.UUID.randomUUID();
    @Test public void outgoingControlsAndExactHangupWorkOnBothTransports()throws Exception{
        for(String mode:new String[]{"vowifi","cellular"})run(mode,false,(fixture,scene)->{
            outgoing(fixture,scene,true);await(scene,fixture,a->a.findViewById(R.id.call_dtmf)!=null&&a.findViewById(R.id.call_dtmf).isEnabled());
            click(R.id.call_mute);scene.onActivity(a->assertTrue(((MaterialButton)a.findViewById(R.id.call_mute)).isChecked()));
            click(R.id.call_mute);scene.onActivity(a->assertFalse(((MaterialButton)a.findViewById(R.id.call_mute)).isChecked()));
            click(R.id.call_speaker);assertEquals(1,fixture.starts.get());capture(mode+"-active-controls");
            dialogAfter(()->click(R.id.call_dtmf));android.os.Bundle arguments=new android.os.Bundle();arguments.putCharSequence(AccessibilityNodeInfo.ACTION_ARGUMENT_SET_TEXT_CHARSEQUENCE,"5");assertTrue(node(R.id.dtmf_signal).performAction(AccessibilityNodeInfo.ACTION_SET_TEXT,arguments));click("android:id/button1");
            assertTrue(fixture.toneReceived.await(10,TimeUnit.SECONDS));assertEquals(1,fixture.dtmf.get());
            click(R.id.call_hangup);await(scene,fixture,a->a.findViewById(R.id.call_dial)!=null);
            assertFalse(fixture.store.load().has("pending_call"));assertEquals(1,fixture.starts.get());assertEquals(1,fixture.ends.get());assertEquals(1,fixture.leases.get());assertEquals(1,fixture.deletes.get());assertTrue(fixture.pcm.get()>1);
        });
    }
    @Test public void pagedOutIncomingNotificationAnswersTheOriginalCall()throws Exception{
        for(String mode:new String[]{"vowifi","cellular"})run(mode,true,(fixture,scene)->{
            click(R.id.tab_settings);await(scene,fixture,a->a.findViewById(R.id.call_answer)!=null);
            String event=mode.equals("cellular")?"cell:fixture-cell-incoming":"call:fixture-vowifi-incoming";
            StatusBarNotification notification=null;for(StatusBarNotification row:context().getSystemService(NotificationManager.class).getActiveNotifications())if(event.equals(row.getTag()))notification=row;
            assertNotNull("The 129th incoming line must have a notification",notification);
            assertEquals(context().getString(R.string.tap_open),notification.getNotification().extras.getCharSequence(Notification.EXTRA_TEXT).toString());
            notification.getNotification().contentIntent.send();await(scene,fixture,a->((TextView)a.findViewById(R.id.page_title)).getText().toString().endsWith(context().getString(R.string.calls))&&a.findViewById(R.id.call_answer)!=null);capture(mode+"-notification-entry");
            click(R.id.call_answer);assertTrue(fixture.started.await(15,TimeUnit.SECONDS));
            await(scene,fixture,a->a.findViewById(R.id.call_mute)!=null);click(R.id.call_hangup);await(scene,fixture,a->a.findViewById(R.id.call_dial)!=null);
            assertEquals(1,fixture.starts.get());assertEquals(1,fixture.ends.get());assertEquals(0,fixture.rejects.get());assertFalse(fixture.store.load().has("pending_call"));
            scene.recreate();fixture.nextIncoming();await(scene,fixture,a->a.findViewById(R.id.call_reject)!=null);click(R.id.call_reject);assertTrue(fixture.rejected.await(10,TimeUnit.SECONDS));assertEquals(1,fixture.rejects.get());
        });
    }
    @Test public void declineDoesNotAcquireMediaOrStartACall()throws Exception{
        for(String mode:new String[]{"vowifi","cellular"})run(mode,true,(fixture,scene)->{
            click(R.id.tab_messages);await(scene,fixture,a->a.findViewById(R.id.call_reject)!=null);click(R.id.call_reject);assertTrue(fixture.rejected.await(10,TimeUnit.SECONDS));
            await(scene,fixture,a->a.findViewById(R.id.call_reject)==null);assertEquals(1,fixture.rejects.get());assertEquals(0,fixture.leases.get());assertEquals(0,fixture.starts.get());assertFalse(fixture.store.load().has("pending_call"));
        });
    }
    @Test public void unrelatedSnapshotsKeepTheSameIncomingControls()throws Exception{
        for(String mode:new String[]{"vowifi","cellular"})run(mode,true,(fixture,scene)->{
            await(scene,fixture,a->a.findViewById(R.id.call_reject)!=null);AtomicReference<View> control=new AtomicReference<>();AtomicReference<JSONObject> snapshot=new AtomicReference<>();AtomicReference<AgentService> owner=new AtomicReference<>();
            scene.onActivity(a->{try{java.lang.reflect.Field field=MainActivity.class.getDeclaredField("service");field.setAccessible(true);owner.set((AgentService)field.get(a));snapshot.set(owner.get().snapshot);control.set(a.findViewById(R.id.call_reject));}catch(Exception error){throw new AssertionError(error);}});
            fixture.publish();await(scene,fixture,a->owner.get().snapshot!=snapshot.get());owner.get().loop.submit(()->{}).get(5,TimeUnit.SECONDS);InstrumentationRegistry.getInstrumentation().waitForIdleSync();scene.onActivity(a->assertSame("Unrelated updates must not detach a user's incoming button",control.get(),a.findViewById(R.id.call_reject)));
            click(R.id.call_reject);assertTrue(fixture.rejected.await(10,TimeUnit.SECONDS));assertEquals(1,fixture.rejects.get());assertEquals(0,fixture.starts.get());
        });
    }
    @Test public void savedPageWinsOverAnAlreadyConsumedColdNotification()throws Exception{
        for(String mode:new String[]{"vowifi","cellular"})run(mode,true,true,(fixture,scene)->{
            await(scene,fixture,a->a.findViewById(R.id.call_reject)!=null);click(R.id.call_reject);assertTrue(fixture.rejected.await(10,TimeUnit.SECONDS));await(scene,fixture,a->a.findViewById(R.id.call_reject)==null);
            click(R.id.tab_messages);
            // ActivityManager may restore its original launch-intent copy after process loss.
            scene.onActivity(a->a.getIntent().putExtra("incoming_event",mode.equals("cellular")?"cell:fixture-cell-incoming":"call:fixture-vowifi-incoming"));
            scene.recreate();await(scene,fixture,a->a.findViewById(R.id.message_body)!=null);
            scene.onActivity(a->assertEquals("MDD · "+context().getString(R.string.messages),((TextView)a.findViewById(R.id.page_title)).getText().toString()));
            fixture.nextIncoming();await(scene,fixture,a->a.findViewById(R.id.call_reject)!=null);click(R.id.call_reject);await(scene,fixture,a->a.findViewById(R.id.call_reject)==null);
            assertEquals(2,fixture.rejects.get());assertEquals(0,fixture.starts.get());assertEquals(0,fixture.leases.get());
        });
    }
    @Test public void unconfirmedHangupRetainsIdentityUntilExactStatusReceipt()throws Exception{
        for(String mode:new String[]{"vowifi","cellular"})run(mode,false,(fixture,scene)->{
            fixture.confirmEnd=false;outgoing(fixture,scene,false);JSONObject original=fixture.store.load().getJSONObject("pending_call");
            click(R.id.call_hangup);assertTrue(fixture.ended.await(10,TimeUnit.SECONDS));assertTrue(fixture.queried.await(10,TimeUnit.SECONDS));
            await(scene,fixture,a->a.findViewById(R.id.call_mute)==null&&a.findViewById(R.id.call_reconcile)!=null);
            assertEquals(original.getString("operation_id"),fixture.store.load().getJSONObject("pending_call").getString("operation_id"));assertEquals(1,fixture.ends.get());assertEquals(1,fixture.starts.get());
            scene.onActivity(a->assertEquals(a.getString(R.string.audio_off),((TextView)a.findViewById(R.id.call_audio_state)).getText().toString()));
            fixture.terminal=true;click(R.id.call_reconcile);await(scene,fixture,a->a.findViewById(R.id.call_dial)!=null);
            assertFalse(fixture.store.load().has("pending_call"));assertEquals(1,fixture.ends.get());assertEquals(1,fixture.starts.get());assertEquals(1,fixture.deletes.get());
        });
    }
    @Test public void mediaClosureAloneDoesNotProveNaturalCallEnd()throws Exception{
        for(String mode:new String[]{"vowifi","cellular"})run(mode,false,(fixture,scene)->{
            outgoing(fixture,scene,false);fixture.remoteEnd(false);assertTrue(fixture.queried.await(10,TimeUnit.SECONDS));
            await(scene,fixture,a->a.findViewById(R.id.call_mute)==null&&a.findViewById(R.id.call_reconcile)!=null);
            assertTrue(fixture.store.load().has("pending_call"));assertEquals(0,fixture.ends.get());
            scene.onActivity(a->{TextView audio=a.findViewById(R.id.call_audio_state);assertEquals(a.getString(R.string.audio_transport_closed),audio.getText().toString());assertNotEquals(a.getString(R.string.call_ended),audio.getText().toString());});
            fixture.terminal=true;click(R.id.call_reconcile);await(scene,fixture,a->a.findViewById(R.id.call_dial)!=null);
            assertFalse(fixture.store.load().has("pending_call"));assertEquals(0,fixture.ends.get());assertEquals(1,fixture.starts.get());assertEquals(1,fixture.deletes.get());
        });
    }
    @Test public void exactStatusReceiptClosesLiveAudioBeforeRetiringOwner()throws Exception{
        for(String mode:new String[]{"vowifi","cellular"})run(mode,false,(fixture,scene)->{
            outgoing(fixture,scene,false);fixture.terminal=true;fixture.deleteGate=new CountDownLatch(1);
            try{click(R.id.call_reconcile);assertTrue(fixture.deleteRequested.await(10,TimeUnit.SECONDS));assertTrue("Microphone/media must close before network DELETE finishes",fixture.mediaClosed.await(5,TimeUnit.SECONDS));await(scene,fixture,a->a.findViewById(R.id.call_mute)==null);}finally{fixture.deleteGate.countDown();}
            await(scene,fixture,a->a.findViewById(R.id.call_dial)!=null);
            assertFalse(fixture.store.load().has("pending_call"));assertEquals(0,fixture.ends.get());assertEquals(1,fixture.deletes.get());
        });
    }
    @Test public void audioLossAfterCanaryButBeforeDispatchNeverStartsACall()throws Exception{
        for(String mode:new String[]{"vowifi","cellular"})for(boolean inbound:new boolean[]{false,true})run(mode,inbound,(fixture,scene)->{
            fixture.holdReady=true;click(R.id.tab_calls);
            if(inbound){await(scene,fixture,a->a.findViewById(R.id.call_answer)!=null);click(R.id.call_answer);}
            else{if(mode.equals("cellular"))click(R.id.route_cellular);await(scene,fixture,a->a.findViewById(R.id.call_dial).isEnabled());scene.onActivity(a->((EditText)a.findViewById(R.id.dial_number)).setText("+15550100999"));dialogAfter(()->click(R.id.call_dial));click("android:id/button1");}
            assertTrue(fixture.canaryObserved.await(10,TimeUnit.SECONDS));
            java.lang.reflect.Field field=ConfigStore.class.getDeclaredField("LOCK");field.setAccessible(true);Object lock=field.get(null);
            CountDownLatch holding=new CountDownLatch(1),release=new CountDownLatch(1);AtomicReference<Throwable> lockFailure=new AtomicReference<>();
            Thread holder=new Thread(()->{synchronized(lock){holding.countDown();try{if(!release.await(12,TimeUnit.SECONDS))throw new AssertionError("Dispatch gate was not released");}catch(Throwable error){lockFailure.set(error);}}},"fixture-durable-gate");holder.start();
            try{
                assertTrue(holding.await(5,TimeUnit.SECONDS));fixture.ready();
                AtomicReference<NativeAudio> audio=new AtomicReference<>();scene.onActivity(a->{try{java.lang.reflect.Field owner=MainActivity.class.getDeclaredField("service");owner.setAccessible(true);audio.set(((AgentService)owner.get(a)).call.audio);}catch(Exception error){throw new AssertionError(error);}});
                java.lang.reflect.Field ready=NativeAudio.class.getDeclaredField("ready");ready.setAccessible(true);((CompletableFuture<?>)ready.get(audio.get())).get(5,TimeUnit.SECONDS);
                fixture.media.close(1001,"fixture media loss");await(scene,fixture,a->audio.get().closed);
            }finally{release.countDown();holder.join(15000);}
            assertFalse("Durable gate did not exit",holder.isAlive());assertNull(lockFailure.get());await(scene,fixture,a->a.findViewById(R.id.call_dial)!=null);
            assertEquals(0,fixture.starts.get());assertEquals(0,fixture.ends.get());assertFalse(fixture.store.load().has("pending_call"));assertEquals(1,fixture.deletes.get());
        });
    }
    @Test public void shortMediaLossResumesOriginalLeaseBeforeLongLossRequiresExplicitEnd()throws Exception{
        for(String mode:new String[]{"vowifi","cellular"})for(boolean inbound:new boolean[]{false,true})run(mode,inbound,(fixture,scene)->{
            fixture.mediaHeaderDelayMS=650;
            if(inbound){click(R.id.tab_calls);await(scene,fixture,a->a.findViewById(R.id.call_answer)!=null);click(R.id.call_answer);assertTrue(fixture.started.await(15,TimeUnit.SECONDS));await(scene,fixture,a->a.findViewById(R.id.call_dtmf)!=null&&a.findViewById(R.id.call_dtmf).isEnabled());}
            else outgoing(fixture,scene,false);
            JSONObject original=fixture.store.load().getJSONObject("pending_call");
            AtomicReference<NativeAudio> audio=new AtomicReference<>();scene.onActivity(a->{try{java.lang.reflect.Field field=MainActivity.class.getDeclaredField("service");field.setAccessible(true);audio.set(((AgentService)field.get(a)).call.audio);}catch(Exception error){throw new AssertionError(error);}});
            click(R.id.call_mute);assertTrue(audio.get().muted);fixture.requireMutedResume=true;
            long lostAt=SystemClock.elapsedRealtime();fixture.interruptMedia(false);
            assertTrue("Fresh capture and decoded playback after resume",fixture.resumedAudio.await(8,TimeUnit.SECONDS));fixture.check();
            assertTrue("Resume stays inside the client's original window",SystemClock.elapsedRealtime()-lostAt<8500);
            await(scene,fixture,a->a.findViewById(R.id.call_dtmf)!=null&&a.findViewById(R.id.call_dtmf).isEnabled());
            assertFalse(audio.get().closed);assertTrue(audio.get().muted);assertEquals(1,fixture.resumes.get());assertEquals(2,fixture.mediaConnections.get());assertTrue(fixture.resumedPCM.get()>1);assertTrue(fixture.mediaRejected.get()>0);
            assertEquals(1,fixture.leases.get());assertEquals(1,fixture.starts.get());assertEquals(0,fixture.ends.get());
            assertEquals(original.getString("operation_id"),fixture.store.load().getJSONObject("pending_call").getString("operation_id"));
            fixture.requireMutedResume=false;click(R.id.call_mute);assertFalse(audio.get().muted);capture(mode+(inbound?"-incoming":"-outgoing")+"-media-resumed");
            fixture.interruptMedia(true);await(scene,fixture,a->audio.get().closed&&a.findViewById(R.id.call_mute)==null&&a.findViewById(R.id.call_hangup)!=null);
            // Explicit injected guard-unknown fault, not a claim that a normal
            // ten-second server guard leaves a healthy call running forever.
            assertTrue(fixture.guardUnknown.await(12,TimeUnit.SECONDS));assertEquals(1,fixture.guardAttempts.get());assertTrue(fixture.queried.await(5,TimeUnit.SECONDS));
            assertEquals(2,fixture.mediaConnections.get());assertEquals(1,fixture.starts.get());assertEquals(0,fixture.ends.get());assertEquals(1,fixture.leases.get());assertTrue(fixture.store.load().has("pending_call"));
            capture(mode+(inbound?"-incoming":"-outgoing")+"-media-stopped-unconfirmed");
            click(R.id.call_hangup);await(scene,fixture,a->a.findViewById(R.id.call_dial)!=null);
            assertEquals(1,fixture.ends.get());assertEquals(1,fixture.deletes.get());assertFalse(fixture.store.load().has("pending_call"));
        });
    }
    @Test public void lateTerminalWriterCannotDeleteTheReplacementOwnersRecord()throws Exception{
        for(String mode:new String[]{"cellular","vowifi"})run(mode,false,(fixture,scene)->{
            outgoing(fixture,scene,false);JSONObject saved=fixture.store.load().getJSONObject("pending_call");
            AtomicReference<AgentService> owner=new AtomicReference<>();scene.onActivity(a->owner.set(service(a)));
            RemoteCall old=owner.get().call;GatewayApi api=(GatewayApi)field(owner.get(),"api");
            AtomicBoolean checking=(AtomicBoolean)field(old,"checking");
            java.lang.reflect.Field field=ConfigStore.class.getDeclaredField("LOCK");field.setAccessible(true);Object lock=field.get(null);
            CountDownLatch holding=new CountDownLatch(1),release=new CountDownLatch(1);AtomicReference<Throwable> error=new AtomicReference<>();
            Thread holder=new Thread(()->{synchronized(lock){holding.countDown();try{if(!release.await(15,TimeUnit.SECONDS))throw new AssertionError("Store gate not released");}catch(Throwable failure){error.set(failure);}}},"fixture-owner-gate");holder.start();
            AtomicReference<RemoteCall> replacement=new AtomicReference<>();
            try{
                assertTrue(holding.await(5,TimeUnit.SECONDS));fixture.terminal=true;click(R.id.call_reconcile);
                await(scene,fixture,a->old.phase.equals("TERMINAL")&&old.state.resource==R.string.call_saving_recovery);
                scene.onActivity(a->{try{RemoteCall next=new RemoteCall(owner.get(),api,saved,owner.get().io);replacement.set(next);owner.get().call=next;owner.get().changed();}catch(Exception failure){throw new AssertionError(failure);}});
            }finally{release.countDown();holder.join(15000);}
            assertFalse(holder.isAlive());assertNull(error.get());await(scene,fixture,a->!checking.get());
            JSONObject retained=fixture.store.load().getJSONObject("pending_call");assertEquals(saved.getString("operation_id"),retained.getString("operation_id"));assertEquals(saved.getString("phase"),retained.getString("phase"));
            assertNull(replacement.get().audio);assertEquals(0,fixture.deletes.get());assertEquals(0,fixture.ends.get());
            click(R.id.call_reconcile);await(scene,fixture,a->a.findViewById(R.id.call_dial)!=null&&a.findViewById(R.id.call_dial).isEnabled());
            assertFalse(fixture.store.load().has("pending_call"));assertEquals(1,fixture.leases.get());assertEquals(1,fixture.starts.get());assertEquals(1,fixture.mediaConnections.get());assertEquals(1,fixture.deletes.get());
        });
    }
    @Test public void configurationReloadReadsTheTerminalCommitAlreadyAdmittedBeforeInvalidation()throws Exception{
        for(String mode:new String[]{"cellular","vowifi"})run(mode,false,(fixture,scene)->{
            outgoing(fixture,scene,false);AtomicReference<AgentService> owner=new AtomicReference<>();scene.onActivity(a->owner.set(service(a)));RemoteCall old=owner.get().call;
            // A redundant restore during live audio must leave its owner intact.
            scene.onActivity(a->owner.get().onStartCommand(new Intent(a,AgentService.class).setAction(AgentService.RESTORE),0,0));assertTrue(old.ownsRecord());assertSame(old,owner.get().call);
            ConfigStore oldStore=(ConfigStore)field(old,"store");DurableFile original=(DurableFile)field(oldStore,"file");CommitGate gate=new CommitGate();
            java.lang.reflect.Field file=ConfigStore.class.getDeclaredField("file");file.setAccessible(true);file.set(oldStore,new DurableFile(original.base,gate));
            try{
                fixture.terminal=true;click(R.id.call_reconcile);assertTrue("Actual encrypted commit must reach the store gate",gate.entered.await(5,TimeUnit.SECONDS));
                await(scene,fixture,a->!old.busy());
                scene.onActivity(a->owner.get().onStartCommand(new Intent(a,AgentService.class).setAction(AgentService.RESTORE),0,0));
                assertFalse("Invalidation precedes the queued store read",old.ownsRecord());
            }finally{gate.release.countDown();}
            await(scene,fixture,a->owner.get().call==null&&a.findViewById(R.id.call_dial)!=null&&a.findViewById(R.id.call_dial).isEnabled());
            assertFalse(fixture.store.load().has("pending_call"));assertEquals(1,fixture.leases.get());assertEquals(1,fixture.starts.get());assertEquals(1,fixture.mediaConnections.get());assertEquals(1,fixture.deletes.get());assertEquals(0,fixture.ends.get());
        });
    }
    private static final class CommitGate implements DurableFile.IO {
        final AndroidStateIO delegate=new AndroidStateIO();final CountDownLatch entered=new CountDownLatch(1),release=new CountDownLatch(1);final AtomicBoolean armed=new AtomicBoolean(true);
        public boolean exists(File file)throws IOException{return delegate.exists(file);}
        public byte[] read(File file)throws IOException{return delegate.read(file);}
        public void writeSynced(File file,byte[] bytes)throws IOException{
            if(armed.compareAndSet(true,false)){entered.countDown();try{if(!release.await(10,TimeUnit.SECONDS))throw new IOException("Commit gate timed out");}catch(InterruptedException error){Thread.currentThread().interrupt();throw new IOException(error);}}
            delegate.writeSynced(file,bytes);
        }
        public void replace(File from,File to)throws IOException{delegate.replace(from,to);}
        public void syncDirectory(File directory)throws IOException{delegate.syncDirectory(directory);}
    }
    @Test public void rejectedCallWorkDoesNotLeaveAnInMemoryOwnerOrWakeLock()throws Exception{
        run("cellular",false,(fixture,scene)->{
            AtomicReference<AgentService> owner=new AtomicReference<>();scene.onActivity(a->owner.set(service(a)));
            await(scene,fixture,a->{try{return field(owner.get(),"messageSyncOwner")==null;}catch(Exception failure){throw new AssertionError(failure);}});
            AtomicReference<PowerManager.WakeLock> acquired=new AtomicReference<>();
            ((ThreadPoolExecutor)owner.get().io).setRejectedExecutionHandler((work,executor)->{acquired.set(owner.get().call.wake);throw new RejectedExecutionException("Fixture executor stopped");});
            owner.get().io.shutdownNow();
            scene.onActivity(a->{try{owner.get().begin(new CallPlan(fixture.line,"cellular","+15550100999",null));fail("Stopped executor admitted a call");}catch(RejectedExecutionException expected){}catch(Exception failure){throw new AssertionError(failure);}});
            assertNotNull(acquired.get());assertFalse(acquired.get().isHeld());assertNull(owner.get().call);assertFalse(owner.get().accountBusy());assertFalse(fixture.store.load().has("pending_call"));assertEquals(0,fixture.starts.get());assertEquals(0,fixture.leases.get());
        });
    }
    @Test public void savedModeOnlyDraftIsNotOverriddenByThePendingCallsMode()throws Exception{
        run("cellular",false,(fixture,scene)->{
            outgoing(fixture,scene,false);click(R.id.tab_messages);click(R.id.route_vowifi);
            // A saved mode can exist while the line selector has no saved row.
            scene.onActivity(a->{try{for(String key:new String[]{"selectedLine","selectedCard"}){java.lang.reflect.Field field=MainActivity.class.getDeclaredField(key);field.setAccessible(true);field.set(a,"");}java.lang.reflect.Field picker=MainActivity.class.getDeclaredField("lineChoice");picker.setAccessible(true);picker.set(a,null);}catch(Exception failure){throw new AssertionError(failure);}});
            scene.recreate();await(scene,fixture,a->a.findViewById(R.id.route_vowifi)!=null);
            scene.onActivity(a->assertTrue("Explicit saved route wins over recovery fallback",((MaterialButton)a.findViewById(R.id.route_vowifi)).isChecked()));
            click(R.id.call_hangup);await(scene,fixture,a->service(a).call==null);assertFalse(fixture.store.load().has("pending_call"));assertEquals(1,fixture.ends.get());
        });
    }
    private static Object field(Object value,String name)throws Exception{java.lang.reflect.Field field=value.getClass().getDeclaredField(name);field.setAccessible(true);return field.get(value);}
    private static AgentService service(MainActivity activity){try{return (AgentService)field(activity,"service");}catch(Exception failure){throw new AssertionError(failure);}}
    private void outgoing(CallGatewayFixture fixture,ActivityScenario<MainActivity> scene,boolean cancelFirst)throws Exception{
        click(R.id.tab_calls);if(fixture.mode.equals("cellular"))click(R.id.route_cellular);
        await(scene,fixture,a->a.findViewById(R.id.call_dial).isEnabled());scene.onActivity(a->((EditText)a.findViewById(R.id.dial_number)).setText("+15550100999"));
        if(cancelFirst){dialogAfter(()->click(R.id.call_dial));click("android:id/button2");assertEquals(0,fixture.leases.get());assertEquals(0,fixture.starts.get());}
        dialogAfter(()->click(R.id.call_dial));click("android:id/button1");assertTrue(fixture.started.await(15,TimeUnit.SECONDS));
        await(scene,fixture,a->a.findViewById(R.id.call_dtmf)!=null&&a.findViewById(R.id.call_dtmf).isEnabled());
    }
    interface Scenario{void run(CallGatewayFixture fixture,ActivityScenario<MainActivity> scene)throws Exception;}
    private void run(String mode,boolean inbound,Scenario scenario)throws Exception{
        run(mode,inbound,false,scenario);
    }
    private void run(String mode,boolean inbound,boolean coldNotification,Scenario scenario)throws Exception{
        Context context=context();assertTrue(context.getPackageName().endsWith(".qa"));
        permission("android.permission.RECORD_AUDIO");if(Build.VERSION.SDK_INT>=33)permission("android.permission.POST_NOTIFICATIONS");
        try(CallGatewayFixture fixture=new CallGatewayFixture(context,mode,inbound)){
            Intent launch=new Intent(context,MainActivity.class);if(coldNotification)launch.setAction(context.getPackageName()+".OPEN.fixture-cold").putExtra("incoming_event",mode.equals("cellular")?"cell:fixture-cell-incoming":"call:fixture-vowifi-incoming");
            try(ActivityScenario<MainActivity> scene=ActivityScenario.launch(launch)){
                assertTrue(fixture.connected.await(10,TimeUnit.SECONDS));await(scene,fixture,a->a.findViewById(R.id.gateway_status)!=null&&context.getString(R.string.link_online).equals(((TextView)a.findViewById(R.id.gateway_status)).getText().toString()));scenario.run(fixture,scene);fixture.check();
            }finally{
                context.stopService(new Intent(context,AgentService.class));InstrumentationRegistry.getInstrumentation().waitForIdleSync();ConfigStore.intent(()->{}).get(10,TimeUnit.SECONDS);
                fixture.store.update(state->{JSONObject call=state.optJSONObject("pending_call");if(call!=null){assertEquals(fixture.lineID,call.getString("line"));assertEquals(fixture.gateway.url("/").toString().replaceAll("/$",""),call.getString("gateway_origin"));state.remove("pending_call");}});fixture.store.clear();context.getSystemService(NotificationManager.class).cancelAll();
            }
        }
    }
    private static Context context(){return ApplicationProvider.getApplicationContext();}
    private static void capture(String name)throws Exception{for(String command:new String[]{"mkdir -p "+EVIDENCE,"screencap -p "+EVIDENCE+"/"+name+".png"}){android.os.ParcelFileDescriptor fd=automation().executeShellCommand(command);try(java.io.InputStream input=new android.os.ParcelFileDescriptor.AutoCloseInputStream(fd)){byte[] bytes=new byte[1024];while(input.read(bytes)!=-1){}}}}
    private static void permission(String permission)throws Exception{android.os.ParcelFileDescriptor fd=automation().executeShellCommand("pm grant "+context().getPackageName()+" "+permission);try(java.io.InputStream input=new android.os.ParcelFileDescriptor.AutoCloseInputStream(fd)){byte[] bytes=new byte[512];while(input.read(bytes)!=-1){}}}
    private static android.app.UiAutomation automation(){android.app.UiAutomation automation=InstrumentationRegistry.getInstrumentation().getUiAutomation();android.accessibilityservice.AccessibilityServiceInfo info=automation.getServiceInfo();info.flags|=android.accessibilityservice.AccessibilityServiceInfo.FLAG_REPORT_VIEW_IDS;automation.setServiceInfo(info);return automation;}
    private static AccessibilityNodeInfo node(int id){return node(context().getPackageName()+":id/"+context().getResources().getResourceEntryName(id));}
    private static AccessibilityNodeInfo node(String id){AccessibilityNodeInfo root=automation().getRootInActiveWindow();assertNotNull(root);assertEquals(context().getPackageName(),String.valueOf(root.getPackageName()));java.util.List<AccessibilityNodeInfo> nodes=root.findAccessibilityNodeInfosByViewId(id);assertEquals("Expected one native control "+id,1,nodes.size());return nodes.get(0);}
    private static void click(int id){click(context().getPackageName()+":id/"+context().getResources().getResourceEntryName(id));}
    private static void click(String id){try{
        long until=SystemClock.elapsedRealtime()+5000;automation().waitForIdle(200,5000);
        AccessibilityNodeInfo target=null;long delay=50;
        do{
            AccessibilityNodeInfo root=automation().getRootInActiveWindow();
            if(root!=null){assertEquals(context().getPackageName(),String.valueOf(root.getPackageName()));java.util.List<AccessibilityNodeInfo> found=root.findAccessibilityNodeInfosByViewId(id);
                assertTrue("Ambiguous native control "+id,found.size()<=1);if(found.size()==1&&found.get(0).isEnabled()&&found.get(0).isVisibleToUser()){target=found.get(0);break;}}
            long remaining=until-SystemClock.elapsedRealtime();if(remaining<=0)break;SystemClock.sleep(Math.min(delay,remaining));delay=Math.min(500,delay*2);
        }while(SystemClock.elapsedRealtime()<until);
        assertNotNull("Native control did not become available: "+id,target);
        assertTrue(target.performAction(AccessibilityNodeInfo.ACTION_CLICK));InstrumentationRegistry.getInstrumentation().waitForIdleSync();automation().waitForIdle(200,5000);
    }catch(TimeoutException error){throw new AssertionError(error);}}
    private static void dialogAfter(Runnable action)throws Exception{android.app.UiAutomation automation=automation();AccessibilityEvent event=automation.executeAndWaitForEvent(action,e->e.getEventType()==AccessibilityEvent.TYPE_WINDOW_STATE_CHANGED&&String.valueOf(e.getClassName()).contains("AlertDialog")&&context().getPackageName().contentEquals(e.getPackageName()==null?"":e.getPackageName()),10000);event.recycle();automation.waitForIdle(200,5000);}
    private static void await(ActivityScenario<MainActivity> scene,CallGatewayFixture fixture,java.util.function.Predicate<MainActivity> condition)throws Exception{AtomicBoolean ready=new AtomicBoolean();long until=SystemClock.elapsedRealtime()+15000;while(!ready.get()&&SystemClock.elapsedRealtime()<until){fixture.check();InstrumentationRegistry.getInstrumentation().waitForIdleSync();scene.onActivity(a->ready.set(condition.test(a)));if(!ready.get())SystemClock.sleep(100);}fixture.check();assertTrue("Native call workflow did not reach expected state",ready.get());}
}
