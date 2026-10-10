package main

import "github.com/DavidVinu/uni-vpn/internal/updater"

// supervise is the restart on Windows (restart_daemon in uni_vpn/cli.py), here so that tests
// run on every system. Task Scheduler restarts nothing that ends normally, and the job object
// ends the children with this process, so this process stays as a small supervisor: it starts
// the program (run) until it ends with something other than "start me again". A program that
// already runs under a supervisor hands the restart back to it.
func supervise(getenv func(string) string, environ []string, run func(env []string) int) int {
	if getenv(updater.SupervisedEnv) != "" {
		return updater.RestartExit
	}
	env := append(append([]string(nil), environ...), updater.SupervisedEnv+"=1")
	for {
		if code := run(env); code != updater.RestartExit {
			return code
		}
	}
}
