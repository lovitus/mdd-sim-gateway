package com.lovitus.mddagent;
import java.net.URI;
import java.util.Locale;
public final class Endpoint {
    public final String origin, host, fingerprint;
    public Endpoint(String input,String pin) {
        try {
            URI u=new URI(input.trim());
            if(!"https".equalsIgnoreCase(u.getScheme())||u.getHost()==null||u.getRawUserInfo()!=null||u.getRawQuery()!=null||u.getRawFragment()!=null||u.getPort()==0||u.getPort()>65535||!(u.getRawPath().isEmpty()||u.getRawPath().equals("/")))throw new IllegalArgumentException();
            host=u.getHost().toLowerCase(Locale.ROOT).replace("[", "").replace("]", "");
            int port=u.getPort()==443?-1:u.getPort();origin=new URI("https",null,host,port,null,null,null).toASCIIString();
            fingerprint=pin.trim().replace(":","").toLowerCase(Locale.ROOT);
            if(!fingerprint.isEmpty()&&!fingerprint.matches("[a-f0-9]{64}"))throw new IllegalArgumentException();
        } catch(Exception e){throw new IllegalArgumentException("Use an HTTPS origin without path, credentials or query; fingerprint must be 64 hex digits.");}
    }
    public String path(String path) {
        if(!path.startsWith("/")||path.startsWith("//")||path.contains("\\")||path.contains("#")||path.contains("\r")||path.contains("\n"))throw new IllegalArgumentException("Invalid same-origin path");
        URI u=URI.create(origin+path); if(!origin.equals(u.getScheme()+"://"+u.getRawAuthority()))throw new IllegalArgumentException("Origin changed");return u.toString();
    }
}
