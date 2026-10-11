/*
Copyright © 2021 Ben Buxton <bbuxton@gmail.com>

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program. If not, see <http://www.gnu.org/licenses/>.
*/
package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	cfgFile   string
	logLevel  string
	logTimes  bool
	logSyslog bool
)

// validateConfigPath validates config file path for security
func validateConfigPath(cfgFile string) error {
	if cfgFile == "" {
		return nil
	}
	
	// Clean and validate path
	cleanPath := filepath.Clean(cfgFile)
	
	// Check for path traversal attempts
	if strings.Contains(cfgFile, "..") {
		return fmt.Errorf("config file path cannot contain '..'")
	}
	
	// Verify file exists and is readable
	info, err := os.Stat(cleanPath)
	if err != nil {
		return fmt.Errorf("config file not accessible: %w", err)
	}
	
	// Verify it's a regular file
	if !info.Mode().IsRegular() {
		return fmt.Errorf("config file must be a regular file, not a directory or special file")
	}

	// Check file size (prevent reading huge files that could cause DoS)
	const maxConfigFileSize = 1048576 // 1MB
	if info.Size() > maxConfigFileSize {
		return fmt.Errorf("config file too large (max 1MB)")
	}

	return nil
}

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "phev2mqtt",
	Short: "A utility for communicating with a Mitsubishi PHEV",
	Long: `See below for subcommands. For further information
	on this tool, see https://github.com/buxtronix/phev2mqtt.`,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		logTimes = viper.GetBool("log_timestamps")
		
		// SECURITY: Validate log level before parsing to prevent panic
		validLogLevels := map[string]bool{
			"panic": true, "fatal": true, "error": true,
			"warning": true, "warn": true, "info": true,
			"debug": true, "trace": true,
		}
		
		if !validLogLevels[strings.ToLower(logLevel)] {
			fmt.Fprintf(os.Stderr, "Invalid log level '%s', using 'info'. Valid levels: panic, fatal, error, warning, info, debug, trace\n", logLevel)
			logLevel = "info"
		}

		level, err := log.ParseLevel(logLevel)
		if err != nil {
			// Should not happen after validation, but handle gracefully
			fmt.Fprintf(os.Stderr, "Failed to parse log level, using info: %v\n", err)
			log.SetLevel(log.InfoLevel)
		} else {
			log.SetLevel(level)
		}
		if logSyslog {
			enableJournalLogging()
		}
		if logTimes {
			log.SetFormatter(&log.TextFormatter{
				FullTimestamp: true,
			})
		} else {
			log.SetFormatter(&log.TextFormatter{
				FullTimestamp: false,
				DisableColors: true,
				DisableTimestamp: true,
			})
		}
	},

	// Uncomment the following line if your bare application
	// has an action associated with it:
	// Run: func(cmd *cobra.Command, args []string) { },
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	cobra.CheckErr(rootCmd.Execute())
}

func init() {
	cobra.OnInitialize(initConfig)

	// Here you will define your flags and configuration settings.
	// Cobra supports persistent flags, which, if defined here,
	// will be global for your application.

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.phev2mqtt.yaml)")
	rootCmd.PersistentFlags().StringVarP(&logLevel, "verbosity", "v", "info", "logging level to use")
	rootCmd.PersistentFlags().BoolVarP(&logTimes, "log_timestamps", "t", false, "coloured logging with timestamps")
	rootCmd.PersistentFlags().BoolVarP(&logSyslog, "log_syslog", "s", false, "plain logging to syslog instead of console")

	viper.BindPFlag("log_timestamps", rootCmd.PersistentFlags().Lookup("log_timestamps"))

	// Cobra also supports local flags, which will only run
	// when this action is called directly.
	// rootCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	if cfgFile != "" {
		// SECURITY: Validate config file path
		if err := validateConfigPath(cfgFile); err != nil {
			fmt.Fprintf(os.Stderr, "SECURITY ERROR: Invalid config file: %v\n", err)
			os.Exit(1)
		}
		
		// Use config file from the flag.
		viper.SetConfigFile(cfgFile)
	} else {
		// Find home directory.
		home, err := os.UserHomeDir()
		cobra.CheckErr(err)

		// Search config in home directory with name ".phev2mqtt" (without extension).
		viper.AddConfigPath(home)
		viper.SetConfigType("yaml")
		viper.SetConfigName(".phev2mqtt")
	}
	viper.AutomaticEnv()           // read in environment variables that match
	viper.BindEnv("log_timestamps", "log_timestamps", "LOG_TIMESTAMPS")

	// If a config file is found, read it in.
	if err := viper.ReadInConfig(); err == nil {
		fmt.Fprintln(os.Stderr, "Using config file:", viper.ConfigFileUsed())
	}
}
