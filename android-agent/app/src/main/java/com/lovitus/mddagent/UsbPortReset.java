package com.lovitus.mddagent;

/** Native USBFS access only through an already permission-checked Android handle. */
final class UsbPortReset {
    static { System.loadLibrary("mdd_usb"); }
    private UsbPortReset() {}
    static native int reset(int fileDescriptor);
}
