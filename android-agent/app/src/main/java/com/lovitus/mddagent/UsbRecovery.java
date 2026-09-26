package com.lovitus.mddagent;

import android.hardware.usb.*;
import android.system.OsConstants;
import java.io.IOException;

/** One recovery plus a bounded slot-handshake followup; sustained health rearms it. */
final class UsbRecovery {
    private int failures;
    private int attempts;
    private long retryAt;
    private long healthySince = -1;
    int resetResult;
    String resetStage = "";

    boolean failed(boolean writeFailure, long now) {
        healthySince = -1;
        failures = writeFailure ? Math.min(2, failures + 1) : 0;
        if (failures < 2 || attempts >= 2) return false;
        if (attempts == 1 && (!retryPending() || now < retryAt)) return false;
        attempts++;
        retryAt = now + 30000;
        return true;
    }

    boolean retryPending() {
        return attempts == 1 && resetResult != 0 && "slot_status".equals(resetStage);
    }

    void healthy(long now) {
        failures = 0;
        if (healthySince < 0) healthySince = now;
        if (now - healthySince >= 60000) {
            attempts = 0;
            retryAt = 0;
            resetResult = 0;
            resetStage = "";
        }
    }

    static final class Failure extends IOException {
        final int code;
        final String stage;
        Failure(String stage, int code) {
            super("USB recovery failed at " + stage + " (" + code + ")");
            this.stage = stage;
            this.code = code;
        }
        static Failure at(String stage, Throwable cause) {
            if (cause instanceof Failure) return (Failure) cause;
            int code = cause instanceof SecurityException ? -OsConstants.EACCES
                    : cause instanceof LinkageError ? -OsConstants.ENOSYS : -OsConstants.EIO;
            Failure failure = new Failure(stage, code);
            failure.initCause(cause);
            return failure;
        }
    }

    static UsbCard reset(UsbManager manager, UsbDevice device) throws Exception {
        try {
            if (!manager.hasPermission(device)) throw new Failure("permission", -OsConstants.EACCES);
            // A device reset must never affect another interface of a composite device.
            if (device.getConfigurationCount() != 1) throw new Failure("device_shape", -OsConstants.ENOTSUP);
            UsbConfiguration config = device.getConfiguration(0);
            if (config.getInterfaceCount() != 1) throw new Failure("device_shape", -OsConstants.ENOTSUP);
            UsbInterface intf = config.getInterface(0);
            if (intf.getInterfaceClass() != 11) throw new Failure("device_shape", -OsConstants.ENOTSUP);
            return new UsbCard(manager, device, true);
        } catch (SecurityException denied) {
            throw Failure.at("permission", denied);
        } catch (LinkageError unavailable) {
            throw Failure.at("native_library", unavailable);
        }
    }
}
