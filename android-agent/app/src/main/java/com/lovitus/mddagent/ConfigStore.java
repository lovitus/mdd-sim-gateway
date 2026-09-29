package com.lovitus.mddagent;

import android.content.Context;
import android.security.keystore.KeyGenParameterSpec;
import android.security.keystore.KeyProperties;
import android.util.Base64;
import org.json.JSONObject;
import javax.crypto.*;
import javax.crypto.spec.GCMParameterSpec;
import java.io.*;
import java.security.KeyStore;
import java.nio.charset.StandardCharsets;

/** One process-wide writer. A failed read never erases or initializes saved state. */
final class ConfigStore {
    private static final Object LOCK = new Object();
    // Lifecycle intent keeps UI order even across Service destruction/rebinding.
    private static final java.util.concurrent.ExecutorService INTENTS = java.util.concurrent.Executors.newSingleThreadExecutor();
    private static volatile long storageEpoch;
    private static final ThreadLocal<Long> actionEpoch = new ThreadLocal<>();
    static long currentEpoch(){return storageEpoch;}
    static java.util.concurrent.Future<?> intent(Runnable action) {
        final Long inherited=actionEpoch.get();
        final long epoch=inherited==null?storageEpoch:inherited;
        return INTENTS.submit(()->{actionEpoch.set(epoch);try{action.run();}finally{actionEpoch.remove();}});
    }
    private static final String ALIAS = "mdd-agent-v2-config";
    private static final int MAX_STATE_BYTES = 1024 * 1024;
    private final Context context;
    private final DurableFile file;
    private final long openedEpoch;
    private final AndroidStateIO files = new AndroidStateIO();
    private static boolean writeFault;
    interface Update { void apply(JSONObject current) throws Exception; }
    ConfigStore(Context c) {
        context = c.getApplicationContext();
        file = new DurableFile(new File(context.getNoBackupFilesDir(), "private-state-v1"), files);
        Long inherited=actionEpoch.get();openedEpoch=inherited==null?storageEpoch:inherited;
    }
    private void requireOwner(){
        if(openedEpoch!=storageEpoch||actionEpoch.get()!=null&&actionEpoch.get()!=storageEpoch)
            throw new IllegalStateException("Storage owner changed; reload settings");
    }
    private File resetMarker(){return new File(file.base+".reset");}
    private void requireResetCommit(JSONObject data)throws Exception{
        if(files.exists(resetMarker())){
            JSONObject marker=new JSONObject(new String(files.read(resetMarker()),StandardCharsets.UTF_8));
            if(marker.getInt("schema")!=1||marker.getString("archive").isEmpty()
                ||!marker.getString("archive").equals(data.optString("retained_recovery_archive")))
                throw new IOException("Local reset interrupted; original material retained");
        }
    }
    private javax.crypto.SecretKey key(boolean initialize) throws Exception {
        KeyStore s = KeyStore.getInstance("AndroidKeyStore"); s.load(null);
        if (!s.containsAlias(ALIAS)) {
            if (!initialize) throw new IOException("Saved encryption key is unavailable");
            KeyGenerator g = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore");
            g.init(new KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_ENCRYPT | KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM).setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE).build());
            g.generateKey();
        }
        return (javax.crypto.SecretKey) s.getKey(ALIAS, null);
    }
    private JSONObject decrypt(byte[] bytes) throws Exception {
        if (bytes.length < 28 || bytes.length > MAX_STATE_BYTES) throw new IOException("Invalid private state");
        Cipher c = Cipher.getInstance("AES/GCM/NoPadding");
        c.init(Cipher.DECRYPT_MODE, key(false), new GCMParameterSpec(128, bytes, 0, 12));
        return new JSONObject(new String(c.doFinal(bytes, 12, bytes.length - 12), StandardCharsets.UTF_8));
    }
    private JSONObject read() throws Exception {
        File base = file.base;
        if (file.exists()) {
            JSONObject envelope = decrypt(file.read());
            if (envelope.getInt("schema") != 1) throw new IOException("Unsupported saved state version");
            JSONObject data=envelope.getJSONObject("data");requireResetCommit(data);return data;
        }
        if(files.exists(resetMarker()))throw new IOException("Local reset interrupted; original material retained");
        if (file.hasRecoveryMaterial()) throw new IOException("Interrupted or missing private state write");
        File legacy = new File(context.getApplicationInfo().dataDir, "shared_prefs/private_config.xml");
        if (files.exists(legacy) || files.exists(new File(legacy + ".bak"))) {
            File backup = new File(legacy + ".bak");
            File source = files.exists(backup) ? backup : legacy;
            if (source.length() > 1024 * 1024) throw new IOException("Legacy settings too large");
            String sealed = "";
            try (FileInputStream input = new FileInputStream(source)) {
                org.xmlpull.v1.XmlPullParser parser = android.util.Xml.newPullParser();
                parser.setInput(input, "UTF-8");
                for (int event=parser.next();event!=org.xmlpull.v1.XmlPullParser.END_DOCUMENT;event=parser.next()) {
                    if(event==org.xmlpull.v1.XmlPullParser.START_TAG && "string".equals(parser.getName()) && "sealed".equals(parser.getAttributeValue(null,"name"))) {
                        if(!sealed.isEmpty()) throw new IOException("Duplicate legacy state");
                        sealed=parser.nextText();
                    }
                }
            }
            if (sealed.isEmpty()) throw new IOException("Existing saved settings cannot be read");
            return decrypt(Base64.decode(sealed, Base64.NO_WRAP));
        }
        KeyStore s = KeyStore.getInstance("AndroidKeyStore"); s.load(null);
        if (s.containsAlias(ALIAS)) throw new IOException("Saved state missing; initialization refused");
        return new JSONObject();
    }
    JSONObject load() {
        synchronized (LOCK) {
            requireOwner();
            try { return read(); }
            catch (Exception e) { throw new IllegalStateException("Saved settings unavailable; data preserved", e); }
        }
    }
    JSONObject update(Update update) {
        synchronized (LOCK) {
            requireOwner();
            if (writeFault) throw new IllegalStateException("Storage write uncertain; retry storage first");
            try {
                JSONObject current = read(); update.apply(current); write(current);
                return new JSONObject(current.toString());
            } catch (Exception e) { throw new IllegalStateException("Private state update failed; data preserved", e); }
        }
    }
    void save(JSONObject value) {
        update(current -> {
            if(current.has("pending_call")) throw new IllegalStateException("Resolve previous call before replacing configuration");
            java.util.Iterator<String> keys = value.keys();
            while (keys.hasNext()) { String k = keys.next(); if (!k.equals("pending_call")) current.put(k, value.get(k)); }
        });
    }
    private void write(JSONObject value) throws Exception {
        byte[] plain = Json.obj("schema", 1, "commit_id", Json.id(), "data", value).toString().getBytes(StandardCharsets.UTF_8);
        if (plain.length > MAX_STATE_BYTES - 28) throw new IOException("Private state capacity reached; existing data preserved");
        Cipher c = Cipher.getInstance("AES/GCM/NoPadding");
        c.init(Cipher.ENCRYPT_MODE, key(true));
        byte[] encrypted = c.doFinal(plain), iv = c.getIV();
        byte[] bytes = new byte[iv.length + encrypted.length];
        System.arraycopy(iv, 0, bytes, 0, iv.length); System.arraycopy(encrypted, 0, bytes, iv.length, encrypted.length);
        try {
            file.write(bytes);
            decrypt(file.read());
        } catch (Exception e) {
            writeFault = true;
            throw e;
        }
    }
    JSONObject retry() {
        synchronized (LOCK) {
            requireOwner();
            try {
                boolean resetPending=false;
                if(files.exists(resetMarker())){try{read();}catch(Exception interrupted){resetPending=true;}}
                if ((!file.exists()||resetPending) && files.exists(file.staged())) {
                    byte[] staged = file.stagedBytes();
                    JSONObject envelope = decrypt(staged);
                    if (envelope.getInt("schema") != 1 || !envelope.has("commit_id")) throw new IOException("Unsupported interrupted state");
                    requireResetCommit(envelope.getJSONObject("data"));
                    file.publish(staged);
                }
                JSONObject current = read();
                files.syncDirectory(file.base.getParentFile());
                writeFault = false;
                return current;
            } catch (Exception e) { throw new IllegalStateException("Saved settings unavailable; data preserved", e); }
        }
    }
    static void requireResettable(JSONObject current){
        if(current.has("pending_call")||current.has("last_sms"))
            throw new IllegalStateException("Known unresolved call or SMS; reset refused");
        org.json.JSONArray messages=current.optJSONArray("message_operations");
        if(current.has("message_operations")&&messages==null)throw new IllegalStateException("Unrecognized message records; reset refused");
        if(messages!=null)for(int i=0;i<messages.length();i++){
            JSONObject row=messages.optJSONObject(i);
            if(row==null||!MessageJournal.resolved(row))throw new IllegalStateException("Known unresolved SMS; reset refused");
        }
    }
    static final class ResetPlan {
        private final long epoch;
        private final byte[][] material;
        private final boolean hadKey;
        private ResetPlan(long epoch,byte[][] material,boolean hadKey){this.epoch=epoch;this.material=material;this.hadKey=hadKey;}
    }
    private File[] resetSources(){
        File legacy=new File(context.getApplicationInfo().dataDir,"shared_prefs/private_config.xml");
        return new File[]{file.base,file.staged(),file.owned(),new File(file.base+".bak"),legacy,new File(legacy+".bak"),resetMarker()};
    }
    private boolean hasKey()throws Exception{KeyStore keys=KeyStore.getInstance("AndroidKeyStore");keys.load(null);return keys.containsAlias(ALIAS);}
    private byte[][] resetMaterial()throws Exception{
        File[] paths=resetSources();byte[][] material=new byte[paths.length][];
        for(int i=0;i<paths.length;i++)material[i]=files.exists(paths[i])?files.read(paths[i]):null;
        return material;
    }
    private void checkResetRecords(byte[][] material)throws Exception{
        for(int i:new int[]{0,1,3,4,5}){
            if(material[i]==null)continue;
            JSONObject candidate;
            try{
                if(i<4)candidate=decrypt(material[i]).getJSONObject("data");
                else{
                    org.xmlpull.v1.XmlPullParser parser=android.util.Xml.newPullParser();
                    parser.setInput(new ByteArrayInputStream(material[i]),"UTF-8");String sealed="";
                    for(int event=parser.next();event!=org.xmlpull.v1.XmlPullParser.END_DOCUMENT;event=parser.next())
                        if(event==org.xmlpull.v1.XmlPullParser.START_TAG&&"string".equals(parser.getName())&&"sealed".equals(parser.getAttributeValue(null,"name")))sealed=parser.nextText();
                    candidate=decrypt(Base64.decode(sealed,Base64.NO_WRAP));
                }
            }catch(Exception unreadable){continue;} // Unknown, never evidence of no remote operation.
            requireResettable(candidate);
        }
    }
    ResetPlan prepareReset(){
        synchronized(LOCK){
            requireOwner();
            try{byte[][] material=resetMaterial();checkResetRecords(material);return new ResetPlan(storageEpoch,material,hasKey());}
            catch(Exception failure){throw new IllegalStateException("Cannot prepare local reset; data preserved",failure);}
        }
    }
    /** Last-resort local reset; adapted from the repository's earlier client F6 recovery. */
    JSONObject resetAfterConfirmation(ResetPlan plan){
        synchronized(LOCK){
            requireOwner();
            try{
                if(plan==null||plan.epoch!=storageEpoch||plan.hadKey!=hasKey()
                    ||!java.util.Arrays.deepEquals(plan.material,resetMaterial()))throw new IOException("Saved state changed; review reset again");
                checkResetRecords(plan.material);
                storageEpoch++;writeFault=true;
                File archive=new File(context.getNoBackupFilesDir(),"private-state-archive-"+Json.id());
                if(!archive.mkdir())throw new IOException("Cannot retain original private state");
                files.syncDirectory(archive.getParentFile());
                org.json.JSONArray entries=new org.json.JSONArray();File[] sources=resetSources();
                for(int i=0;i<sources.length;i++){
                    byte[] original=plan.material[i];String name=i+"-"+sources[i].getName();
                    entries.put(Json.obj("file",name,"present",original!=null,"sha256",original==null?"":Json.sha(original)));
                    if(original==null)continue;
                    File copy=new File(archive,name);files.writeSynced(copy,original);
                    if(!java.util.Arrays.equals(original,files.read(copy)))throw new IOException("Retained material verification failed");
                }
                byte[] manifest=Json.obj("schema",1,"files",entries).toString().getBytes(StandardCharsets.UTF_8);
                File manifestFile=new File(archive,"manifest.json");files.writeSynced(manifestFile,manifest);
                if(!java.util.Arrays.equals(manifest,files.read(manifestFile)))throw new IOException("Retained manifest verification failed");
                files.syncDirectory(archive);files.syncDirectory(archive.getParentFile());
                // The durable intent prevents old XML/base fallback if replacement is interrupted.
                files.writeSynced(resetMarker(),Json.obj("schema",1,"archive",archive.getName()).toString().getBytes(StandardCharsets.UTF_8));
                files.syncDirectory(file.base.getParentFile());
                JSONObject fresh=Json.obj("available",false,"share",false,"retained_recovery_archive",archive.getName());
                write(fresh);requireResetCommit(read());writeFault=false;
                return fresh;
            }catch(Exception failure){throw new IllegalStateException("Local reset not confirmed; recovery material retained",failure);}
        }
    }
    void clear() {
        update(current -> {
            if (current.has("pending_call")) throw new IllegalStateException("Resolve previous call before signing out");
            org.json.JSONArray messages=current.optJSONArray("message_operations");
            if(messages!=null)for(int i=0;i<messages.length();i++)if(!MessageJournal.resolved(messages.getJSONObject(i)))throw new IllegalStateException("Unresolved messages must be retained");
            java.util.ArrayList<String> keys = new java.util.ArrayList<>(); current.keys().forEachRemaining(keys::add);
            // This is commit provenance, not a credential; the durable reset marker still binds it.
            for (String key : keys) if(!key.equals("retained_recovery_archive"))current.remove(key);
        });
    }
    JSONObject signOut(){return update(current->{
        for(String key:new String[]{"token","csrf","agent_token","agent_id"})current.remove(key);
        current.put("available",false).put("share",false);
    });}
}
