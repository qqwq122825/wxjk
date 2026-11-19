#import <Foundation/Foundation.h>
NS_ASSUME_NONNULL_BEGIN
// Local-only rolling diagnostics. Never pass tokens, payloads, PNG data or coordinates.
FOUNDATION_EXPORT NSString *JKMonitorDebugLogPath(void);
FOUNDATION_EXPORT void JKMonitorDebugLog(NSString *event, NSString *format, ...) NS_FORMAT_FUNCTION(2, 3);
NS_ASSUME_NONNULL_END
