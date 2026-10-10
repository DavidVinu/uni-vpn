package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/DavidVinu/uni-vpn/internal/daemon"
	"github.com/DavidVinu/uni-vpn/internal/updater"
)

const commitB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestVersionLineCarriesTheCommit(t *testing.T) {
	old := daemon.Commit
	defer func() { daemon.Commit = old }()
	daemon.Commit = ""
	if versionLine() != "uni-vpn "+daemon.Version {
		t.Fatal(versionLine())
	}
	daemon.Commit = commitB
	if versionLine() != "uni-vpn "+daemon.Version+" "+commitB {
		t.Fatal(versionLine())
	}
}

// The cases of RestartTests in tests/test_updater.py.

func TestWindowsSupervisesTheNewProgram(t *testing.T) {
	codes := []int{updater.RestartExit, updater.RestartExit, 0}
	var envs [][]string
	rc := supervise(func(string) string { return "" }, []string{"PATH=/bin"}, func(env []string) int {
		envs = append(envs, env)
		code := codes[0]
		codes = codes[1:]
		return code
	})
	if rc != 0 || len(envs) != 3 || !slices.Equal(envs[0], []string{"PATH=/bin", "UNI_VPN_SUPERVISED=1"}) {
		t.Fatal(rc, envs)
	}
}

func TestWindowsChildHandsBackToItsSupervisor(t *testing.T) {
	rc := supervise(func(key string) string {
		if key == "UNI_VPN_SUPERVISED" {
			return "1"
		}
		return ""
	}, nil, func([]string) int { t.Fatal("nested"); return 0 })
	if rc != updater.RestartExit {
		t.Fatal(rc)
	}
}

type fakeSelfUpdater struct {
	disabled string
	commit   string
	err      error
	applied  int
}

func (f *fakeSelfUpdater) Disabled() string                      { return f.disabled }
func (f *fakeSelfUpdater) Check(context.Context) (string, error) { return f.commit, f.err }
func (f *fakeSelfUpdater) Apply() (string, error)                { f.applied++; return f.commit, nil }

func runUpdate(u *fakeSelfUpdater, dryRun bool, controlRC int) (int, string, []string) {
	var out strings.Builder
	var actions []string
	rc := update{u: u, root: "/app", out: &out, control: func(action string) (int, error) {
		actions = append(actions, action)
		return controlRC, nil
	}}.run(context.Background(), dryRun)
	return rc, out.String(), actions
}

func TestUpdateInstallsAndRestartsTheService(t *testing.T) {
	u := &fakeSelfUpdater{commit: commitB}
	rc, out, actions := runUpdate(u, false, 0)
	if rc != 0 || u.applied != 1 || !slices.Equal(actions, []string{"restart"}) ||
		out != "-> Updated to bbbbbbb\nService restarted.\n" {
		t.Fatal(rc, out, actions)
	}
	rc, out, _ = runUpdate(&fakeSelfUpdater{}, false, 1)
	if rc != 1 || out != "-> This is the current version\nRestarting the service failed, see: uni-vpn log\n" {
		t.Fatal(rc, out)
	}
}

func TestUpdateFailuresAndDryRun(t *testing.T) {
	rc, out, actions := runUpdate(&fakeSelfUpdater{err: errors.New("checking for updates failed: offline")}, false, 0)
	if rc != 1 || out != "Update failed: checking for updates failed: offline\n" || actions != nil {
		t.Fatal(rc, out, actions)
	}
	rc, out, actions = runUpdate(&fakeSelfUpdater{disabled: "this copy is a git checkout, update it with git pull"}, false, 0)
	if rc != 1 || !strings.Contains(out, "git pull") || actions != nil {
		t.Fatal(rc, out, actions)
	}
	u := &fakeSelfUpdater{commit: commitB}
	rc, out, actions = runUpdate(u, true, 0)
	if rc != 0 || u.applied != 0 || actions != nil ||
		out != "-> would download the current version in /app and restart the service\n" {
		t.Fatal(rc, out, actions)
	}
}
