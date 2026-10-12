// Command uni-vpn is the Go core of uni-vpn. It serves the same local API as the Python
// service and reads the same config.toml and keyring entries.
//
// It implements what the service, the installers and the app need: daemon, update, setup,
// uninstall and app. The other commands of uni_vpn/cli.py run in the Python core while it is
// installed next to this one (fallback.go).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/DavidVinu/uni-vpn/internal/coremain"
	"github.com/DavidVinu/uni-vpn/internal/credentials"
	"github.com/DavidVinu/uni-vpn/internal/daemon"
	"github.com/DavidVinu/uni-vpn/internal/desktop"
	"github.com/DavidVinu/uni-vpn/internal/platform"
	"github.com/DavidVinu/uni-vpn/internal/wintunnel"
)

// apiTokenEnv: when set, the local API answers only requests that carry this token.
const apiTokenEnv = "UNI_VPN_API_TOKEN"

const usage = "usage: uni-vpn [--config CONFIG] [--version] {daemon,update,setup,uninstall,app} ..."

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
			if run, ok := installCommands[command]; ok { // setup.go
				return run(configPath, args[i+1:])
			}
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
		if code, ok := runPython(argv); ok {
			return code
		}
		fmt.Fprintf(os.Stderr, "uni-vpn: '%s' is not available in the Go core yet, use the Python core\n", command)
	}
	return 2
}

func cmdDaemon(configArg string) int {
	exe := executable()
	harden()
	umask() // lock file, log and everything else readable only by the user
	// The Android app hands over its keys this way; openconnect and ocproxy need neither.
	apiToken := os.Getenv(apiTokenEnv)
	os.Unsetenv(apiTokenEnv)
	if platform.IsAndroid {
		credentials.AndroidStore()
		os.Unsetenv(credentials.KeyEnv)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	err := coremain.Run(ctx, coremain.Options{ConfigArg: configArg, APIToken: apiToken,
		Extend: func(log *slog.Logger, o *daemon.Options) {
			u := coreUpdater()
			app := desktop.New(u.Root)
			if platform.IsMacOS {
				go app.UseUpdatedApp()
			}
			windows := &appWindows{restart: app.RestartApp, log: log}
			o.Updater, o.AppPolled = u, windows.polled
		}})
	var logged *coremain.LoggedError
	switch {
	case err == nil:
		return 0
	case errors.Is(err, coremain.ErrRestart):
		return restart(exe)
	case errors.Is(err, coremain.ErrLocked):
		fmt.Fprintln(os.Stderr, err)
		return 0
	case errors.As(err, &logged):
		return 1
	}
	fmt.Fprintln(os.Stderr, err)
	return 1
}
