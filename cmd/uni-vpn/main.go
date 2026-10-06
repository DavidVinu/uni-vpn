// Command uni-vpn is the Go core of uni-vpn. It serves the same local API as the Python
// service and reads the same config.toml and keyring entries.
//
// So far it implements the "daemon" and "update" subcommands (cmd_daemon and cmd_update of
// uni_vpn/cli.py); the other commands stay with the Python core.
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/DavidVinu/uni-vpn/internal/config"
	"github.com/DavidVinu/uni-vpn/internal/daemon"
	"github.com/DavidVinu/uni-vpn/internal/logsetup"
	"github.com/DavidVinu/uni-vpn/internal/platform"
	"github.com/DavidVinu/uni-vpn/internal/winsys"
	"github.com/DavidVinu/uni-vpn/internal/wintunnel"
)

const usage = "usage: uni-vpn [--config CONFIG] [--version] {daemon,update [--dry-run]}"

func main() {
	// The hidden Ctrl+C helper of the Windows tunnel; returns for any other argv.
	wintunnel.RunCtrlCHelper(os.Args)
	os.Exit(run(os.Args[1:]))
}

func run(argv []string) int {
	var args []string
	for _, a := range argv {
		if a != "" { // install.sh passes empty arguments under bash 3.2
			args = append(args, a)
		}
	}
	configPath, command, dryRun := "", "", false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--version":
			fmt.Println(versionLine())
			return 0
		case a == "-h" || a == "--help":
			fmt.Println(usage)
			return 0
		case a == "--config":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, usage+"\nuni-vpn: error: argument --config: expected one argument")
				return 2
			}
			i++
			configPath = args[i]
		case strings.HasPrefix(a, "--config="):
			configPath = strings.TrimPrefix(a, "--config=")
		case a == "--dry-run" && command == "update":
			dryRun = true
		case a == "--dry-run":
			fmt.Fprintf(os.Stderr, "%s\nuni-vpn: error: unrecognized arguments: %s\n", usage, a)
			return 2
		case command == "":
			command = a
		default:
			fmt.Fprintf(os.Stderr, "%s\nuni-vpn: error: unrecognized arguments: %s\n", usage, a)
			return 2
		}
	}
	switch command {
	case "daemon":
		return cmdDaemon(configPath)
	case "update":
		return cmdUpdate(dryRun)
	case "":
		fmt.Fprintln(os.Stderr, usage+"\nuni-vpn: error: the following arguments are required: command")
	default:
		fmt.Fprintf(os.Stderr, "uni-vpn: '%s' is not available in the Go core yet, use the Python core\n", command)
	}
	return 2
}

func cmdDaemon(configArg string) int {
	exe := executable()
	harden()
	umask() // lock file, log and everything else readable only by the user
	lockPath := platform.LockFile()
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	lock, err := acquireLock(lockPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if lock == nil {
		fmt.Fprintln(os.Stderr, "uni-vpn daemon is already running")
		return 0
	}
	defer lock.Close()
	log, tail, err := logsetup.Setup(platform.LogFile(), slog.LevelInfo)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	cfgPath := configArg
	if cfgPath == "" {
		cfgPath = config.DefaultPath()
	}
	configError := ""
	cfg, err := config.Load(configArg)
	if err != nil {
		var ce *config.ConfigError
		if !errors.As(err, &ce) {
			log.Error(err.Error())
			return 1
		}
		fallback := config.Default()
		ports := config.PortsFromBroken(configArg)
		if p, ok := ports["socks_port"]; ok {
			fallback.SocksPort = p
		}
		if p, ok := ports["http_port"]; ok {
			fallback.HTTPPort = p
		}
		cfg = &fallback
		configError = err.Error()
		log.Error(configError)
	}
	log.Info(fmt.Sprintf("uni-vpn %s starting (SOCKS %d, status %d)", daemon.Version, cfg.SocksPort, cfg.HTTPPort))
	if platform.IsWindows {
		winsys.KillChildrenWithUs()
		winsys.AllowCtrlCForChildren()
		if !winsys.IsAdmin() {
			log.Warn("Not running elevated: openconnect cannot create the Wintun adapter")
		}
	}
	_, statErr := os.Stat(cfgPath)
	d := daemon.New(cfg, log, daemon.Options{ConfigError: configError, LogTail: tail.Lines, ConfigPath: cfgPath,
		NeedsSetup: errors.Is(statErr, fs.ErrNotExist), Updater: coreUpdater()})
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, os.Interrupt)
	go func() {
		<-signals
		d.Stop()
	}()
	d.Run()
	log.Info("uni-vpn stopped")
	if d.RestartRequested {
		lock.Close()
		if h, ok := log.Handler().(interface{ Close() error }); ok {
			h.Close() // the new program opens the log file again
		}
		return restart(exe)
	}
	return 0
}
