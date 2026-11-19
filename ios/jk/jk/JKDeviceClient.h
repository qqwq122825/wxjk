#import <Foundation/Foundation.h>

NS_ASSUME_NONNULL_BEGIN
FOUNDATION_EXPORT NSString * const JKDeviceClientStateNotification;

// Native device registration, authenticated WSS, heartbeats and command receipts.
// No browser connection is needed after a phone-owned image task is accepted.
@interface JKDeviceClient : NSObject
+ (instancetype)sharedClient;
@property(nonatomic, readonly) NSDictionary<NSString *, id> *currentState;

// Automatically called by +load after three seconds. Configuration is read from
// the built-in https://wxjk.jk92.cc by default; no address UI or saved URL required.
// An explicit JKMonitor.server_url host configuration can override it for testing.
- (void)startFromMainBundleConfiguration;
- (void)configureWithServerURL:(NSURL *)URL enrollmentToken:(nullable NSString *)token;
- (void)stop;
@end
NS_ASSUME_NONNULL_END
