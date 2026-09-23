package com.lovitus.mddagent;

import android.app.*;
import android.content.*;
import android.os.*;
import android.service.notification.StatusBarNotification;
import android.view.accessibility.*;
import android.widget.*;
import androidx.test.core.app.*;
import androidx.test.platform.app.InstrumentationRegistry;
import androidx.test.ext.junit.runners.AndroidJUnit4;
import org.json.*;
import org.junit.Test;
import org.junit.runner.RunWith;
import java.util.concurrent.*;
import java.util.concurrent.atomic.*;
import static org.junit.Assert.*;

@RunWith(AndroidJUnit4.class)
public class SmsFlowTest {
    private static final String EVIDENCE="/data/local/tmp/mdd-native-ui-sms-"+java.util.UUID.randomUUID();
    @Test public void unknownAAndSuccessfulBSurviveAndQueryOnlyTheOriginalPayload()throws Exception{
        for(String mode:new String[]{"cellular","vowifi"})run(mode,(fixture,scene)->{
            click(R.id.tab_messages);if(mode.equals("cellular"))click(R.id.route_cellular);await(fixture,()->view(scene,R.id.message_send,true));
            send(scene,"+15550100123","original A");await(fixture,()->!fixture.firstID.isEmpty()&&row(fixture,fixture.firstID).optString("state").equals("unknown")&&view(scene,R.id.message_send,true));
            assertTrue("Unknown journal must refresh without rebuilding the page",root().findAccessibilityNodeInfosByViewId(id(R.id.message_reconcile)).size()>0);
            send(scene,"+15550100999","later B");await(fixture,()->!fixture.secondID.isEmpty()&&row(fixture,fixture.secondID).optString("state").equals("submitted"));
            fixture.appendPartial();await(fixture,()->row(fixture,fixture.firstID).optString("state").equals("submission_observed"));assertEquals("original A",row(fixture,fixture.firstID).getString("body"));
            scene.recreate();await(fixture,()->view(scene,R.id.message_body,false));assertEquals(2,Json.array(new ConfigStore(context()).load(),"message_operations").length());
            node(R.id.message_reconcile).performAction(AccessibilityNodeInfo.AccessibilityAction.ACTION_SHOW_ON_SCREEN.getId());idle();capture(mode+"-unresolved-original");
            click(R.id.message_reconcile);await(fixture,()->row(fixture,fixture.firstID).optString("state").equals("submitted"));
            assertEquals(2,fixture.sends.get());assertEquals(1,fixture.receipts.get());assertFalse(row(fixture,fixture.firstID).has("body"));assertFalse(row(fixture,fixture.secondID).has("body"));
            scene.onActivity(a->assertEquals("later B",((EditText)a.findViewById(R.id.message_body)).getText().toString()));
        });
    }
    @Test public void catchupBeyondFiftyRetriesAnUnchangedSnapshotAndPostsOneSummary()throws Exception{
        run("vowifi",(fixture,scene)->{
            fixture.failNextSync.set(true);fixture.appendReceived(151);await(fixture,()->synced(fixture,151));
            assertTrue(fixture.syncs.get()>=4);assertEquals(1,summaries());StatusBarNotification note=summary();assertEquals(context().getString(R.string.tap_open),note.getNotification().extras.getCharSequence(Notification.EXTRA_TEXT).toString());
            click(R.id.tab_messages);click(R.id.message_sync);await(fixture,()->synced(fixture,151)&&syncIdle(scene));assertEquals(1,summaries());assertEquals(0,fixture.sends.get());
        });
    }
    @Test public void disabledNotificationsDoNotBlockCatchupOrPretendToPost()throws Exception{
        run("cellular",(fixture,scene)->{
            int original=context().getSystemService(NotificationManager.class).getNotificationChannel("incoming").getImportance();
            try{channelBlocked(true);assertEquals(NotificationManager.IMPORTANCE_NONE,context().getSystemService(NotificationManager.class).getNotificationChannel("incoming").getImportance());fixture.appendReceived(151);await(fixture,()->synced(fixture,151));
                assertEquals("suppressed_by_permission",MessageSync.state(fixture.store.load(),fixture.scope).getString("notification_status"));assertEquals(0,summaries());assertEquals(0,fixture.sends.get());}finally{channelBlocked(original==NotificationManager.IMPORTANCE_NONE,original);assertEquals(original,context().getSystemService(NotificationManager.class).getNotificationChannel("incoming").getImportance());}
        });
    }
    @Test public void archivedConversationHistoryCannotKeepPagingAcrossAccountChange()throws Exception{
        run("vowifi",(fixture,scene)->{
            click(R.id.tab_messages);dialogAfter(()->click(R.id.message_conversations));await(fixture,()->!root().findAccessibilityNodeInfosByText("+15550100888").isEmpty());
            AccessibilityNodeInfo list=node(R.id.message_conversation_list);assertTrue(list.getChildCount()>0);assertTrue(list.getChild(0).performAction(AccessibilityNodeInfo.ACTION_CLICK));idle();
            await(fixture,()->!root().findAccessibilityNodeInfosByViewId(id(R.id.message_history_more)).isEmpty()&&node(R.id.message_history_more).isEnabled());
            java.util.List<String> first=historyItems(scene);assertEquals(50,first.size());for(int i=0;i<50;i++)assertTrue(first.get(i).endsWith("fixture message "+(i+16)));
            click(R.id.message_history_more);await(fixture,()->historyItems(scene).size()==65);java.util.List<String> all=historyItems(scene);for(int i=0;i<65;i++)assertTrue(all.get(i).endsWith("fixture message "+(i+1)));capture("archived-history-65");click("android:id/button2");
            dialogAfter(()->click(R.id.message_conversations));await(fixture,()->!root().findAccessibilityNodeInfosByText("+15550100888").isEmpty());list=node(R.id.message_conversation_list);assertTrue(list.getChild(0).performAction(AccessibilityNodeInfo.ACTION_CLICK));idle();await(fixture,()->!root().findAccessibilityNodeInfosByViewId(id(R.id.message_history_more)).isEmpty()&&node(R.id.message_history_more).isEnabled());
            fixture.historyGate=new CountDownLatch(1);click(R.id.message_history_more);assertTrue(fixture.lateHistory.await(5,TimeUnit.SECONDS));
            scene.onActivity(a->{try{java.lang.reflect.Field field=MainActivity.class.getDeclaredField("service");field.setAccessible(true);((AgentService)field.get(a)).prepareLogin();}catch(Exception failure){throw new AssertionError(failure);}});
            fixture.historyGate.countDown();await(fixture,()->{AccessibilityNodeInfo current=automation().getRootInActiveWindow();return current!=null&&context().getPackageName().equals(String.valueOf(current.getPackageName()))&&current.findAccessibilityNodeInfosByViewId(id(R.id.message_history_list)).isEmpty();});assertEquals(0,fixture.sends.get());
        });
    }
    @Test public void authStopRejectsSnapshotAndReconnectTriggersUntilExplicitRetry()throws Exception{
        run("vowifi",(fixture,scene)->{
            fixture.syncError.set(401);fixture.appendReceived(1);await(fixture,()->flag(scene,"messageSyncStopped"));int attempts=fixture.syncs.get();
            long old=observerGeneration(scene);fixture.appendReceived(1);fixture.observer.close(1001,"fixture reconnect");await(fixture,()->fixture.connections.get()==2&&observerGeneration(scene)!=old&&online(scene));drainLink(scene);await(fixture,()->syncIdle(scene));
            assertTrue(flag(scene,"messageSyncStopped"));assertEquals(attempts,fixture.syncs.get());
            fixture.syncError.set(0);click(R.id.tab_messages);click(R.id.message_sync);await(fixture,()->synced(fixture,2));assertFalse(flag(scene,"messageSyncStopped"));
        });
    }
    @Test public void newAccountSyncDoesNotWaitForOrCommitTheOldResponse()throws Exception{
        run("vowifi",(fixture,scene)->{
            fixture.oldSyncGate=new CountDownLatch(1);fixture.blockOldSync.set(true);fixture.appendReceived(1);assertTrue(fixture.oldSyncBlocked.await(10,TimeUnit.SECONDS));
            try{
                scene.onActivity(a->{try{service(a).prepareLogin();}catch(Exception error){throw new AssertionError(error);}});
                fixture.store.update(state->state.put("token","fixture-other-session").put("csrf","fixture-other-csrf").put("account_scope",fixture.otherScope).put("available",true));
                scene.onActivity(a->a.startForegroundService(new Intent(a,AgentService.class).setAction(AgentService.START)));
                await(fixture,()->(fixture.stream+":1").equals(MessageSync.state(fixture.store.load(),fixture.otherScope).optString("cursor"))&&syncIdle(scene));
                assertEquals(fixture.stream+":0",MessageSync.state(fixture.store.load(),fixture.scope).getString("cursor"));
            }finally{fixture.oldSyncGate.countDown();}
            assertTrue(fixture.oldSyncReturned.await(5,TimeUnit.SECONDS));scene.recreate();await(fixture,()->view(scene,R.id.availability_toggle,false)&&syncIdle(scene));assertEquals(fixture.otherScope,fixture.store.load().getString("account_scope"));assertEquals(fixture.stream+":0",MessageSync.state(fixture.store.load(),fixture.scope).getString("cursor"));assertEquals(0,fixture.sends.get());
        });
    }
    @Test public void exhaustedRetryBudgetIgnoresSnapshotsButNewConnectionCanRecover()throws Exception{
        run("vowifi",(fixture,scene)->{
            scene.onActivity(a->{try{java.lang.reflect.Field field=AgentService.class.getDeclaredField("messageSyncFailures");field.setAccessible(true);field.setInt(service(a),6);}catch(Exception error){throw new AssertionError(error);}});
            fixture.failNextSync.set(true);fixture.appendReceived(1);await(fixture,()->flag(scene,"messageSyncStopped"));int attempts=fixture.syncs.get();fixture.appendReceived(1);await(fixture,()->snapshotCount(scene)==2);drainLink(scene);assertEquals(attempts,fixture.syncs.get());
            fixture.observer.close(1001,"fixture recovered transport");await(fixture,()->fixture.connections.get()==2&&synced(fixture,2));assertFalse(flag(scene,"messageSyncStopped"));
        });
    }
    @Test public void pr8TlsReadLossRecoversAfterObserverReconnectWithoutSending()throws Exception{
        run("vowifi",(fixture,scene)->{
            AtomicReference<GatewayApi> client=new AtomicReference<>();AtomicReference<okhttp3.OkHttpClient> previous=new AtomicReference<>();AtomicBoolean fail=new AtomicBoolean(true);CountDownLatch injected=new CountDownLatch(1);
            scene.onActivity(a->{try{
                AgentService owner=service(a);java.lang.reflect.Field api=AgentService.class.getDeclaredField("api");api.setAccessible(true);GatewayApi current=(GatewayApi)api.get(owner);client.set(current);previous.set(current.http);
                okhttp3.OkHttpClient fault=current.http.newBuilder().addInterceptor(chain->{
                    if(chain.request().url().encodedPath().equals("/v1/messages")&&"true".equals(chain.request().url().queryParameter("sync"))&&fail.compareAndSet(true,false)){injected.countDown();throw new javax.net.ssl.SSLException("synthetic interrupted TLS read");}
                    return chain.proceed(chain.request());
                }).build();java.lang.reflect.Field http=GatewayApi.class.getDeclaredField("http");http.setAccessible(true);http.set(current,fault);
                java.lang.reflect.Field budget=AgentService.class.getDeclaredField("messageSyncFailures");budget.setAccessible(true);budget.setInt(owner,6);
            }catch(Exception e){throw new AssertionError(e);}});
            try{
                fixture.appendReceived(1);assertTrue(injected.await(5,TimeUnit.SECONDS));await(fixture,()->flag(scene,"messageSyncStopped"));
                assertFalse("ordinary TLS read was latched as identity failure",flag(scene,"messageSyncTerminal"));
                long generation=observerGeneration(scene);fixture.observer.close(1001,"same certificate restored");
                await(fixture,()->observerGeneration(scene)!=generation&&online(scene)&&synced(fixture,1)&&syncIdle(scene));
                assertEquals(0,fixture.sends.get());assertFalse(flag(scene,"messageSyncStopped"));assertEquals(1,summaries());
            }finally{java.lang.reflect.Field http=GatewayApi.class.getDeclaredField("http");http.setAccessible(true);http.set(client.get(),previous.get());}
        });
    }
    private static JSONObject row(SmsGatewayFixture fixture,String operation)throws Exception{return MessageJournal.find(fixture.store.load(),fixture.scope,operation);}
    private static boolean synced(SmsGatewayFixture fixture,int count){JSONObject state=MessageSync.state(fixture.store.load(),fixture.scope);return (fixture.stream+":"+count).equals(state.optString("cursor"))&&Json.array(state,"pending").length()==0;}
    private static int summaries(){int count=0;for(StatusBarNotification row:context().getSystemService(NotificationManager.class).getActiveNotifications())if(row.getId()==2)count++;return count;}
    private static StatusBarNotification summary(){for(StatusBarNotification row:context().getSystemService(NotificationManager.class).getActiveNotifications())if(row.getId()==2)return row;throw new AssertionError("Missing summary");}
    interface Scenario{void run(SmsGatewayFixture fixture,ActivityScenario<MainActivity> scene)throws Exception;}
    private void run(String mode,Scenario scenario)throws Exception{permission(true);try(SmsGatewayFixture fixture=new SmsGatewayFixture(context(),mode)){
        try(ActivityScenario<MainActivity> scene=ActivityScenario.launch(MainActivity.class)){assertTrue(fixture.connected.await(10,TimeUnit.SECONDS));await(fixture,()->synced(fixture,0)&&view(scene,R.id.availability_toggle,false)&&syncIdle(scene));scenario.run(fixture,scene);fixture.check();}
        finally{context().stopService(new Intent(context(),AgentService.class));InstrumentationRegistry.getInstrumentation().waitForIdleSync();ConfigStore.intent(()->{}).get(10,TimeUnit.SECONDS);fixture.store.update(state->state.remove("message_operations"));fixture.store.clear();context().getSystemService(NotificationManager.class).cancelAll();permission(true);}
    }}
    private static void send(ActivityScenario<MainActivity> scene,String recipient,String body)throws Exception{scene.onActivity(a->{((EditText)a.findViewById(R.id.dial_number)).setText(recipient);((EditText)a.findViewById(R.id.message_body)).setText(body);});dialogAfter(()->click(R.id.message_send));click("android:id/button1");}
    private static Context context(){return ApplicationProvider.getApplicationContext();}
    private static void capture(String name)throws Exception{shell("mkdir -p "+EVIDENCE);shell("screencap -p "+EVIDENCE+"/"+name+".png");}
    private static AgentService service(MainActivity activity)throws Exception{java.lang.reflect.Field field=MainActivity.class.getDeclaredField("service");field.setAccessible(true);return (AgentService)field.get(activity);}
    private static java.util.List<String> historyItems(ActivityScenario<MainActivity> scene){AtomicReference<java.util.List<String>> values=new AtomicReference<>(new java.util.ArrayList<>());scene.onActivity(a->{try{java.lang.reflect.Field field=MainActivity.class.getDeclaredField("activeHistoryDialog");field.setAccessible(true);AlertDialog dialog=(AlertDialog)field.get(a);if(dialog==null)return;ListView list=dialog.findViewById(R.id.message_history_list);if(list==null||list.getAdapter()==null)return;java.util.ArrayList<String> result=new java.util.ArrayList<>();for(int i=0;i<list.getAdapter().getCount();i++)result.add(String.valueOf(list.getAdapter().getItem(i)));values.set(result);}catch(Exception error){throw new AssertionError(error);}});return values.get();}
    private static long observerGeneration(ActivityScenario<MainActivity> scene){AtomicLong value=new AtomicLong();scene.onActivity(a->{try{java.lang.reflect.Field field=AgentService.class.getDeclaredField("observer");field.setAccessible(true);Link link=(Link)field.get(service(a));if(link!=null)value.set(link.generation());}catch(Exception error){throw new AssertionError(error);}});return value.get();}
    private static boolean online(ActivityScenario<MainActivity> scene){AtomicBoolean value=new AtomicBoolean();scene.onActivity(a->{try{value.set(service(a).online());}catch(Exception error){throw new AssertionError(error);}});return value.get();}
    private static int snapshotCount(ActivityScenario<MainActivity> scene){AtomicInteger value=new AtomicInteger();scene.onActivity(a->{try{value.set(Json.array(service(a).snapshot,"messages").length());}catch(Exception error){throw new AssertionError(error);}});return value.get();}
    private static void drainLink(ActivityScenario<MainActivity> scene)throws Exception{AtomicReference<AgentService> owner=new AtomicReference<>();scene.onActivity(a->{try{owner.set(service(a));}catch(Exception error){throw new AssertionError(error);}});owner.get().loop.submit(()->{}).get(5,TimeUnit.SECONDS);idle();}
    private static boolean flag(ActivityScenario<MainActivity> scene,String name){AtomicBoolean value=new AtomicBoolean();scene.onActivity(a->{try{java.lang.reflect.Field field=AgentService.class.getDeclaredField(name);field.setAccessible(true);value.set(field.getBoolean(service(a)));}catch(Exception error){throw new AssertionError(error);}});return value.get();}
    private static boolean syncIdle(ActivityScenario<MainActivity> scene){AtomicBoolean idle=new AtomicBoolean();scene.onActivity(a->{try{java.lang.reflect.Field field=AgentService.class.getDeclaredField("messageSyncOwner");field.setAccessible(true);idle.set(service(a)!=null&&field.get(service(a))==null);}catch(Exception error){throw new AssertionError(error);}});return idle.get();}
    private static String id(int id){return context().getPackageName()+":id/"+context().getResources().getResourceEntryName(id);}
    private static void permission(boolean granted)throws Exception{if(Build.VERSION.SDK_INT<33)return;shell("pm grant "+context().getPackageName()+" android.permission.POST_NOTIFICATIONS");}
    private static void channelBlocked(boolean blocked)throws Exception{
        channelBlocked(blocked,-1);
    }
    private static void channelBlocked(boolean blocked,int restoreImportance)throws Exception{
        assertTrue(context().getPackageName().endsWith(".qa"));
        context().startActivity(new Intent(android.provider.Settings.ACTION_CHANNEL_NOTIFICATION_SETTINGS).putExtra(android.provider.Settings.EXTRA_APP_PACKAGE,context().getPackageName()).putExtra(android.provider.Settings.EXTRA_CHANNEL_ID,"incoming").addFlags(Intent.FLAG_ACTIVITY_NEW_TASK));
        automation().waitForIdle(200,5000);
        long until=SystemClock.elapsedRealtime()+10000;AccessibilityNodeInfo toggle=null;
        while(toggle==null&&SystemClock.elapsedRealtime()<until){AccessibilityNodeInfo current=automation().getRootInActiveWindow();if(current!=null&&"com.android.settings".equals(String.valueOf(current.getPackageName()))&&exactText(current,context().getString(R.string.app_name))&&exactText(current,context().getString(R.string.notification_events))){for(String resource:new String[]{"android:id/switch_widget","com.android.settings:id/switch_widget"}){java.util.List<AccessibilityNodeInfo> nodes=current.findAccessibilityNodeInfosByViewId(resource);if(nodes.size()==1&&nodes.get(0).isCheckable()&&nodes.get(0).isVisibleToUser()){toggle=nodes.get(0);break;}}}if(toggle==null)SystemClock.sleep(100);}
        assertNotNull("Exact QA channel switch must be visible",toggle);if(toggle.isChecked()==blocked){AccessibilityNodeInfo action=toggle;for(int i=0;i<4&&!action.isClickable()&&action.getParent()!=null;i++)action=action.getParent();assertTrue("QA channel preference row must be clickable",action.isClickable());assertTrue("QA channel click must be accepted",action.performAction(AccessibilityNodeInfo.ACTION_CLICK));}
        automation().waitForIdle(200,5000);int importance=context().getSystemService(NotificationManager.class).getNotificationChannel("incoming").getImportance();assertEquals("Settings must change the actual QA channel",blocked,importance==NotificationManager.IMPORTANCE_NONE);
        if(!blocked&&restoreImportance>0&&importance!=restoreImportance){
            // Android 9's master switch restores DEFAULT; restore the original behavior too.
            android.content.res.Resources settings=context().getPackageManager().getResourcesForApplication("com.android.settings");
            settingsClick(settings.getString(settings.getIdentifier("notification_importance_title","string","com.android.settings")));
            String resource=restoreImportance==NotificationManager.IMPORTANCE_HIGH?"notification_importance_high":restoreImportance==NotificationManager.IMPORTANCE_LOW?"notification_importance_low":restoreImportance==NotificationManager.IMPORTANCE_MIN?"notification_importance_min":"notification_importance_default";
            settingsClick(settings.getString(settings.getIdentifier(resource,"string","com.android.settings")));
            assertEquals(restoreImportance,context().getSystemService(NotificationManager.class).getNotificationChannel("incoming").getImportance());
        }
        assertTrue(automation().performGlobalAction(android.accessibilityservice.AccessibilityService.GLOBAL_ACTION_BACK));
        context().startActivity(new Intent(context(),MainActivity.class).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK|Intent.FLAG_ACTIVITY_REORDER_TO_FRONT|Intent.FLAG_ACTIVITY_SINGLE_TOP));idle();
    }
    private static void settingsClick(String text)throws Exception{
        automation().waitForIdle(200,5000);AccessibilityNodeInfo current=automation().getRootInActiveWindow();assertNotNull(current);assertEquals("com.android.settings",String.valueOf(current.getPackageName()));AccessibilityNodeInfo match=null;
        for(AccessibilityNodeInfo node:current.findAccessibilityNodeInfosByText(text))if(text.contentEquals(node.getText()==null?"":node.getText())){assertNull("Expected one Settings label",match);match=node;}
        assertNotNull("Missing Settings behavior: "+text,match);match.performAction(AccessibilityNodeInfo.AccessibilityAction.ACTION_SHOW_ON_SCREEN.getId());for(int i=0;i<4&&!match.isClickable()&&match.getParent()!=null;i++)match=match.getParent();assertTrue(match.isClickable());assertTrue(match.performAction(AccessibilityNodeInfo.ACTION_CLICK));automation().waitForIdle(200,5000);
    }
    private static void shell(String command)throws Exception{android.os.ParcelFileDescriptor fd=automation().executeShellCommand(command);try(java.io.InputStream input=new android.os.ParcelFileDescriptor.AutoCloseInputStream(fd)){byte[] bytes=new byte[256];while(input.read(bytes)!=-1){}}}
    private static boolean exactText(AccessibilityNodeInfo root,String text){for(AccessibilityNodeInfo node:root.findAccessibilityNodeInfosByText(text))if(text.contentEquals(node.getText()==null?"":node.getText()))return true;return false;}
    private static android.app.UiAutomation automation(){android.app.UiAutomation automation=InstrumentationRegistry.getInstrumentation().getUiAutomation();android.accessibilityservice.AccessibilityServiceInfo info=automation.getServiceInfo();info.flags|=android.accessibilityservice.AccessibilityServiceInfo.FLAG_REPORT_VIEW_IDS;automation.setServiceInfo(info);return automation;}
    private static AccessibilityNodeInfo root(){AccessibilityNodeInfo root=automation().getRootInActiveWindow();assertNotNull(root);assertEquals(context().getPackageName(),String.valueOf(root.getPackageName()));return root;}
    private static AccessibilityNodeInfo node(int value){java.util.List<AccessibilityNodeInfo> nodes=root().findAccessibilityNodeInfosByViewId(id(value));assertEquals(1,nodes.size());return nodes.get(0);}
    private static void click(int id){click(id(id));}
    private static void click(String id){idle();java.util.List<AccessibilityNodeInfo> nodes=root().findAccessibilityNodeInfosByViewId(id);assertEquals(1,nodes.size());nodes.get(0).performAction(AccessibilityNodeInfo.AccessibilityAction.ACTION_SHOW_ON_SCREEN.getId());idle();nodes=root().findAccessibilityNodeInfosByViewId(id);assertEquals(1,nodes.size());assertTrue(nodes.get(0).performAction(AccessibilityNodeInfo.ACTION_CLICK));idle();}
    private static void idle(){try{InstrumentationRegistry.getInstrumentation().waitForIdleSync();automation().waitForIdle(200,5000);}catch(TimeoutException error){throw new AssertionError(error);}}
    private static boolean view(ActivityScenario<MainActivity> scene,int id,boolean enabled){AtomicBoolean found=new AtomicBoolean();scene.onActivity(a->{android.view.View view=a.findViewById(id);found.set(view!=null&&(!enabled||view.isEnabled()));});return found.get();}
    private static void dialogAfter(Runnable action)throws Exception{AccessibilityEvent event=automation().executeAndWaitForEvent(action,e->e.getEventType()==AccessibilityEvent.TYPE_WINDOW_STATE_CHANGED&&String.valueOf(e.getClassName()).contains("AlertDialog")&&context().getPackageName().contentEquals(e.getPackageName()==null?"":e.getPackageName()),10000);event.recycle();idle();}
    interface Check{boolean ready()throws Exception;}
    private static void await(SmsGatewayFixture fixture,Check check)throws Exception{long until=SystemClock.elapsedRealtime()+20000;while(SystemClock.elapsedRealtime()<until){fixture.check();if(check.ready())return;SystemClock.sleep(100);}fixture.check();fail("SMS workflow did not reach expected state");}
}
