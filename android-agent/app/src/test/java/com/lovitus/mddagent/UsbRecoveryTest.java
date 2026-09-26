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

    @Test public void followupDoesNotRetryOtherStagesOrACompletedReset() {
        for (String stage : new String[]{"permission", "device_shape", "claim", "reset",
                "reclaim", "power_on", "identity", ""}) {
            UsbRecovery recovery = new UsbRecovery();
            assertFalse(recovery.failed(true, 0));
            assertTrue(recovery.failed(true, 1));
            recovery.resetStage = stage;
            recovery.resetResult = stage.isEmpty() ? 0 : -5;
            assertFalse(stage, recovery.failed(true, 30001));
            assertFalse(stage, recovery.failed(true, 600001));
        }
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
