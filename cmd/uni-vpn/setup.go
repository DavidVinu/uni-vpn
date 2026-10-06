package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/DavidVinu/uni-vpn/internal/config"
	"github.com/DavidVinu/uni-vpn/internal/credentials"
	"github.com/DavidVinu/uni-vpn/internal/daemon"
	"github.com/DavidVinu/uni-vpn/internal/desktop"
	"github.com/DavidVinu/uni-vpn/internal/setup"
)

// The commands the installers and the app need besides "daemon": setup, uninstall and app
// (cmd_setup, cmd_uninstall and cmd_app of uni_vpn/cli.py, with the same options).

// installCommands take the --config value and the arguments after the command.
var installCommands = map[string]func(configPath string, args []string) int{
	"setup":     cmdSetup,
	"uninstall": cmdUninstall,
	"app":       cmdApp,
}

func init() {
	// The service uninstalls itself once the installer's folder is gone (internal/daemon/removal.go).
	daemon.DefaultRemoveSelf = func() int { return setup.New(setup.Root()).Remove() }
}

// option is one flag of a subcommand; value means it takes an argument.
type option struct {
	name  string
	value bool
}

// command describes a subcommand for parseArgs, with argparse's usage and help texts.
type command struct {
	name, usage, help string
	options           []option
}

var (
	setupCommand = command{name: "setup",
		usage: "usage: uni-vpn setup [-h] [--dry-run] [--user USER] [--university UNIVERSITY]\n                     [--no-gui]",
		help: `
options:
  -h, --help            show this help message and exit
  --dry-run
  --user USER           University ID
  --university UNIVERSITY
                        University from uni_vpn/universities.json, for example
                        heidelberg
  --no-gui              Ask in the terminal instead of opening the setup
                        assistant`,
		options: []option{{"--dry-run", false}, {"--user", true}, {"--university", true}, {"--no-gui", false},
			{"--no-browser", false}}}
	uninstallCommand = command{name: "uninstall", usage: "usage: uni-vpn uninstall [-h] [--yes] [--dry-run]",
		help: `
options:
  -h, --help  show this help message and exit
  --yes       Delete the keyring entries without asking
  --dry-run`,
		options: []option{{"--yes", false}, {"--dry-run", false}}}
	appCommand = command{name: "app", usage: "usage: uni-vpn app [-h] [--settings]",
		help: `
options:
  -h, --help  show this help message and exit
  --settings  open it at Settings`,
		options: []option{{"--settings", false}}}
)

// parseArgs reads the options like argparse: --name, --name VALUE, --name=VALUE, and a unique
// prefix of a name. code >= 0 means the command ends here with that exit code.
func parseArgs(c command, args []string) (values map[string]string, code int) {
	values = map[string]string{}
	fail := func(format string, a ...any) (map[string]string, int) {
		fmt.Fprintf(os.Stderr, "%s\nuni-vpn %s: error: %s\n", c.usage, c.name, fmt.Sprintf(format, a...))
		return nil, 2
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-h" || a == "--help" {
			fmt.Println(c.usage + "\n" + c.help)
			return nil, 0
		}
		name, value, hasValue := strings.Cut(a, "=")
		if !strings.HasPrefix(name, "--") {
			return fail("unrecognized arguments: %s", strings.Join(args[i:], " "))
		}
		var found []option
		for _, o := range c.options {
			if o.name == name {
				found = []option{o}
				break
			}
			if strings.HasPrefix(o.name, name) {
				found = append(found, o)
			}
		}
		switch {
		case len(found) == 0:
			return fail("unrecognized arguments: %s", strings.Join(args[i:], " "))
		case len(found) > 1:
			names := make([]string, len(found))
			for j, o := range found {
				names[j] = o.name
			}
			return fail("ambiguous option: %s could match %s", name, strings.Join(names, ", "))
		}
		o := found[0]
		switch {
		case !o.value && hasValue:
			return fail("argument %s: ignored explicit argument '%s'", o.name, value)
		case !o.value:
			values[o.name] = "1"
		case hasValue:
			values[o.name] = value
		case i+1 < len(args) && !strings.HasPrefix(args[i+1], "-"):
			i++
			values[o.name] = args[i]
		default:
			return fail("argument %s: expected one argument", o.name)
		}
	}
	return values, -1
}

// report ends a command after an error, as cli.main does.
func report(err error) int {
	var ce *config.ConfigError
	var ke *credentials.KeyringError
	switch {
	case errors.As(err, &ce):
		fmt.Println(err)
		return 2
	case errors.As(err, &ke):
		fmt.Println(err)
		return 1
	case errors.Is(err, setup.ErrNoTerminal):
		fmt.Println("\nNo terminal to answer questions: run the installer in a terminal")
		return 1
	}
	fmt.Println(err)
	return 1
}

func cmdSetup(_ string, args []string) int {
	values, code := parseArgs(setupCommand, args)
	if code >= 0 {
		return code
	}
	a := setup.Args{DryRun: values["--dry-run"] != "", NoGUI: values["--no-gui"] != "",
		NoBrowser: values["--no-browser"] != "", User: values["--user"], University: values["--university"]}
	code, err := setup.New(setup.Root()).Setup(a)
	if err != nil {
		return report(err)
	}
	return code
}

func cmdUninstall(_ string, args []string) int {
	values, code := parseArgs(uninstallCommand, args)
	if code >= 0 {
		return code
	}
	code, err := setup.New(setup.Root()).Uninstall(setup.Args{DryRun: values["--dry-run"] != "", Yes: values["--yes"] != ""})
	if err != nil {
		return report(err)
	}
	return code
}

func cmdApp(configPath string, args []string) int {
	values, code := parseArgs(appCommand, args)
	if code >= 0 {
		return code
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return report(err)
	}
	page := ""
	if values["--settings"] != "" {
		page = "#settings"
	}
	link := fmt.Sprintf("http://127.0.0.1:%d/", cfg.HTTPPort)
	if desktop.New(setup.Root()).OpenApp(cfg.HTTPPort, page) || desktop.OpenURL(link) {
		return 0
	}
	fmt.Printf("No desktop to show the app on; the page is %s\n", link)
	return 1
}
