package com.lovitus.mddagent;

import android.hardware.usb.*;
import android.system.OsConstants;
import java.io.IOException;

/** One reset per transport failure episode, rearmed only after sustained health. */
final class UsbRecovery {
    private int failures;
    private boolean attempted;
    private long healthySince = -1;
    int resetResult;

    boolean failed(boolean writeFailure) {
        healthySince = -1;
        failures = writeFailure ? Math.min(2, failures + 1) : 0;
        if (failures < 2 || attempted) return false;
        attempted = true;
        return true;
    }

    void healthy(long now) {
        failures = 0;
        if (healthySince < 0) healthySince = now;
        if (now - healthySince >= 60000) {
            attempted = false;
            resetResult = 0;
        }
    }

    static final class Failure extends IOException {
        final int code;
        Failure(int code) { super("USB recovery failed (" + code + ")"); this.code = code; }
    }

    static UsbCard reset(UsbManager manager, UsbDevice device) throws Exception {
        try {
            if (!manager.hasPermission(device)) throw new Failure(-OsConstants.EACCES);
            // A device reset must never affect another interface of a composite device.
            if (device.getConfigurationCount() != 1) throw new Failure(-OsConstants.ENOTSUP);
            UsbConfiguration config = device.getConfiguration(0);
            if (config.getInterfaceCount() != 1) throw new Failure(-OsConstants.ENOTSUP);
            UsbInterface intf = config.getInterface(0);
            if (intf.getInterfaceClass() != 11) throw new Failure(-OsConstants.ENOTSUP);
            return new UsbCard(manager, device, true);
        } catch (SecurityException denied) {
            throw new Failure(-OsConstants.EACCES);
        } catch (LinkageError unavailable) {
            throw new Failure(-OsConstants.ENOSYS);
        }
    }
}
