package com.lovitus.mddagent;

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;

/** Small in-memory journal of allowlisted state transitions; never stores payloads. */
final class AgentActivityLog {
    static final int LIMIT = 8;

    static final class Entry {
        final int message;
        final long timestamp;

        Entry(int message, long timestamp) {
            this.message = message;
            this.timestamp = timestamp;
        }
    }

    private final ArrayDeque<Entry> entries = new ArrayDeque<>();
    private long revision;

    synchronized void add(int message) {
        if (message == 0) return;
        entries.addFirst(new Entry(message, System.currentTimeMillis()));
        while (entries.size() > LIMIT) entries.removeLast();
        revision++;
    }

    synchronized List<Entry> recent() {
        return Collections.unmodifiableList(new ArrayList<>(entries));
    }

    synchronized long revision() {
        return revision;
    }
}
