package com.lovitus.mddagent;

import java.io.IOException;
import java.security.cert.CertificateException;
import java.util.Collections;
import java.util.IdentityHashMap;
import java.util.Set;
import java.util.concurrent.CompletionException;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.TimeoutException;
import javax.net.ssl.SSLPeerUnverifiedException;

/** Read/reconnect classification only. Never enables retries for paid submissions. */
final class TransportFailures {
    static boolean identityFailure(Throwable error) {
        Set<Throwable> seen = Collections.newSetFromMap(new IdentityHashMap<>());
        for (Throwable cause = error; cause != null && seen.add(cause); cause = cause.getCause()) {
            if (cause instanceof CertificateException || cause instanceof SSLPeerUnverifiedException) return true;
        }
        return false;
    }
    static boolean terminalRead(Throwable error) {
        if (identityFailure(error)) return true;
        Set<Throwable> seen = Collections.newSetFromMap(new IdentityHashMap<>());
        while ((error instanceof ExecutionException || error instanceof CompletionException)
                && error.getCause() != null && seen.add(error)) error = error.getCause();
        if (error instanceof GatewayApi.Failure) {
            int status = ((GatewayApi.Failure) error).status;
            return status < 500 && status != 408 && status != 429;
        }
        // Storage/schema errors stay stopped. SSL I/O without rejected identity
        // is ordinary transport loss, just as on the observer/reader sockets.
        return !(error instanceof IOException || error instanceof TimeoutException);
    }
}
