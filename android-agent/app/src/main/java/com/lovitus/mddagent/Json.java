package com.lovitus.mddagent;
import org.json.*;
import java.security.MessageDigest;
import java.util.*;
final class Json {
    private Json() {}
    static JSONObject obj(Object... pairs) { try { JSONObject o=new JSONObject(); for(int i=0;i<pairs.length;i+=2) o.put((String)pairs[i],pairs[i+1]); return o; } catch(JSONException e) { throw new IllegalArgumentException(e); } }
    static JSONObject object(JSONObject o,String k) { JSONObject v=o==null?null:o.optJSONObject(k); return v==null?new JSONObject():v; }
    static JSONArray array(JSONObject o,String k) { JSONArray v=o==null?null:o.optJSONArray(k); return v==null?new JSONArray():v; }
    static String id() { return UUID.randomUUID().toString(); }
    static byte[] unhex(String h) { if(h.length()%2!=0)throw new IllegalArgumentException("Invalid hex"); byte[] b=new byte[h.length()/2]; for(int i=0;i<b.length;i++){int a=Character.digit(h.charAt(i*2),16),c=Character.digit(h.charAt(i*2+1),16);if(a<0||c<0)throw new IllegalArgumentException("Invalid hex");b[i]=(byte)(a*16+c);}return b; }
    static String hex(byte[] b) { StringBuilder s=new StringBuilder(); for(byte v:b)s.append(String.format(Locale.ROOT,"%02x",v&255)); return s.toString(); }
    static String sha(byte[] b) { try{return hex(MessageDigest.getInstance("SHA-256").digest(b));}catch(Exception e){throw new IllegalStateException(e);} }
}
