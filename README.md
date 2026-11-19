# iPhone 设备监控后台

设备统一连接服务器，新设备进入总台待分配列表。总台创建代理账号并下发设备，代理只查看和操作自己名下的设备。

自有 App 加载 dylib，`+load` 延迟 3 秒自动初始化。dylib 将当前活动的 `UIWindow` 渲染为 PNG；收到实时任务后由手机每 3 秒运行，网页关闭不停止任务。App 不活动时暂停，恢复后继续未过期任务。

生产地址已内置为 `https://wxjk.jk92.cc`，不读取旧地址，不显示输入框或悬浮窗。App 实际加载 dylib 后 3 秒自动上线，并请求服务端生成一条有效的首图指令；首张自动上报，连续每 3 秒采集仍由后台开启。连接不依赖窗口，截图仅针对 App 当前可见、活动的 UIKit 窗口；熄屏、后台时暂停。

已实现 Go 服务端、Vue 总台与代理网页，以及 iOS 端窗口采集、任务持久化、设备注册、Keychain 身份、WSS 心跳和退避重连、指令回执及 HTTPS PNG 上传。服务端具备位置上报和查询，iOS 定位采集适配器仍待接入。

启动服务器和网页：

```sh
python3 scripts/server.py
```

打开 [本地后台](http://127.0.0.1:8080)，首次创建总台账号。[服务器启动和部署说明](server/README.md)

```sh
python3 scripts/build_ios.py
python3 scripts/test_ios.py
python3 scripts/test_startup_ios.py
```

真机产物：`build/ios/device/jk.dylib`。接入步骤、配置和运行状态见 [iOS dylib 接入说明](docs/iOS接入说明.md)。

本地设计文档：

- [后台和客户端总体方案](docs/监控后台设计方案.md)：账号权限、设备分配、心跳、指令队列、实时图片会话、定位、数据库和实施顺序。
- [设备通信协议](docs/设备通信协议.md)：注册、WSS、心跳、指令确认、图片和定位上报、重连以及错误码。
- [iOS 内部图片采集方案](docs/iOS内部图片采集方案.md)：自有 App 加载 dylib、活动窗口选择、UIKit 渲染及后台暂停与恢复。

`ios/jk` 保留原动态库工程，最低支持 iOS 15.6。新增独立构建脚本供自有 App 嵌入，产物使用 `@rpath/jk.dylib`。默认构建不执行 MonkeyDev 打包或安装；需要原包装流程时显式设置 `JK_BUILD_PACKAGE=YES`。
