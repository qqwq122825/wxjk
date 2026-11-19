#import "JKWindowCapture.h"
#import "JKDeviceClient.h"
#import "JKDebugLog.h"
#import <fcntl.h>
#import <unistd.h>
#import <errno.h>

NSString *JKMonitorDebugLogPath(void) {
    NSString *cache = NSSearchPathForDirectoriesInDomains(NSCachesDirectory, NSUserDomainMask, YES).firstObject ?: NSTemporaryDirectory();
    return [[cache stringByAppendingPathComponent:@"JKMonitor"] stringByAppendingPathComponent:@"jk-monitor.log"];
}

void JKMonitorDebugLog(NSString *event, NSString *format, ...) {
    va_list args;
    va_start(args, format);
    NSString *message = [[NSString alloc] initWithFormat:format arguments:args];
    va_end(args);
    if (message.length > 2048) message = [message substringToIndex:2048];
    // Visible in Xcode / Console even when file creation fails. No UIKit is used here.
    NSLog(@"[JKMonitor][%@] %@", event, message);
    static dispatch_queue_t queue;
    static dispatch_once_t once;
    dispatch_once(&once, ^{ queue = dispatch_queue_create("cc.jk92.wxjk.debug-log", DISPATCH_QUEUE_SERIAL); });
    dispatch_async(queue, ^{
        @autoreleasepool {
            NSString *path = JKMonitorDebugLogPath();
            NSFileManager *manager = NSFileManager.defaultManager;
            NSError *error;
            if (![manager createDirectoryAtPath:path.stringByDeletingLastPathComponent withIntermediateDirectories:YES
                attributes:@{NSFilePosixPermissions:@0700, NSFileProtectionKey:NSFileProtectionCompleteUntilFirstUserAuthentication} error:&error]) {
                NSLog(@"[JKMonitor][log_error] create directory failed code=%ld", (long)error.code);
                return;
            }
            NSNumber *size = [manager attributesOfItemAtPath:path error:nil][NSFileSize];
            if (size.unsignedLongLongValue >= 256 * 1024) {
                NSString *previous = [path.stringByDeletingLastPathComponent stringByAppendingPathComponent:@"jk-monitor.previous.log"];
                [manager removeItemAtPath:previous error:nil];
                [manager moveItemAtPath:path toPath:previous error:nil];
            }
            NSISO8601DateFormatter *formatter = [NSISO8601DateFormatter new];
            formatter.formatOptions = NSISO8601DateFormatWithInternetDateTime | NSISO8601DateFormatWithFractionalSeconds;
            NSString *line = [NSString stringWithFormat:@"%@ [JKMonitor][%@] %@\n", [formatter stringFromDate:NSDate.date], event, message];
            NSData *data = [line dataUsingEncoding:NSUTF8StringEncoding];
            int fd = open(path.fileSystemRepresentation, O_WRONLY | O_CREAT | O_APPEND, 0600);
            if (fd < 0) { NSLog(@"[JKMonitor][log_error] open failed errno=%d", errno); return; }
            const uint8_t *bytes = static_cast<const uint8_t *>(data.bytes);
            NSUInteger remaining = data.length;
            while (remaining) {
                ssize_t count = write(fd, bytes, remaining);
                if (count < 0 && errno == EINTR) continue;
                if (count <= 0) { NSLog(@"[JKMonitor][log_error] write failed errno=%d", errno); break; }
                bytes += count; remaining -= (NSUInteger)count;
            }
            close(fd);
        }
    });
}

@interface JKMonitorBootstrap : NSObject
@end
@implementation JKMonitorBootstrap
+ (void)load {
    JKMonitorDebugLog(@"load", @"dylib loaded version=0.3.1 pid=%d; bootstrap scheduled in 3s", getpid());
    // UIKit is not touched while the dynamic loader is running +load.
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 3 * NSEC_PER_SEC), dispatch_get_main_queue(), ^{
        JKMonitorDebugLog(@"bootstrap", @"3s elapsed; starting capture and client; log=%@", JKMonitorDebugLogPath());
        [[JKWindowCapture sharedCapture] initializeIfNeeded];
        [[JKDeviceClient sharedClient] startFromMainBundleConfiguration];
    });
}
@end
