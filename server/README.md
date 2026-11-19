# 设备监控服务器和网页后台

Go 服务端和 Vue 网页由同一个进程提供。总台创建代理，新设备首先进入待分配池，下发给代理后只有相应账号能查看和操作。当前图片源是自有 iPhone App 的活动窗口 PNG。

## 本地启动

在项目根目录运行：

```sh
python3 scripts/server.py
```

打开 [本地后台](http://127.0.0.1:8080)。首次在网页创建总台管理员账号，没有预置账号或固定密码。总台创建代理后，将其初始账号、密码交给对应使用者；代理首次登录必须改密。

脚本会在需要时安装网页锁定依赖并构建前端；Go 不在 PATH 时，从官方地址下载并校验到项目 `.toolchains`，不替换系统 Go。要求 Python 3.12 或更新、Node.js 24、npm，以及支持 CGO 的 C 编译器。macOS 可使用 Xcode Command Line Tools，Linux 可使用 GCC。

默认监听 `127.0.0.1:8080`，仅本机访问。默认数据位于 `server/.data`，包含数据库、图片和 `device-token.key`。重启时保留该目录。修改源代码后重新执行启动命令即可重新构建需要更新的网页。

本地 HTTP 用于网页开发；iPhone 正式接入使用 HTTPS/WSS。当前服务器没有被发布到公网。

## 已实现的操作

| 总台 | 代理 |
| --- | --- |
| 查看全部设备和待分配池 | 查看自己的设备 |
| 创建代理、禁用账号、重置初始密码 | 首次改密和修改自己的密码 |
| 下发、批量下发、转移和回收设备 | 查看自己本次分配内的图片和位置 |
| 启用和停用设备 | 请求单张图片和启动或停止实时任务 |
| 查看任务记录和操作日志 | 查看名下设备的任务记录 |

图片和位置读取由后端逐次校验设备归属，图片目录不公开。回收或转移后，旧代理失去后续访问权限；新代理不继承旧分配期间图片。设备令牌与网页登录会话分开。

密码使用 Argon2id，Cookie 会话支持撤销，修改请求要求 CSRF Token。禁用账号或重置初始密码会撤销相应登录会话。设备 Token 由持久化密钥和安装秘密派生，数据库中只保存其摘要；相同安装重试登记返回同一设备身份。

## 独立设备详情页

点击设备名称或“查看”进入独立页面，地址为 `/#/devices/<设备ID>`，可以刷新、收藏或在新标签打开。浏览器前进、后退以及返回列表均可使用，仍由原账号权限控制。

图片区按原比例显示，支持竖屏 9:16 和横屏 16:9；提供历史缩略图、前后切换、大图查看、原始尺寸、25% 至 300% 缩放及 PNG 下载。浏览历史图片时，自动刷新不会切走当前选中的图片。回到最新后继续跟随最新画面。

位置区域预留百度地图卡片，当前不加载第三方地图；有定位数据时显示 WGS84 原始经纬度、精度和采集时间，并可复制坐标。百度地图实际展示及所需坐标转换待接入地图服务时实现。

本地测试数据可用 `python3 scripts/seed_photo_demo.py` 准备，默认下载并导入 10 张 900 × 1600（9:16）的 Unsplash 图片，提示输入本地总台密码。使用 `PHOTO_DEMO_ORIENTATION=landscape` 可切换为 1600 × 900 横屏组。脚本仅允许回环 HTTP 地址，默认账号为 `reviewadmin`，可通过 `PHOTO_DEMO_USERNAME` 指定已有本地测试总台；不创建或修改账号密码。安装身份和照片分别保存在 `build/photo-demo-portrait` 与 `build/photo-demo`，来源记录在各自的 `sources.json`。测试设备名称和备注明确标识测试数据，不代表真实手机采集。

## 手机指令和任务

WSS 地址为 `/api/v1/device/ws`，通过 Bearer 设备令牌认证，与现有 dylib 协议一致。服务器先保存指令，再通知手机；尚未确认时约每 10 秒重投，手机重连时同步未完成指令。

网页的“开始实时采集”会显示等待手机回复。手机确认接收后，任务独立运行；关闭页面或退出网页登录不会停止任务。任务可设置截止，也可持续至显式停止。设备不活动时暂停，恢复后由手机继续。设备无连接时无法立即停止本地任务，服务器保存停止指令并拒收已停止任务的图片。

同设备重复开始返回现有任务。停止记录和任务版本阻止迟到的开始消息恢复旧任务。尚未确认的旧停止指令优先下发，避免新旧任务在手机上冲突。

图片接口验证 PNG 格式、像素尺寸、分配批次、任务版本和帧序号。晚到旧帧不会覆盖最新帧，报告编号用于重复上传去重。实时任务维护最新图，每 30 秒保留一张历史，单次截图保留。图片和位置保留 7 天，运行期间每小时清理。

单张 PNG 最多 8 MB、最大尺寸 2560 × 2560；每设备图片上传上限为每 10 秒 10 次。待分配设备额外限制每日图片总量为 64 MB，待分配池最多 1000 台；登记接口有来源限流。面向受控设备部署时可设置 `ENROLLMENT_TOKEN`，由主 App 配置提供，手机不会另弹凭据输入框。

服务端已实现位置上报、查询、坐标和精度校验，并在详情展示最新坐标及地图链接。当前 dylib 尚未实现定位采集，网页会根据设备能力禁用获取位置按钮，不显示为已经具备的能力。

## 配置

| 环境变量 | 默认值或用途 |
| --- | --- |
| `LISTEN_ADDR` | `127.0.0.1:8080` |
| `DATA_DIR` | 当前运行目录的 `.data`；启动脚本在 `server` 目录运行 |
| `DATABASE_URL` | 留空用 SQLite；填写 PostgreSQL DSN 后使用 PostgreSQL |
| `SETUP_TOKEN` | 远程首次初始化的口令；纯本机首次初始化可不设置 |
| `ENROLLMENT_TOKEN` | 可选设备登记准入凭据 |
| `COOKIE_SECURE` | 通过 HTTPS 反向代理访问时设为 `true` |
| `TLS_CERT`、`TLS_KEY` | 同时填写时由 Go 直接提供 HTTPS |

PostgreSQL 示例：

```text
DATABASE_URL=postgres://monitor:YOUR_PASSWORD@db.example:5432/monitor?sslmode=require
```

保留 `DATA_DIR` 中的图片和密钥，不能只备份数据库。SQLite 已完成集成测试；PostgreSQL 驱动和建表路径已接入，尚未在真实 PostgreSQL 实例验证。当前连接管理适用于单个服务器实例，不支持多个实例共享设备 WSS 连接。

## 构建与验证

```sh
python3 scripts/server.py --build
python3 scripts/server.py --test
python3 scripts/test_server_ios.py
```

可执行文件输出到 `build/server/jk-monitor`，网页资源嵌入其中，无需另开网页进程。

Go 测试包括初始化口令、CSRF、账号权限、首次改密、安装身份、防止跨代理访问、图片回收后的授权、上传去重、乱序帧、浏览器退出不停止任务、WSS 替换和重连、任务持久化、位置校验、密码重置和禁用会话、待分配图片额度及停止指令过期重建，并启用 race 检查。

`test_server_ios.py` 创建专用模拟器，将真实 dylib 连接至真实 Go HTTPS/WSS 服务，完成设备登记、分配、单张 PNG、手机实时任务、网页登录退出后继续采集和停止确认。产物位于 `build/tests/server-ios-integration.json` 和 `server-ios-window.png`。临时测试数据和模拟器在测试后清理。

## HTTPS 部署文件

已提供 Dockerfile、Compose 和 Caddy 配置。Docker 使用分别构建网页与 Go 的阶段，最终镜像仅包含运行文件。[Docker 构建说明](https://docs.docker.com/build/building/multi-stage/)

从项目根目录准备 `server/.env`，参照 `.env.example` 填写实际域名及随机初始化口令，然后运行：

```sh
docker compose --env-file server/.env -f server/compose.yaml up --build -d
```

域名应解析到服务器，开放 80 与 443。Caddy 为域名申请证书并反向代理 HTTP 和 WSS。[Caddy 反向代理和 HTTPS 说明](https://caddyserver.com/docs/quick-starts/reverse-proxy)

在 `https://你的域名` 初始化总台，输入部署的 `SETUP_TOKEN`。iPhone 首次提示框填写同一服务器地址。Compose 将应用端口保留在内部网络，数据保存在命名卷。

本机未执行 Docker 构建或公网部署，这些配置需在实际服务器验收。部署完成后再连接真实 iPhone，验证实际锁屏、特殊渲染视图和网络切换。

## 备份

SQLite 版本先停止服务，再完整复制数据目录，包含数据库及其 WAL 文件、图片和 `device-token.key`，然后重新启动。恢复时使用同一完整目录；丢失密钥会使原设备 Token 无效。PostgreSQL 版本同时备份数据库和该目录，恢复后核对图片与报告记录。
