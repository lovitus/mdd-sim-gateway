#import <AVFoundation/AVFoundation.h>
#import <dispatch/dispatch.h>

int mdd_request_microphone_authorization(unsigned int timeout_ms) {
    @autoreleasepool {
        AVAuthorizationStatus status =
            [AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeAudio];
        if (status == AVAuthorizationStatusAuthorized) return 0;
        if (status == AVAuthorizationStatusDenied) return 1;
        if (status == AVAuthorizationStatusRestricted) return 2;
        if (status != AVAuthorizationStatusNotDetermined) return 4;

        __block BOOL granted = NO;
        dispatch_semaphore_t completed = dispatch_semaphore_create(0);
        [AVCaptureDevice requestAccessForMediaType:AVMediaTypeAudio
                                completionHandler:^(BOOL value) {
            granted = value;
            dispatch_semaphore_signal(completed);
        }];
        dispatch_time_t deadline = dispatch_time(
            DISPATCH_TIME_NOW, (int64_t)timeout_ms * NSEC_PER_MSEC);
        if (dispatch_semaphore_wait(completed, deadline) != 0) return 3;
        return granted ? 0 : 1;
    }
}
