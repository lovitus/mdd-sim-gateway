#include <errno.h>
#include <jni.h>
#include <linux/usbdevice_fs.h>
#include <stdlib.h>
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

/* Follow AOSP libusbhost usb_device_bulk_transfer: one ioctl on the owned fd.
 * Capture errno before cleanup; never retry or log the transferred payload. */
JNIEXPORT jint JNICALL
Java_com_lovitus_mddagent_UsbPortReset_transfer(JNIEnv *env, jclass type, jint fd,
        jint endpoint, jbyteArray buffer, jint offset, jint length, jint timeout) {
    (void)type;
    if (fd < 0) return -EBADF;
    if (!buffer || offset < 0 || length <= 0 || length > 65546 || timeout <= 0 ||
            timeout > 5000 || (endpoint & ~0x8f) || !(endpoint & 0x0f)) return -EINVAL;
    jsize capacity = (*env)->GetArrayLength(env, buffer);
    if (offset > capacity || length > capacity - offset) return -EINVAL;
    jbyte *bytes = malloc((size_t)length);
    if (!bytes) return -ENOMEM;
    int input = endpoint & 0x80;
    if (!input) {
        (*env)->GetByteArrayRegion(env, buffer, offset, length, bytes);
        if ((*env)->ExceptionCheck(env)) {
            free(bytes);
            return -EINVAL;
        }
    }
    struct usbdevfs_bulktransfer request = {
        .ep = (unsigned int)endpoint,
        .len = (unsigned int)length,
        .timeout = (unsigned int)timeout,
        .data = bytes
    };
    int transferred = ioctl(fd, USBDEVFS_BULK, &request);
    int result = transferred < 0 ? -errno : transferred;
    if (transferred > length) {
        result = -EOVERFLOW;
    } else if (input && transferred > 0) {
        (*env)->SetByteArrayRegion(env, buffer, offset, transferred, bytes);
    }
    free(bytes);
    return result;
}
