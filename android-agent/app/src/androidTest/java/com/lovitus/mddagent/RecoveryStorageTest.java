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
    @Test public void explicitResetEscrowsIncompleteBootstrapAndFencesOldWriters()throws Exception {
        Context context=ApplicationProvider.getApplicationContext();assertTrue(context.getPackageName().endsWith(".qa"));
        File base=new File(context.getNoBackupFilesDir(),"private-state-v1"),owned=new File(base+".owned"),staged=new File(base+".new");
        for(int shape=0;shape<3;shape++){
            ConfigStore store=new ConfigStore(context);store.retry();store.clear();store.save(Json.obj("marker","original"));
            ConfigStore stale=new ConfigStore(context);byte[] original=Files.readAllBytes(base.toPath());
            java.util.Set<String> keys=keys();java.util.Set<String> priorArchives=archives(context);
            Files.delete(base.toPath());Files.deleteIfExists(staged.toPath());
            if(shape==0)Files.delete(owned.toPath());
            else if(shape==2)Files.write(staged.toPath(),new byte[]{1,2,3});
            try{
                assertThrows(IllegalStateException.class,store::retry);
                assertFalse(base.exists());
                org.json.JSONObject empty=store.resetAfterConsent();assertFalse(empty.has("marker"));
                assertTrue(keys().containsAll(keys));assertTrue(keys().size()>keys.size());
                assertThrows(IllegalStateException.class,()->stale.update(c->c.put("resurrected",true)));
                ConfigStore fresh=new ConfigStore(context);fresh.update(c->c.put("after_reset",true));assertTrue(fresh.load().getBoolean("after_reset"));assertFalse(fresh.load().has("resurrected"));
                java.util.Set<String> created=archives(context);created.removeAll(priorArchives);assertEquals(1,created.size());
                File archive=new File(new File(context.getNoBackupFilesDir(),"private-state-recovery"),created.iterator().next());
                assertTrue(new File(archive,"manifest.json").isFile());
                if(shape==2)assertArrayEquals(new byte[]{1,2,3},Files.readAllBytes(new File(archive,"1-private-state-v1.new").toPath()));
            }finally{
                if(!base.exists()){Files.write(base.toPath(),original);Files.write(owned.toPath(),new byte[]{1});Files.deleteIfExists(staged.toPath());}
                ConfigStore cleanup=new ConfigStore(context);cleanup.retry();cleanup.clear();removeNewArchives(context,priorArchives);
            }
        }
    }
    @Test public void validCiphertextCanRepairOnlyAMissingOwnershipMarker()throws Exception {
        Context context=ApplicationProvider.getApplicationContext();ConfigStore store=new ConfigStore(context);store.clear();store.save(Json.obj("marker","keep"));
        File base=new File(context.getNoBackupFilesDir(),"private-state-v1"),owned=new File(base+".owned");byte[] original=Files.readAllBytes(base.toPath());
        try{Files.delete(owned.toPath());assertThrows(IllegalStateException.class,store::load);assertEquals("keep",store.retry().getString("marker"));assertArrayEquals(original,Files.readAllBytes(base.toPath()));
            Files.write(owned.toPath(),new byte[]{2});assertThrows(IllegalStateException.class,store::retry);assertArrayEquals(original,Files.readAllBytes(base.toPath()));}
        finally{Files.write(owned.toPath(),new byte[]{1});store.retry();store.clear();}
    }
    @Test public void refusedResetCannotPoisonOrEraseKnownCallAndMessageRecords()throws Exception{
        Context context=ApplicationProvider.getApplicationContext();ConfigStore store=new ConfigStore(context);store.clear();
        try{store.update(c->c.put("pending_call",Json.obj("operation_id","original")));assertThrows(IllegalStateException.class,store::resetAfterConsent);
            store.update(c->c.put("setting","still-writable"));assertEquals("original",store.load().getJSONObject("pending_call").getString("operation_id"));
            store.update(c->{c.remove("pending_call");c.put("message_operations",new org.json.JSONArray().put(Json.obj("state","unknown","operation_id","original-message")));});
            assertThrows(IllegalStateException.class,store::resetAfterConsent);store.update(c->c.put("setting","still-writable"));
            assertEquals("original-message",store.load().getJSONArray("message_operations").getJSONObject(0).getString("operation_id"));
        }finally{store.update(c->{c.remove("pending_call");c.remove("message_operations");});store.clear();}
    }
    @Test public void resetCannotBypassAnAuthenticatedStagedUnresolvedRecord()throws Exception{
        Context context=ApplicationProvider.getApplicationContext();ConfigStore store=new ConfigStore(context);store.retry();store.clear();
        File base=new File(context.getNoBackupFilesDir(),"private-state-v1"),staged=new File(base+".new"),owned=new File(base+".owned");
        for(boolean message:new boolean[]{false,true}){
            store.update(c->{if(message)c.put("message_operations",new org.json.JSONArray().put(Json.obj("state","unknown","operation_id","staged-sms")));else c.put("pending_call",Json.obj("operation_id","staged-call"));});
            byte[] bytes=Files.readAllBytes(base.toPath());java.util.Set<String> before=archives(context);
            try{
                Files.write(staged.toPath(),bytes);Files.delete(base.toPath());Files.delete(owned.toPath());
                assertThrows(IllegalStateException.class,store::retry);
                assertThrows(IllegalStateException.class,store::resetAfterConsent);
                assertArrayEquals(bytes,Files.readAllBytes(staged.toPath()));assertFalse(base.exists());assertEquals(before,archives(context));
            }finally{
                Files.write(base.toPath(),bytes);Files.write(owned.toPath(),new byte[]{1});Files.deleteIfExists(staged.toPath());store.retry();
                store.update(c->{c.remove("pending_call");c.remove("message_operations");});store.clear();
            }
        }
    }
    static java.util.Set<String> keys()throws Exception{java.security.KeyStore store=java.security.KeyStore.getInstance("AndroidKeyStore");store.load(null);return new java.util.HashSet<>(java.util.Collections.list(store.aliases()));}
    static java.util.Set<String> archives(Context context){String[] names=new File(context.getNoBackupFilesDir(),"private-state-recovery").list();return names==null?new java.util.HashSet<>():new java.util.HashSet<>(java.util.Arrays.asList(names));}
    static void removeNewArchives(Context context,java.util.Set<String> before)throws Exception{
        java.util.Set<String> created=archives(context);created.removeAll(before);for(String name:created){File archive=new File(new File(context.getNoBackupFilesDir(),"private-state-recovery"),name);try(java.util.stream.Stream<java.nio.file.Path> paths=Files.walk(archive.toPath())){for(java.nio.file.Path path:(Iterable<java.nio.file.Path>)paths.sorted(java.util.Comparator.reverseOrder())::iterator)Files.delete(path);}}
    }
}
