package com.lovitus.mddagent;

import org.junit.Test;
import java.util.List;
import static org.junit.Assert.assertEquals;

public class AgentActivityLogTest {
    @Test public void keepsOnlyNewestEightAllowlistedEventsInReverseChronologicalOrder() {
        AgentActivityLog log = new AgentActivityLog();
        for (int event = 1; event <= 10; event++) log.add(event);

        List<AgentActivityLog.Entry> entries = log.recent();
        assertEquals(AgentActivityLog.LIMIT, entries.size());
        assertEquals(10, entries.get(0).message);
        assertEquals(3, entries.get(entries.size() - 1).message);
        assertEquals(10, log.revision());
    }
}
