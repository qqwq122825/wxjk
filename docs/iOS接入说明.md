# iOS dylib 接入说明

当前实现适用于自有或已授权 App，最低 iOS 15.6。dylib 被实际加载后，`+load` 延迟 3 秒在主线程初始化并连接内置地址 `https://wxjk.jk92.cc`，不读取旧 UserDefaults 地址，不弹框、无悬浮按钮。每次启动或从后台恢复发送一个启动标识，服务端生成有效的 GET_IMAGE 指令，自动上报一张当前 App 窗口 PNG；同一标识的断线重连不会重复创建指令。连续每 3 秒采集仍由后台开启。

## 构建和加入主项目

```sh
python3 scripts/build_ios.py
```

产物为 `build/ios/device/jk.dylib`、`JKWindowCapture.h` 和 `JKDeviceClient.h`；模拟器版本在 `build/ios/simulator`。dylib 的安装名为 `@rpath/jk.dylib`，构建脚本不会连接设备或安装包。

将真机 dylib 加入主项目链接阶段，并复制到 App 的 Frameworks 目录，按主项目身份签名；确保主项目的 Runpath Search Paths 包含 `@executable_path/Frameworks`。仅复制文件不会触发 `+load`，必须由主项目链接或实际加载。已启动进程中的初始化不会因重复调用而建立多个定时器。

原 Xcode 工程已补齐所需框架和 C++17 设置。MonkeyDev 包装默认关闭，显式设置 `JK_BUILD_PACKAGE=YES` 才执行原包装脚本，自动安装默认关闭。

## 统一服务器配置

默认服务器常量位于 `ios/jk/jk/JKDeviceClient.mm` 的 `DCDefaultServerAddress`。无需地址配置文件或本地输入；不自动重启、退出宿主 App。

需要授权测试或独立部署时，可选用宿主 `JKMonitor.plist` 或 Info.plist 的 `JKMonitor` 字典显式指定 `server_url`、`enrollment_token`。`startup_capture=false` 可关闭自动首图而保留后台指令控制；正常生产接入无需这些参数。旧版的 `prompt_for_server` 和已保存的地址不再控制启动。

首次上线请求 `/api/v1/device/register`，将安装标识、随机注册秘密、设备 ID 和设备令牌保存在当前 App 的 Keychain，按服务器地址隔离。随机注册秘密在第一次请求前保存，便于服务端安全识别同一次安装的注册重试。下次启动复用设备身份。注册准入凭据由部署策略决定，客户端不会额外弹出凭据输入框；服务端要求凭据时通过部署配置提供。

服务端需实现 [设备通信协议](设备通信协议.md) 中的注册、WSS、同步、命令结果和图片上传。没有生产服务时，连接失败会按退避策略重试，不会伪造在线状态。

主项目也可显式提供运行时配置，适合授权管理部署；不会创建任何设置页面：

```objc
#import "JKDeviceClient.h"

[[JKDeviceClient sharedClient] configureWithServerURL:serverURL
                                    enrollmentToken:enrollmentToken];
```

## 窗口选择和本地观察

默认从前台活动场景选择 App 的可见普通层级窗口，优先唯一 key window；没有 key window 但只有一个可见普通窗口时也可采集，不依赖设置弹窗或另建窗口。传统非 Scene App 使用 AppDelegate 的 window。多场景项目可在主线程设置自己的窗口提供器：

```objc
#import "JKWindowCapture.h"

[JKWindowCapture sharedCapture].windowProvider = ^UIWindow *{
    return currentSceneDelegate.window;
};
```

窗口必须可见且场景为 foregroundActive。每次采集都重新获取窗口；同一窗口中的文字、图片和控件一起绘制为 PNG，不合成其他窗口。不等待下一次 screen update；直接在目标尺寸的画布上渲染，最长边默认 1280，再在串行后台队列编码 PNG。一次只处理一帧，忙时跳过，避免堆积。App 还没有可见窗口时不生成空白图、不阻塞联网；首图指令暂存，窗口出现后重试。Metal、受保护视频等特殊渲染内容仍需单独真机验收。

主项目可监听 `JKWindowCaptureStateNotification`、`JKWindowCaptureFrameNotification` 和 `JKDeviceClientStateNotification`。回调在主线程执行；PNG 可以从通知的 `frame` 对象读取，也可设置 `frameHandler`。帧回调不应执行阻塞网络或大量磁盘操作。

## 设备自主任务

通过已认证 WSS 接收以下开始指令；本地调试也可调用采集器的 `handleCommand:completion:`。时间由服务端生成，示例 ID 均需替换：

```json
{
  "type": "command.dispatch",
  "command": "START_IMAGE_SESSION",
  "command_id": "unique-start-command",
  "expires_at": "2026-11-11T23:59:00+08:00",
  "assignment_id": "current-assignment",
  "assignment_version": 1,
  "parameters": {
    "session_id": "unique-task-id",
    "session_version": 1,
    "capture_source": "uikit_window",
    "capture_scope": "active_window",
    "interval_ms": 3000,
    "max_long_edge_px": 1280,
    "task_expires_at": null
  }
}
```

`expires_at` 是首次接收指令的期限。`task_expires_at` 是任务截止，为空表示一直到显式停止。手机保存任务后才确认接受；首张上传完成后报告运行中。网页可以关闭，手机任务不依赖网页连接。

停止指令使用同一任务 ID 和更高版本：

```json
{
  "type": "command.dispatch",
  "command": "STOP_IMAGE_SESSION",
  "command_id": "unique-stop-command",
  "expires_at": "2026-11-11T23:59:00+08:00",
  "parameters": {"session_id": "unique-task-id", "session_version": 2}
}
```

停止记录保存到本地，重复开始不会新建任务，迟到的开始不会复活已停止任务。新任务必须使用新 ID。当前实现保留最多 1024 个任务版本记录，达到上限时拒绝新任务，后续由服务端状态归档流程扩展。

## 熄屏和应用活动状态

`willResignActive`、进入后台、目标场景失去活动或受保护数据不可用时，立即失效正在编码的帧，取消当前上传，停止采集定时器；任务保持待运行。来电、控制中心等造成失去活动时也采用同样规则。

重新活动时检查窗口、授权和任务截止后继续。进程重新启动时读取已保存任务；被停止和已过期的任务不恢复。不根据连接状态猜测手机锁屏，`screen_state` 无可靠来源时为 unknown。

实时帧只允许一个采集任务和一个上传请求，不堆积历史帧。上传慢时跳过采集时机；未上传成功的实时帧直接丢弃，下一次生成新帧。输出默认最大长边 1280 像素，限制为 320 到 2560，PNG 最多 8 MB。

## WSS 和失败处理

- 握手及初次同步超时为 10 秒。
- 失败重试上限依次为 1、2、4、8、16、32、60 秒，实际延迟在 0.5 秒与该上限之间随机；完成同步后才重置失败计数。
- 每 30 秒发送应用心跳及 WebSocket ping，10 秒未收到 pong 则重连。
- 旧连接和旧注册请求的异步回调按连接代次丢弃，避免覆盖新连接。
- App 回到前台或网络恢复时立即尝试连接；进入后台关闭 WSS，保留手机任务。
- HTTP 401、403、`auth.invalid` 或 `device.disabled` 停止自动重试并终止采集，等待有效配置和服务端处理。
- 指令先保存再确认；未完成任务及未确认结果在重连后同步，不丢弃待处理结果。
- HTTPS 上传超时 15 秒，资源总超时 20 秒。TLS 使用系统验证，不接受自签名绕过，不跟随携带设备令牌的重定向。

主项目通过 `setCaptureAuthorized:NO` 可以立即结束当前任务并停止输出。设备归属改变时，上传配置更新也结束旧任务；服务端仍必须校验每次上传的设备和归属。

## 验证与范围

```sh
python3 scripts/test_ios.py
python3 scripts/test_startup_ios.py
```

测试创建独立模拟器，在自己的测试 App 中验证 UIKit PNG、生命周期暂停恢复、停止和重复指令、任务持久化、TLS 上传、首次 WSS 503、已连接后强制断线、重连不重置帧序号、旧归属指令拒绝、服务端停用、401 停止重试。临时测试根证书只安装在该专用模拟器，结束后删除模拟器，不修改主机信任。

启动首图集成测试使用独立模拟器和真实 Go 服务，通过宿主显式覆盖本地 HTTPS 地址（不连接生产），验证旧地址被忽略、延迟窗口不阻塞上线、自动首图、后台拒绝采集和恢复后的单张上报。

测试产物保存在 `build/tests`。其中 `active-window.png` 是采集器实际生成的窗口图片；各阶段 JSON 是验收结果。模拟器的非活动测试通过公开生命周期通知触发，真实手机按键熄屏和目标 App 特殊渲染内容仍需真机验收。

当前已有 [Go 服务端和总台代理网页](../server/README.md)，可与本 dylib 联调。原本地模拟服务继续用于失败场景测试。手机端定位采集、APNs、证书发布和主项目发布签名仍待接入；生产域名与真实 iPhone 需现场验收。
