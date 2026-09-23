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
import java.util.Arrays;

/** One process-wide writer. Reads never erase data or replace a missing key. */
final class ConfigStore {
    private static final Object LOCK = new Object();
    private static final java.util.concurrent.ExecutorService INTENTS = java.util.concurrent.Executors.newSingleThreadExecutor();
    static java.util.concurrent.Future<?> intent(Runnable action) { return INTENTS.submit(action); }
    private static final String ALIAS = "mdd-agent-v2-config";
    private static final byte[] MAGIC = {'M','D','D','S','T','A','T','E',2};
    private static final int MAX_STATE_BYTES = 1024 * 1024;
    private static boolean writeFault;
    private static volatile long generation;
    private long epoch;
    private final Context context;
    private final DurableFile file;
    private final AndroidStateIO files = new AndroidStateIO();
    interface Update { void apply(JSONObject current) throws Exception; }
    ConfigStore(Context c) {
        context = c.getApplicationContext();
        file = new DurableFile(new File(context.getNoBackupFilesDir(), "private-state-v1"), files);
        epoch=generation;
    }
    private SecretKey key(String alias,boolean initialize) throws Exception {
        KeyStore s = KeyStore.getInstance("AndroidKeyStore"); s.load(null);
        if (!s.containsAlias(alias)) {
            if (!initialize) throw new IOException("Saved encryption key is unavailable");
            KeyGenerator g = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore");
            g.init(new KeyGenParameterSpec.Builder(alias, KeyProperties.PURPOSE_ENCRYPT | KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM).setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE).build());
            g.generateKey();
        }
        return (SecretKey)s.getKey(alias, null);
    }
    // Only explicit reset creates a new named key. Its authenticated header keeps
    // archived ciphertext associated with the old key; no old alias is deleted.
    private static final class Layout {
        final String alias; final byte[] header;
        Layout(String alias,byte[] header){this.alias=alias;this.header=header;}
    }
    private static Layout layout(byte[] bytes)throws IOException {
        if(bytes.length<28||bytes.length>MAX_STATE_BYTES)throw new IOException("Invalid private state");
        if(!Arrays.equals(MAGIC,Arrays.copyOf(bytes,MAGIC.length)))return new Layout(ALIAS,new byte[0]);
        int length=bytes[MAGIC.length]&255,offset=MAGIC.length+1;
        if(length<1||length>128||bytes.length<offset+length+28)throw new IOException("Invalid key header");
        String alias=new String(bytes,offset,length,StandardCharsets.US_ASCII);
        if(!alias.matches("mdd-agent-v2-config-[0-9a-f-]{36}"))throw new IOException("Unsupported key identity");
        return new Layout(alias,Arrays.copyOf(bytes,offset+length));
    }
    private JSONObject decrypt(byte[] bytes) throws Exception {
        Layout layout=layout(bytes);int offset=layout.header.length;
        Cipher c = Cipher.getInstance("AES/GCM/NoPadding");
        c.init(Cipher.DECRYPT_MODE, key(layout.alias,false), new GCMParameterSpec(128, bytes, offset, 12));
        if(offset>0)c.updateAAD(layout.header);
        return new JSONObject(new String(c.doFinal(bytes, offset+12, bytes.length-offset-12), StandardCharsets.UTF_8));
    }
    private JSONObject envelope(byte[] bytes)throws Exception{
        JSONObject value=decrypt(bytes);
        if(value.getInt("schema")!=1||value.optString("commit_id").isEmpty())throw new IOException("Unsupported saved state version");
        value.getJSONObject("data");return value;
    }
    private File legacy(){return new File(context.getApplicationInfo().dataDir,"shared_prefs/private_config.xml");}
    private JSONObject read() throws Exception {
        if (file.exists()) {
            file.validateOwner();
            return envelope(file.read()).getJSONObject("data");
        }
        if (file.hasRecoveryMaterial()) throw new IOException("Interrupted or missing private state write");
        File legacy=legacy(),backup=new File(legacy+".bak");
        if (files.exists(legacy) || files.exists(backup)) {
            File source=files.exists(backup)?backup:legacy;
            if (source.length() > MAX_STATE_BYTES) throw new IOException("Legacy settings too large");
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
        // A reset key surviving missing files also means this is not a new install.
        java.util.Enumeration<String> names=s.aliases();
        while(names.hasMoreElements())if(names.nextElement().startsWith(ALIAS+"-"))throw new IOException("Reset state missing; initialization refused");
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
            if(epoch!=generation)throw new IllegalStateException("Private-state owner was replaced");
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
        boolean existing=file.exists();
        String alias=existing?layout(file.read()).alias:ALIAS;
        boolean initialize=!existing&&!file.hasRecoveryMaterial()&&!files.exists(legacy())&&!files.exists(new File(legacy()+".bak"));
        write(value,alias,initialize);
    }
    private void write(JSONObject value,String alias,boolean initialize) throws Exception {
        byte[] header=new byte[0];
        if(!alias.equals(ALIAS)){
            byte[] name=alias.getBytes(StandardCharsets.US_ASCII);header=new byte[MAGIC.length+1+name.length];
            System.arraycopy(MAGIC,0,header,0,MAGIC.length);header[MAGIC.length]=(byte)name.length;System.arraycopy(name,0,header,MAGIC.length+1,name.length);
        }
        byte[] plain=Json.obj("schema",1,"commit_id",Json.id(),"data",value).toString().getBytes(StandardCharsets.UTF_8);
        if(plain.length>MAX_STATE_BYTES-28-header.length)throw new IOException("Private state capacity reached; existing data preserved");
        Cipher c=Cipher.getInstance("AES/GCM/NoPadding");c.init(Cipher.ENCRYPT_MODE,key(alias,initialize));if(header.length>0)c.updateAAD(header);
        byte[] encrypted=c.doFinal(plain),iv=c.getIV(),bytes=new byte[header.length+iv.length+encrypted.length];
        System.arraycopy(header,0,bytes,0,header.length);System.arraycopy(iv,0,bytes,header.length,iv.length);System.arraycopy(encrypted,0,bytes,header.length+iv.length,encrypted.length);
        try{file.write(bytes);envelope(file.read());}
        catch(Exception e){writeFault=true;throw e;}
    }
    JSONObject retry() {
        synchronized (LOCK) {
            if(epoch!=generation)throw new IllegalStateException("Private-state owner was replaced");
            try {
                if(file.exists()){
                    envelope(file.read()); // authenticate first, never recreate a key
                    file.repairMissingOwner();
                }else if(files.exists(file.staged())){
                    byte[] staged=file.stagedBytes();envelope(staged);file.publish(staged);
                }
                JSONObject current=read();files.syncDirectory(file.base.getParentFile());writeFault=false;return current;
            }catch(Exception e){throw new IllegalStateException("Saved settings unavailable; data preserved",e);}
        }
    }
    private static void requireNoUnresolved(JSONObject current)throws Exception{
        if(current.has("pending_call"))throw new IllegalStateException("Resolve previous call before reset");
        if(current.optJSONObject("last_sms")!=null)throw new IllegalStateException("Legacy message uncertainty must be retained");
        org.json.JSONArray messages=current.optJSONArray("message_operations");
        if(messages!=null)for(int i=0;i<messages.length();i++)if(!MessageJournal.resolved(messages.getJSONObject(i)))throw new IllegalStateException("Unresolved messages must be retained");
    }
    void clear() {
        update(current -> {
            requireNoUnresolved(current);
            java.util.ArrayList<String> keys=new java.util.ArrayList<>();current.keys().forEachRemaining(keys::add);
            for(String key:keys)current.remove(key);
        });
    }
    /** Called only after two explicit UI confirmations. No remote action is performed. */
    JSONObject resetAfterConsent(){
        synchronized(LOCK){
            if(epoch!=generation)throw new IllegalStateException("Private-state owner was replaced");
            try{
                JSONObject readable=null;try{readable=file.exists()?envelope(file.read()).getJSONObject("data"):read();}catch(Exception unreadable){/* raw materials are archived below */}
                if(readable!=null)requireNoUnresolved(readable);
                // A complete interrupted candidate is recovery material, not permission
                // to discard an operation. Never replace it merely because the base
                // is absent/corrupt or the ownership marker is incomplete.
                JSONObject candidate=null;
                if(readable==null&&files.exists(file.staged()))try{candidate=envelope(file.stagedBytes()).getJSONObject("data");}catch(Exception unreadable){/* preserve raw bytes below; no inferred outcome */}
                if(candidate!=null)requireNoUnresolved(candidate);
                archiveRecoveryMaterial();
                // The original files and keys have been preserved. A fresh, independently
                // named key also works when the original key is missing or invalidated.
                String alias=ALIAS+"-"+Json.id();
                files.writeSynced(file.owned(),new byte[]{1});files.syncDirectory(file.base.getParentFile());
                write(new JSONObject(),alias,true);
                epoch=++generation;writeFault=false;
                return read();
            }catch(Exception failure){throw new IllegalStateException("Reset incomplete; encrypted recovery materials retained",failure);}
        }
    }
    private void archiveRecoveryMaterial()throws Exception{
        File root=new File(context.getNoBackupFilesDir(),"private-state-recovery");
        if(!files.exists(root)){if(!root.mkdir())throw new IOException("Cannot create private recovery archive");files.syncDirectory(root.getParentFile());}
        String[] previous=root.list();if(previous==null||previous.length>=8)throw new IOException("Recovery archive capacity reached; preserve existing archives");
        File archive=new File(root,Json.id());if(!archive.mkdir())throw new IOException("Cannot create recovery archive");files.syncDirectory(root);
        File[] inputs={file.base,file.staged(),file.owned(),new File(file.base+".bak"),legacy(),new File(legacy()+".bak")};
        JSONObject manifest=Json.obj("schema",1,"keys_preserved",true,"files",new JSONObject());
        JSONObject hashes=manifest.getJSONObject("files");
        for(int i=0;i<inputs.length;i++)if(files.exists(inputs[i])){
            byte[] original=files.read(inputs[i]);String name=i+"-"+inputs[i].getName();File saved=new File(archive,name);
            files.writeSynced(saved,original);
            if(!Arrays.equals(original,files.read(saved)))throw new IOException("Recovery archive verification failed");
            hashes.put(name,Json.sha(original));
        }
        files.writeSynced(new File(archive,"manifest.json"),manifest.toString().getBytes(StandardCharsets.UTF_8));
        files.syncDirectory(archive);files.syncDirectory(root);
    }
    JSONObject signOut(){return update(current->{
        for(String key:new String[]{"token","csrf","agent_token","agent_id"})current.remove(key);
        current.put("available",false).put("share",false);
    });}
}
