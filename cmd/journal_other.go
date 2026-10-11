//go:build !linux

package cmd

import log "github.com/sirupsen/logrus"

func enableJournalLogging() {
	log.Warn("--log_syslog is only supported on Linux; continuing with console logging")
}
