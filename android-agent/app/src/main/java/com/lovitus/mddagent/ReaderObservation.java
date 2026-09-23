package com.lovitus.mddagent;

import org.json.JSONObject;

/** Immutable facts from one discovery owner, not an alias to a mutable JSON tree. */
final class ReaderObservation {
    final Object owner;
    final long epoch, observedAt;
    private final String encoded;
    ReaderObservation(Object owner, long epoch, long observedAt, JSONObject topology) {
        this.owner = owner; this.epoch = epoch; this.observedAt = observedAt;
        encoded = topology.toString();
    }
    boolean current(Object expected, long expectedEpoch, long now) {
        return owner == expected && epoch == expectedEpoch && now >= observedAt && now - observedAt <= 20000;
    }
    JSONObject topology() {
        try { return new JSONObject(encoded); }
        catch (org.json.JSONException impossible) { throw new IllegalStateException("Invalid reader observation", impossible); }
    }
}
