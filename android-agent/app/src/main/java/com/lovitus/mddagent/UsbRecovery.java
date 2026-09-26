package com.lovitus.mddagent;

import android.hardware.usb.*;
import android.system.OsConstants;

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

    static int reset(UsbManager manager, UsbDevice device) {
        UsbDeviceConnection connection = null;
        try {
            if (!manager.hasPermission(device)) return -OsConstants.EACCES;
            // A device reset must never affect another interface of a composite device.
            if (device.getConfigurationCount() != 1) return -OsConstants.ENOTSUP;
            UsbConfiguration config = device.getConfiguration(0);
            if (config.getInterfaceCount() != 1) return -OsConstants.ENOTSUP;
            UsbInterface intf = config.getInterface(0);
            if (intf.getInterfaceClass() != 11) return -OsConstants.ENOTSUP;
            connection = manager.openDevice(device);
            if (connection == null) return -OsConstants.EACCES;
            if (!Ccid.apduLevel(connection.getRawDescriptors(), intf.getId())) return -OsConstants.ENOTSUP;
            if (!connection.claimInterface(intf, false)) return -OsConstants.EBUSY;
            if (!connection.releaseInterface(intf)) return -OsConstants.EIO;
            return UsbPortReset.reset(connection.getFileDescriptor());
        } catch (SecurityException denied) {
            return -OsConstants.EACCES;
        } catch (LinkageError unavailable) {
            return -OsConstants.ENOSYS;
        } finally {
            if (connection != null) connection.close();
        }
    }
}
