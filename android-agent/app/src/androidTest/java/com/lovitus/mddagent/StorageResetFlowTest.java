package com.lovitus.mddagent;

import android.content.*;
import android.os.*;
import android.view.accessibility.*;
import androidx.test.core.app.*;
import androidx.test.platform.app.InstrumentationRegistry;
import androidx.test.ext.junit.runners.AndroidJUnit4;
import java.io.File;
import java.nio.file.Files;
import java.util.*;
import java.util.concurrent.*;
import org.junit.Test;
import org.junit.runner.RunWith;
import static org.junit.Assert.*;

/** A QA-only user path from unreadable saved bytes; no network or system data clear. */
@RunWith(AndroidJUnit4.class)
public class StorageResetFlowTest {
    @Test public void unreadableStateHasATwiceConfirmedResetAndPreservesOriginalBytesAndKeys()throws Exception {
        Context context=ApplicationProvider.getApplicationContext();assertTrue(context.getPackageName().endsWith(".qa"));context.stopService(new Intent(context,AgentService.class));
        ConfigStore.intent(()->{}).get(10,TimeUnit.SECONDS);ConfigStore store=new ConfigStore(context);store.retry();store.clear();store.save(Json.obj("token","fixture-old-token","available",false));
        File base=new File(context.getNoBackupFilesDir(),"private-state-v1");byte[] good=Files.readAllBytes(base.toPath()),bad=good.clone();bad[bad.length-1]^=1;Files.write(base.toPath(),bad);
        Set<String> archives=RecoveryStorageTest.archives(context),keys=RecoveryStorageTest.keys();
        try(ActivityScenario<MainActivity> scene=ActivityScenario.launch(MainActivity.class)){
            waitFor(context,R.id.storage_reset);click(context,R.id.storage_reset);waitForId("android:id/button1");clickId("android:id/button1");
            waitFor(context,R.id.storage_reset_phrase);Bundle text=new Bundle();text.putCharSequence(AccessibilityNodeInfo.ACTION_ARGUMENT_SET_TEXT_CHARSEQUENCE,"RESET");assertTrue(node(id(context,R.id.storage_reset_phrase)).performAction(AccessibilityNodeInfo.ACTION_SET_TEXT,text));clickId("android:id/button1");
            waitFor(context,R.id.connect_gateway);ConfigStore.intent(()->{}).get(10,TimeUnit.SECONDS);
            ConfigStore fresh=new ConfigStore(context);assertTrue(fresh.load().optString("token").isEmpty());assertTrue(RecoveryStorageTest.keys().containsAll(keys));
            Set<String> created=RecoveryStorageTest.archives(context);created.removeAll(archives);assertEquals(1,created.size());File escrow=new File(new File(new File(context.getNoBackupFilesDir(),"private-state-recovery"),created.iterator().next()),"0-private-state-v1");assertArrayEquals(bad,Files.readAllBytes(escrow.toPath()));
            assertThrows(IllegalStateException.class,()->store.update(c->c.put("token","stale")));
        }finally{
            context.stopService(new Intent(context,AgentService.class));ConfigStore.intent(()->{}).get(10,TimeUnit.SECONDS);InstrumentationRegistry.getInstrumentation().waitForIdleSync();
            ConfigStore cleanup=new ConfigStore(context);try{cleanup.retry();}catch(Exception e){Files.write(base.toPath(),good);Files.write(new File(base+".owned").toPath(),new byte[]{1});cleanup.retry();}cleanup.clear();RecoveryStorageTest.removeNewArchives(context,archives);
        }
    }
    private static String id(Context context,int id){return context.getPackageName()+":id/"+context.getResources().getResourceEntryName(id);}
    private static android.app.UiAutomation automation(){android.app.UiAutomation value=InstrumentationRegistry.getInstrumentation().getUiAutomation();android.accessibilityservice.AccessibilityServiceInfo info=value.getServiceInfo();info.flags|=android.accessibilityservice.AccessibilityServiceInfo.FLAG_REPORT_VIEW_IDS;value.setServiceInfo(info);return value;}
    private static AccessibilityNodeInfo node(String id){AccessibilityNodeInfo root=automation().getRootInActiveWindow();if(root==null)return null;java.util.List<AccessibilityNodeInfo> nodes=root.findAccessibilityNodeInfosByViewId(id);return nodes.size()==1?nodes.get(0):null;}
    private static void waitFor(Context c,int id)throws Exception{waitForId(id(c,id));}
    private static void waitForId(String id)throws Exception{long deadline=SystemClock.elapsedRealtime()+15000;while(SystemClock.elapsedRealtime()<deadline){InstrumentationRegistry.getInstrumentation().waitForIdleSync();AccessibilityNodeInfo view=node(id);if(view!=null&&view.isVisibleToUser())return;SystemClock.sleep(50);}fail("Missing native control "+id);}
    private static void click(Context c,int id)throws Exception{clickId(id(c,id));}
    private static void clickId(String id)throws Exception{waitForId(id);AccessibilityNodeInfo view=node(id);assertTrue(view.isEnabled());assertTrue(view.performAction(AccessibilityNodeInfo.ACTION_CLICK));InstrumentationRegistry.getInstrumentation().waitForIdleSync();}
}
