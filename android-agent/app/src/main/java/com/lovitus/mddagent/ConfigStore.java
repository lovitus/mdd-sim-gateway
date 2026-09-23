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
    static java.util.concurrent.Future<?> intent(Runnable action) { return INTENTS.submit(action); }
    private static final String ALIAS = "mdd-agent-v2-config";
    private static final int MAX_STATE_BYTES = 1024 * 1024;
    private final Context context;
    private final DurableFile file;
    private final AndroidStateIO files = new AndroidStateIO();
    private static boolean writeFault;
    interface Update { void apply(JSONObject current) throws Exception; }
    ConfigStore(Context c) {
        context = c.getApplicationContext();
        file = new DurableFile(new File(context.getNoBackupFilesDir(), "private-state-v1"), files);
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
            return envelope.getJSONObject("data");
        }
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
            try { return read(); }
            catch (Exception e) { throw new IllegalStateException("Saved settings unavailable; data preserved", e); }
        }
    }
    JSONObject update(Update update) {
        synchronized (LOCK) {
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
            try {
                if (!file.exists() && files.exists(file.staged())) {
                    byte[] staged = file.stagedBytes();
                    JSONObject envelope = decrypt(staged);
                    if (envelope.getInt("schema") != 1 || !envelope.has("commit_id")) throw new IOException("Unsupported interrupted state");
                    envelope.getJSONObject("data");
                    file.publish(staged);
                }
                JSONObject current = read();
                files.syncDirectory(file.base.getParentFile());
                writeFault = false;
                return current;
            } catch (Exception e) { throw new IllegalStateException("Saved settings unavailable; data preserved", e); }
        }
    }
    static final class ResetBlocked extends IllegalStateException {
        ResetBlocked() { super("Resolve saved call and uncertain messages before resetting"); }
    }
    static void requireResettable(JSONObject readable) {
        if (readable.has("pending_call") || readable.has("last_sms")) throw new ResetBlocked();
        org.json.JSONArray messages = readable.optJSONArray("message_operations");
        if (messages != null) for (int i = 0; i < messages.length(); i++) {
            JSONObject row = messages.optJSONObject(i);
            if (row == null || !MessageJournal.resolved(row)) throw new ResetBlocked();
        }
    }
    /** Explicit destructive action only. Unreadable bytes are archived before
     * publishing a fresh encrypted state; no exception path calls this method. */
    File resetAfterConfirmation(boolean confirmed) {
        if(!confirmed)throw new IllegalArgumentException("Explicit reset confirmation required");
        synchronized(LOCK){
            // A stale Activity must not reset a readable call that another
            // owner journaled after the confirmation dialog was opened.
            JSONObject readable=null;
            try{readable=read();}catch(Exception unavailable){/* explicitly confirmed unreadable-state reset */}
            if(readable!=null)requireResettable(readable);
            try{
                File parent=context.getNoBackupFilesDir();
                File archive=new File(parent,"private-state-archive-"+Json.id());
                if(!archive.mkdir())throw new IOException("Cannot create private recovery archive");
                files.syncDirectory(parent);
                File legacy=new File(context.getApplicationInfo().dataDir,"shared_prefs/private_config.xml");
                File[] material={file.base,file.staged(),file.owned(),new File(file.base+".bak"),legacy,new File(legacy+".bak")};
                for(int i=0;i<material.length;i++){
                    if(!files.exists(material[i]))continue;
                    byte[] bytes=files.read(material[i]);File copy=new File(archive,i+"-"+material[i].getName());
                    files.writeSynced(copy,bytes);
                    if(!java.util.Arrays.equals(bytes,files.read(copy)))throw new IOException("Recovery archive verification failed");
                }
                files.syncDirectory(archive);files.syncDirectory(parent);
                // Do not delete the original Keystore key. Archived ciphertext
                // remains available for a separately authorized investigation.
                write(new JSONObject());writeFault=false;return archive;
            }catch(Exception failure){writeFault=true;throw new IllegalStateException("Reset failed; recovery material preserved",failure);}
        }
    }
    void clear() {
        update(current -> {
            if (current.has("pending_call")) throw new IllegalStateException("Resolve previous call before signing out");
            org.json.JSONArray messages=current.optJSONArray("message_operations");
            if(messages!=null)for(int i=0;i<messages.length();i++)if(!MessageJournal.resolved(messages.getJSONObject(i)))throw new IllegalStateException("Unresolved messages must be retained");
            java.util.ArrayList<String> keys = new java.util.ArrayList<>(); current.keys().forEachRemaining(keys::add);
            for (String key : keys) current.remove(key);
        });
    }
    JSONObject signOut(){return update(current->{
        for(String key:new String[]{"token","csrf","agent_token","agent_id"})current.remove(key);
        current.put("available",false).put("share",false);
    });}
}
