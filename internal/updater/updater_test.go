package updater

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

const (
	commitA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	commitB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type file struct {
	name, body string
	mode       os.FileMode
}

func makeZip(t *testing.T, files ...file) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, f := range files {
		h := &zip.FileHeader{Name: f.name, Method: zip.Deflate}
		if f.mode != 0 {
			h.SetMode(f.mode)
		}
		out, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		out.Write([]byte(f.body))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func digest(data []byte) FileDigest {
	sum := sha256.Sum256(data)
	return FileDigest{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
}

// release is the fake GitHub release: the manifest and the zips, served by an httptest server
// that every request goes to, whatever its URL.
type release struct {
	mu       sync.Mutex
	manifest any
	files    map[string][]byte
	requests []string
}

func (r *release) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, req.URL.String())
	name := strings.TrimPrefix(req.URL.Path, "/DavidVinu/uni-vpn/releases/latest/download/")
	if name == "core-manifest.json" {
		json.NewEncoder(w).Encode(r.manifest)
		return
	}
	if data, ok := r.files[name]; ok {
		w.Write(data)
		return
	}
	http.NotFound(w, req)
}

func (r *release) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

// redirect sends every request to the test server, keeping the path.
type redirect struct{ target *url.URL }

func (rt redirect) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != "github.com" {
		return nil, fmt.Errorf("unexpected host %s", req.URL.Host)
	}
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = rt.target.Scheme, rt.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

type fixture struct {
	t       *testing.T
	root    string
	release *release
	u       *Updater
	ran     []string
	version string // what the new program prints for --version
	runErr  error
}

func newFixture(t *testing.T, zipData []byte) *fixture {
	parent := t.TempDir()
	root := filepath.Join(parent, "app")
	f := &fixture{t: t, root: root, version: "uni-vpn 0.1.0 " + commitB + "\n"}
	write(t, filepath.Join(root, "bin", "uni-vpn-core"), "old core")
	write(t, filepath.Join(root, "bin", "uni-vpn"), "python entry point")
	write(t, filepath.Join(root, "uni_vpn", "__init__.py"), "V = 1\n")
	name := ZipName("linux", "amd64")
	f.release = &release{files: map[string][]byte{name: zipData},
		manifest: Manifest{Commit: commitB, Files: map[string]FileDigest{name: digest(zipData)}}}
	server := httptest.NewServer(f.release)
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	f.u = &Updater{Root: root, Commit: commitA, GOOS: "linux", GOARCH: "amd64", Exe: ExeName("linux"),
		Client: &http.Client{Transport: redirect{target}}, Files: osFS{},
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			body, _ := os.ReadFile(name)
			f.ran = append(f.ran, string(body)+" "+strings.Join(args, " "))
			return []byte(f.version), f.runErr
		}}
	return f
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// leftovers are staging folders next to the root.
func (f *fixture) leftovers() []string {
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(f.root), stagePrefix+"*"))
	return matches
}

func (f *fixture) checkFails(want string) {
	f.t.Helper()
	commit, err := f.u.Check(context.Background())
	var ue *Error
	if !errors.As(err, &ue) || !strings.Contains(err.Error(), want) {
		f.t.Fatalf("got %q %v, expected an error with %q", commit, err, want)
	}
	if f.u.Pending() != "" || len(f.leftovers()) != 0 {
		f.t.Fatal("something stayed staged", f.u.Pending(), f.leftovers())
	}
	if read(f.t, filepath.Join(f.root, "bin", "uni-vpn-core")) != "old core" {
		f.t.Fatal("the program was changed")
	}
}

var coreZip = []file{{name: "bin/uni-vpn-core", body: "new core", mode: 0o755}}

func TestUpdateInstallsTheNewCoreAndKeepsEverythingElse(t *testing.T) {
	f := newFixture(t, makeZip(t, coreZip...))
	commit, err := f.u.Check(context.Background())
	if err != nil || commit != commitB || f.u.Pending() != commitB {
		t.Fatal(commit, err)
	}
	if read(t, filepath.Join(f.root, "bin", "uni-vpn-core")) != "old core" {
		t.Fatal("check must not install")
	}
	if len(f.ran) != 1 || f.ran[0] != "new core --version" {
		t.Fatal(f.ran)
	}
	staged := f.leftovers()
	if len(staged) != 1 {
		t.Fatal(staged)
	}
	// A second check while one is pending downloads nothing.
	before := f.release.count()
	if again, _ := f.u.Check(context.Background()); again != commitB || f.release.count() != before {
		t.Fatal("checked again")
	}
	if got, err := f.u.Apply(); err != nil || got != commitB {
		t.Fatal(got, err)
	}
	if read(t, filepath.Join(f.root, "bin", "uni-vpn-core")) != "new core" {
		t.Fatal("not installed")
	}
	if st, _ := os.Stat(filepath.Join(f.root, "bin", "uni-vpn-core")); st.Mode().Perm() != 0o755 {
		t.Fatal(st.Mode())
	}
	if read(t, filepath.Join(f.root, "bin", "uni-vpn")) != "python entry point" ||
		read(t, filepath.Join(f.root, "uni_vpn", "__init__.py")) != "V = 1\n" {
		t.Fatal("the Python files must stay")
	}
	if read(t, filepath.Join(f.root, CommitFile)) != commitB+"\n" ||
		read(t, filepath.Join(f.root, FilesList)) != "bin/uni-vpn-core\n" {
		t.Fatal("records")
	}
	if f.u.Pending() != "" || len(f.leftovers()) != 0 {
		t.Fatal("left behind", f.leftovers())
	}
	if _, err := f.u.Apply(); err == nil {
		t.Fatal("applied twice")
	}
}

func TestRequestsGoToTheReleaseOnly(t *testing.T) {
	f := newFixture(t, makeZip(t, coreZip...))
	if _, err := f.u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"/DavidVinu/uni-vpn/releases/latest/download/core-manifest.json",
		"/DavidVinu/uni-vpn/releases/latest/download/uni-vpn-core-linux-amd64.zip"}
	if !slices.Equal(f.release.requests, want) {
		t.Fatal(f.release.requests)
	}
}

func TestCurrentVersionDownloadsNothing(t *testing.T) {
	f := newFixture(t, makeZip(t, coreZip...))
	f.u.Commit = commitB
	if commit, err := f.u.Check(context.Background()); commit != "" || err != nil {
		t.Fatal(commit, err)
	}
	if f.release.count() != 1 || len(f.ran) != 0 {
		t.Fatal(f.release.requests)
	}
}

func TestDeveloperBuildsAndCheckoutsNeverUpdate(t *testing.T) {
	f := newFixture(t, makeZip(t, coreZip...))
	f.u.Commit = ""
	if commit, err := f.u.Check(context.Background()); commit != "" || err != nil || f.release.count() != 0 {
		t.Fatal(commit, err)
	}
	if f.u.Disabled() == "" {
		t.Fatal("no reason")
	}
	f.u.Commit = "not-a-commit"
	if commit, _ := f.u.Check(context.Background()); commit != "" || f.release.count() != 0 {
		t.Fatal("updated a build with a broken commit")
	}
	f.u.Commit = commitA
	os.Mkdir(filepath.Join(f.root, ".git"), 0o755)
	if commit, err := f.u.Check(context.Background()); commit != "" || err != nil || f.release.count() != 0 {
		t.Fatal(commit, err)
	}
	if !strings.Contains(f.u.Disabled(), "git") {
		t.Fatal(f.u.Disabled())
	}
}

func TestRootIsTheParentOfBin(t *testing.T) {
	root, problem := rootOf(filepath.Join("/opt", "app", "bin", "uni-vpn-core"), ExeName("linux"))
	if root != filepath.Join("/opt", "app") || problem != "" {
		t.Fatal(root, problem)
	}
	for _, exe := range []string{"/usr/local/bin/uni-vpn", "/tmp/uni-vpn-core", "/opt/app/sbin/uni-vpn-core"} {
		if _, problem := rootOf(exe, ExeName("linux")); problem == "" {
			t.Fatal(exe)
		}
	}
}

func TestTamperedDownloadIsRefused(t *testing.T) {
	f := newFixture(t, makeZip(t, coreZip...))
	f.release.files[ZipName("linux", "amd64")] = makeZip(t, file{name: "bin/uni-vpn-core", body: "bad core", mode: 0o755})
	f.checkFails("damaged")
	if len(f.ran) != 0 {
		t.Fatal("ran a program that failed the hash check")
	}
}

func TestOversizedDownloadIsRefused(t *testing.T) {
	data := makeZip(t, coreZip...)
	f := newFixture(t, data)
	f.release.files[ZipName("linux", "amd64")] = append(data, make([]byte, 100)...)
	f.checkFails("larger than expected")
}

func TestManifestIsChecked(t *testing.T) {
	data := makeZip(t, coreZip...)
	name := ZipName("linux", "amd64")
	for _, c := range []struct {
		manifest any
		want     string
	}{
		{map[string]any{"commit": "main", "files": map[string]any{}}, "no valid version number"},
		{map[string]any{"commit": strings.ToUpper(commitB), "files": map[string]any{}}, "no valid version number"},
		{map[string]any{"commit": commitB, "files": map[string]any{name: map[string]any{"sha256": "abc", "size": 3}}}, "bad entry"},
		{map[string]any{"commit": commitB, "files": map[string]any{name: map[string]any{"sha256": digest(data).SHA256, "size": 0}}}, "bad entry"},
		{map[string]any{"commit": commitB, "files": map[string]any{ZipName("linux", "arm64"): digest(data)}}, "no update for this computer"},
		{"just text", "unexpected answer"},
	} {
		f := newFixture(t, data)
		f.release.manifest = c.manifest
		f.checkFails(c.want)
	}
}

func TestWrongCommitIsNotInstalled(t *testing.T) {
	f := newFixture(t, makeZip(t, coreZip...))
	f.version = "uni-vpn 0.1.0 " + commitA + "\n"
	f.checkFails("not the requested version")
}

func TestBrokenNewVersionIsNotInstalled(t *testing.T) {
	f := newFixture(t, makeZip(t, coreZip...))
	f.version, f.runErr = "panic: something\nexec format error\n", errors.New("exit status 2")
	f.checkFails("the new version does not start: exec format error")
}

func TestPathsOutsideTheRootAreRefused(t *testing.T) {
	for _, name := range []string{"../evil", "bin/../../evil", "/etc/evil", "bin\\..\\..\\evil", "C:/evil", "./bin/x", "bin//x"} {
		f := newFixture(t, makeZip(t, append([]file{{name: name, body: "evil"}}, coreZip...)...))
		f.checkFails("unexpected path")
		if _, err := os.Stat(filepath.Join(filepath.Dir(f.root), "evil")); err == nil {
			t.Fatal("wrote outside the root:", name)
		}
	}
}

func TestSymlinksAndForeignZipsAreRefused(t *testing.T) {
	f := newFixture(t, makeZip(t, append([]file{{name: "bin/link", body: "/etc/passwd", mode: os.ModeSymlink | 0o777}}, coreZip...)...))
	f.checkFails("unexpected file")
	f = newFixture(t, makeZip(t, file{name: "bin/something-else", body: "x"}))
	f.checkFails("does not look like uni-vpn")
	f = newFixture(t, makeZip(t, append([]file{{name: "BIN/uni-vpn-core", body: "x"}}, coreZip...)...))
	f.checkFails("appears twice")
	f = newFixture(t, []byte("not a zip at all"))
	f.checkFails("damaged")
}

func TestFilesAnEarlierVersionInstalledAreRemoved(t *testing.T) {
	f := newFixture(t, makeZip(t, coreZip...))
	write(t, filepath.Join(f.root, "bin", "gone"), "from an earlier version")
	write(t, filepath.Join(f.root, FilesList), "bin/uni-vpn-core\nbin/gone\n../outside\n")
	write(t, filepath.Join(filepath.Dir(f.root), "outside"), "not ours")
	if _, err := f.u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.u.Apply(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.root, "bin", "gone")); err == nil {
		t.Fatal("stale file stayed")
	}
	if read(t, filepath.Join(filepath.Dir(f.root), "outside")) != "not ours" ||
		read(t, filepath.Join(f.root, "bin", "uni-vpn")) != "python entry point" {
		t.Fatal("removed a file the updater did not install")
	}
}

func TestDiscardRemovesTheStagedFiles(t *testing.T) {
	f := newFixture(t, makeZip(t, coreZip...))
	if _, err := f.u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.u.Discard()
	if f.u.Pending() != "" || len(f.leftovers()) != 0 {
		t.Fatal("not discarded")
	}
}

// winFS behaves like Windows for the files in busy: they cannot be replaced or deleted, only
// renamed (and stay busy under the new name).
type winFS struct{ busy map[string]bool }

func (w *winFS) Rename(oldpath, newpath string) error {
	if w.busy[newpath] {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: errors.New("Access is denied.")}
	}
	if w.busy[oldpath] {
		delete(w.busy, oldpath)
		w.busy[newpath] = true
	}
	return os.Rename(oldpath, newpath)
}

func (w *winFS) Remove(name string) error {
	if w.busy[name] {
		return &os.PathError{Op: "remove", Path: name, Err: errors.New("Access is denied.")}
	}
	return os.Remove(name)
}

func TestWindowsMovesTheRunningProgramAside(t *testing.T) {
	data := makeZip(t, file{name: "bin/uni-vpn-core.exe", body: "new core"})
	f := newFixture(t, data)
	name := ZipName("windows", "amd64")
	f.release.files[name] = data
	f.release.manifest = Manifest{Commit: commitB, Files: map[string]FileDigest{name: digest(data)}}
	f.u.GOOS, f.u.Exe, f.u.Windows = "windows", ExeName("windows"), true
	exe := filepath.Join(f.root, "bin", "uni-vpn-core.exe")
	write(t, exe, "old core")
	// The supervisor of an earlier update still runs the program from two updates ago.
	write(t, exe+".old", "core from two updates ago")
	fsys := &winFS{busy: map[string]bool{exe: true, exe + ".old": true}}
	f.u.Files = fsys

	if _, err := f.u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.u.Apply(); err != nil {
		t.Fatal(err)
	}
	if read(t, exe) != "new core" || read(t, exe+".1.old") != "old core" ||
		read(t, exe+".old") != "core from two updates ago" {
		t.Fatal("the running program was not moved aside")
	}
	if read(t, filepath.Join(f.root, FilesList)) != "bin/uni-vpn-core.exe\n" {
		t.Fatal(read(t, filepath.Join(f.root, FilesList)))
	}

	// The next start, once the old programs have ended.
	fsys.busy = map[string]bool{}
	write(t, filepath.Join(f.root, "bin", "uni-vpn.old"), "not ours")
	f.u.RemoveOld()
	for _, p := range []string{exe + ".old", exe + ".1.old"} {
		if _, err := os.Stat(p); err == nil {
			t.Fatal("stayed:", p)
		}
	}
	if read(t, exe) != "new core" || read(t, filepath.Join(f.root, "bin", "uni-vpn.old")) != "not ours" {
		t.Fatal("removed a file the updater did not create")
	}
}

func TestWindowsFileNotInUseIsReplacedDirectly(t *testing.T) {
	f := newFixture(t, makeZip(t, coreZip...))
	f.u.Windows = true
	f.u.Files = &winFS{busy: map[string]bool{}}
	if _, err := f.u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.u.Apply(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.root, "bin", "uni-vpn-core.old")); err == nil {
		t.Fatal("moved aside without need")
	}
}
