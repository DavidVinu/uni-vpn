package winsys

import (
	"reflect"
	"strings"
	"testing"
)

func TestQuoteArgMatchesList2cmdline(t *testing.T) {
	for arg, want := range map[string]string{
		"plain":                    `plain`,
		"a b":                      `"a b"`,
		"":                         `""`,
		`C:\Program Files\x\`:      `"C:\Program Files\x\\"`,
		`say "hi"`:                 `"say \"hi\""`,
		`back\\"q`:                 `back\\\\\"q`,
		"tab\there":                "\"tab\there\"",
		`C:\Program Files\uni-vpn`: `"C:\Program Files\uni-vpn"`,
	} {
		if got := QuoteArg(arg); got != want {
			t.Errorf("QuoteArg(%q) = %s, want %s", arg, got, want)
		}
	}
}

func TestRenderTask(t *testing.T) {
	text := RenderTask(`C:\Py\pythonw.exe`, `C:\Program Files\uni-vpn\bin\uni-vpn`, `UNI\a&b`, `C:\Program Files\uni-vpn`)
	for _, want := range []string{
		`<?xml version="1.0" encoding="UTF-16"?>`,
		`<UserId>UNI\a&amp;b</UserId>`,
		`<Command>C:\Py\pythonw.exe</Command>`,
		`<Arguments>-I "C:\Program Files\uni-vpn\bin\uni-vpn" daemon</Arguments>`,
		`<RunLevel>HighestAvailable</RunLevel>`,
		`<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>`,
	} {
		if !strings.Contains(text, want) {
			t.Fatal(want)
		}
	}
}

func TestRenderTaskBinaryDiffersOnlyInTheProgram(t *testing.T) {
	py := RenderTask(`C:\Py\pythonw.exe`, `C:\x\uni-vpn`, "u", `C:\w`)
	gobin := RenderTaskBinary(`C:\Program Files\uni-vpn\uni-vpn.exe`, "u", `C:\w`)
	want := strings.Replace(strings.Replace(py, `<Command>C:\Py\pythonw.exe</Command>`,
		`<Command>C:\Program Files\uni-vpn\uni-vpn.exe</Command>`, 1),
		`<Arguments>-I C:\x\uni-vpn daemon</Arguments>`, `<Arguments>daemon</Arguments>`, 1)
	if gobin != want || py == want {
		t.Fatal(gobin)
	}
}

func TestTrustedEnv(t *testing.T) {
	current := []string{"=C:=C:\\x", "temp=C:\\T", "ComSpec=C:\\evil.exe", "Path=C:\\evil", "USERNAME=ab", "PSModulePath=x"}
	got := TrustedEnv(current, `C:\Windows\System32`, `C:\Windows`)
	want := []string{
		`TEMP=C:\T`, "USERNAME=ab",
		`SystemRoot=C:\Windows`, `windir=C:\Windows`, `ComSpec=C:\Windows\System32\cmd.exe`,
		`PATH=C:\Windows\System32;C:\Windows;C:\Windows\System32\Wbem;C:\Windows\System32\WindowsPowerShell\v1.0`,
		"PATHEXT=.COM;.EXE;.BAT;.CMD;.VBS;.JS;.WSF",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
}

func TestCurrentUser(t *testing.T) {
	t.Setenv("USERDOMAIN", "UNI")
	t.Setenv("USERNAME", "ab123")
	if got := CurrentUser(); got != `UNI\ab123` {
		t.Fatal(got)
	}
	t.Setenv("USERDOMAIN", "")
	if got := CurrentUser(); got != "ab123" {
		t.Fatal(got)
	}
}

func TestUserPathComparison(t *testing.T) {
	if normcase(`C:\Tools\Uni-VPN\`) != normcase(`c:/tools/uni-vpn`) {
		t.Fatal("trailing backslash or case")
	}
	if got := pathEntries(`a;;b;`); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatal(got)
	}
}
