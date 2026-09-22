package com.lovitus.mddagent;

import java.io.IOException;
import java.net.InetSocketAddress;
import java.security.cert.CertificateException;
import java.security.cert.X509Certificate;
import javax.net.ssl.*;

/** Adapts the legacy VpcdClient leaf-fingerprint flow without sending application data. */
final class CertificateProbe {
    static final class Presented {
        final String origin,fingerprint,subject,issuer;
        final long notBefore,notAfter;
        Presented(Endpoint endpoint,X509Certificate leaf)throws Exception{
            leaf.checkValidity();origin=endpoint.origin;fingerprint=Json.sha(leaf.getEncoded());
            subject=leaf.getSubjectX500Principal().getName();issuer=leaf.getIssuerX500Principal().getName();
            notBefore=leaf.getNotBefore().getTime();notAfter=leaf.getNotAfter().getTime();
        }
    }
    static Presented inspect(Endpoint endpoint)throws Exception{
        final X509Certificate[] observed=new X509Certificate[1];
        X509TrustManager inspection=new X509TrustManager(){
            public X509Certificate[] getAcceptedIssuers(){return new X509Certificate[0];}
            public void checkClientTrusted(X509Certificate[] chain,String type)throws CertificateException{throw new CertificateException("Inspection only");}
            public void checkServerTrusted(X509Certificate[] chain,String type)throws CertificateException{
                if(chain!=null&&chain.length>0)observed[0]=chain[0];
                // Always abort before TLS finishes. This context cannot carry a login or HTTP request.
                throw new CertificateException("Certificate inspection complete");
            }
        };
        SSLContext context=SSLContext.getInstance("TLS");context.init(new KeyManager[0],new TrustManager[]{inspection},null);
        int port=java.net.URI.create(endpoint.origin).getPort();if(port<0)port=443;
        try(SSLSocket socket=(SSLSocket)context.getSocketFactory().createSocket()){
            if(!endpoint.host.contains(":")&&!endpoint.host.matches("[0-9.]+")){
                SSLParameters parameters=socket.getSSLParameters();parameters.setServerNames(java.util.Collections.singletonList(new SNIHostName(endpoint.host)));socket.setSSLParameters(parameters);
            }
            socket.connect(new InetSocketAddress(endpoint.host,port),10000);socket.setSoTimeout(10000);
            try{socket.startHandshake();}catch(SSLException failure){if(observed[0]==null)throw failure;}
        }
        if(observed[0]==null)throw new IOException("No server certificate received");
        return new Presented(endpoint,observed[0]);
    }
}
