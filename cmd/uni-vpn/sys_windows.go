package main

import "golang.org/x/sys/windows"

// harden: no core files on Windows; Windows Error Reporting is per process.
func harden() {}

func umask() {}

// normalPriority: Task Scheduler started the service of earlier versions below normal priority
// (task priority 7), and an update replaces only the program. All of the browser's traffic to
// the university goes through the service; openconnect, started from here, gets its priority.
func normalPriority() {
	_ = windows.SetPriorityClass(windows.CurrentProcess(), windows.NORMAL_PRIORITY_CLASS)
}
