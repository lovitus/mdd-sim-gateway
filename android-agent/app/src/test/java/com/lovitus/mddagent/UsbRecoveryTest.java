package com.lovitus.mddagent;

import org.junit.Test;
import static org.junit.Assert.*;

public class UsbRecoveryTest {
    @Test public void failedSlotHandshakeGetsOneDelayedFollowupNotAnEndlessResetLoop() {
        UsbRecovery recovery = new UsbRecovery();
        assertFalse(recovery.failed(true, 0));
        assertTrue(recovery.failed(true, 10));
        recovery.resetResult = -5;
        recovery.resetStage = "slot_status";

        assertFalse(recovery.failed(true, 30009));
        assertTrue("A failed slot handshake must not permanently latch recovery off",
                recovery.failed(true, 30010));
        for (long now : new long[]{30011, 60010, 600000}) {
            assertFalse("No third reset in the same failure episode", recovery.failed(true, now));
        }
    }

    @Test public void followupDoesNotRetryUnsafeFailureStages() {
        for (String stage : new String[]{"permission", "device_shape", "claim", "reset",
                "reclaim", "power_on", "identity"}) {
            UsbRecovery recovery = new UsbRecovery();
            assertFalse(recovery.failed(true, 0));
            assertTrue(recovery.failed(true, 1));
            recovery.resetStage = stage;
            recovery.resetResult = -5;
            assertFalse(stage, recovery.failed(true, 30001));
            assertFalse(stage, recovery.failed(true, 600001));
        }
    }

    @Test public void renewedWriteFailuresAfterSuccessfulResetGetOnlyOneDelayedFollowup() {
        UsbRecovery recovery = new UsbRecovery();
        assertFalse(recovery.failed(true, 0));
        assertTrue(recovery.failed(true, 10));
        recovery.resetResult = 0;
        recovery.resetStage = "";
        recovery.healthy(1000);
        recovery.healthy(10000);

        assertFalse("One new write failure does not reset the reader", recovery.failed(true, 20000));
        assertFalse("Keep the original cooldown after a short recovery", recovery.failed(true, 30009));
        assertTrue("A short-lived successful reset must not permanently disable recovery",
                recovery.failed(true, 30010));
        recovery.healthy(31000);
        assertFalse(recovery.failed(true, 40000));
        assertFalse("No third reset when the second recovery is also unstable", recovery.failed(true, 60010));
        assertFalse(recovery.failed(true, 600000));

        recovery.healthy(700000);
        recovery.healthy(760000);
        assertFalse(recovery.failed(true, 800000));
        assertTrue("Sustained health starts a new bounded recovery episode", recovery.failed(true, 800010));
    }

    @Test public void onlySustainedHealthRearmsTheBudget() {
        for (long healthyDuration : new long[]{59999, 60000}) {
            UsbRecovery recovery = new UsbRecovery();
            recovery.failed(true, 0);
            recovery.failed(true, 1);
            recovery.resetStage = "slot_status";
            recovery.resetResult = -5;
            recovery.failed(true, 30001);
            recovery.healthy(100000);
            recovery.healthy(100000 + healthyDuration);
            assertFalse(recovery.failed(true, 200000));
            assertEquals(healthyDuration >= 60000, recovery.failed(true, 200001));
        }
    }
}
