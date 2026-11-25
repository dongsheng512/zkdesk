# ZKDesk

基于 **Go + Wails v3 + React (TypeScript)** 构建的 macOS 原生中控（ZKTeco）考勤机桌面管理工具。

通过内置 **Wine 11 (Rosetta 2) + 纯 Go 交叉编译 64 位 Windows COM Worker (`cmd/zk-worker` → `zk_worker.exe`) + 中控官方 64 位 SDK (`zkemkeeper.dll`)**，无需 Windows 虚拟机或 Python 运行时，即可在 macOS 上直连中控新旧固件考勤机（含 2024/2025 新固件加密通讯与 `WithMask` 考勤协议）。

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

1. 前往本仓库 [Releases](https://github.com/dongsheng512/zkdesk/releases) 下载最新 `ZKDesk-*-macos-*.zip` 并解压，将 `ZKDesk.app` 拖入「应用程序」文件夹。
2. 安装并首次放行 **Wine Stable**（`/Applications/Wine Stable.app` 或 `brew install --cask --no-quarantine wine-stable`；Apple Silicon 需已安装 Rosetta 2）。
3. 因安装包使用本地 ad-hoc 签名，首次打开如遇 macOS 提示“无法验证开发者”，可执行一次放行命令后再打开：
   ```bash
   xattr -dr com.apple.quarantine /Applications/ZKDesk.app
   ```

## 从源码构建前提

- macOS 12+（Apple Silicon 需已安装 Rosetta 2）
- 已安装并放行 **Wine Stable**（`/Applications/Wine Stable.app` 或 `/opt/homebrew/bin/wine`）
- Go 1.24+、Node.js 20+、[Wails v3 CLI](https://v3alpha.wails.io/)
- 中控 64 位 SDK DLL 已预置于 [runtime/sdk_x64/](runtime/sdk_x64/README.md)，克隆后可直接构建打包

## 开发、测试与打包

```bash
# 运行全部后端单元测试（含竞态检测）
go test -race -v ./internal/...

# 校验 Windows 64 位 Worker 交叉编译
GOOS=windows GOARCH=amd64 go vet ./cmd/zk-worker/...

# 开发模式启动（热重载）
wails3 dev -config ./build/config.yml

# 生产构建并打包签名 macOS .app（自动编译 zk_worker.exe 与前端）
wails3 task package
open bin/ZKDesk.app
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
