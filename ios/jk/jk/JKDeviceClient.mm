#import "JKDeviceClient.h"
#import "JKWindowCapture.h"
#import "JKReconnectPolicy.hpp"
#import <CommonCrypto/CommonDigest.h>
#import <Network/Network.h>
#import <Security/Security.h>

NSString * const JKDeviceClientStateNotification = @"JKDeviceClientStateNotification";
static NSString * const DCDefaultServerAddress = @"https://wxjk.jk92.cc";

static NSString *DCString(id value) { return [value isKindOfClass:NSString.class] ? value : nil; }
static void DCMain(dispatch_block_t block) {
    if (NSThread.isMainThread) block(); else dispatch_async(dispatch_get_main_queue(), block);
}
static NSDate *DCDate(id value) {
    if (!DCString(value)) return nil;
    NSISO8601DateFormatter *formatter = [NSISO8601DateFormatter new];
    NSDate *date = [formatter dateFromString:value];
    if (!date) { formatter.formatOptions |= NSISO8601DateFormatWithFractionalSeconds; date = [formatter dateFromString:value]; }
    return date;
}
static NSString *DCHash(NSString *value) {
    NSData *data = [value dataUsingEncoding:NSUTF8StringEncoding];
    unsigned char digest[CC_SHA256_DIGEST_LENGTH];
    CC_SHA256(data.bytes, (CC_LONG)data.length, digest);
    NSMutableString *result = [NSMutableString new];
    for (NSUInteger i = 0; i < sizeof(digest); i++) [result appendFormat:@"%02x", digest[i]];
    return result;
}

static NSURL *DCServerURL(NSString *input) {
    NSString *value = [input stringByTrimmingCharactersInSet:NSCharacterSet.whitespaceAndNewlineCharacterSet];
    if (!value.length) return nil;
    if (![value containsString:@"://"]) value = [@"https://" stringByAppendingString:value];
    NSURLComponents *parts = [NSURLComponents componentsWithString:value];
    if (![parts.scheme.lowercaseString isEqualToString:@"https"] || !parts.host.length || parts.user.length ||
        parts.password.length || parts.query.length || parts.fragment.length) return nil;
    parts.scheme = @"https";
    if ([parts.path isEqualToString:@"/"]) parts.path = @"";
    if (parts.port && (parts.port.integerValue <= 0 || parts.port.integerValue > 65535)) return nil;
    return parts.URL;
}

@interface JKDeviceClient () <NSURLSessionWebSocketDelegate> {
    jk::ReconnectPolicy _retry;
}
@property(nonatomic) NSURL *serverURL;
@property(nonatomic) NSString *enrollmentToken;
@property(nonatomic) NSMutableDictionary *identity;
@property(nonatomic) NSString *identityAccount;
@property(nonatomic) NSURL *journalURL;
@property(nonatomic) NSMutableDictionary<NSString *, NSMutableDictionary *> *journal;
@property(nonatomic) NSMutableSet<NSString *> *executing;
@property(nonatomic) NSURLSession *session;
@property(nonatomic) NSURLSessionDataTask *registrationTask;
@property(nonatomic) NSURLSessionWebSocketTask *socket;
@property(nonatomic) dispatch_source_t heartbeat;
@property(nonatomic) dispatch_block_t retryBlock;
@property(nonatomic) nw_path_monitor_t pathMonitor;
@property(nonatomic) NSMutableArray *observers;
@property(nonatomic) BOOL running;
@property(nonatomic) BOOL foreground;
@property(nonatomic) BOOL socketOpen;
@property(nonatomic) BOOL synced;
@property(nonatomic) BOOL drainScheduled;
@property(nonatomic) BOOL initialized;
@property(nonatomic) NSUInteger generation;
@property(nonatomic) NSUInteger attempts;
@property(nonatomic) NSUInteger registrationGeneration;
@property(nonatomic) NSString *connectionID;
@property(nonatomic) NSDictionary *lastState;
@property(nonatomic) NSString *startupCaptureID;
@property(nonatomic) BOOL startupCaptureEnabled;
@property(nonatomic) NSString *currentAssignmentID;
@property(nonatomic) NSUInteger currentAssignmentVersion;
@end

@implementation JKDeviceClient

+ (instancetype)sharedClient {
    static JKDeviceClient *client;
    static dispatch_once_t once;
    dispatch_once(&once, ^{ client = [JKDeviceClient new]; });
    return client;
}

- (instancetype)init {
    if ((self = [super init])) {
        _identity = [NSMutableDictionary new];
        _journal = [NSMutableDictionary new];
        _executing = [NSMutableSet new];
        _observers = [NSMutableArray new];
    }
    return self;
}

- (void)initializeIfNeeded {
    if (self.initialized) return;
    self.initialized = YES;
    self.foreground = UIApplication.sharedApplication.applicationState == UIApplicationStateActive;
    NSURLSessionConfiguration *configuration = NSURLSessionConfiguration.ephemeralSessionConfiguration;
    configuration.timeoutIntervalForRequest = 15;
    configuration.timeoutIntervalForResource = 20;
    configuration.HTTPShouldSetCookies = NO;
    self.session = [NSURLSession sessionWithConfiguration:configuration delegate:self delegateQueue:NSOperationQueue.mainQueue];
    __weak JKDeviceClient *weakSelf = self;
    for (NSString *name in @[UIApplicationDidBecomeActiveNotification, UIApplicationDidEnterBackgroundNotification,
                              UIWindowDidBecomeKeyNotification, UISceneDidActivateNotification,
                              JKWindowCaptureStateNotification]) {
        [self.observers addObject:[NSNotificationCenter.defaultCenter addObserverForName:name object:nil
            queue:NSOperationQueue.mainQueue usingBlock:^(NSNotification *note) {
                JKDeviceClient *client = weakSelf;
                if (!client) return;
                if ([note.name isEqualToString:UIApplicationDidBecomeActiveNotification]) {
                    if (!client.foreground) client.startupCaptureID = NSUUID.UUID.UUIDString;
                    client.foreground = YES;
                    [client cancelRetry];
                    [client connectIfNeeded];
                    [client scheduleDrain];
                } else if ([note.name isEqualToString:UIApplicationDidEnterBackgroundNotification]) {
                    client.foreground = NO;
                    [client disconnect];
                    [client publish:@"waiting_foreground" reason:@"application_background" delay:0];
                } else if (client.synced) {
                    if ([note.name isEqualToString:JKWindowCaptureStateNotification])
                        [client send:@{@"type": @"device.capture_state", @"capture": note.userInfo ?: @{}}];
                }
            }]];
    }
    self.pathMonitor = nw_path_monitor_create();
    nw_path_monitor_set_update_handler(self.pathMonitor, ^(nw_path_t path) {
        JKDeviceClient *client = weakSelf;
        if (nw_path_get_status(path) == nw_path_status_satisfied && client.running && client.foreground && !client.socket) {
            [client cancelRetry];
            [client connectIfNeeded];
        }
    });
    nw_path_monitor_set_queue(self.pathMonitor, dispatch_get_main_queue());
    nw_path_monitor_start(self.pathMonitor);
}

- (void)startFromMainBundleConfiguration {
    DCMain(^{
        [self initializeIfNeeded];
        if (self.serverURL) return;
        NSURL *file = [NSBundle.mainBundle URLForResource:@"JKMonitor" withExtension:@"plist"];
        NSDictionary *configuration = file ? [NSDictionary dictionaryWithContentsOfURL:file error:nil] : nil;
        if (!configuration) configuration = [NSBundle.mainBundle objectForInfoDictionaryKey:@"JKMonitor"];
        if (![configuration isKindOfClass:NSDictionary.class]) configuration = @{};
        // Production needs no plist or saved address. Explicit host overrides are for authorized testing/deployment.
        NSString *configured = DCString(configuration[@"server_url"]);
        NSURL *URL = DCServerURL(configured.length ? configured : DCDefaultServerAddress);
        if (!URL) { [self publish:@"configuration_error" reason:@"invalid_server_url" delay:0]; return; }
        self.startupCaptureEnabled = !configuration[@"startup_capture"] || [configuration[@"startup_capture"] boolValue];
        self.startupCaptureID = NSUUID.UUID.UUIDString;
        [self configureWithServerURL:URL enrollmentToken:DCString(configuration[@"enrollment_token"])];
    });
}

- (void)configureWithServerURL:(NSURL *)URL enrollmentToken:(NSString *)token {
    DCMain(^{
        [self initializeIfNeeded];
        if (![URL.scheme.lowercaseString isEqualToString:@"https"] || !URL.host.length ||
            URL.user.length || URL.password.length || URL.query.length || URL.fragment.length) {
            [self publish:@"configuration_error" reason:@"https_server_url_required" delay:0];
            return;
        }
        [self disconnect];
        BOOL changed = self.serverURL && ![self.serverURL.absoluteString isEqual:URL.absoluteString];
        if (changed) [JKWindowCapture.sharedCapture setCaptureAuthorized:NO];
        self.serverURL = URL;
        self.enrollmentToken = [token copy];
        self.identityAccount = URL.absoluteString;
        NSURL *support = [[NSFileManager.defaultManager URLsForDirectory:NSApplicationSupportDirectory inDomains:NSUserDomainMask] firstObject];
        self.journalURL = [[support URLByAppendingPathComponent:@"JKMonitor" isDirectory:YES]
            URLByAppendingPathComponent:[NSString stringWithFormat:@"commands-%@.json", DCHash(URL.absoluteString)]];
        [self loadIdentity];
        [self loadJournal];
        self.running = YES;
        [self connectIfNeeded];
    });
}

- (NSDictionary *)keychainQuery {
    return @{(__bridge id)kSecClass: (__bridge id)kSecClassGenericPassword,
             (__bridge id)kSecAttrService: [@"JKMonitor." stringByAppendingString:NSBundle.mainBundle.bundleIdentifier ?: @"app"],
             (__bridge id)kSecAttrAccount: self.identityAccount};
}

- (void)loadIdentity {
    NSMutableDictionary *query = [[self keychainQuery] mutableCopy];
    query[(__bridge id)kSecReturnData] = @YES;
    CFTypeRef result = NULL;
    OSStatus status = SecItemCopyMatching((__bridge CFDictionaryRef)query, &result);
    NSData *data = status == errSecSuccess ? CFBridgingRelease(result) : nil;
    id raw = data ? [NSJSONSerialization JSONObjectWithData:data options:NSJSONReadingMutableContainers error:nil] : nil;
    self.identity = [raw isKindOfClass:NSDictionary.class] ? raw : [NSMutableDictionary new];
    if (!DCString(self.identity[@"installation_id"]).length) self.identity[@"installation_id"] = NSUUID.UUID.UUIDString;
    if (!DCString(self.identity[@"registration_secret"]).length) {
        uint8_t bytes[32];
        if (SecRandomCopyBytes(kSecRandomDefault, sizeof(bytes), bytes) == errSecSuccess)
            self.identity[@"registration_secret"] = [[NSData dataWithBytes:bytes length:sizeof(bytes)] base64EncodedStringWithOptions:0];
    }
}

- (BOOL)saveIdentity {
    NSData *data = [NSJSONSerialization dataWithJSONObject:self.identity options:0 error:nil];
    NSMutableDictionary *query = [[self keychainQuery] mutableCopy];
    NSDictionary *attributes = @{(__bridge id)kSecValueData: data,
                                (__bridge id)kSecAttrAccessible: (__bridge id)kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly};
    OSStatus status = SecItemUpdate((__bridge CFDictionaryRef)query, (__bridge CFDictionaryRef)attributes);
    if (status == errSecItemNotFound) {
        [query addEntriesFromDictionary:attributes];
        status = SecItemAdd((__bridge CFDictionaryRef)query, NULL);
    }
    return status == errSecSuccess;
}

- (NSURL *)endpoint:(NSString *)path {
    return [self.serverURL URLByAppendingPathComponent:[@"api/v1/device/" stringByAppendingString:path]];
}

- (void)connectIfNeeded {
    if (!self.running || !self.foreground || self.socket || self.registrationTask) return;
    if (!DCString(self.identity[@"device_token"]).length) { [self registerDevice]; return; }
    [self cancelRetry];
    NSUInteger generation = ++self.generation;
    self.attempts += 1;
    NSURLComponents *components = [NSURLComponents componentsWithURL:[self endpoint:@"ws"] resolvingAgainstBaseURL:NO];
    components.scheme = @"wss";
    NSMutableURLRequest *request = [NSMutableURLRequest requestWithURL:components.URL];
    request.timeoutInterval = 10;
    [request setValue:[@"Bearer " stringByAppendingString:self.identity[@"device_token"]] forHTTPHeaderField:@"Authorization"];
    self.socket = [self.session webSocketTaskWithRequest:request];
    self.socket.maximumMessageSize = 64 * 1024;
    [self publish:@"connecting" reason:@"opening_wss" delay:0];
    [self.socket resume];
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 10 * NSEC_PER_SEC), dispatch_get_main_queue(), ^{
        if (generation == self.generation && !self.synced && self.socket)
            [self retryAfterFailure:@"connection_or_sync_timeout"];
    });
}

- (void)registerDevice {
    if (!DCString(self.identity[@"registration_secret"]).length) {
        self.running = NO;
        [self publish:@"storage_error" reason:@"registration_secret_unavailable" delay:0];
        return;
    }
    if (![self saveIdentity]) {
        self.running = NO;
        [self publish:@"storage_error" reason:@"keychain_write_failed" delay:0];
        return;
    }
    NSMutableURLRequest *request = [NSMutableURLRequest requestWithURL:[self endpoint:@"register"]];
    request.HTTPMethod = @"POST";
    [request setValue:@"application/json" forHTTPHeaderField:@"Content-Type"];
    NSMutableDictionary *registration = [@{
        @"installation_id": self.identity[@"installation_id"], @"registration_secret": self.identity[@"registration_secret"],
        @"platform": @"ios", @"os_version": UIDevice.currentDevice.systemVersion, @"client_version": @"0.3.0",
        @"capabilities": @{@"capture_sources": @[@"uikit_window"], @"capture_scopes": @[@"active_window"], @"min_image_interval_ms": @500,
                            @"location": @NO, @"location_push": @NO}
    } mutableCopy];
    if (self.enrollmentToken.length) registration[@"enrollment_token"] = self.enrollmentToken;
    request.HTTPBody = [NSJSONSerialization dataWithJSONObject:registration options:0 error:nil];
    NSUInteger generation = ++self.registrationGeneration;
    [self publish:@"registering" reason:@"first_device_registration" delay:0];
    self.registrationTask = [self.session dataTaskWithRequest:request completionHandler:^(NSData *data, NSURLResponse *response, NSError *error) {
        DCMain(^{
            if (generation != self.registrationGeneration || !self.running) return;
            self.registrationTask = nil;
            NSInteger status = [response isKindOfClass:NSHTTPURLResponse.class] ? ((NSHTTPURLResponse *)response).statusCode : 0;
            if (status == 401 || status == 403) { [self blockForAuthorization:status]; return; }
            if (error || status < 200 || status >= 300) { [self retryAfterFailure:@"registration_failed"]; return; }
            id result = data.length <= 64 * 1024 ? [NSJSONSerialization JSONObjectWithData:data options:0 error:nil] : nil;
            if (![result isKindOfClass:NSDictionary.class] || !DCString(result[@"device_id"]).length ||
                !DCString(result[@"device_token"]).length) {
                [self retryAfterFailure:@"invalid_registration_response"];
                return;
            }
            self.identity[@"device_id"] = result[@"device_id"];
            self.identity[@"device_token"] = result[@"device_token"];
            if (![self saveIdentity]) {
                self.running = NO;
                [self publish:@"storage_error" reason:@"device_identity_not_saved" delay:0];
                return;
            }
            [self connectIfNeeded];
        });
    }];
    [self.registrationTask resume];
}

- (void)URLSession:(NSURLSession *)session webSocketTask:(NSURLSessionWebSocketTask *)task
    didOpenWithProtocol:(NSString *)protocol {
    if (task != self.socket || !self.running) return;
    self.socketOpen = YES;
    [self publish:@"synchronizing" reason:@"wss_open" delay:0];
    [self receive:task generation:self.generation];
    NSMutableArray *pending = [NSMutableArray new];
    for (NSString *key in self.journal) [pending addObject:@{@"command_id": key, @"status": self.journal[key][@"status"] ?: @"received"}];
    [self send:@{@"type": @"device.hello", @"device_id": self.identity[@"device_id"],
                 @"client_version": @"0.3.0", @"app_state": @"foreground", @"pending_commands": pending,
                 @"startup_capture_id": self.startupCaptureEnabled ? (self.startupCaptureID ?: @"") : @"",
                 @"capture": JKWindowCapture.sharedCapture.currentState}];
}

- (void)receive:(NSURLSessionWebSocketTask *)task generation:(NSUInteger)generation {
    [task receiveMessageWithCompletionHandler:^(NSURLSessionWebSocketMessage *message, NSError *error) {
        DCMain(^{
            if (generation != self.generation || task != self.socket) return;
            if (error) {
                NSInteger status = [task.response isKindOfClass:NSHTTPURLResponse.class] ? ((NSHTTPURLResponse *)task.response).statusCode : 0;
                if (status == 401 || status == 403) [self blockForAuthorization:status];
                else [self retryAfterFailure:@"wss_receive_failed"];
                return;
            }
            NSData *data = message.type == NSURLSessionWebSocketMessageTypeString
                ? [message.string dataUsingEncoding:NSUTF8StringEncoding] : message.data;
            id value = data.length <= 64 * 1024 ? [NSJSONSerialization JSONObjectWithData:data options:0 error:nil] : nil;
            if (![value isKindOfClass:NSDictionary.class]) { [self retryAfterFailure:@"invalid_wss_message"]; return; }
            [self handleMessage:value];
            if (generation == self.generation && task == self.socket) [self receive:task generation:generation];
        });
    }];
}

- (void)handleMessage:(NSDictionary *)message {
    NSString *type = DCString(message[@"type"]);
    if ([type isEqualToString:@"device.welcome"]) {
        self.connectionID = DCString(message[@"connection_id"]);
    } else if ([type isEqualToString:@"device.disabled"] || [type isEqualToString:@"auth.invalid"]) {
        [self blockForAuthorization:[type isEqualToString:@"device.disabled"] ? 403 : 401];
    } else if ([type isEqualToString:@"device.sync"]) {
        NSDictionary *assignment = [message[@"assignment"] isKindOfClass:NSDictionary.class] ? message[@"assignment"] : nil;
        if (!assignment || ![assignment[@"version"] isKindOfClass:NSNumber.class] ||
            ![message[@"commands"] isKindOfClass:NSArray.class]) {
            [self retryAfterFailure:@"invalid_sync_response"];
            return;
        }
        NSString *assignmentID = DCString(assignment[@"id"]);
        NSUInteger revision = [assignment[@"version"] unsignedIntegerValue];
        self.currentAssignmentID = assignmentID;
        self.currentAssignmentVersion = revision;
        [JKWindowCapture.sharedCapture configureUploadURL:[self endpoint:@"images"] deviceID:self.identity[@"device_id"]
            deviceToken:self.identity[@"device_token"] assignmentID:assignmentID assignmentVersion:revision];
        [JKWindowCapture.sharedCapture setCaptureAuthorized:YES];
        self.synced = YES;
        self->_retry.synchronized();
        [self publish:@"connected" reason:@"device_synchronized" delay:0];
        [self startHeartbeat];
        for (id command in message[@"commands"]) {
            if ([command isKindOfClass:NSDictionary.class]) [self acceptCommand:command];
        }
        for (NSString *key in self.journal) {
            NSDictionary *entry = self.journal[key];
            if (entry[@"result"] && ![entry[@"result_acked"] boolValue]) [self send:entry[@"result"]];
        }
        [self scheduleDrain];
    } else if ([type isEqualToString:@"command.dispatch"] && self.synced) {
        [self acceptCommand:message];
    } else if ([type isEqualToString:@"command.result_ack"]) {
        NSString *key = DCString(message[@"command_id"]);
        if (key && self.journal[key]) {
            self.journal[key][@"result_acked"] = @YES;
            [self saveJournal];
        }
    }
}

- (void)startHeartbeat {
    if (self.heartbeat) return;
    self.heartbeat = dispatch_source_create(DISPATCH_SOURCE_TYPE_TIMER, 0, 0, dispatch_get_main_queue());
    dispatch_source_set_timer(self.heartbeat, DISPATCH_TIME_NOW, 30 * NSEC_PER_SEC, 2 * NSEC_PER_SEC);
    __weak JKDeviceClient *weakSelf = self;
    dispatch_source_set_event_handler(self.heartbeat, ^{
        JKDeviceClient *client = weakSelf;
        if (!client.synced || !client.socket) return;
        [client send:@{@"type": @"device.heartbeat", @"message_id": NSUUID.UUID.UUIDString,
                       @"connection_id": client.connectionID ?: @"", @"app_state": @"foreground",
                       @"screen_state": @"unknown", @"capture": JKWindowCapture.sharedCapture.currentState}];
        NSUInteger generation = client.generation;
        __block BOOL pong = NO;
        [client.socket sendPingWithPongReceiveHandler:^(NSError *error) {
            DCMain(^{
                if (generation != client.generation) return;
                pong = error == nil;
                if (error) [client retryAfterFailure:@"heartbeat_failed"];
            });
        }];
        dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 10 * NSEC_PER_SEC), dispatch_get_main_queue(), ^{
            if (generation == client.generation && client.synced && !pong) [client retryAfterFailure:@"heartbeat_timeout"];
        });
    });
    dispatch_resume(self.heartbeat);
}

- (void)send:(NSDictionary *)object {
    if (!self.socket || !self.socketOpen) return;
    NSData *data = [NSJSONSerialization dataWithJSONObject:object options:0 error:nil];
    if (!data) return;
    NSUInteger generation = self.generation;
    NSURLSessionWebSocketMessage *message = [[NSURLSessionWebSocketMessage alloc] initWithString:[[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding]];
    [self.socket sendMessage:message completionHandler:^(NSError *error) {
        DCMain(^{ if (error && generation == self.generation) [self retryAfterFailure:@"wss_send_failed"]; });
    }];
}

- (void)acceptCommand:(NSDictionary *)command {
    NSString *key = DCString(command[@"command_id"]);
    NSDate *expiry = DCDate(command[@"expires_at"]);
    if (!key.length || key.length > 200 || !DCString(command[@"command"]).length || !expiry) return;
    NSMutableDictionary *existing = self.journal[key];
    if (existing) {
        // A duplicate ID must identify exactly the same business request.
        if (![existing[@"command"] isEqual:command]) return;
        [self send:@{@"type": @"command.ack", @"command_id": key, @"status": @"received"}];
        if (existing[@"result"]) [self send:existing[@"result"]];
        return;
    }
    [self pruneJournal];
    if (self.journal.count >= 256) {
        [self send:@{@"type": @"command.progress", @"command_id": key, @"status": @"busy", @"reason": @"journal_full"}];
        return;
    }
    self.journal[key] = [@{@"command": command, @"status": @"received"} mutableCopy];
    if (![self saveJournal]) { [self.journal removeObjectForKey:key]; return; }
    [self send:@{@"type": @"command.ack", @"command_id": key, @"status": @"received"}];
    [self scheduleDrain];
}

- (void)scheduleDrain {
    if (self.drainScheduled) return;
    self.drainScheduled = YES;
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, NSEC_PER_SEC), dispatch_get_main_queue(), ^{
        self.drainScheduled = NO;
        [self drain];
    });
}

- (void)drain {
    if (!self.running || !self.synced || !self.foreground) return;
    for (NSString *key in self.journal.allKeys) {
        NSMutableDictionary *entry = self.journal[key];
        if (entry[@"result"] || [self.executing containsObject:key]) continue;
        NSDictionary *command = entry[@"command"];
        NSString *kind = DCString(command[@"command"]);
        if (![kind isEqualToString:@"STOP_IMAGE_SESSION"] &&
            (![DCString(command[@"assignment_id"]) ?: @"" isEqual:self.currentAssignmentID ?: @""] ||
             ![command[@"assignment_version"] isKindOfClass:NSNumber.class] ||
             [command[@"assignment_version"] unsignedIntegerValue] != self.currentAssignmentVersion)) {
            [self finishCommand:key result:@{@"status": @"canceled", @"reason": @"assignment_changed"}];
            continue;
        }
        if (DCDate(command[@"expires_at"]).timeIntervalSinceNow <= 0) {
            [self finishCommand:key result:@{@"status": @"expired"}];
            continue;
        }
        [self.executing addObject:key];
        [JKWindowCapture.sharedCapture handleCommand:command completion:^(NSDictionary *result, NSError *error) {
            [self.executing removeObject:key];
            if (self.journal[key] != entry) return;
            if (error.code == JKWindowCaptureWaitingForeground || error.code == JKWindowCaptureBusy ||
                error.code == JKWindowCaptureNoWindow || error.code == JKWindowCaptureIncomplete) {
                entry[@"status"] = @"waiting_foreground";
                [self saveJournal];
                [self send:@{@"type": @"command.progress", @"command_id": key, @"status": @"waiting_foreground"}];
                [self scheduleDrain];
                return;
            }
            if (error) [self finishCommand:key result:@{@"status": @"failed", @"error_code": @(error.code),
                                                       @"error_message": error.localizedDescription}];
            else [self finishCommand:key result:result];
        }];
    }
}

- (void)finishCommand:(NSString *)key result:(NSDictionary *)result {
    if (!self.journal[key]) return;
    NSMutableDictionary *receipt = [result mutableCopy];
    receipt[@"type"] = @"command.result";
    receipt[@"command_id"] = key;
    receipt[@"result_id"] = NSUUID.UUID.UUIDString;
    self.journal[key][@"result"] = receipt;
    self.journal[key][@"status"] = @"finished";
    if ([self saveJournal]) [self send:receipt];
}

- (void)loadJournal {
    NSData *data = [NSData dataWithContentsOfURL:self.journalURL];
    id raw = data ? [NSJSONSerialization JSONObjectWithData:data options:NSJSONReadingMutableContainers error:nil] : nil;
    self.journal = [raw isKindOfClass:NSDictionary.class] && [raw count] <= 256 ? raw : [NSMutableDictionary new];
    [self.executing removeAllObjects];
    [self pruneJournal];
}

- (void)pruneJournal {
    for (NSString *key in self.journal.allKeys) {
        NSDictionary *entry = self.journal[key];
        NSDate *expiry = DCDate(entry[@"command"][@"expires_at"]);
        if (entry[@"result"] && expiry && expiry.timeIntervalSinceNow < -86400) [self.journal removeObjectForKey:key];
    }
}

- (BOOL)saveJournal {
    NSData *data = [NSJSONSerialization dataWithJSONObject:self.journal options:0 error:nil];
    NSURL *directory = [self.journalURL URLByDeletingLastPathComponent];
    BOOL ok = data && directory && [NSFileManager.defaultManager createDirectoryAtURL:directory
        withIntermediateDirectories:YES attributes:@{NSFileProtectionKey: NSFileProtectionCompleteUntilFirstUserAuthentication}
        error:nil] && [data writeToURL:self.journalURL options:NSDataWritingAtomic error:nil];
    if (ok) [self.journalURL setResourceValue:@YES forKey:NSURLIsExcludedFromBackupKey error:nil];
    else [self publish:@"storage_error" reason:@"command_journal_write_failed" delay:0];
    return ok;
}

- (void)retryAfterFailure:(NSString *)reason {
    [self disconnect];
    if (!self.running || !self.foreground) return;
    double delay = _retry.nextDelay((double)arc4random_uniform(1000000) / 1000000);
    [self publish:@"retrying" reason:reason delay:delay];
    NSUInteger generation = self.generation;
    __weak JKDeviceClient *weakSelf = self;
    self.retryBlock = dispatch_block_create((dispatch_block_flags_t)0, ^{
        JKDeviceClient *client = weakSelf;
        if (!client || generation != client.generation) return;
        client.retryBlock = nil;
        [client connectIfNeeded];
    });
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(delay * NSEC_PER_SEC)), dispatch_get_main_queue(), self.retryBlock);
}

- (void)cancelRetry {
    if (self.retryBlock) { dispatch_block_cancel(self.retryBlock); self.retryBlock = nil; }
}

- (void)disconnect {
    [self cancelRetry];
    self.generation += 1;
    self.registrationGeneration += 1;
    [self.registrationTask cancel];
    self.registrationTask = nil;
    NSURLSessionWebSocketTask *socket = self.socket;
    self.socket = nil;
    self.socketOpen = NO;
    self.synced = NO;
    [socket cancelWithCloseCode:NSURLSessionWebSocketCloseCodeGoingAway reason:nil];
    if (self.heartbeat) { dispatch_source_cancel(self.heartbeat); self.heartbeat = nil; }
}

- (void)blockForAuthorization:(NSInteger)status {
    self.running = NO;
    [self disconnect];
    [JKWindowCapture.sharedCapture setCaptureAuthorized:NO];
    [self publish:status == 403 ? @"device_disabled" : @"authentication_required" reason:@"server_rejected_identity" delay:0];
}

- (void)stop {
    DCMain(^{ self.running = NO; [self disconnect]; [self publish:@"stopped" reason:@"client_stopped" delay:0]; });
}

- (void)URLSession:(NSURLSession *)session task:(NSURLSessionTask *)task didCompleteWithError:(NSError *)error {
    if (task != self.socket || !self.running) return;
    NSInteger status = [task.response isKindOfClass:NSHTTPURLResponse.class] ? ((NSHTTPURLResponse *)task.response).statusCode : 0;
    if (status == 401 || status == 403) [self blockForAuthorization:status];
    else [self retryAfterFailure:@"wss_closed"];
}

- (void)URLSession:(NSURLSession *)session webSocketTask:(NSURLSessionWebSocketTask *)task
    didCloseWithCode:(NSURLSessionWebSocketCloseCode)closeCode reason:(NSData *)reason {
    if (task == self.socket && self.running) [self retryAfterFailure:@"peer_closed_wss"];
}

- (void)URLSession:(NSURLSession *)session task:(NSURLSessionTask *)task
    willPerformHTTPRedirection:(NSHTTPURLResponse *)response newRequest:(NSURLRequest *)request
    completionHandler:(void (^)(NSURLRequest * _Nullable))completionHandler {
    completionHandler(nil);
}

- (void)publish:(NSString *)state reason:(NSString *)reason delay:(double)delay {
    self.lastState = @{@"state": state, @"reason": reason, @"retry_delay_seconds": @(delay),
                        @"connection_attempts": @(self.attempts), @"retry_failures": @(_retry.failures)};
    [NSNotificationCenter.defaultCenter postNotificationName:JKDeviceClientStateNotification object:self userInfo:self.lastState];
}

- (NSDictionary *)currentState {
    __block NSDictionary *value;
    dispatch_block_t read = ^{ value = self.lastState ?: @{@"state": @"not_initialized"}; };
    if (NSThread.isMainThread) read(); else dispatch_sync(dispatch_get_main_queue(), read);
    return value;
}
@end
