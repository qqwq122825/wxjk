#import "JKWindowCapture.h"
#import "JKCapturePolicy.hpp"
#import <ImageIO/ImageIO.h>
#import <mach/mach_time.h>

NSString * const JKWindowCaptureStateNotification = @"JKWindowCaptureStateNotification";
NSString * const JKWindowCaptureFrameNotification = @"JKWindowCaptureFrameNotification";
NSString * const JKWindowCaptureErrorDomain = @"JKWindowCaptureErrorDomain";

static double JKNow(void) {
    static mach_timebase_info_data_t info;
    static dispatch_once_t once;
    dispatch_once(&once, ^{ mach_timebase_info(&info); });
    return (double)mach_continuous_time() * info.numer / info.denom / 1e9;
}

static void JKMain(dispatch_block_t block) {
    if (NSThread.isMainThread) block();
    else dispatch_async(dispatch_get_main_queue(), block);
}

static NSError *JKError(JKWindowCaptureError code, NSString *message) {
    return [NSError errorWithDomain:JKWindowCaptureErrorDomain code:code
                          userInfo:@{NSLocalizedDescriptionKey: message}];
}

static NSString *JKString(id value) {
    return [value isKindOfClass:NSString.class] ? value : nil;
}

static BOOL JKNumber(id value) {
    return [value isKindOfClass:NSNumber.class] && CFGetTypeID((__bridge CFTypeRef)value) != CFBooleanGetTypeID();
}

static NSString *JKTimestamp(NSDate *date) {
    static NSISO8601DateFormatter *formatter;
    static dispatch_once_t once;
    dispatch_once(&once, ^{ formatter = [NSISO8601DateFormatter new]; });
    return [formatter stringFromDate:date];
}

static NSDate *JKParseDate(id value) {
    if (!JKString(value)) return nil;
    NSISO8601DateFormatter *formatter = [NSISO8601DateFormatter new];
    NSDate *result = [formatter dateFromString:value];
    if (!result) {
        formatter.formatOptions |= NSISO8601DateFormatWithFractionalSeconds;
        result = [formatter dateFromString:value];
    }
    return result;
}

@interface JKCapturedFrame ()
@property(nonatomic, readwrite) NSData *PNGData;
@property(nonatomic, readwrite) NSDictionary<NSString *, id> *metadata;
@end
@implementation JKCapturedFrame
@end

@interface JKWindowCapture () <NSURLSessionTaskDelegate> {
    jk::CapturePolicy _policy;
}
@property(nonatomic) BOOL initialized;
@property(nonatomic) BOOL applicationActive;
@property(nonatomic) BOOL protectedDataAvailable;
@property(nonatomic) BOOL captureAuthorized;
@property(nonatomic) BOOL captureBusy;
@property(nonatomic) NSUInteger invalidation;
@property(nonatomic) NSUInteger uploadGeneration;
@property(nonatomic) NSUInteger maxLongEdge;
@property(nonatomic) NSUInteger frameSequence;
@property(nonatomic) dispatch_source_t timer;
@property(nonatomic) dispatch_queue_t encodingQueue;
@property(nonatomic) NSMutableArray *observers;
@property(nonatomic) NSURL *stateURL;
@property(nonatomic) NSDate *taskExpiresAt;
@property(nonatomic) NSString *startCommandID;
@property(nonatomic) NSDictionary *taskContext;
@property(nonatomic) NSDictionary *lastState;
@property(nonatomic) NSURL *uploadURL;
@property(nonatomic) NSString *deviceID;
@property(nonatomic) NSString *deviceToken;
@property(nonatomic) NSString *assignmentID;
@property(nonatomic) NSUInteger assignmentVersion;
@property(nonatomic) NSURLSession *URLSession;
@property(nonatomic) NSURLSessionUploadTask *uploadTask;
@end

@implementation JKWindowCapture

+ (instancetype)sharedCapture {
    static JKWindowCapture *capture;
    static dispatch_once_t once;
    dispatch_once(&once, ^{ capture = [JKWindowCapture new]; });
    return capture;
}

- (instancetype)init {
    if ((self = [super init])) {
        _captureAuthorized = YES;
        _maxLongEdge = 1280;
        _taskContext = @{};
        _observers = [NSMutableArray new];
        _encodingQueue = dispatch_queue_create("com.jk.window-capture.encoding", DISPATCH_QUEUE_SERIAL);
    }
    return self;
}

- (void)initializeIfNeeded {
    JKMain(^{
        if (self.initialized) return;
        self.initialized = YES;
        UIApplication *app = UIApplication.sharedApplication;
        self.applicationActive = app.applicationState == UIApplicationStateActive;
        self.protectedDataAvailable = app.protectedDataAvailable;
        NSURL *directory = [[NSFileManager.defaultManager URLsForDirectory:NSApplicationSupportDirectory
                                                                  inDomains:NSUserDomainMask] firstObject];
        self.stateURL = [[directory URLByAppendingPathComponent:@"JKMonitor" isDirectory:YES]
                         URLByAppendingPathComponent:@"capture-task.json"];
        [self restoreTask];
        NSArray *names = @[
            UIApplicationWillResignActiveNotification,
            UIApplicationDidEnterBackgroundNotification,
            UIApplicationDidBecomeActiveNotification,
            UIApplicationProtectedDataWillBecomeUnavailable,
            UIApplicationProtectedDataDidBecomeAvailable,
            UISceneWillDeactivateNotification,
            UISceneDidActivateNotification,
            UISceneDidEnterBackgroundNotification,
            UISceneDidDisconnectNotification,
            UIWindowDidBecomeKeyNotification,
            UIWindowDidResignKeyNotification
        ];
        __weak JKWindowCapture *weakSelf = self;
        for (NSString *name in names) {
            id observer = [NSNotificationCenter.defaultCenter addObserverForName:name object:nil
                queue:NSOperationQueue.mainQueue usingBlock:^(NSNotification *note) {
                    [weakSelf lifecycleChanged:note];
                }];
            [self.observers addObject:observer];
        }
        [self refreshActivity];
        [self publishState:nil reason:@"initialized"];
    });
}

- (void)lifecycleChanged:(NSNotification *)note {
    if ([note.name isEqualToString:UIApplicationWillResignActiveNotification] ||
        [note.name isEqualToString:UIApplicationDidEnterBackgroundNotification]) {
        self.applicationActive = NO;
    } else if ([note.name isEqualToString:UIApplicationDidBecomeActiveNotification]) {
        self.applicationActive = YES;
    } else if ([note.name isEqualToString:UIApplicationProtectedDataWillBecomeUnavailable]) {
        self.protectedDataAvailable = NO;
    } else if ([note.name isEqualToString:UIApplicationProtectedDataDidBecomeAvailable]) {
        self.protectedDataAvailable = YES;
    }
    // Deactivation is delivered before activationState necessarily changes.
    // Invalidate pending frames immediately, then resolve a new window next turn.
    if ([note.name isEqualToString:UISceneWillDeactivateNotification] ||
        [note.name isEqualToString:UISceneDidEnterBackgroundNotification] ||
        [note.name isEqualToString:UISceneDidDisconnectNotification] ||
        [note.name isEqualToString:UIWindowDidResignKeyNotification]) {
        [self invalidatePendingWork];
        self->_policy.setActive(false, JKNow());
    }
    [self refreshActivity];
    if ([note.name isEqualToString:UISceneWillDeactivateNotification]) {
        self->_policy.setActive(false, JKNow());
        [self updateTimer];
        [self publishState:@"waiting_foreground" reason:@"scene_inactive"];
    }
    dispatch_async(dispatch_get_main_queue(), ^{ [self refreshActivity]; });
}

- (UIWindow *)eligibleWindow:(NSError **)error {
    UIApplication *app = UIApplication.sharedApplication;
    if (!self.applicationActive || app.applicationState != UIApplicationStateActive ||
        !self.protectedDataAvailable || !app.protectedDataAvailable || !self.captureAuthorized) {
        if (error) *error = JKError(JKWindowCaptureWaitingForeground, @"应用不活动或受保护数据不可用，已暂停采集");
        return nil;
    }
    UIWindow *window;
    if (self.windowProvider) {
        window = self.windowProvider();
    } else {
        NSMutableArray<UIWindow *> *candidates = [NSMutableArray new];
        for (UIScene *scene in app.connectedScenes) {
            if (![scene isKindOfClass:UIWindowScene.class] || scene.activationState != UISceneActivationStateForegroundActive) continue;
            for (UIWindow *candidate in ((UIWindowScene *)scene).windows) {
                if (candidate.isKeyWindow && !candidate.hidden && candidate.alpha > 0) [candidates addObject:candidate];
            }
        }
        if (app.connectedScenes.count == 0 && [app.delegate respondsToSelector:@selector(window)]) {
            UIWindow *legacyWindow = app.delegate.window;
            if (legacyWindow.isKeyWindow && !legacyWindow.hidden) [candidates addObject:legacyWindow];
        }
        if (candidates.count > 1) {
            if (error) *error = JKError(JKWindowCaptureAmbiguousWindow, @"多个前台窗口，需要主项目提供 windowProvider");
            return nil;
        }
        window = candidates.firstObject;
    }
    BOOL sceneActive = window.windowScene ? window.windowScene.activationState == UISceneActivationStateForegroundActive
                                         : app.connectedScenes.count == 0;
    if (!window || window.hidden || window.alpha <= 0 || !sceneActive ||
        !isfinite(window.bounds.size.width) || !isfinite(window.bounds.size.height) ||
        window.bounds.size.width <= 0 || window.bounds.size.height <= 0) {
        if (error) *error = JKError(JKWindowCaptureNoWindow, @"当前没有可采集的活动窗口");
        return nil;
    }
    return window;
}

- (void)refreshActivity {
    NSAssert(NSThread.isMainThread, @"UIKit access must be on main");
    BOOL active = [self eligibleWindow:nil] != nil;
    if (_policy.active && !active) [self invalidatePendingWork];
    _policy.setActive(active, JKNow());
    if (_policy.expire(JKNow())) {
        [self invalidatePendingWork];
        [self persistTask:nil];
        [self publishState:@"expired" reason:@"task_deadline_reached"];
    } else if (_policy.desiredRunning) {
        [self publishState:active ? @"ready" : @"waiting_foreground"
                     reason:active ? @"active_window_available" : @"application_not_ready"];
    }
    [self updateTimer];
}

- (void)invalidatePendingWork {
    self.invalidation += 1;
    self.uploadGeneration += 1;
    [self.uploadTask cancel];
    self.uploadTask = nil;
}

- (void)updateTimer {
    BOOL needed = _policy.desiredRunning && _policy.active;
    if (!needed && self.timer) {
        dispatch_source_cancel(self.timer);
        self.timer = nil;
    }
    if (!needed || self.timer) return;
    self.timer = dispatch_source_create(DISPATCH_SOURCE_TYPE_TIMER, 0, 0, dispatch_get_main_queue());
    dispatch_source_set_timer(self.timer, DISPATCH_TIME_NOW, NSEC_PER_SEC, 100 * NSEC_PER_MSEC);
    __weak JKWindowCapture *weakSelf = self;
    dispatch_source_set_event_handler(self.timer, ^{ [weakSelf tick]; });
    dispatch_resume(self.timer);
}

- (void)tick {
    if (_policy.expire(JKNow())) {
        [self invalidatePendingWork];
        [self persistTask:nil];
        [self updateTimer];
        [self publishState:@"expired" reason:@"task_deadline_reached"];
        return;
    }
    if (!_policy.due(JKNow()) || self.captureBusy || self.uploadTask) return;
    if (![self eligibleWindow:nil]) { [self refreshActivity]; return; }
    _policy.attempted(JKNow());
    NSString *taskID = [NSString stringWithUTF8String:_policy.taskID.c_str()];
    NSUInteger version = _policy.version;
    NSUInteger frame = ++self.frameSequence;
    NSError *saveError;
    if (![self persistTask:&saveError]) {
        [self publishState:@"persistence_failed" reason:saveError.localizedDescription];
        return;
    }
    NSMutableDictionary *context = [self.taskContext mutableCopy];
    [context addEntriesFromDictionary:@{@"session_id": taskID, @"session_version": @(version),
                                        @"frame_seq": @(frame)}];
    [self captureWithContext:context completion:^(JKCapturedFrame *result, NSError *error) {
        if (error) {
            if (error.code != JKWindowCaptureCanceled) [self publishState:@"capture_failed" reason:error.localizedDescription];
            return;
        }
        [self deliverFrame:result completion:^(NSError *deliveryError) {
            if (deliveryError.code == JKWindowCaptureCanceled || !self->_policy.desiredRunning ||
                self->_policy.taskID != taskID.UTF8String || self->_policy.version != version) return;
            if (deliveryError) [self publishState:@"upload_failed" reason:deliveryError.localizedDescription];
            else [self publishState:@"running" reason:self.uploadURL ? @"frame_uploaded" : @"frame_ready_locally"];
        }];
    }];
}

- (void)captureOnceWithCompletion:(JKCaptureCompletion)completion {
    JKMain(^{
        [self initializeIfNeeded];
        [self captureWithContext:@{} completion:completion];
    });
}

- (void)captureWithContext:(NSDictionary *)context completion:(JKCaptureCompletion)completion {
    NSError *windowError;
    UIWindow *window = [self eligibleWindow:&windowError];
    if (!window) { completion(nil, windowError); return; }
    if (self.captureBusy || self.uploadTask) {
        completion(nil, JKError(JKWindowCaptureBusy, @"上一帧仍在生成或上传"));
        return;
    }
    self.captureBusy = YES;
    NSUInteger ticket = self.invalidation;
    uint64_t policyTicket = _policy.generation;
    BOOL taskFrame = context[@"session_id"] != nil;
    CGRect bounds = window.bounds;
    CGSize size = bounds.size;
    CGFloat nativeScale = window.screen.scale;
    CGFloat ratio = MIN(nativeScale, (CGFloat)self.maxLongEdge / MAX(size.width, size.height));
    CGSize pixels = CGSizeMake(MAX(1, floor(size.width * ratio)), MAX(1, floor(size.height * ratio)));
    UIGraphicsImageRendererFormat *format = [UIGraphicsImageRendererFormat defaultFormat];
    format.scale = 1;
    format.opaque = YES;
    UIGraphicsImageRenderer *renderer = [[UIGraphicsImageRenderer alloc] initWithSize:pixels format:format];
    __block BOOL complete = NO;
    UIImage *image;
    NSDate *capturedAt = NSDate.date;
    @try {
        [window layoutIfNeeded];
        image = [renderer imageWithActions:^(UIGraphicsImageRendererContext *renderContext) {
            CGContextRef cg = renderContext.CGContext;
            CGContextSetRGBFillColor(cg, 1, 1, 1, 1);
            CGContextFillRect(cg, (CGRect){CGPointZero, pixels});
            CGContextScaleCTM(cg, pixels.width / size.width, pixels.height / size.height);
            CGContextTranslateCTM(cg, -bounds.origin.x, -bounds.origin.y);
            complete = [window drawViewHierarchyInRect:bounds afterScreenUpdates:YES];
        }];
    } @catch (NSException *exception) {
        self.captureBusy = NO;
        completion(nil, JKError(JKWindowCaptureIncomplete, @"窗口渲染失败"));
        return;
    }
    CGImageRef cgImage = image.CGImage;
    if (!complete || !cgImage) {
        self.captureBusy = NO;
        completion(nil, JKError(JKWindowCaptureIncomplete, @"窗口图像不完整"));
        return;
    }
    CGImageRetain(cgImage);
    NSUInteger width = CGImageGetWidth(cgImage), height = CGImageGetHeight(cgImage);
    __weak UIWindow *weakWindow = window;
    dispatch_async(self.encodingQueue, ^{
        @autoreleasepool {
            NSMutableData *data = [NSMutableData new];
            CGImageDestinationRef destination = CGImageDestinationCreateWithData((__bridge CFMutableDataRef)data,
                                                                                 CFSTR("public.png"), 1, NULL);
            BOOL encoded = NO;
            if (destination) {
                CGImageDestinationAddImage(destination, cgImage, NULL);
                encoded = CGImageDestinationFinalize(destination);
                CFRelease(destination);
            }
            CGImageRelease(cgImage);
            dispatch_async(dispatch_get_main_queue(), ^{
                self.captureBusy = NO;
                UIWindow *currentWindow = [self eligibleWindow:nil];
                BOOL valid = ticket == self.invalidation && currentWindow && currentWindow == weakWindow &&
                             CGRectEqualToRect(currentWindow.bounds, bounds);
                if (taskFrame) valid = valid && self->_policy.valid(policyTicket, JKNow());
                if (!valid) {
                    completion(nil, JKError(JKWindowCaptureCanceled, @"应用状态、窗口或任务已变化，本帧已丢弃"));
                    return;
                }
                if (!encoded || data.length == 0 || data.length > 8 * 1024 * 1024) {
                    completion(nil, JKError(JKWindowCaptureIncomplete, @"PNG 编码失败或超过 8 MB 限制"));
                    return;
                }
                NSMutableDictionary *metadata = [context mutableCopy];
                [metadata addEntriesFromDictionary:@{
                    @"report_id": NSUUID.UUID.UUIDString,
                    @"capture_source": @"uikit_window", @"capture_scope": @"active_window",
                    @"captured_at": JKTimestamp(capturedAt), @"app_state": @"foreground",
                    @"is_cached": @NO, @"pixel_width": @(width), @"pixel_height": @(height)
                }];
                JKCapturedFrame *frame = [JKCapturedFrame new];
                frame.PNGData = data;
                frame.metadata = metadata;
                completion(frame, nil);
            });
        }
    });
}

- (void)handleCommand:(NSDictionary<NSString *,id> *)command
          completion:(void (^)(NSDictionary *, NSError *))completion {
    // Copy input before crossing queues; the caller must not mutate nested values.
    NSDictionary *input = [command copy];
    JKMain(^{
        [self initializeIfNeeded];
        NSString *type = JKString(input[@"command"]);
        NSString *commandID = JKString(input[@"command_id"]);
        NSDictionary *parameters = [input[@"parameters"] isKindOfClass:NSDictionary.class] ? input[@"parameters"] : @{};
        NSDate *commandExpires = JKParseDate(input[@"expires_at"]);
        if (!type.length || !commandID.length || !commandExpires || commandExpires.timeIntervalSinceNow <= 0) {
            completion(nil, JKError(JKWindowCaptureInvalidCommand, @"缺少有效指令编号、类型或 expires_at"));
            return;
        }
        if ([type isEqualToString:@"GET_IMAGE"] || [type isEqualToString:@"START_IMAGE_SESSION"]) {
            if (![JKString(parameters[@"capture_source"]) isEqualToString:@"uikit_window"] ||
                ![JKString(parameters[@"capture_scope"]) isEqualToString:@"active_window"]) {
                completion(nil, JKError(JKWindowCaptureInvalidCommand, @"仅支持 uikit_window / active_window"));
                return;
            }
            if (self.uploadURL &&
                (![JKString(input[@"assignment_id"]) ?: @"" isEqual:self.assignmentID ?: @""] ||
                 !JKNumber(input[@"assignment_version"]) ||
                 [input[@"assignment_version"] unsignedIntegerValue] != self.assignmentVersion)) {
                completion(nil, JKError(JKWindowCaptureStaleCommand, @"设备归属已变化，拒绝执行旧批次采集"));
                return;
            }
        }
        if ([type isEqualToString:@"GET_IMAGE"]) {
            NSNumber *edge = parameters[@"max_long_edge_px"] ?: @1280;
            if (!JKNumber(edge) || !isfinite(edge.doubleValue) || edge.doubleValue < 320 || edge.doubleValue > 2560) {
                completion(nil, JKError(JKWindowCaptureInvalidCommand, @"输出大小无效"));
                return;
            }
            NSMutableDictionary *context = [NSMutableDictionary dictionaryWithDictionary:@{@"command_id": commandID}];
            [self addAssignment:input to:context];
            NSUInteger oldEdge = self.maxLongEdge;
            self.maxLongEdge = edge.unsignedIntegerValue;
            [self captureWithContext:context completion:^(JKCapturedFrame *frame, NSError *error) {
                if (error) { completion(nil, error); return; }
                [self deliverFrame:frame completion:^(NSError *uploadError) {
                    if (uploadError) completion(nil, uploadError);
                    else completion(@{@"command_id": commandID, @"status": self.uploadURL ? @"succeeded" : @"frame_ready",
                                      @"report_id": frame.metadata[@"report_id"]}, nil);
                }];
            }];
            self.maxLongEdge = oldEdge;
            return;
        }
        NSString *sessionID = JKString(parameters[@"session_id"]);
        NSNumber *version = parameters[@"session_version"];
        if (!sessionID.length || !JKNumber(version) || version.doubleValue <= 0 ||
            version.doubleValue != floor(version.doubleValue) || version.doubleValue > 9007199254740991.0) {
            completion(nil, JKError(JKWindowCaptureInvalidCommand, @"缺少有效 session_id / session_version"));
            return;
        }
        jk::CapturePolicy previous = self->_policy;
        NSDictionary *oldContext = self.taskContext;
        NSDate *oldExpiry = self.taskExpiresAt;
        NSString *oldCommand = self.startCommandID;
        NSUInteger oldMax = self.maxLongEdge;
        NSUInteger oldSequence = self.frameSequence;
        jk::Decision decision;
        if ([type isEqualToString:@"START_IMAGE_SESSION"]) {
            NSNumber *interval = parameters[@"interval_ms"] ?: @3000;
            NSNumber *edge = parameters[@"max_long_edge_px"] ?: @1280;
            id rawExpiry = parameters[@"task_expires_at"];
            NSDate *expiry = rawExpiry && rawExpiry != NSNull.null ? JKParseDate(rawExpiry) : nil;
            if (!JKNumber(interval) || !JKNumber(edge) || edge.doubleValue < 320 || edge.doubleValue > 2560 ||
                !isfinite(edge.doubleValue) || (rawExpiry && rawExpiry != NSNull.null && !expiry)) {
                completion(nil, JKError(JKWindowCaptureInvalidCommand, @"采集间隔、输出大小或任务截止时间无效"));
                return;
            }
            double deadline = expiry ? JKNow() + expiry.timeIntervalSinceNow : 0;
            decision = self->_policy.start(sessionID.UTF8String, version.unsignedLongLongValue,
                                            interval.doubleValue / 1000, deadline, JKNow());
            if (decision == jk::Decision::accepted) {
                NSMutableDictionary *context = [NSMutableDictionary new];
                [self addAssignment:input to:context];
                self.taskContext = context;
                self.taskExpiresAt = expiry;
                self.startCommandID = commandID;
                self.frameSequence = 0;
                self.maxLongEdge = edge.unsignedIntegerValue;
            }
        } else if ([type isEqualToString:@"STOP_IMAGE_SESSION"]) {
            decision = self->_policy.stop(sessionID.UTF8String, version.unsignedLongLongValue);
        } else {
            completion(nil, JKError(JKWindowCaptureInvalidCommand, @"未知采集指令"));
            return;
        }
        if (decision != jk::Decision::accepted && decision != jk::Decision::duplicate) {
            JKWindowCaptureError code = decision == jk::Decision::stale ? JKWindowCaptureStaleCommand :
                decision == jk::Decision::conflict ? JKWindowCaptureTaskConflict : JKWindowCaptureInvalidCommand;
            completion(nil, JKError(code, @"指令版本陈旧、任务冲突或参数无效"));
            return;
        }
        NSError *saveError;
        if (![self persistTask:&saveError]) {
            self->_policy = previous;
            self.taskContext = oldContext;
            self.taskExpiresAt = oldExpiry;
            self.startCommandID = oldCommand;
            self.maxLongEdge = oldMax;
            self.frameSequence = oldSequence;
            completion(nil, saveError);
            return;
        }
        if (decision == jk::Decision::accepted) [self invalidatePendingWork];
        [self refreshActivity];
        NSString *status = [type isEqualToString:@"STOP_IMAGE_SESSION"] ? @"stopped" :
            (self->_policy.active ? @"accepted" : @"waiting_foreground");
        if (!self->_policy.desiredRunning) [self publishState:@"stopped" reason:@"stop_command"];
        completion(@{@"command_id": commandID, @"session_id": sessionID, @"status": status,
                      @"duplicate": @(decision == jk::Decision::duplicate)}, nil);
    });
}

- (void)addAssignment:(NSDictionary *)input to:(NSMutableDictionary *)context {
    if (JKString(input[@"assignment_id"])) context[@"assignment_id"] = input[@"assignment_id"];
    if (JKNumber(input[@"assignment_version"])) context[@"assignment_version"] = input[@"assignment_version"];
}

- (void)setCaptureAuthorized:(BOOL)authorized {
    JKMain(^{
        self->_captureAuthorized = authorized;
        [self invalidatePendingWork];
        if (!authorized && self->_policy.desiredRunning) {
            self->_policy.stop(self->_policy.taskID, self->_policy.version + 1);
            [self persistTask:nil];
            [self publishState:@"stopped" reason:@"authorization_revoked"];
        }
        if (self.initialized) [self refreshActivity];
    });
}

- (NSDictionary *)currentState {
    __block NSDictionary *state;
    dispatch_block_t read = ^{ state = self.lastState ?: @{@"state": @"not_initialized"}; };
    if (NSThread.isMainThread) read(); else dispatch_sync(dispatch_get_main_queue(), read);
    return state;
}

- (void)publishState:(NSString *)state reason:(NSString *)reason {
    NSString *value = state ?: (_policy.desiredRunning ? (_policy.active ? @"ready" : @"waiting_foreground") : @"idle");
    NSDictionary *snapshot = @{@"state": value, @"reason": reason ?: @"",
        @"desired_running": @(_policy.desiredRunning),
        @"session_id": _policy.taskID.empty() ? @"" : [NSString stringWithUTF8String:_policy.taskID.c_str()],
        @"session_version": @(_policy.version), @"screen_state": @"unknown"};
    if ([snapshot isEqual:self.lastState]) return;
    self.lastState = snapshot;
    [NSNotificationCenter.defaultCenter postNotificationName:JKWindowCaptureStateNotification object:self userInfo:snapshot];
}

- (BOOL)persistTask:(NSError **)error {
    NSMutableArray *records = [NSMutableArray new];
    for (const auto &entry : _policy.records) {
        [records addObject:@{@"id": [NSString stringWithUTF8String:entry.first.c_str()],
                            @"version": @(entry.second.version), @"terminal": @(entry.second.terminal)}];
    }
    NSDictionary *snapshot = @{@"format": @1, @"records": records,
        @"task_id": _policy.taskID.empty() ? @"" : [NSString stringWithUTF8String:_policy.taskID.c_str()],
        @"version": @(_policy.version), @"desired_running": @(_policy.desiredRunning),
        @"interval": @(_policy.interval), @"expires_at": self.taskExpiresAt ? JKTimestamp(self.taskExpiresAt) : NSNull.null,
        @"context": self.taskContext ?: @{}, @"command_id": self.startCommandID ?: @"",
        @"max_long_edge_px": @(self.maxLongEdge), @"frame_seq": @(self.frameSequence)};
    NSError *underlying;
    NSData *data = [NSJSONSerialization dataWithJSONObject:snapshot options:0 error:&underlying];
    NSURL *directory = [self.stateURL URLByDeletingLastPathComponent];
    BOOL ok = data && directory && [NSFileManager.defaultManager createDirectoryAtURL:directory
        withIntermediateDirectories:YES attributes:@{NSFileProtectionKey: NSFileProtectionCompleteUntilFirstUserAuthentication}
        error:&underlying] && [data writeToURL:self.stateURL options:NSDataWritingAtomic error:&underlying];
    if (ok) {
        [self.stateURL setResourceValue:@YES forKey:NSURLIsExcludedFromBackupKey error:nil];
    } else if (error) {
        *error = JKError(JKWindowCapturePersistenceFailed, underlying.localizedDescription ?: @"无法持久保存任务，未确认接收");
    }
    return ok;
}

- (void)restoreTask {
    NSData *data = [NSData dataWithContentsOfURL:self.stateURL];
    id raw = data ? [NSJSONSerialization JSONObjectWithData:data options:0 error:nil] : nil;
    if (![raw isKindOfClass:NSDictionary.class] || ![raw[@"format"] isEqual:@1]) return;
    NSDictionary *saved = raw;
    NSArray *records = [saved[@"records"] isKindOfClass:NSArray.class] ? saved[@"records"] : @[];
    if (records.count > 1024) return;
    for (id value in records) {
        if (![value isKindOfClass:NSDictionary.class]) return;
        NSString *key = JKString(value[@"id"]);
        if (!key.length || key.length > 200 || !JKNumber(value[@"version"]) ||
            [value[@"version"] unsignedLongLongValue] == 0) return;
        _policy.records[key.UTF8String] = {[value[@"version"] unsignedLongLongValue], [value[@"terminal"] boolValue] != NO};
    }
    NSString *taskID = JKString(saved[@"task_id"]);
    if (!taskID.length || !JKNumber(saved[@"version"]) || !JKNumber(saved[@"interval"])) return;
    auto found = _policy.records.find(taskID.UTF8String);
    if (found == _policy.records.end() || found->second.terminal || ![saved[@"desired_running"] boolValue]) return;
    double interval = [saved[@"interval"] doubleValue];
    if (!isfinite(interval) || interval < 3 || interval > 3600) return;
    self.taskExpiresAt = JKParseDate(saved[@"expires_at"]);
    if (saved[@"expires_at"] != NSNull.null && !self.taskExpiresAt) return;
    _policy.taskID = taskID.UTF8String;
    _policy.version = [saved[@"version"] unsignedLongLongValue];
    _policy.interval = interval;
    _policy.deadline = self.taskExpiresAt ? JKNow() + self.taskExpiresAt.timeIntervalSinceNow : 0;
    _policy.desiredRunning = true;
    self.taskContext = [saved[@"context"] isKindOfClass:NSDictionary.class] ? saved[@"context"] : @{};
    self.assignmentID = JKString(self.taskContext[@"assignment_id"]);
    self.assignmentVersion = [self.taskContext[@"assignment_version"] unsignedIntegerValue];
    self.startCommandID = JKString(saved[@"command_id"]);
    self.maxLongEdge = MIN(2560, MAX(320, [saved[@"max_long_edge_px"] unsignedIntegerValue]));
    self.frameSequence = [saved[@"frame_seq"] unsignedIntegerValue];
    _policy.expire(JKNow());
}

- (void)configureUploadURL:(NSURL *)URL deviceID:(NSString *)deviceID deviceToken:(NSString *)token
             assignmentID:(NSString *)assignmentID assignmentVersion:(NSUInteger)assignmentVersion {
    JKMain(^{
        [self initializeIfNeeded];
        BOOL changed = ![self.assignmentID ?: @"" isEqual:assignmentID ?: @""] ||
                       self.assignmentVersion != assignmentVersion ||
                       (self.deviceID.length && ![self.deviceID isEqual:deviceID ?: @""]);
        [self invalidatePendingWork];
        if (changed && self->_policy.desiredRunning) [self setCaptureAuthorized:NO];
        self.uploadURL = URL;
        self.deviceID = [deviceID copy];
        self.deviceToken = [token copy];
        self.assignmentID = [assignmentID copy];
        self.assignmentVersion = assignmentVersion;
    });
}

- (void)deliverFrame:(JKCapturedFrame *)frame completion:(void (^)(NSError *))completion {
    if (!frame) { completion(JKError(JKWindowCaptureIncomplete, @"没有可交付图片")); return; }
    NSUInteger deliveryTicket = self.invalidation;
    [NSNotificationCenter.defaultCenter postNotificationName:JKWindowCaptureFrameNotification object:self userInfo:@{@"frame": frame}];
    if (self.frameHandler) self.frameHandler(frame);
    if (deliveryTicket != self.invalidation || ![self eligibleWindow:nil]) {
        completion(JKError(JKWindowCaptureCanceled, @"交付期间应用或任务状态变化"));
        return;
    }
    if (!self.uploadURL) { completion(nil); return; }
    if (![self.uploadURL.scheme.lowercaseString isEqualToString:@"https"] || !self.uploadURL.host.length ||
        !self.deviceID.length || !self.deviceToken.length || self.uploadTask) {
        completion(JKError(JKWindowCaptureUploadFailed, @"缺少 HTTPS 上传配置或上一帧仍在上传"));
        return;
    }
    NSMutableDictionary *metadata = [frame.metadata mutableCopy];
    metadata[@"device_id"] = self.deviceID;
    if (self.assignmentID) metadata[@"assignment_id"] = self.assignmentID;
    metadata[@"assignment_version"] = @(self.assignmentVersion);
    NSData *JSON = [NSJSONSerialization dataWithJSONObject:metadata options:0 error:nil];
    NSString *boundary = [@"JK-" stringByAppendingString:NSUUID.UUID.UUIDString];
    NSMutableData *body = [NSMutableData new];
    void (^append)(NSString *) = ^(NSString *text) { [body appendData:[text dataUsingEncoding:NSUTF8StringEncoding]]; };
    append([NSString stringWithFormat:@"--%@\r\nContent-Disposition: form-data; name=\"metadata\"\r\nContent-Type: application/json\r\n\r\n", boundary]);
    [body appendData:JSON];
    append([NSString stringWithFormat:@"\r\n--%@\r\nContent-Disposition: form-data; name=\"image\"; filename=\"window.png\"\r\nContent-Type: image/png\r\n\r\n", boundary]);
    [body appendData:frame.PNGData];
    append([NSString stringWithFormat:@"\r\n--%@--\r\n", boundary]);
    NSMutableURLRequest *request = [NSMutableURLRequest requestWithURL:self.uploadURL];
    request.HTTPMethod = @"POST";
    request.timeoutInterval = 15;
    [request setValue:[@"Bearer " stringByAppendingString:self.deviceToken] forHTTPHeaderField:@"Authorization"];
    [request setValue:[NSString stringWithFormat:@"multipart/form-data; boundary=%@", boundary] forHTTPHeaderField:@"Content-Type"];
    if (!self.URLSession) {
        NSURLSessionConfiguration *configuration = NSURLSessionConfiguration.ephemeralSessionConfiguration;
        configuration.timeoutIntervalForResource = 20;
        configuration.HTTPShouldSetCookies = NO;
        self.URLSession = [NSURLSession sessionWithConfiguration:configuration delegate:self delegateQueue:nil];
    }
    NSUInteger ticket = self.uploadGeneration;
    self.uploadTask = [self.URLSession uploadTaskWithRequest:request fromData:body
        completionHandler:^(NSData *data, NSURLResponse *response, NSError *error) {
            dispatch_async(dispatch_get_main_queue(), ^{
                if (ticket != self.uploadGeneration) {
                    completion(JKError(JKWindowCaptureCanceled, @"上传已因状态变化取消"));
                    return;
                }
                self.uploadTask = nil;
                NSInteger status = [response isKindOfClass:NSHTTPURLResponse.class] ? ((NSHTTPURLResponse *)response).statusCode : 0;
                if (error || status < 200 || status >= 300) {
                    completion(JKError(JKWindowCaptureUploadFailed, error.localizedDescription ?: [NSString stringWithFormat:@"上传失败 HTTP %ld", (long)status]));
                } else completion(nil);
            });
        }];
    [self.uploadTask resume];
}

- (void)URLSession:(NSURLSession *)session task:(NSURLSessionTask *)task
    willPerformHTTPRedirection:(NSHTTPURLResponse *)response newRequest:(NSURLRequest *)request
    completionHandler:(void (^)(NSURLRequest * _Nullable))completionHandler {
    // Never forward a device bearer token to a redirect destination.
    completionHandler(nil);
}

@end
