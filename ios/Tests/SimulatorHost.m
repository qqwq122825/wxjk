#import <UIKit/UIKit.h>
#import "JKWindowCapture.h"
#import "JKDeviceClient.h"

static NSMutableArray<NSString *> *failures;
static NSUInteger frameCount;
static BOOL restoring;
static NSString *networkURL;
static BOOL rejectAuth;
static BOOL promptTest;
static BOOL verifySavedAddress;
static BOOL realServer;

// Tests exercise the internal first-launch form without exposing a settings API.
@interface JKDeviceClient (OnboardingTests)
- (void)requestServerAddress;
- (BOOL)saveServerAddress:(NSString *)address error:(NSError **)error;
@end

static void Check(BOOL condition, NSString *message) {
    if (!condition) { [failures addObject:message]; NSLog(@"FAIL: %@", message); }
}

static NSURL *Document(NSString *name) {
    return [[[NSFileManager.defaultManager URLsForDirectory:NSDocumentDirectory inDomains:NSUserDomainMask] firstObject]
            URLByAppendingPathComponent:name];
}

static NSDictionary *Command(NSString *kind, NSString *task, NSUInteger version) {
    NSISO8601DateFormatter *formatter = [NSISO8601DateFormatter new];
    NSMutableDictionary *parameters = [@{@"session_id": task, @"session_version": @(version)} mutableCopy];
    if ([kind isEqualToString:@"START_IMAGE_SESSION"]) {
        [parameters addEntriesFromDictionary:@{@"capture_source": @"uikit_window", @"capture_scope": @"active_window",
                                               @"interval_ms": @3000, @"max_long_edge_px": @1280}];
    }
    return @{@"command": kind, @"command_id": NSUUID.UUID.UUIDString,
             @"expires_at": [formatter stringFromDate:[NSDate dateWithTimeIntervalSinceNow:120]],
             @"parameters": parameters};
}

static void Later(double seconds, dispatch_block_t block) {
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(seconds * NSEC_PER_SEC)), dispatch_get_main_queue(), block);
}

static void Finish(NSString *phase) {
    NSDictionary *result = @{@"phase": phase, @"passed": @(failures.count == 0), @"failures": failures,
                              @"frames": @(frameCount), @"client": JKDeviceClient.sharedClient.currentState,
                              @"capture": JKWindowCapture.sharedCapture.currentState};
    [[NSJSONSerialization dataWithJSONObject:result options:NSJSONWritingPrettyPrinted error:nil]
        writeToURL:Document([phase stringByAppendingPathExtension:@"json"]) atomically:YES];
    NSLog(@"JK_CAPTURE_TEST %@ %@", phase, failures.count ? @"FAILED" : @"PASSED");
    exit(failures.count ? 1 : 0);
}

static void BeginRestoreTest(void) {
    JKWindowCapture *capture = JKWindowCapture.sharedCapture;
    NSString *task = [NSString stringWithContentsOfURL:Document(@"persisted-task.txt") encoding:NSUTF8StringEncoding error:nil];
    Check([capture.currentState[@"session_id"] isEqual:task] && [capture.currentState[@"desired_running"] boolValue],
          @"phone-owned task survives process restart and delayed bootstrap");
    Later(1, ^{
        Check(frameCount > 0, @"restored foreground task produces a PNG without a viewer");
        [capture handleCommand:Command(@"STOP_IMAGE_SESSION", task, 2) completion:^(NSDictionary *result, NSError *error) {
            Check(!error && [result[@"status"] isEqual:@"stopped"], @"restored task can be stopped");
            Finish(@"phase2");
        }];
    });
}

static void BeginTaskTest(void) {
    JKWindowCapture *capture = JKWindowCapture.sharedCapture;
    NSString *task = NSUUID.UUID.UUIDString;
    NSDictionary *start = Command(@"START_IMAGE_SESSION", task, 1);
    [capture handleCommand:start completion:^(NSDictionary *result, NSError *error) {
        Check(!error && [result[@"status"] isEqual:@"accepted"], @"start accepted after persistence");
        [capture handleCommand:start completion:^(NSDictionary *duplicate, NSError *duplicateError) {
            Check(!duplicateError && [duplicate[@"duplicate"] boolValue], @"duplicate start is idempotent");
        }];
    }];
    Later(4.2, ^{
        Check(frameCount >= 2, @"three-second task captures without browser renewal");
        [NSNotificationCenter.defaultCenter postNotificationName:UIApplicationWillResignActiveNotification object:UIApplication.sharedApplication];
        NSUInteger pausedAt = frameCount;
        Later(3.5, ^{
            Check(frameCount == pausedAt, @"no new frames after resign-active notification");
            Check([capture.currentState[@"desired_running"] boolValue], @"inactive pauses but preserves the task");
            [NSNotificationCenter.defaultCenter postNotificationName:UIApplicationDidBecomeActiveNotification object:UIApplication.sharedApplication];
            Later(1, ^{
                Check(frameCount > pausedAt, @"active notification resumes the pending task");
                [capture handleCommand:Command(@"STOP_IMAGE_SESSION", task, 2) completion:^(NSDictionary *result, NSError *error) {
                    Check(!error && [result[@"status"] isEqual:@"stopped"], @"stop acknowledged");
                    NSUInteger stoppedAt = frameCount;
                    Later(3.3, ^{
                        Check(frameCount == stoppedAt, @"stopped task does not emit more PNG frames");
                        [capture handleCommand:start completion:^(NSDictionary *late, NSError *lateError) {
                            Check(lateError.code == JKWindowCaptureStaleCommand && !late, @"late start cannot revive stopped task");
                        }];
                        NSString *persisted = NSUUID.UUID.UUIDString;
                        [persisted writeToURL:Document(@"persisted-task.txt") atomically:YES encoding:NSUTF8StringEncoding error:nil];
                        [capture handleCommand:Command(@"START_IMAGE_SESSION", persisted, 1)
                            completion:^(NSDictionary *result, NSError *error) {
                                Check(!error, @"second phone-owned task persisted for restart test");
                                Finish(@"phase1");
                            }];
                    });
                }];
            });
        });
    });
}

static void BeginTests(void) {
    JKWindowCapture *capture = JKWindowCapture.sharedCapture;
    Check(![capture.currentState[@"state"] isEqual:@"not_initialized"], @"+load initialized automatically after three seconds");
    if (realServer) {
        [JKDeviceClient.sharedClient configureWithServerURL:[NSURL URLWithString:networkURL] enrollmentToken:nil];
        __block void (^poll)(void);
        poll = ^{
            if (frameCount >= 3 && ![capture.currentState[@"desired_running"] boolValue]) {
                poll = nil;
                Finish(@"server-integration");
            } else Later(0.5, poll);
        };
        Later(0.5, poll);
        Later(32, ^{
            Check(NO, @"real server did not complete screenshot, autonomous task and stop flow");
            Finish(@"server-integration");
        });
        return;
    }
    if (promptTest) {
        [JKDeviceClient.sharedClient requestServerAddress];
        Later(0.5, ^{
            UIWindowScene *scene = (UIWindowScene *)UIApplication.sharedApplication.connectedScenes.anyObject;
            UIViewController *modal = scene.windows.firstObject.rootViewController.presentedViewController;
            Check([modal isKindOfClass:UIAlertController.class], @"server configuration is a single native input alert");
            Check(((UIAlertController *)modal).textFields.count == 1, @"only one server-address field is shown");
            NSError *invalidError;
            Check(![JKDeviceClient.sharedClient saveServerAddress:@"http://localhost" error:&invalidError] && invalidError,
                  @"invalid/insecure address is not saved");
            NSError *saveError;
            NSString *bareAddress = [networkURL substringFromIndex:8];
            Check([JKDeviceClient.sharedClient saveServerAddress:bareAddress error:&saveError] && !saveError,
                  @"bare host address is normalized and saved");
            Check([[NSUserDefaults.standardUserDefaults stringForKey:@"JKMonitor.serverURL.v1"] isEqual:networkURL],
                  @"server URL is persisted in the host app");
            [JKDeviceClient.sharedClient stop];
            // The test exits immediately; flush preferences before process death.
            [NSUserDefaults.standardUserDefaults synchronize];
            Finish(@"phase5");
        });
        return;
    }
    if (verifySavedAddress) {
        Later(2, ^{
            Check([JKDeviceClient.sharedClient.currentState[@"state"] isEqual:@"authentication_required"],
                  @"next launch uses saved server URL and receives its 401 response");
            Check([JKDeviceClient.sharedClient.currentState[@"connection_attempts"] integerValue] == 1,
                  @"saved configuration starts networking automatically");
            UIWindowScene *scene = (UIWindowScene *)UIApplication.sharedApplication.connectedScenes.anyObject;
            Check(scene.windows.firstObject.rootViewController.presentedViewController == nil,
                  @"subsequent launch does not show another form or page");
            Finish(@"phase6");
        });
        return;
    }
    if (networkURL) {
        [JKDeviceClient.sharedClient configureWithServerURL:[NSURL URLWithString:networkURL] enrollmentToken:@"test-enrollment"];
        Later(rejectAuth ? 9 : 19, ^{
            NSDictionary *state = JKDeviceClient.sharedClient.currentState;
            if (rejectAuth) {
                Check([state[@"state"] isEqual:@"authentication_required"], @"HTTP 401 blocks connection retries");
                Check([state[@"connection_attempts"] integerValue] == 1, @"invalid identity is not retried repeatedly");
                Finish(@"phase4");
            } else {
                Check(frameCount >= 3, @"WSS command produces actual PNG uploads after forced disconnect");
                Check([state[@"connection_attempts"] integerValue] >= 3, @"initial 503 and dropped WSS both trigger reconnects");
                Check([state[@"state"] isEqual:@"device_disabled"], @"server disabled message stops automatic reconnect");
                Check(![capture.currentState[@"desired_running"] boolValue], @"server stop command ends autonomous task");
                Finish(@"phase3");
            }
        });
        return;
    }
    if (restoring) { BeginRestoreTest(); return; }
    [capture captureOnceWithCompletion:^(JKCapturedFrame *frame, NSError *error) {
        Check(frame && !error, @"active UIKit window renders successfully");
        const unsigned char expected[] = {137, 80, 78, 71, 13, 10, 26, 10};
        Check(frame.PNGData.length > 8 && memcmp(frame.PNGData.bytes, expected, 8) == 0, @"output has a valid PNG signature");
        Check([frame.metadata[@"capture_source"] isEqual:@"uikit_window"], @"frame declares window source");
        Check([frame.metadata[@"pixel_width"] integerValue] <= 1280 && [frame.metadata[@"pixel_height"] integerValue] <= 1280,
              @"configured maximum output size respected");
        [frame.PNGData writeToURL:Document(@"active-window.png") atomically:YES];
        // Rendering is synchronous, PNG encoding is asynchronous. Invalidation
        // in between must discard the encoded image rather than deliver it.
        [capture captureOnceWithCompletion:^(JKCapturedFrame *late, NSError *lateError) {
            Check(!late && lateError.code == JKWindowCaptureCanceled, @"frame encoded across deactivation is discarded");
            [capture captureOnceWithCompletion:^(JKCapturedFrame *inactive, NSError *inactiveError) {
                Check(!inactive && inactiveError.code == JKWindowCaptureWaitingForeground, @"one-shot rejects inactive app");
                [NSNotificationCenter.defaultCenter postNotificationName:UIApplicationDidBecomeActiveNotification object:UIApplication.sharedApplication];
                BeginTaskTest();
            }];
        }];
        [NSNotificationCenter.defaultCenter postNotificationName:UIApplicationWillResignActiveNotification object:UIApplication.sharedApplication];
    }];
}

@interface HostSceneDelegate : UIResponder <UIWindowSceneDelegate>
@property(nonatomic) UIWindow *window;
@end

@implementation HostSceneDelegate
- (void)scene:(UIScene *)scene willConnectToSession:(UISceneSession *)session options:(UISceneConnectionOptions *)options {
    self.window = [[UIWindow alloc] initWithWindowScene:(UIWindowScene *)scene];
    UIViewController *controller = [UIViewController new];
    controller.view.backgroundColor = [UIColor colorWithRed:0.08 green:0.15 blue:0.25 alpha:1];
    UILabel *label = [[UILabel alloc] initWithFrame:CGRectMake(24, 130, 340, 180)];
    label.text = @"JK UIKit Window\nOwn App Capture\nPNG · 3 second task";
    label.numberOfLines = 0;
    label.font = [UIFont boldSystemFontOfSize:26];
    label.textColor = UIColor.whiteColor;
    [controller.view addSubview:label];
    UIView *card = [[UIView alloc] initWithFrame:CGRectMake(24, 360, 280, 160)];
    card.backgroundColor = [UIColor colorWithRed:0.2 green:0.75 blue:0.6 alpha:1];
    card.layer.cornerRadius = 20;
    [controller.view addSubview:card];
    self.window.rootViewController = controller;
    [self.window makeKeyAndVisible];
}
@end

@interface HostAppDelegate : UIResponder <UIApplicationDelegate>
@end
@implementation HostAppDelegate
- (BOOL)application:(UIApplication *)application didFinishLaunchingWithOptions:(NSDictionary *)options {
    failures = [NSMutableArray new];
    restoring = [NSProcessInfo.processInfo.arguments containsObject:@"--verify-restored"];
    NSArray *arguments = NSProcessInfo.processInfo.arguments;
    NSUInteger URLIndex = [arguments indexOfObject:@"--network-url"];
    if (URLIndex != NSNotFound && URLIndex + 1 < arguments.count) networkURL = arguments[URLIndex + 1];
    rejectAuth = [arguments containsObject:@"--reject-auth"];
    promptTest = [arguments containsObject:@"--prompt-test"];
    verifySavedAddress = [arguments containsObject:@"--verify-saved-address"];
    realServer = [arguments containsObject:@"--real-server"];
    if (networkURL) {
        [NSNotificationCenter.defaultCenter addObserverForName:JKDeviceClientStateNotification object:nil
            queue:NSOperationQueue.mainQueue usingBlock:^(NSNotification *note) { NSLog(@"JK_NETWORK %@", note.userInfo); }];
    }
    JKWindowCapture.sharedCapture.frameHandler = ^(JKCapturedFrame *frame) {
        frameCount++;
        if (frameCount == 1) [frame.PNGData writeToURL:Document(@"active-window.png") atomically:YES];
        Check(NSThread.isMainThread, @"frame callback is on main thread");
        Check(frame.PNGData.length > 0, @"task frame is not empty");
    };
    Later(3.8, ^{ BeginTests(); });
    Later(40, ^{ Check(NO, @"test timeout"); Finish(restoring ? @"phase2" : @"phase1"); });
    return YES;
}
@end

int main(int argc, char *argv[]) {
    @autoreleasepool { return UIApplicationMain(argc, argv, nil, NSStringFromClass(HostAppDelegate.class)); }
}
