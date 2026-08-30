# 威联通（QNAP）运行说明

DJ 4G Hub 可作为 Linux 串口服务运行在 QTS / QuTS hero 的原生 Linux
环境，或由 Container Station 提供的容器中。短信转发、Telegram、飞书和
自动接听逻辑均在此服务内运行；浏览器只用于配置和查看状态。

## 适用前提

- 模块已经以 Quectel 标准 USB 串口方式枚举，系统能看见 AT 端口，常见为
  `/dev/ttyUSB2`。
- 本版本**不**在 Linux 上直接处理原始 DJI `2ca3:4006` 私有 USB 接口。若
  NAS 只识别为该私有接口，请先在 Mac 上验证模块或改为能暴露标准 AT 串口的
  配置。
- 需给运行服务的账户访问 AT 端口的权限；先检查 `ls -l /dev/ttyUSB*`。

## 构建

在有 Go 1.26+ 的 Linux、Mac 或 CI 环境中，按 NAS 的 CPU 选择架构：

```sh
./scripts/build-linux.sh amd64  # 常见 Intel / AMD 威联通
./scripts/build-linux.sh arm64  # ARM64 威联通
```

生成的静态二进制位于 `dist/linux-<arch>/dj4ghub`。复制到 NAS 的共享目录，
例如 `/share/Container/dj4ghub/dj4ghub`，并赋予执行权限。

## 首次运行

请明确传入 AT 端口，避免模块的多个 USB 串口被误判：

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

## Container Station

容器必须直通对应的串口设备，例如将宿主机 `/dev/ttyUSB2` 映射到容器中同名
路径，并把 `automation.json` 所在目录挂载为可写卷。不要只映射 USB 总线而
忽略串口节点；自动接听和短信收发都需要 AT 端口。

容器内播放语音提示还需要把模块的 UAC/音频设备与 ALSA 设备一同映射，并在
自动化页面填写适合容器的“高级播放命令”。在未确认音频路由前，先关闭
“接听前尝试启用模块 USB Audio”。
