#import "JKWindowCapture.h"
#import "JKDeviceClient.h"

@interface JKMonitorBootstrap : NSObject
@end

@implementation JKMonitorBootstrap
+ (void)load {
    // UIKit is not touched while the dynamic loader is running +load.
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 3 * NSEC_PER_SEC),
                   dispatch_get_main_queue(), ^{
        [[JKWindowCapture sharedCapture] initializeIfNeeded];
        [[JKDeviceClient sharedClient] startFromMainBundleConfiguration];
    });
}
@end
