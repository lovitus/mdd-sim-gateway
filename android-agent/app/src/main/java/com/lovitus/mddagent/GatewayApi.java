package com.lovitus.mddagent;
import okhttp3.*;
import org.json.*;
import java.io.IOException;
import java.security.*;
import java.security.cert.*;
import javax.net.ssl.*;
import java.util.concurrent.TimeUnit;
final class GatewayApi {
    private final java.util.Map<Object,Boolean> cancelled=java.util.Collections.synchronizedMap(new java.util.WeakHashMap<>());
    private final java.util.Map<Object,java.util.Set<Call>> active=new java.util.HashMap<>();
    private final java.util.Set<Call> allRequests=java.util.Collections.newSetFromMap(new java.util.IdentityHashMap<>());
    private volatile boolean closed;
    final Endpoint endpoint; final OkHttpClient http; volatile String token, csrf;
    private volatile String accountScope="";
    private volatile String accountName="";
    static final MediaType JSON=MediaType.get("application/json; charset=utf-8");
    GatewayApi(Endpoint e,String token,String csrf) throws Exception {
        this.endpoint=e;this.token=token;this.csrf=csrf;
        OkHttpClient.Builder b=new OkHttpClient.Builder().connectTimeout(15,TimeUnit.SECONDS).readTimeout(210,TimeUnit.SECONDS).callTimeout(220,TimeUnit.SECONDS).pingInterval(30,TimeUnit.SECONDS).retryOnConnectionFailure(false).followRedirects(false).followSslRedirects(false);
        if(!e.fingerprint.isEmpty()){
            X509TrustManager trust=new X509TrustManager(){public X509Certificate[] getAcceptedIssuers(){return new X509Certificate[0];} public void checkClientTrusted(X509Certificate[] chain,String type)throws CertificateException{throw new CertificateException("Client TLS not supported");}
                public void checkServerTrusted(X509Certificate[] chain,String type)throws CertificateException{if(chain==null||chain.length==0)throw new CertificateException("Missing server certificate");chain[0].checkValidity();if(!MessageDigest.isEqual(Json.unhex(e.fingerprint),Json.unhex(Json.sha(chain[0].getEncoded()))))throw new CertificateException("Server certificate changed");}};
            SSLContext context=SSLContext.getInstance("TLS");context.init(null,new TrustManager[]{trust},null);b.sslSocketFactory(context.getSocketFactory(),trust);
            // Explicit out-of-band certificate pin is the identity for a self-signed endpoint.
            // It applies only to this configured origin, never redirects/other destinations.
            b.hostnameVerifier((host,session)->{try{return host.equalsIgnoreCase(e.host)&&MessageDigest.isEqual(Json.unhex(e.fingerprint),Json.unhex(Json.sha(session.getPeerCertificates()[0].getEncoded())));}catch(Exception failure){return false;}});
        }
        http=b.build();
    }
    Request.Builder request(String path){if(closed)throw new IllegalStateException("Gateway connection was closed");Request.Builder b=new Request.Builder().url(endpoint.path(path)).header("Origin",endpoint.origin).header("Cache-Control","no-store");if(!token.isEmpty())b.header("X-MDD-Session",token).header("Authorization","Bearer "+token).header("Cookie","mdd_session="+token);return b;}
    JSONObject json(String method,String path,JSONObject input)throws Exception{return json(method,path,input,null);}
    JSONObject json(String method,String path,JSONObject input,Object tag)throws Exception{
        Request.Builder b=request(path).tag(tag);if(!method.equals("GET")){b.header("X-MDD-CSRF-Token",csrf);b.method(method,RequestBody.create(input==null?"{}":input.toString(),JSON));}
        Call request=http.newCall(b.build());request.timeout().timeout(tag==null?30:215,TimeUnit.SECONDS);
        synchronized(active){if(closed||tag!=null&&cancelled.containsKey(tag))throw new IOException("Operation cancelled");allRequests.add(request);if(tag!=null)active.computeIfAbsent(tag,key->new java.util.HashSet<>()).add(request);}
        try(Response r=request.execute()){
            if(r.body()==null)throw new IOException("Empty gateway response");
            okio.BufferedSource source=r.body().source(); if(source.request(2*1024*1024+1L))throw new IOException("Gateway response too large");
            String raw=source.readUtf8();JSONObject v=raw.isEmpty()?new JSONObject():new JSONObject(raw);
            if(!r.isSuccessful())throw new Failure(r.code(),v.optString("code",v.optString("detail","Request rejected")));
            return v;
        }finally{synchronized(active){allRequests.remove(request);if(tag!=null){java.util.Set<Call> owned=active.get(tag);if(owned!=null){owned.remove(request);if(owned.isEmpty())active.remove(tag);}}}}
    }
    void login(String username,String password)throws Exception{JSONObject v=json("POST","/api/auth/login",Json.obj("username",username,"password",password));token=v.getString("token");csrf=v.getString("csrf");}
    String authenticatedScope()throws Exception{
        if(token.isEmpty())throw new IOException("Sign in required");
        if(!accountScope.isEmpty())return accountScope;
        JSONObject status=json("GET","/api/auth/status",null);
        if(!status.optBoolean("authenticated")||!token.equals(status.optString("token"))||status.optString("username").isEmpty())throw new IOException("Authenticated account unavailable");
        // A cache namespace only, never permission to adopt a previous session's call.
        accountName=status.getString("username");accountScope=scope(endpoint.origin,endpoint.fingerprint,accountName);
        return accountScope;
    }
    String authenticatedName()throws Exception{authenticatedScope();return accountName;}
    static String scope(String origin,String pin,String username){return Json.sha((origin+"\n"+pin+"\n"+username).getBytes(java.nio.charset.StandardCharsets.UTF_8));}
    JSONObject exactLine(String id,String expectedCard)throws Exception{
        JSONObject line=json("GET","/v1/mobile/lines/"+CallPlan.encode(id),null);
        if(!id.equals(line.optString("id"))||!expectedCard.equals(line.optString("card_id"))||!line.optBoolean("enabled"))throw new IOException("线路身份已变化或不可用，请重新选择");
        return line;
    }
    static void requireReady(JSONObject line,String operation)throws IOException{
        // Same machine keys as Go internal/operations/catalog.go; never parse IMS display text.
        JSONObject readiness=Json.object(Json.object(line,"operations"),operation);
        if(!readiness.optBoolean("ready"))throw new IOException("所选通道尚未就绪，请检查网关设备状态");
    }
    void cancel(Object tag){synchronized(active){cancelled.put(tag,true);java.util.Set<Call> owned=active.get(tag);if(owned!=null)for(Call c:owned)c.cancel();}}
    void close(){
        final java.util.List<Call> pending;
        synchronized(active){if(closed)return;closed=true;pending=new java.util.ArrayList<>(allRequests);token="";csrf="";accountScope="";accountName="";}
        java.util.concurrent.ExecutorService cleanup=http.dispatcher().executorService();
        cleanup.execute(()->{try{for(Call request:pending)request.cancel();http.dispatcher().cancelAll();http.connectionPool().evictAll();}finally{cleanup.shutdown();}});
    }
    static final class Failure extends IOException {final int status;Failure(int status,String message){super("HTTP "+status+": "+message);this.status=status;}}
}
