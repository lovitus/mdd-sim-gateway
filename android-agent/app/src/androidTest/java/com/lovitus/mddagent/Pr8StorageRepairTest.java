package com.lovitus.mddagent;
import android.app.UiAutomation;
import android.content.*;
import android.os.SystemClock;
import android.view.accessibility.AccessibilityNodeInfo;
import androidx.test.core.app.*;
import androidx.test.platform.app.InstrumentationRegistry;
import androidx.test.ext.junit.runners.AndroidJUnit4;
import java.io.File;
import java.nio.file.Files;
import java.security.KeyStore;
import java.util.concurrent.TimeUnit;
import org.json.JSONObject;
import org.junit.Test;
import org.junit.runner.RunWith;
import static org.junit.Assert.*;

@RunWith(AndroidJUnit4.class)
public class Pr8StorageRepairTest {
    private static Context context(){Context c=ApplicationProvider.getApplicationContext();assertTrue(c.getPackageName().endsWith(".qa"));return c;}
    private static File base(Context c){return new File(c.getNoBackupFilesDir(),"private-state-v1");}
    private static void drain()throws Exception{ConfigStore.intent(()->{}).get(10,TimeUnit.SECONDS);InstrumentationRegistry.getInstrumentation().waitForIdleSync();}
    @Test public void bootstrapCrashShapesRetainMaterialUntilExplicitReset()throws Exception{
        Context c=context();ConfigStore store=new ConfigStore(c);store.clear();
        File file=base(c),staged=new File(file+".new"),marker=new File(file+".owned");
        for(int shape=0;shape<3;shape++){
            store.save(Json.obj("test","prior"));byte[] good=Files.readAllBytes(file.toPath());byte[] partial={1,2,3};
            try{
                Files.delete(file.toPath());Files.deleteIfExists(staged.toPath());if(shape==0)Files.delete(marker.toPath());if(shape==2)Files.write(staged.toPath(),partial);
                for(int wrapper=0;wrapper<2;wrapper++){assertThrows(IllegalStateException.class,()->new ConfigStore(c).retry());assertFalse(file.exists());}
                assertThrows(IllegalArgumentException.class,()->store.resetAfterConfirmation(false));assertFalse(file.exists());
                File archive=store.resetAfterConfirmation(true);assertTrue(archive.isDirectory());assertEquals(0,new ConfigStore(c).load().length());
                if(shape==2)assertArrayEquals(partial,Files.readAllBytes(new File(archive,"1-private-state-v1.new").toPath()));
                if(shape>0)assertArrayEquals(new byte[]{1},Files.readAllBytes(new File(archive,"2-private-state-v1.owned").toPath()));
                // Same original key can still read old ciphertext after reset.
                Files.write(file.toPath(),good);assertEquals("prior",store.retry().getString("test"));
            }finally{Files.write(file.toPath(),good);Files.write(marker.toPath(),new byte[]{1});Files.deleteIfExists(staged.toPath());store.retry();store.clear();}
        }
    }
    @Test public void corruptStorageHasDoubleConfirmedNativeEscapeAndKeepsCiphertext()throws Exception{
        Context c=context();ConfigStore store=new ConfigStore(c);store.clear();store.save(Json.obj("token","original-private-token"));File file=base(c);byte[] good=Files.readAllBytes(file.toPath()),bad=good.clone();bad[bad.length-1]^=1;Files.write(file.toPath(),bad);
        try(ActivityScenario<MainActivity> scene=ActivityScenario.launch(MainActivity.class)){
            awaitNode(id(R.id.storage_reset));click(id(R.id.storage_reset));awaitNode("android:id/button1");click("android:id/button2");assertArrayEquals(bad,Files.readAllBytes(file.toPath()));
            click(id(R.id.storage_reset));awaitNode("android:id/button1");click("android:id/button1");awaitText(c.getString(R.string.storage_reset_final));assertArrayEquals(bad,Files.readAllBytes(file.toPath()));click("android:id/button1");
            awaitNode(id(R.id.connect_gateway));drain();assertFalse(store.load().has("token"));
            boolean retained=false;File[] archives=c.getNoBackupFilesDir().listFiles((dir,name)->name.startsWith("private-state-archive-"));assertNotNull(archives);for(File dir:archives){File saved=new File(dir,"0-private-state-v1");if(saved.exists()&&java.util.Arrays.equals(bad,Files.readAllBytes(saved.toPath())))retained=true;}
            assertTrue("explicit reset discarded encrypted recovery material",retained);
        }finally{c.stopService(new Intent(c,AgentService.class));drain();Files.write(file.toPath(),good);store.retry();store.clear();}
    }
    private static UiAutomation automation(){UiAutomation a=InstrumentationRegistry.getInstrumentation().getUiAutomation();android.accessibilityservice.AccessibilityServiceInfo i=a.getServiceInfo();i.flags|=android.accessibilityservice.AccessibilityServiceInfo.FLAG_REPORT_VIEW_IDS;a.setServiceInfo(i);return a;}
    private static String id(int value){return context().getPackageName()+":id/"+context().getResources().getResourceEntryName(value);}
    private static AccessibilityNodeInfo awaitNode(String resource){long end=SystemClock.elapsedRealtime()+10000;while(SystemClock.elapsedRealtime()<end){AccessibilityNodeInfo root=automation().getRootInActiveWindow();if(root!=null){java.util.List<AccessibilityNodeInfo> list=root.findAccessibilityNodeInfosByViewId(resource);if(list.size()==1&&list.get(0).isVisibleToUser())return list.get(0);}SystemClock.sleep(40);}throw new AssertionError("Missing native action "+resource);}
    private static void awaitText(String text){long end=SystemClock.elapsedRealtime()+5000;while(SystemClock.elapsedRealtime()<end){AccessibilityNodeInfo root=automation().getRootInActiveWindow();if(root!=null&&!root.findAccessibilityNodeInfosByText(text).isEmpty())return;SystemClock.sleep(40);}throw new AssertionError("Second explicit reset warning missing");}
    private static void click(String resource){assertTrue(awaitNode(resource).performAction(AccessibilityNodeInfo.ACTION_CLICK));InstrumentationRegistry.getInstrumentation().waitForIdleSync();}
}
