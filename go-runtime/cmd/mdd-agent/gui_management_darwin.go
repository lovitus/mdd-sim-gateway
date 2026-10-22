//go:build gui && darwin

package main

import (
	"context"
	"fyne.io/fyne/v2"
)

func showInstalledServiceGUI(context.Context, fyne.Window, string, func(config, string, guiServiceClient)) bool {
	return false
}
