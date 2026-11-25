# 中控 64 位 SDK DLL 放置说明

因厂商二进制文件体积与分发授权原因，本仓库不直接包含 ZKTeco 官方 64 位 Windows SDK DLL。

在构建或运行 `ZKDesk` 前，请将中控官方 **64 位 Pull/Standalone SDK（如 v6.3.1.54）** 的全部 `.dll` 文件放置于本目录（`runtime/sdk_x64/`），至少应包含：

- `zkemkeeper.dll`
- `commpro.dll`
- `comms.dll`
- `rscagent.dll`
- `rscomm.dll`
- `tcpcomm.dll`
- `usbcomm.dll`
- `zkemsdk.dll`
- `plcommpro.dll` / `plcomms.dll` / `plrscomm.dll` / `pltcpcomm.dll` / `plusbcomm.dll`（若随 SDK 提供）

放置完成后运行 `wails3 task package`，打包脚本会自动交叉编译 `runtime/zk_worker.exe` 并将 `runtime/` 一并打入 `bin/ZKDesk.app/Contents/Resources/runtime`。
