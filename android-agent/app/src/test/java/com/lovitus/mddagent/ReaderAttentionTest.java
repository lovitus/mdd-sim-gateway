package com.lovitus.mddagent;

import org.junit.Test;
import java.util.Arrays;
import java.util.Collections;
import static org.junit.Assert.assertEquals;

public class ReaderAttentionTest {
    @Test public void attachedDevicesRemainPendingUntilPermissionSharingAndScan(){
        assertEquals(2,ReaderAttention.pending(Arrays.asList("usb-a","usb-b"),Collections.emptyList(),Collections.emptyList(),false,false));
        assertEquals(2,ReaderAttention.pending(Arrays.asList("usb-a","usb-b"),Arrays.asList("usb-a","usb-b"),Collections.emptyList(),true,false));
        assertEquals(1,ReaderAttention.pending(Arrays.asList("usb-a","usb-b"),Arrays.asList("usb-a","usb-b"),Collections.singletonList("usb-a"),true,true));
        assertEquals(0,ReaderAttention.pending(Arrays.asList("usb-a","usb-b"),Arrays.asList("usb-a","usb-b"),Arrays.asList("usb-a","usb-b"),true,true));
    }

    @Test public void unrelatedReaderScansDoNotClearTheInsertedDevicesBadge(){
        assertEquals(1,ReaderAttention.pending(Collections.singletonList("usb-current"),Collections.singletonList("usb-current"),Collections.singletonList("usb-old"),true,true));
    }

    @Test public void previouslyScannedReaderNeedsAttentionWhenAgentLinkDrops(){
        assertEquals(1,ReaderAttention.pending(Collections.singletonList("usb-a"),Collections.singletonList("usb-a"),
                Collections.singletonList("usb-a"),true,false));
    }
}
