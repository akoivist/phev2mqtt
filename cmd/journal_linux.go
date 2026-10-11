//go:build linux

package cmd

import "github.com/wercker/journalhook"

func enableJournalLogging() {
	journalhook.Enable()
}
