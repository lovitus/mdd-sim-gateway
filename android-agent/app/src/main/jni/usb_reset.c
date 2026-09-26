#include <errno.h>
#include <jni.h>
#include <linux/usbdevice_fs.h>
#include <sys/ioctl.h>

/* Same owned-descriptor USBFS operation as AOSP usb_device_reset/libusb.
 * No device enumeration, opening paths, root, hidden Java API or retry. */
JNIEXPORT jint JNICALL
Java_com_lovitus_mddagent_UsbPortReset_reset(JNIEnv *env, jclass type, jint fd) {
    (void)env;
    (void)type;
    if (fd < 0) return -EBADF;
    if (ioctl(fd, USBDEVFS_RESET, 0) == 0) return 0;
    return -errno;
}
