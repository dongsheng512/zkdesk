package main

import (
	"embed"
	"log"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v3/pkg/application"

	"zk_mac/internal/models"
	"zk_mac/internal/service"
	"zk_mac/internal/store"
	"zk_mac/internal/worker"
)

//go:embed all:frontend/dist
var assets embed.FS

func init() {
	application.RegisterEvent[models.ProgressEvent]("zk:progress")
}

func resolveDataDir() string {
	cfgDir, err := os.UserConfigDir()
	if err != nil || cfgDir == "" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".zk_mac")
	}
	return filepath.Join(cfgDir, "zk_mac")
}

func main() {
	dataDir := resolveDataDir()
	st, err := store.New(dataDir)
	if err != nil {
		log.Fatalf("初始化数据目录失败: %v", err)
	}
	wm := worker.NewManager(dataDir)
	zkSvc := service.NewZKService(st, wm)

	app := application.New(application.Options{
		Name:        "ZKDesk",
		Description: "ZKTeco 考勤机人员同步、人脸管理与考勤拉取客户端 (macOS)",
		Services: []application.Service{
			application.NewService(zkSvc),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		OnShutdown: func() {
			wm.Shutdown()
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})
	zkSvc.AttachApp(app)

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:     "ZKDesk",
		Width:     1240,
		Height:    800,
		MinWidth:  980,
		MinHeight: 640,
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 44,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		URL: "/",
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
