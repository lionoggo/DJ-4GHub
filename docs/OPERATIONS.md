# 运行与通知配置手册

本手册记录 DJ 4G Hub 在 macOS 与威联通 NAS 上的可复现运行流程。它以
DJI/Baiwang `2ca3:4006` 模块为验证对象；不涉及刷写模块固件。

> [!IMPORTANT]
> 控制台可以读取短信、管理 SIM，并且能够配置自动接听。只应部署在受信任的
> 设备和局域网中，绝不能将 7575 端口直接暴露到公网。对外访问请使用 VPN 或
> NAS 已认证的 HTTPS 反向代理。

## 1. 选择运行位置

| 场景 | 推荐方式 | 控制台地址 |
| --- | --- | --- |
| 临时使用或调试 | macOS 本地服务 | `http://127.0.0.1:7575/` |
| 24 小时短信、来电与录音服务 | QNAP Container Station | NAS 局域网地址或受保护的反向代理 |

macOS 与 NAS 可以使用同一仓库，但不要同时把同一个模块连接到两台机器。消息
凭据、自动化规则和录音都保存在运行服务的机器上，彼此不会自动同步。

## 2. macOS 快速启动

1. 用可传输数据的 USB-C 线连接模块。
2. 在仓库根目录构建并启动：

   ```sh
   ./scripts/build-macos.sh
   ./dist/dj4ghub start
   ```

3. 打开 `http://127.0.0.1:7575/`，确认“设备在线”、SIM 已插入和运营商已注册。

如需使用发行包，按根目录 [README](../README.md) 的安装步骤执行。模块切到
“上网模式”后会重新枚举；配置短信和来电自动化时应保持“短信模式”。

## 3. QNAP Container Station 部署

完整兼容性说明、镜像构建和 USB 权限要求见 [QNAP.md](QNAP.md)。TS-464C2 为
`linux/amd64`，可直接使用仓库的 QNAP Compose 配置。

```sh
git clone https://github.com/lionoggo/DJ-4GHub.git
cd DJ-4GHub
mkdir -p /share/Container/dj4ghub
docker compose -f packaging/qnap/docker-compose.yml up -d --build
```

启动后检查：

```sh
docker compose -f packaging/qnap/docker-compose.yml ps
docker compose -f packaging/qnap/docker-compose.yml logs --tail=100 dj4ghub
```

日志应包含 `USB AT · 2ca3:4006`，并且健康检查接口应返回 `"ok":true`。默认
Compose 仅监听 NAS 本机回环地址，这是安全默认值。要从局域网访问，请优先通过
QTS 已认证反向代理或 VPN 发布服务。

仅在受信任局域网内需要直接打开控制台时，给 Compose 明确传入 NAS 的固定 LAN
地址；不要使用 `0.0.0.0`，并在 NAS 防火墙中限制来源网段：

```sh
DJ4GHUB_BIND=192.168.1.20 \
  docker compose -f packaging/qnap/docker-compose.yml up -d --build
```

上例中的地址必须替换为该 NAS 自己的固定局域网地址。随后从同一局域网打开
`http://192.168.1.20:7575/`。

运行数据都位于 `/share/Container/dj4ghub`：

| 内容 | 路径 | 处理方式 |
| --- | --- | --- |
| 自动化与消息凭据 | `automation.json` | 仅服务账号可读；不要提交到 Git 或发送给他人 |
| 来电录音 | `recordings/` | 含个人语音；按实际需求备份与清理 |
| 服务日志 | Container Station 日志 | 对外提交前脱敏号码、验证码与设备标识 |

更新代码后重新构建并重启即可；先备份上述目录：

```sh
docker compose -f packaging/qnap/docker-compose.yml up -d --build
```

## 4. 一次性配置通知

进入控制台的“自动化”页面。配置由服务立即保存，网页不会回显 Bot Token、App
Secret 或 Webhook 签名密钥。

### Telegram

1. 启用 Telegram 短信转发。
2. 填入 Bot Token 与一个或多个 Chat ID。
3. 保存后向模块号码发送一条测试短信。

新短信会以文本转发。启用“录制来电方语音”和“将录音转发到 Telegram”后，通话
结束时会把 WAV 录音作为附件发送。

### 飞书企业应用机器人私聊

此方式不需要飞书群。使用已发布的企业自建应用，并在应用后台完成：

1. 启用机器人能力，并将目标用户纳入应用可见范围。
2. 申请并发布 `im:message`、`im:message:send_as_bot` 权限。
3. 在控制台选择“企业应用机器人私聊”，填写 App ID、App Secret、收件人类型
   （推荐 `email`）和收件人。
4. 普通飞书租户的 API 地址保持 `https://open.feishu.cn`。
5. 保存后向模块号码发送一条测试短信，确认私聊机器人收到通知。

若租户开发者后台为该应用指定了其他 API 域名，应以后台页面为准；不要把 App
Secret 发送给未确认的第三方域名。

### 飞书群机器人

选择“群机器人 Webhook”，填入群机器人 Webhook；如群机器人启用签名，再填入
签名密钥。此方式只适合群通知，不能发送给指定个人。

## 5. 自动接听、提示音与录音

推荐先稳定短信与通知，再开启来电自动化：

1. 在“自动化”启用来电规则；白名单留空表示所有号码。
2. 设置接听延迟（已验证的起点为 10 秒）和提示语文本。
3. 启用“录制来电方语音”，确认适用的录音与保存规则。
4. 首先完成一次来电测试：确认自动接听、来电方能听到提示语、挂断后出现 WAV
   录音。
5. 最后启用 Telegram 录音附件转发，并再次测试。

QNAP 上提示音与录音依赖模块实际通话音频路由。服务会优先走模块内语音桥接；
ALSA/UAC 设备仅作为兼容回退。不要将电脑扬声器能否听到声音视为来电方是否能
听到提示音的判据。

## 6. 验收清单

每次新部署、更新镜像或重新插拔模块后，按顺序验证：

1. 概览页显示设备在线、SIM 已插入、运营商和 LTE 信号正常。
2. 发送一条测试短信，确认 Telegram 与飞书私聊均收到文本。
3. 拨打一通测试电话，确认约定延迟后自动接听且来电方能听见完整提示语。
4. 挂断后确认“通话”页面出现录音，下载或播放检查内容。
5. 确认 Telegram 收到 WAV 附件（如已开启录音转发）。
6. 重启容器一次，再复查状态和自动化配置是否仍在。

## 7. 常见问题

| 现象 | 首先检查 |
| --- | --- |
| NAS 页面无法从另一台设备打开 | Compose 默认只绑定 NAS 本机；使用 VPN/反向代理，或检查受控的局域网监听与防火墙 |
| 页面在线但短信未转发 | 模块是否处于短信模式、SIM 是否注册、通知开关及目标配置是否已保存 |
| 飞书私聊无消息 | 应用是否已发布、机器人是否启用、用户是否在可见范围、收件人类型和值是否匹配 |
| 自动接听但无提示音 | 先看来电方而非 NAS 扬声器；检查模块语音桥接日志，再检查 UAC/ALSA 设备 |
| 有提示音但无录音 | 确认已启用录音、来电已挂断、`recordings/` 可写且模块通话音频路由正常 |
| 上网模式后设备短暂离线 | USB 重新枚举正常；等待模块回到目标模式后再刷新 |

## 8. 当前通知能力与路线图

| 内容 | Telegram | 飞书私聊 |
| --- | --- | --- |
| 新短信文本 | 已支持 | 已支持 |
| 通话录音 WAV 附件 | 已支持 | 计划中 |

飞书录音转发可通过“上传文件 → 获得 `file_key` → 发送文件消息”实现。该流程还需
为飞书应用增加文件资源权限（通常为 `im:resource`），并对单个文件大小、失败重
试和隐私保留策略做约束。详见飞书的[上传文件接口](https://open.feishu.cn/document/server-docs/im-v1/file/create)
与[发送消息接口](https://open.feishu.cn/document/server-docs/im-v1/message/create)。
