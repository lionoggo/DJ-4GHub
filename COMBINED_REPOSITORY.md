# DJ-4GHub 合并说明

本仓库整合了两个原有工作树的当前源码状态：

- 根目录：DJ4Hub 的 Go 服务、内嵌网页、构建脚本、许可证与第三方声明。
- `mavo/`：MaVo Swift Package；其中 `GateCPromptPlayer` 负责把提示音送入模块 USB Audio/UAC 通道，`GateCCallRecorder` 负责录制来电方下行语音。

不包含任何本机运行配置、Telegram / 飞书凭据、录音、日志、构建产物或外部 Git 元数据。

开发时可分别验证：

```sh
go test ./cmd/dj4ghub-macos
cd mavo && swift build -c release --product GateCPromptPlayer --product GateCCallRecorder
```

新远程仓库地址确定后，再按需要修改 Go module 路径和文档中的发布链接。
