package main

// harden: no core files on Windows; Windows Error Reporting is per process.
func harden() {}

func umask() {}
