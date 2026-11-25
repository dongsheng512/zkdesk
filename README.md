# ZKDesk

基于 **Go + Wails v3 + React (TypeScript)** 构建的跨平台（**macOS & Windows 64 位**）中控（ZKTeco）考勤机桌面管理工具。

- **Windows 64 位原生版**：直接原生运行纯 Go 64 位 COM Worker (`zk_worker.exe`) 调用中控官方 64 位 SDK (`zkemkeeper.dll`)，**无需 Wine、无需虚拟机、解压仅 ~15 MB**。
- **macOS 原生版**：通过内置精简 **Wine 11 (Rosetta 2) + 纯 Go 64 位 Worker + 中控官方 64 位 SDK**，无需 Windows 虚拟机或 Python 运行时，即可在 macOS 上直连中控新旧固件考勤机（含 2024/2025 新固件加密通讯与 `WithMask` 考勤协议）。

## 核心功能

1. **考勤设备总览**
   - 管理多台考勤机配置，支持手动连接/断开、实时状态探测、序列号/固件/容量查看，以及设备时钟偏差监测与一键校时。
2. **本地优先人员管理**
   - 以本地人员主库（`master_users.json`）为基准，各考勤机保持独立名单；
   - 支持**本地人员库**、**人员比对**、**单机名单**三视图切换，按需手动勾选考勤机执行拉取、下发、离职残留清理；
   - 支持 CSV 部门/在职状态导入、主库 JSON/Excel 备份恢复，所有写入/删除操作前自动留存安全备份。
3. **人脸照片库（按需懒加载）**
   - 列表默认隐藏照片并按需懒加载本地缓存，兼顾隐私与性能；
   - 支持单人或批量下载可见光人脸原图、单张/文件夹按工号批量上传（自动规范缩放为基线 JPEG），以及手动选择目标考勤机同步或删除人脸。
4. **考勤数据累计库与导出**
   - 支持弹窗勾选已连接考勤机增量拉取并自动合并去重到本地累计库，支持按日期清理本地历史缓存（不改动考勤机原数据）；
   - 支持原始打卡流水与每日首末打卡汇总视图，支持导出 **Excel (`.xlsx` 双表)**、**CSV**、**ZKTime GBK `.txt`** 以及双向兼容 **ZKTime `.abt`（LH5 压缩）** 导入导出。
5. **操作审计与诊断日志**
   - 持久化记录所有同步、写入、校时、删除、拉取与清理历史；Worker 异常输出自动滚动写入 `~/Library/Application Support/zk_mac/logs/worker.log`。

## 直接安装（推荐普通用户）

前往本仓库 [Releases](https://github.com/dongsheng512/zkdesk/releases) 按系统选择下载：

### Windows（64 位原生版，无需 Wine）
1. 下载 `ZKDesk-*-windows-amd64.zip`（约 11 MB）并解压到固定目录。
2. 首次使用前，右键目录中的 `register-sdk-admin.bat` 选择 **「以管理员身份运行」** 完成 64 位 `zkemkeeper.dll` 一次性注册（需 Win10/11 64 位，内置 Edge WebView2）。
3. 之后直接双击 `ZKDesk.exe` 即可使用。

### macOS（Apple Silicon）
1. 下载对应版本：
   - **独立开箱版（推荐）**：`ZKDesk-*-macos-arm64-standalone.zip`（约 88 MB，已内置精简 Wine 11 + 中控 64 位 SDK，**无需额外安装 Wine**；需已安装 Rosetta 2）。
   - **轻量版**：`ZKDesk-*-macos-arm64.zip`（约 14 MB，适合本机已安装 `Wine Stable.app` 的用户）。
2. 解压后将 `ZKDesk.app` 拖入「应用程序」文件夹。
3. 因安装包使用本地 ad-hoc 签名，首次打开如遇 macOS 提示“无法验证开发者”，请在终端执行一次放行命令后再打开：
   ```bash
   xattr -dr com.apple.quarantine /Applications/ZKDesk.app
   ```

## 从源码构建前提

- macOS 12+ 或 Windows 10/11 64 位
- Go 1.24+、Node.js 20+、[Wails v3 CLI](https://v3alpha.wails.io/)
- 打包 macOS 版时需本机已安装 `Wine Stable.app`；打包 Windows 64 位便携版可在 macOS 上直接交叉编译，无需 Windows 机器或 Docker
- 中控 64 位 SDK DLL 已预置于 [runtime/sdk_x64/](runtime/sdk_x64/README.md)，克隆后可直接构建打包

## 开发、测试与打包

```bash
# 运行全部后端单元测试（含竞态检测）
go test -race -v ./internal/...

# 校验 Windows 64 位主程序与 Worker 交叉编译
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go vet ./... ./cmd/zk-worker/...

# 开发模式启动（热重载）
wails3 dev -config ./build/config.yml

# 打包 macOS 独立开箱版（内置精简 Wine 11 + 64 位 SDK）
wails3 task package:standalone

# 打包 macOS 轻量版（依赖本机已安装的 Wine）
wails3 task package

# 交叉编译打包 Windows 64 位原生便携版（无需 Wine）
wails3 task package:windows
```

首次启动时，应用会自动在 `~/Library/Application Support/zk_mac/wine64` 初始化独立 Wine 环境并静默注册 64 位 `zkemkeeper.dll`（约需 10 秒，之后常驻复用）。

## 项目结构

```text
.
├── main.go                     # Wails v3 桌面应用入口
├── cmd/zk-worker/              # 纯 Go 64 位 Windows COM Worker（交叉编译为 zk_worker.exe）
├── internal/
│   ├── models/                 # 前后端共享数据模型
│   ├── worker/                 # Wine Worker 进程托管、超时保护与滚动日志
│   ├── store/                  # 本地 JSON 原子持久化、权限加固与乱码自动修复
│   ├── service/                # 按业务域拆分的后端服务（设备/人员/人脸/考勤）
│   └── exporter/               # Excel / CSV / ZKTime TXT / ABT(LH5) / 人脸图片规范化
├── frontend/                   # React + TypeScript + Vite 前端界面
└── runtime/sdk_x64/            # 放置中控官方 64 位 SDK DLL（见目录内 README）
```
