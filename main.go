// Command media-tools is a desktop front end for ffmpeg, ffprobe and yt-dlp.
package main

import (
	"embed"
	"log"

	"media-tools/internal/app"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

// main builds the application and hands control to the Wails runtime.
func main() {
	backend, err := app.NewApp()
	if err != nil {
		log.Fatalf("media-tools: %v", err)
	}
	cfg := backend.GetSettings()
	startup, shutdown := app.Lifecycle(backend)

	err = wails.Run(&options.App{
		Title:       "Media Tools",
		Width:       cfg.WindowWidth,
		Height:      cfg.WindowHeight,
		MinWidth:    960,
		MinHeight:   640,
		AssetServer: &assetserver.Options{Assets: assets},
		// The page's hall colour, so the window never flashes white on open
		BackgroundColour: &options.RGBA{R: 0x24, G: 0x25, B: 0x28, A: 255},
		// Files dropped on the window arrive as absolute paths through
		// OnFileDrop; the webview's own handling would navigate to the file.
		DragAndDrop: &options.DragAndDrop{
			EnableFileDrop:     true,
			DisableWebViewDrop: true,
		},
		OnStartup:  startup,
		OnShutdown: shutdown,
		Bind:       []interface{}{backend},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
		},
	})
	if err != nil {
		log.Fatalf("media-tools: %v", err)
	}
}
