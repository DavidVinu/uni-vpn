package main

import (
	"reflect"
	"testing"
)

func TestParseArgs(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want map[string]string
		code int
	}{
		{nil, map[string]string{}, -1},
		{[]string{"--dry-run", "--no-gui", "--user", "ab123", "--university=ethz", "--no-browser"},
			map[string]string{"--dry-run": "1", "--no-gui": "1", "--user": "ab123", "--university": "ethz", "--no-browser": "1"}, -1},
		{[]string{"--dry", "--univ", "bonn"}, map[string]string{"--dry-run": "1", "--university": "bonn"}, -1},
		{[]string{"--no"}, nil, 2}, // --no-gui or --no-browser
		{[]string{"--user"}, nil, 2},
		{[]string{"--user", "--dry-run"}, nil, 2},
		{[]string{"--dry-run=1"}, nil, 2},
		{[]string{"extra"}, nil, 2},
		{[]string{"--yes"}, nil, 2},
		{[]string{"-h"}, nil, 0},
	} {
		got, code := parseArgs(setupCommand, tc.args)
		if code != tc.code || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%v: %v %d", tc.args, got, code)
		}
	}
	if got, code := parseArgs(uninstallCommand, []string{"--yes", "--dry-run"}); code != -1 || len(got) != 2 {
		t.Fatal(got, code)
	}
	if got, code := parseArgs(appCommand, []string{"--settings"}); code != -1 || got["--settings"] == "" {
		t.Fatal(got, code)
	}
}
