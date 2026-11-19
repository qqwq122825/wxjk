#import <Foundation/Foundation.h>
#import <UIKit/UIKit.h>

NS_ASSUME_NONNULL_BEGIN

FOUNDATION_EXPORT NSString * const JKWindowCaptureStateNotification;
FOUNDATION_EXPORT NSString * const JKWindowCaptureFrameNotification;
FOUNDATION_EXPORT NSString * const JKWindowCaptureErrorDomain;

typedef NS_ENUM(NSInteger, JKWindowCaptureError) {
    JKWindowCaptureWaitingForeground = 1,
    JKWindowCaptureNoWindow,
    JKWindowCaptureAmbiguousWindow,
    JKWindowCaptureIncomplete,
    JKWindowCaptureBusy,
    JKWindowCaptureCanceled,
    JKWindowCaptureInvalidCommand,
    JKWindowCaptureStaleCommand,
    JKWindowCaptureTaskConflict,
    JKWindowCapturePersistenceFailed,
    JKWindowCaptureUploadFailed
};

@interface JKCapturedFrame : NSObject
@property(nonatomic, readonly) NSData *PNGData;
@property(nonatomic, readonly) NSDictionary<NSString *, id> *metadata;
@end

typedef UIWindow * _Nullable (^JKCaptureWindowProvider)(void);
typedef void (^JKCaptureCompletion)(JKCapturedFrame * _Nullable frame,
                                    NSError * _Nullable error);

// Public methods may be called on any queue. Set the two block properties on the
// main queue. Providers, completions and notifications execute on the main queue.
// Loading the dylib initializes this singleton after three seconds. It does not
// start capturing until a task or one-shot request is explicitly submitted.
@interface JKWindowCapture : NSObject
+ (instancetype)sharedCapture;
- (void)initializeIfNeeded;

@property(nonatomic, copy, nullable) JKCaptureWindowProvider windowProvider;
@property(nonatomic, copy, nullable) void (^frameHandler)(JKCapturedFrame *frame);
@property(nonatomic, readonly) NSDictionary<NSString *, id> *currentState;

- (void)captureOnceWithCompletion:(JKCaptureCompletion)completion;

// Optional transport: HTTPS multipart upload with the protocol's metadata/image
// parts. Credentials are supplied by the host, never compiled into the dylib.
- (void)configureUploadURL:(nullable NSURL *)URL
                 deviceID:(nullable NSString *)deviceID
              deviceToken:(nullable NSString *)token
             assignmentID:(nullable NSString *)assignmentID
        assignmentVersion:(NSUInteger)assignmentVersion;

// Applies START_IMAGE_SESSION / STOP_IMAGE_SESSION / GET_IMAGE dictionaries from
// the authenticated command channel. START is persisted before acknowledgement.
// completion receives {status, command_id, session_id?, ...} or an NSError.
// A stopped task ID cannot be reused; generate a new ID for a new task.
- (void)handleCommand:(NSDictionary<NSString *, id> *)command
          completion:(void (^)(NSDictionary * _Nullable result,
                                NSError * _Nullable error))completion;

// Host can disable capture immediately when device access is revoked.
- (void)setCaptureAuthorized:(BOOL)authorized;
@end

NS_ASSUME_NONNULL_END
