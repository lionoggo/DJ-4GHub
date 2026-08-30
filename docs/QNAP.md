# 威联通（QNAP）运行说明

DJ 4G Hub 可作为 Linux 服务运行在 QTS / QuTS hero 的原生 Linux 环境，或
由 Container Station 提供的容器中。短信转发、Telegram、飞书和自动接听逻辑
均在此服务内运行；浏览器只用于配置和查看状态。

## 适用前提

- 标准 Quectel AT 串口：系统能看见 `/dev/ttyUSB*` 或 `/dev/ttyACM*`，建议
  显式传入 AT 端口。
- DJI/Baiwang 私有接口：当前模块常显示为 `2ca3:4006`，没有 `ttyUSB` 节点。
  本仓库的 Linux 原生 USB 传输会直接访问此接口，不依赖 Linux libusb；在
  Container Station 中仍需要 USB 设备权限，首次运行必须实机验证。
- TS-464C2 使用 `linux/amd64` 镜像。其他机型必须改用与 NAS CPU 匹配的镜像。

## 构建

在装有 Go 1.26+ 的构建机中，按 NAS 的 CPU 选择架构：

```sh
./scripts/build-linux.sh amd64  # 常见 Intel / AMD 威联通
./scripts/build-linux.sh arm64  # ARM64 威联通
```

生成的二进制位于 `dist/linux-<arch>/dj4ghub`。复制到 NAS 的共享目录，
例如 `/share/Container/dj4ghub/dj4ghub`，并赋予执行权限。

该脚本使用原生 Linux USB 传输，因此可在 macOS 上直接交叉构建 `linux/amd64`
静态二进制，不需要 QNAP 本机安装 Go、libusb 或编译工具。

## 首次运行

对于标准串口模块，请明确传入 AT 端口，避免模块的多个 USB 串口被误判：

```sh
/share/Container/dj4ghub/dj4ghub \
  -port /dev/ttyUSB2 \
  -listen 127.0.0.1:7575 \
  -automation-config /share/Container/dj4ghub/automation.json
```

配置文件会以仅拥有者可读写的权限保存，其中含 Telegram Bot Token、飞书
Webhook 与签名密钥。请把它放在受限共享目录中且不要提交到 Git。

默认只监听 NAS 本机。不要直接把控制台暴露到公网；若需远程访问，请使用
VPN、SSH 隧道，或由 NAS 的已认证反向代理提供保护。

对于 DJI/Baiwang 私有接口，模块的 `/dev/bus/usb/...` 节点通常仅允许 QTS
管理员访问。免容器方式可先验证私有 AT、实体 SIM、短信与自动接听控制；来电
提示音、录音和中文语音合成仍推荐下文的 Container Station 运行时，因为它提供
ALSA、eSpeak NG 和 ffmpeg。

## Container Station

TS-464C2 推荐从仓库根目录使用 `packaging/qnap/docker-compose.yml` 构建。该
配置会以原生 USB 访问当前模块的私有 `2ca3:4006` 接口，并将运行配置、录音目录
挂载到 `/share/Container/dj4ghub`。容器仅发布到 NAS 本机的 `127.0.0.1:7575`；
若要远程访问，请在 QTS 的已认证反向代理或 VPN 后使用。

```sh
mkdir -p /share/Container/dj4ghub
docker compose -f packaging/qnap/docker-compose.yml up -d --build
```

容器启动后，先检查日志中是否出现 `USB AT · 2ca3:4006`，再配置短信与自动
接听规则。若没有出现，请停止容器，不要反复切换模块的 USB 模式。

对于标准串口模块，可以不使用特权容器，仅映射对应的 `/dev/ttyUSB2` 等设备到
容器，并通过 `-port` 明确指定。私有 USB 接口没有串口节点，不能只映射
`/dev/ttyUSB*`。

## 来电提示音与录音（Linux ALSA 预览）

容器镜像带有 `aplay`、`arecord`、`ffmpeg` 与中文 eSpeak NG。模块在通话中必须
枚举出对应的 UAC 音频设备；在容器内先运行 `aplay -l` 与 `arecord -l`，确认模块
播放与采集设备后，再在“自动化”页面填写 ALSA 名称，例如 `plughw:1,0`。

启用时按以下顺序验证：先开启“接听前尝试启用模块 USB Audio”并只测试提示音；
确认来电方能听到声音后，再开启录音。采集设备留空时会沿用播放设备。设备编号会
随 USB 插拔变化，因此需要在每次重新枚举后确认。该 UAC 路由仍需在 TS-464C2
实机完成验证。
