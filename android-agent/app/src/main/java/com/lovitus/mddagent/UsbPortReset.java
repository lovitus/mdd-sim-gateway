package com.lovitus.mddagent;

/** Native USBFS access only through an already permission-checked Android handle. */
final class UsbPortReset {
    static { System.loadLibrary("mdd_usb"); }
    private UsbPortReset() {}
    static native int reset(int fileDescriptor);
    // Same USBFS bulk request as Android, but retain -errno instead of collapsing it to -1.
    static native int transfer(int fileDescriptor, int endpoint, byte[] buffer,
                               int offset, int length, int timeoutMillis);
}
