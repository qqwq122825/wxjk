# iOS dylib 接入说明

当前实现适用于自有 App，最低 iOS 15.6。dylib 被实际加载后，`+load` 延迟 3 秒在主线程初始化窗口采集器和设备客户端。首次没有保存地址时，只显示一个原生服务器地址输入框；没有设置页、修改入口或其他界面。地址保存后，后续启动静默连接。

## 构建和加入主项目

```sh
python3 scripts/build_ios.py
```

产物为 `build/ios/device/jk.dylib`、`JKWindowCapture.h` 和 `JKDeviceClient.h`；模拟器版本在 `build/ios/simulator`。dylib 的安装名为 `@rpath/jk.dylib`，构建脚本不会连接设备或安装包。

将真机 dylib 加入主项目链接阶段，并复制到 App 的 Frameworks 目录，按主项目身份签名；确保主项目的 Runpath Search Paths 包含 `@executable_path/Frameworks`。仅复制文件不会触发 `+load`，必须由主项目链接或实际加载。已启动进程中的初始化不会因重复调用而建立多个定时器。

原 Xcode 工程已补齐所需框架和 C++17 设置。MonkeyDev 包装默认关闭，显式设置 `JK_BUILD_PACKAGE=YES` 才执行原包装脚本，自动安装默认关闭。

## 统一服务器配置

默认由首次输入框获取地址，支持输入 HTTPS URL 或省略协议的域名与端口，省略时补为 HTTPS。确认保存后写入当前 App 的 UserDefaults，后续直接读取；无效地址不能提交。选择稍后设置时不保存，下次启动仍显示这一个输入框。

连接失败仅更新内部状态并自动重试，不反复弹框。没有修改地址的用户入口。图片采集、上传、重连和指令处理均静默运行。

可选的 `JKMonitor.plist` 用于部署参数，例如注册准入凭据和首次输入框的预填地址。示例在 `ios/Examples/JKMonitor.example.plist`。需要无交互管理部署时可显式设置 `prompt_for_server=false`，以配置中的地址直接连接；正常接入保持默认即可。

首次上线请求 `/api/v1/device/register`，将安装标识、随机注册秘密、设备 ID 和设备令牌保存在当前 App 的 Keychain，按服务器地址隔离。随机注册秘密在第一次请求前保存，便于服务端安全识别同一次安装的注册重试。下次启动复用设备身份。注册准入凭据由部署策略决定，客户端不会额外弹出凭据输入框；服务端要求凭据时通过部署配置提供。

服务端需实现 [设备通信协议](设备通信协议.md) 中的注册、WSS、同步、命令结果和图片上传。没有生产服务时，连接失败会按退避策略重试，不会伪造在线状态。

主项目也可显式提供运行时配置，适合自动管理部署；这不会创建任何设置页面：

```objc
#import "JKDeviceClient.h"

[[JKDeviceClient sharedClient] configureWithServerURL:serverURL
                                    enrollmentToken:enrollmentToken];
```

## 窗口选择和本地观察

默认从前台活动场景选择唯一 key window。传统非 Scene App 可使用其 AppDelegate 的 key window。多场景项目可在主线程设置自己的窗口提供器：

```objc
#import "JKWindowCapture.h"

[JKWindowCapture sharedCapture].windowProvider = ^UIWindow *{
    return currentSceneDelegate.window;
};
```

窗口必须可见且场景为 foregroundActive。每次采集都重新获取窗口；同一窗口中的文字、图片和控件一起绘制为 PNG，不合成其他窗口。

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
```

测试创建独立模拟器，在自己的测试 App 中验证 UIKit PNG、生命周期暂停恢复、停止和重复指令、任务持久化、TLS 上传、首次 WSS 503、已连接后强制断线、重连不重置帧序号、旧归属指令拒绝、服务端停用、401 停止重试、首次地址输入框和地址跨进程保存。临时测试根证书只安装在该专用模拟器，结束后删除模拟器，不修改主机信任。

测试产物保存在 `build/tests`。其中 `active-window.png` 是采集器实际生成的窗口图片；各阶段 JSON 是验收结果。模拟器的非活动测试通过公开生命周期通知触发，真实手机按键熄屏和目标 App 特殊渲染内容仍需真机验收。

当前已有 [Go 服务端和总台代理网页](../server/README.md)，可与本 dylib 联调。原本地模拟服务继续用于失败场景测试。手机端定位采集、APNs、证书发布和主项目发布签名仍待接入；生产域名与真实 iPhone 需现场验收。
