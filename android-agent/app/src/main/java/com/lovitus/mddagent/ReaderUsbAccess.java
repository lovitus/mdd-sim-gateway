package com.lovitus.mddagent;

import android.app.PendingIntent;
import android.content.Context;
import android.content.Intent;
import android.hardware.usb.UsbDevice;
import android.hardware.usb.UsbManager;
import android.net.Uri;
import android.os.Build;
import android.os.SystemClock;
import java.util.*;
import java.util.function.LongSupplier;

/** Main-thread owner of USB permission requests, not card I/O or sharing intent. */
final class ReaderUsbAccess {
    interface Platform {
        Map<String,Boolean> attached();
        void request(String attachment, PendingIntent result);
    }

    private final Platform platform;
    private final Context context;
    private final LongSupplier clock;
    private final Set<String> attempted = new HashSet<>();
    private String pendingAttachment = "", pendingNonce = "";
    private PendingIntent pendingIntent;
    private long requestedAt;
    private boolean waiting;
    private boolean failed;

    ReaderUsbAccess(Context context, Platform platform, LongSupplier clock) {
        this.context=context;this.platform=platform;this.clock=clock;
    }

    void reconcile(boolean allowed, String explicitAttachment) {
        Map<String,Boolean> current;
        try { current = platform.attached(); }
        catch (RuntimeException unavailable) { failed=true;android.util.Log.w("MDDUSB","USB permission inventory failed",unavailable);return; }
        failed=false;
        attempted.retainAll(current.keySet());
        waiting = current.containsValue(false);
        if (!pendingNonce.isEmpty() && (!current.containsKey(pendingAttachment)
                || Boolean.TRUE.equals(current.get(pendingAttachment)))) cancel();
        if (!allowed) return;
        if (explicitAttachment!=null && pendingAttachment.equals(explicitAttachment) && expired()) cancel();
        if (!pendingNonce.isEmpty()) return;
        for (Map.Entry<String,Boolean> entry : current.entrySet()) {
            String key = entry.getKey();
            if (entry.getValue() || (explicitAttachment == null ? attempted.contains(key) : !key.equals(explicitAttachment))) continue;
            attempted.add(key);
            pendingAttachment = key;
            pendingNonce = UUID.randomUUID().toString();
            requestedAt=clock.getAsLong();
            Intent intent=new Intent(context.getPackageName()+".USB_PERMISSION").setPackage(context.getPackageName())
                .setData(Uri.parse("mdd-usb-permission:"+pendingNonce));
            int flags=PendingIntent.FLAG_CANCEL_CURRENT|(Build.VERSION.SDK_INT>=31?PendingIntent.FLAG_MUTABLE:0);
            try {
                pendingIntent=PendingIntent.getBroadcast(context,0,intent,flags);
                platform.request(key,pendingIntent);
            } catch (RuntimeException failure) {
                failed=true;android.util.Log.w("MDDUSB","USB permission request failed",failure);cancel();
            }
            return;
        }
    }

    boolean result(Intent result) {
        Uri data=result.getData();
        if (data==null || !"mdd-usb-permission".equals(data.getScheme()) || pendingNonce.isEmpty()
                || !pendingNonce.equals(data.getSchemeSpecificPart())) return false;
        String attachment=pendingAttachment;
        // The callback is a wakeup, never authority to access a card. Re-read the OS.
        cancel();
        try { return Boolean.TRUE.equals(platform.attached().get(attachment)); }
        catch (RuntimeException unavailable) { failed=true;android.util.Log.w("MDDUSB","USB permission readback failed",unavailable);return false; }
    }

    void detached(String attachment) {
        attempted.remove(attachment);
        if (pendingAttachment.equals(attachment)) cancel();
    }

    void cancel() {
        if(pendingIntent!=null)pendingIntent.cancel();pendingIntent=null;
        pendingAttachment = pendingNonce = "";
        // Keep refusals for attachments still present, including across Pause/resume.
    }

    boolean waiting() { return waiting; }
    private boolean expired(){return clock.getAsLong()-requestedAt>=120000;}
    boolean canRequest(String attachment){return pendingNonce.isEmpty() || pendingAttachment.equals(attachment)&&expired();}
    int status(String attachment){
        if(pendingAttachment.equals(attachment)&&!expired())return R.string.reader_usb_permission_requesting;
        if(attempted.contains(attachment))return R.string.reader_usb_permission_retry;
        return failed?R.string.reader_usb_permission_failed:R.string.reader_usb_wait_permission;
    }

    static String key(UsbDevice device) {
        return device.getDeviceName()+":"+device.getDeviceId()+":"+device.getVendorId()+":"+device.getProductId();
    }

    static ReaderUsbAccess android(Context context) {
        UsbManager usb = context.getSystemService(UsbManager.class);
        return new ReaderUsbAccess(context,new Platform() {
            private Map<String,UsbDevice> devices() {
                Map<String,UsbDevice> found = new TreeMap<>();
                for (UsbDevice device : usb.getDeviceList().values()) {
                    for (int i=0; i<device.getInterfaceCount(); i++) {
                        if (device.getInterface(i).getInterfaceClass()==11) {
                            found.put(key(device), device);
                            break;
                        }
                    }
                }
                return found;
            }
            public Map<String,Boolean> attached() {
                Map<String,Boolean> facts = new LinkedHashMap<>();
                for (Map.Entry<String,UsbDevice> entry : devices().entrySet())
                    facts.put(entry.getKey(), usb.hasPermission(entry.getValue()));
                return facts;
            }
            public void request(String attachment, PendingIntent result) {
                UsbDevice device = devices().get(attachment);
                if (device==null || usb.hasPermission(device)) return;
                usb.requestPermission(device,result);
            }
        },SystemClock::elapsedRealtime);
    }
}
