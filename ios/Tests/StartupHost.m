#import <UIKit/UIKit.h>
#import "JKDeviceClient.h"
#import "JKWindowCapture.h"
static UIWindow *hostWindow;
static NSMutableArray *failures;
static NSDate *started,*connected,*windowReady;
static NSUInteger frames;
static BOOL inactiveRejected;
static void Later(double seconds, dispatch_block_t block) { dispatch_after(dispatch_time(DISPATCH_TIME_NOW,(int64_t)(seconds*NSEC_PER_SEC)),dispatch_get_main_queue(),block); }
static NSURL *Document(NSString *name) {return [[[NSFileManager.defaultManager URLsForDirectory:NSDocumentDirectory inDomains:NSUserDomainMask] firstObject] URLByAppendingPathComponent:name];}
static void Finish(void) {
 if(frames!=2)[failures addObject:@"expected one automatic image per app foreground, exactly two across two activations"];
 if(!connected || !windowReady || [connected compare:windowReady]!=NSOrderedAscending)[failures addObject:@"online connection waited for delayed host window"];
 if(!inactiveRejected)[failures addObject:@"inactive capture was not rejected"];
 if(hostWindow.rootViewController.presentedViewController)[failures addObject:@"unexpected address popup"];
 for(UIView *view in hostWindow.subviews)if([view.accessibilityIdentifier isEqual:@"JKMonitorConfigureServer"])[failures addObject:@"unexpected floating settings UI"];
 NSDictionary *result=@{@"passed":@(failures.count==0),@"failures":failures,@"frames":@(frames),@"connected_before_window":@(connected && windowReady && [connected compare:windowReady]==NSOrderedAscending),@"connected_after_seconds":connected?@([connected timeIntervalSinceDate:started]):@(-1),@"window_ready_after_seconds":windowReady?@([windowReady timeIntervalSinceDate:started]):@(-1),@"inactive_capture_rejected":@(inactiveRejected),@"client":JKDeviceClient.sharedClient.currentState};
 NSData *data=[NSJSONSerialization dataWithJSONObject:result options:NSJSONWritingPrettyPrinted error:nil];[data writeToURL:Document(@"builtin-startup.json") atomically:YES];NSLog(@"BUILTIN_STARTUP %@",[[NSString alloc]initWithData:data encoding:NSUTF8StringEncoding]);exit(failures.count?1:0);
}
@interface StartupSceneDelegate:UIResponder<UIWindowSceneDelegate>
@property(nonatomic)UIWindow *window;
@end
@implementation StartupSceneDelegate
- (void)scene:(UIScene *)scene willConnectToSession:(UISceneSession *)session options:(UISceneConnectionOptions *)options {
 self.window=[[UIWindow alloc]initWithWindowScene:(UIWindowScene *)scene];hostWindow=self.window;
 UIViewController *controller=[UIViewController new];controller.view.backgroundColor=[UIColor colorWithRed:0.08 green:0.17 blue:0.14 alpha:1];
 UILabel *label=[[UILabel alloc]initWithFrame:CGRectMake(24,150,340,200)];label.text=@"JK 自有 App 窗口\n启动 3 秒自动上线\n启动首张 JPG 自动上报\n连续采集由后台开启";label.numberOfLines=0;label.textColor=UIColor.whiteColor;label.font=[UIFont boldSystemFontOfSize:23];[controller.view addSubview:label];
 UIView *card=[[UIView alloc]initWithFrame:CGRectMake(24,390,300,180)];card.backgroundColor=[UIColor colorWithRed:0.4 green:0.8 blue:0.6 alpha:1];card.layer.cornerRadius=24;[controller.view addSubview:card];
 self.window.rootViewController=controller;
 Later(5,^{windowReady=NSDate.date;[self.window makeKeyAndVisible];});
}
@end
@interface StartupAppDelegate:UIResponder<UIApplicationDelegate>
@end
@implementation StartupAppDelegate
- (BOOL)application:(UIApplication *)application didFinishLaunchingWithOptions:(NSDictionary *)options {
 failures=[NSMutableArray new];started=NSDate.date;
 // A stale saved URL must not override the built-in/default host configuration.
 [NSUserDefaults.standardUserDefaults setObject:@"https://obsolete.invalid" forKey:@"JKMonitor.serverURL.v1"];
 [NSNotificationCenter.defaultCenter addObserverForName:JKDeviceClientStateNotification object:nil queue:NSOperationQueue.mainQueue usingBlock:^(NSNotification *note){if(!connected && [note.userInfo[@"state"] isEqual:@"connected"])connected=NSDate.date;}];
 JKWindowCapture.sharedCapture.frameHandler=^(JKCapturedFrame *frame){
  frames++;NSData *data=frame.imageData;
  UIImage *decoded=[UIImage imageWithData:data];
  if(data.length<4 || memcmp(data.bytes,"\xff\xd8\xff",3)!=0 || !decoded.CGImage)[failures addObject:@"startup frame is not a complete decodable JPEG"];
  [data writeToURL:Document(@"startup-window.jpg") atomically:YES];
  if(frames==1)Later(1,^{
   [NSNotificationCenter.defaultCenter postNotificationName:UIApplicationWillResignActiveNotification object:application];
   [JKWindowCapture.sharedCapture captureOnceWithCompletion:^(JKCapturedFrame *ignored,NSError *error){inactiveRejected=!ignored && error.code==JKWindowCaptureWaitingForeground;}];
   [NSNotificationCenter.defaultCenter postNotificationName:UIApplicationDidEnterBackgroundNotification object:application];
   Later(2,^{[NSNotificationCenter.defaultCenter postNotificationName:UIApplicationDidBecomeActiveNotification object:application];});
  });
  if(frames==2)Later(1,^{Finish();});
 };
 Later(19,^{Finish();});return YES;
}
@end
int main(int argc,char **argv){@autoreleasepool{return UIApplicationMain(argc,argv,nil,NSStringFromClass(StartupAppDelegate.class));}}
