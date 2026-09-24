package com.lovitus.mddagent;

import java.util.Collection;
import java.util.HashSet;

final class ReaderAttention {
    private ReaderAttention() {}

    static int pending(Collection<String> attached, Collection<String> permitted,
            Collection<String> scanned, boolean sharing, boolean linkOnline) {
        if (attached == null || attached.isEmpty()) return 0;
        HashSet<String> granted = new HashSet<>(permitted == null ? java.util.Collections.emptySet() : permitted);
        HashSet<String> observed = new HashSet<>(scanned == null ? java.util.Collections.emptySet() : scanned);
        int count = 0;
        for (String device : attached) {
            if (!sharing || !linkOnline || !granted.contains(device) || !observed.contains(device)) count++;
        }
        return count;
    }

}
