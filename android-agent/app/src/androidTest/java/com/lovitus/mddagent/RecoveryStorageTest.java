package com.lovitus.mddagent;

import android.content.Context;
import androidx.test.core.app.ApplicationProvider;
import androidx.test.ext.junit.runners.AndroidJUnit4;
import org.junit.Test;
import org.junit.runner.RunWith;
import java.io.File;
import java.nio.file.Files;
import static org.junit.Assert.*;

@RunWith(AndroidJUnit4.class)
public class RecoveryStorageTest {
    @Test public void unreadableCiphertextIsPreservedAcrossNewWrappers()throws Exception {
        Context context=ApplicationProvider.getApplicationContext();ConfigStore store=new ConfigStore(context);
        store.clear();store.save(Json.obj("token","test-private-token"));
        File file=new File(context.getNoBackupFilesDir(),"private-state-v1");
        byte[] good=Files.readAllBytes(file.toPath()),bad=good.clone();bad[bad.length-1]^=1;
        try {
            Files.write(file.toPath(),bad);
            for(int i=0;i<2;i++){
                try{new ConfigStore(context).load();fail("Corrupt storage became setup");}catch(IllegalStateException expected){}
                assertArrayEquals(bad,Files.readAllBytes(file.toPath()));
            }
        }finally{Files.write(file.toPath(),good);store.retry();store.clear();}
    }
    @Test public void ordinaryUpdatesCannotErasePendingCall()throws Exception {
        Context context=ApplicationProvider.getApplicationContext();ConfigStore store=new ConfigStore(context);store.clear();
        try {
            store.update(c->c.put("pending_call",Json.obj("operation_id","original")));
            new ConfigStore(context).update(c->c.put("available",true));
            assertEquals("original",store.load().getJSONObject("pending_call").getString("operation_id"));
            try{store.clear();fail("Unresolved call erased");}catch(IllegalStateException expected){}
        }finally{store.update(c->c.remove("pending_call"));store.clear();}
    }
    @Test public void oversizedStateIsRejectedBeforeReplacingTheReadableCiphertext()throws Exception{
        Context context=ApplicationProvider.getApplicationContext();ConfigStore store=new ConfigStore(context);store.clear();store.save(Json.obj("marker","keep"));
        File file=new File(context.getNoBackupFilesDir(),"private-state-v1");byte[] before=Files.readAllBytes(file.toPath());char[] large=new char[1024*1024];java.util.Arrays.fill(large,'x');
        try{assertThrows(IllegalStateException.class,()->store.update(c->c.put("oversized",new String(large))));assertArrayEquals(before,Files.readAllBytes(file.toPath()));assertEquals("keep",store.load().getString("marker"));store.update(c->c.put("after",true));assertTrue(store.load().getBoolean("after"));}finally{store.clear();}
    }
}
