package com.lovitus.mddagent;
import android.content.Context;
import android.content.SharedPreferences;
import android.security.keystore.KeyGenParameterSpec;
import android.security.keystore.KeyProperties;
import android.util.Base64;
import org.json.JSONObject;
import javax.crypto.*;
import javax.crypto.spec.GCMParameterSpec;
import java.security.KeyStore;
import java.nio.charset.StandardCharsets;
/** No password persistence, backup, exported secrets or plaintext session tokens. */
final class ConfigStore {
    private final SharedPreferences p; private final String alias="mdd-agent-v2-config";
    ConfigStore(Context c){p=c.getSharedPreferences("private_config",Context.MODE_PRIVATE);}
    private javax.crypto.SecretKey key() throws Exception {
        KeyStore s=KeyStore.getInstance("AndroidKeyStore");s.load(null);
        if(!s.containsAlias(alias)){KeyGenerator g=KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES,"AndroidKeyStore");g.init(new KeyGenParameterSpec.Builder(alias,KeyProperties.PURPOSE_ENCRYPT|KeyProperties.PURPOSE_DECRYPT).setBlockModes(KeyProperties.BLOCK_MODE_GCM).setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE).build());g.generateKey();}
        return (javax.crypto.SecretKey)s.getKey(alias,null);
    }
    synchronized JSONObject load(){try{String v=p.getString("sealed","");if(v.isEmpty())return new JSONObject();byte[] all=Base64.decode(v,Base64.NO_WRAP);Cipher c=Cipher.getInstance("AES/GCM/NoPadding");c.init(Cipher.DECRYPT_MODE,key(),new GCMParameterSpec(128,all,0,12));return new JSONObject(new String(c.doFinal(all,12,all.length-12),StandardCharsets.UTF_8));}catch(Exception e){p.edit().clear().commit();return new JSONObject();}}
    synchronized void save(JSONObject value){try{Cipher c=Cipher.getInstance("AES/GCM/NoPadding");c.init(Cipher.ENCRYPT_MODE,key());byte[] b=c.doFinal(value.toString().getBytes(StandardCharsets.UTF_8)),iv=c.getIV(),out=new byte[iv.length+b.length];System.arraycopy(iv,0,out,0,iv.length);System.arraycopy(b,0,out,iv.length,b.length);if(!p.edit().putString("sealed",Base64.encodeToString(out,Base64.NO_WRAP)).commit())throw new IllegalStateException("Credential storage unavailable");}catch(Exception e){throw new IllegalStateException("Credential storage unavailable",e);}}
    synchronized void clear(){p.edit().clear().commit();}
}
