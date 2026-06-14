package main

import (
	"embed"
	"runtime"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var appIcon []byte

//go:embed wails.json
var wailsConfig []byte

func main() {
	// Create an instance of the app structure
	app := NewApp()
	info := appInfo()

	// Create application with options
	err := wails.Run(&options.App{
		Title:  info.Name,
		Width:  1360,
		Height: 900,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 246, G: 244, B: 239, A: 1},
		Menu:             applicationMenu(app, info.Name),
		Mac: &mac.Options{
			About: &mac.AboutInfo{
				Title:   info.Name,
				Message: "Version " + info.Version + "\n\n" + info.Disclaimer,
				Icon:    appIcon,
			},
		},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}

func applicationMenu(app *App, name string) *menu.Menu {
	if runtime.GOOS == "darwin" {
		return menu.NewMenuFromItems(menu.AppMenu(), menu.EditMenu(), menu.WindowMenu())
	}

	helpMenu := menu.NewMenu()
	helpMenu.AddText("About "+name, nil, func(_ *menu.CallbackData) {
		app.ShowAbout()
	})
	help := menu.SubMenu("Help", helpMenu)
	return menu.NewMenuFromItems(help)
}
