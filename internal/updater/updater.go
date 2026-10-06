// Package updater is the self-updater of the Go core. It mirrors uni_vpn/updater.py, but
// installs the release files of docs/go-switch.md (core-manifest.json and one zip per
// platform) instead of the source archive.
//
// The URLs are constants: on Windows the service runs elevated and writes to Program Files,
// so nothing the user can change may decide which files it installs.
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
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/platform"
)

const (
	ManifestURL = "https://github.com/DavidVinu/uni-vpn/releases/latest/download/core-manifest.json"
	// DownloadURL is where the zips named in the manifest are.
	DownloadURL = "https://github.com/DavidVinu/uni-vpn/releases/latest/download/"
	CommitFile  = ".commit"
	// FilesList records the files the Go updater installed, one relative path per line.
	FilesList = ".core-files"
	// RestartExit is the exit code of a daemon that updated itself and wants to be started
	// again (EX_TEMPFAIL), the same as the Python core's.
	RestartExit   = 75
	SupervisedEnv = "UNI_VPN_SUPERVISED"

	maxManifest = 1 << 20
	maxDownload = 256 << 20
	maxUnpacked = 512 << 20
	// stagePrefix names the folder a new version is unpacked into, next to the install root.
	stagePrefix = ".uni-vpn-core-update-"
)

var (
	commitRe = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ValidCommit reports whether s is a full commit id.
func ValidCommit(s string) bool { return commitRe.MatchString(s) }

// Error is an update problem in plain words; the service logs it and tries again later.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

func errorf(format string, args ...any) error { return &Error{fmt.Sprintf(format, args...)} }

// Runner runs a program with a time limit and returns what it printed.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// RunHidden is the real Runner: no console window on Windows.
func RunHidden(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	platform.HideWindow(cmd)
	cmd.WaitDelay = 5 * time.Second
	return cmd.CombinedOutput()
}

// FS is what install does with files. On Windows a running program can be renamed but not
// replaced or deleted; tests imitate that on any system.
type FS interface {
	Rename(oldpath, newpath string) error
	Remove(name string) error
}

type osFS struct{}

func (osFS) Rename(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }
func (osFS) Remove(name string) error             { return os.Remove(name) }

// Updater checks for and installs new versions of the files under Root.
type Updater struct {
	// Root is the install root: the parent of bin/, which holds the running program.
	Root string
	// Commit is the commit the running program was built from, "" for a developer build.
	Commit string
	GOOS   string
	GOARCH string
	Client *http.Client
	Run    Runner
	Files  FS
	// Windows moves a file that is in use out of the way before replacing it.
	Windows bool
	// Exe is the program's own path below Root, slash-separated.
	Exe string
	// rootErr is why Root is not an installation the updater may change.
	rootErr string

	mu      sync.Mutex
	pending string
	staged  *staged
}

// ExeName is the Go core's file name below the install root.
func ExeName(goos string) string {
	if goos == "windows" {
		return "bin/uni-vpn-core.exe"
	}
	return "bin/uni-vpn-core"
}

// New updates the installation the running program is part of.
func New(commit string) *Updater {
	u := &Updater{Commit: commit, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Client: http.DefaultClient,
		Run: RunHidden, Files: osFS{}, Windows: runtime.GOOS == "windows", Exe: ExeName(runtime.GOOS)}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		u.rootErr = "the program cannot find its own folder"
		return u
	}
	u.Root, u.rootErr = rootOf(exe, u.Exe)
	return u
}

// rootOf is the install root of the program at exe: the parent of its bin folder. Only
// bin/uni-vpn-core of an installation counts; for a copy anywhere else (say /usr/local/bin)
// the root would be a folder the updater has no business writing to.
func rootOf(exe, name string) (root, problem string) {
	root = filepath.Dir(filepath.Dir(exe))
	if !strings.EqualFold(filepath.Join(root, filepath.FromSlash(name)), exe) {
		return root, "the program is not in the bin folder of an installation"
	}
	return root, ""
}

// Installed is the commit the running program was built from, "" when unknown.
func (u *Updater) Installed() string { return u.Commit }

// Pending is the commit that is downloaded and ready to install, "" when there is none.
func (u *Updater) Pending() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.pending
}

// Disabled says why this copy never updates itself, "" when it does.
func (u *Updater) Disabled() string {
	switch {
	case !ValidCommit(u.Commit):
		return "this copy was built without a version number"
	case u.rootErr != "":
		return u.rootErr
	case exists(filepath.Join(u.Root, ".git")):
		return "this copy is a git checkout, update it with git pull"
	}
	return ""
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// Manifest is core-manifest.json.
type Manifest struct {
	Commit   string                `json:"commit"`
	Handover bool                  `json:"handover"`
	Files    map[string]FileDigest `json:"files"`
}

type FileDigest struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// ZipName is the release file for a platform.
func ZipName(goos, goarch string) string { return fmt.Sprintf("uni-vpn-core-%s-%s.zip", goos, goarch) }

func (u *Updater) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "uni-vpn")
	resp, err := u.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > limit {
		return nil, errors.New("the file is larger than expected")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("the file is larger than expected")
	}
	return data, nil
}

// LatestManifest fetches and checks core-manifest.json.
func (u *Updater) LatestManifest(ctx context.Context) (*Manifest, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	data, err := u.get(ctx, ManifestURL, maxManifest)
	if err != nil {
		return nil, errorf("checking for updates failed: %s", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, errorf("unexpected answer when checking for updates: %s", err)
	}
	if !ValidCommit(m.Commit) {
		return nil, errorf("unexpected answer when checking for updates: no valid version number")
	}
	for name, f := range m.Files {
		if !sha256Re.MatchString(f.SHA256) || f.Size <= 0 || f.Size > maxDownload {
			return nil, errorf("unexpected answer when checking for updates: bad entry for %s", name)
		}
	}
	return &m, nil
}

// Check downloads, unpacks and tests the newest version. It returns its commit, ready for
// Apply, or "" when this one is current or this copy does not update itself.
func (u *Updater) Check(ctx context.Context) (string, error) {
	if p := u.Pending(); p != "" {
		return p, nil
	}
	if u.Disabled() != "" {
		return "", nil
	}
	m, err := u.LatestManifest(ctx)
	if err != nil {
		return "", err
	}
	if m.Commit == u.Commit {
		return "", nil
	}
	name := ZipName(u.GOOS, u.GOARCH)
	digest, ok := m.Files[name]
	if !ok {
		return "", errorf("there is no update for this computer (%s/%s)", u.GOOS, u.GOARCH)
	}
	dctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	data, err := u.get(dctx, DownloadURL+name, digest.Size)
	if err != nil {
		return "", errorf("downloading the update failed: %s", err)
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != digest.Size || hex.EncodeToString(sum[:]) != digest.SHA256 {
		return "", errorf("the download is damaged, try again")
	}
	s, err := u.stage(data, m.Commit)
	if err != nil {
		return "", err
	}
	if err := u.smokeTest(ctx, s); err != nil {
		s.discard()
		return "", err
	}
	u.mu.Lock()
	u.pending, u.staged = m.Commit, s
	u.mu.Unlock()
	return m.Commit, nil
}

// staged is a new version unpacked next to the program, not yet in place.
type staged struct {
	folder string
	files  []string // slash-separated, relative to the root
	commit string
}

func (s *staged) discard() { os.RemoveAll(s.folder) }

// CheckPath accepts a slash-separated relative path that stays inside the root.
func CheckPath(name string) error {
	bad := name == "" || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/")
	for _, part := range strings.Split(name, "/") {
		bad = bad || part == "" || part == "." || part == ".."
	}
	if bad {
		return fmt.Errorf("unexpected path in the download: %q", name)
	}
	return nil
}

// fold compares paths the way case-insensitive file systems do (macOS, Windows).
func fold(name string) string { return strings.ToLower(name) }

// stage unpacks the zip into a new folder next to the root (same file system, so installing
// is a rename). Nothing under the root changes.
func (u *Updater) stage(data []byte, commit string) (*staged, error) {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errorf("the download is damaged, try again (%s)", err)
	}
	var entries []*zip.File
	seen := map[string]bool{}
	var total uint64
	for _, f := range archive.File {
		name := strings.TrimSuffix(f.Name, "/")
		if err := CheckPath(name); err != nil {
			return nil, &Error{err.Error()}
		}
		if f.FileInfo().IsDir() {
			continue
		}
		if !f.Mode().IsRegular() {
			return nil, errorf("unexpected file in the download: %q", name)
		}
		if seen[fold(name)] {
			return nil, errorf("unexpected path in the download: %q appears twice", name)
		}
		seen[fold(name)] = true
		total += f.UncompressedSize64
		if total > maxUnpacked {
			return nil, errorf("the download is larger than expected")
		}
		entries = append(entries, f)
	}
	if !seen[fold(u.Exe)] {
		return nil, errorf("the download does not look like uni-vpn")
	}
	folder, err := os.MkdirTemp(filepath.Dir(u.Root), stagePrefix)
	if err != nil {
		return nil, errorf("unpacking the update failed: %s", err)
	}
	s := &staged{folder: folder, commit: commit}
	for _, f := range entries {
		if err := unpack(f, filepath.Join(folder, filepath.FromSlash(f.Name))); err != nil {
			s.discard()
			if errors.Is(err, zip.ErrChecksum) || errors.Is(err, zip.ErrFormat) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil, errorf("the download is damaged, try again (%s)", err)
			}
			return nil, errorf("unpacking the update failed: %s", err)
		}
		s.files = append(s.files, f.Name)
	}
	return s, nil
}

func unpack(f *zip.File, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	r, err := f.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	// The archive's own size is checked by archive/zip while reading, like the checksum.
	_, err = io.Copy(out, io.LimitReader(r, int64(f.UncompressedSize64)+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	mode := fs.FileMode(0o644)
	if strings.HasPrefix(f.Name, "bin/") || f.Mode().Perm()&0o111 != 0 {
		mode = 0o755
	}
	// Explicitly: the service runs with umask 077.
	return os.Chmod(dest, mode)
}

// smokeTest: the new program must start and know its commit.
func (u *Updater) smokeTest(ctx context.Context, s *staged) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, err := u.Run(ctx, filepath.Join(s.folder, filepath.FromSlash(u.Exe)), "--version")
	if err != nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		detail := strings.TrimSpace(lines[len(lines)-1])
		if detail == "" {
			detail = err.Error()
		}
		return errorf("the new version does not start: %s", detail)
	}
	if !strings.Contains(string(out), s.commit) {
		return errorf("the download is not the requested version")
	}
	return nil
}

// Apply installs the pending version. It only moves files; the service calls it once the
// tunnel is idle and then restarts.
func (u *Updater) Apply() (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	s := u.staged
	u.pending, u.staged = "", nil
	if s == nil {
		return "", errorf("no update pending")
	}
	defer s.discard()
	if err := u.install(s); err != nil {
		return "", errorf("installing the update failed: %s", err)
	}
	return s.commit, nil
}

// Discard forgets the pending version.
func (u *Updater) Discard() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.staged != nil {
		u.staged.discard()
	}
	u.pending, u.staged = "", nil
}

func (u *Updater) path(rel string) string { return filepath.Join(u.Root, filepath.FromSlash(rel)) }

// install moves the files one by one: on Windows the running service has the program folder
// as its working directory, so the folder itself cannot be swapped.
func (u *Updater) install(s *staged) error {
	previous := u.InstalledFiles()
	// Listed before they are in place, so a crash halfway still leaves them known.
	if err := u.writeList(union(previous, s.files)); err != nil {
		return err
	}
	for _, rel := range s.files {
		if err := u.replace(filepath.Join(s.folder, filepath.FromSlash(rel)), u.path(rel)); err != nil {
			return err
		}
	}
	// Files an earlier version installed that this one no longer has.
	for _, rel := range previous {
		if !slices.ContainsFunc(s.files, func(f string) bool { return fold(f) == fold(rel) }) {
			if st, err := os.Lstat(u.path(rel)); err == nil && st.Mode().IsRegular() {
				_ = u.Files.Remove(u.path(rel))
			}
		}
	}
	if err := u.writeList(s.files); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(u.Root, CommitFile), []byte(s.commit+"\n"), 0o644)
}

// replace puts src in place of dest. A file Windows keeps open (the running program, the
// open app window) cannot be replaced, but it can be renamed: it moves to <name>.old and
// RemoveOld deletes it at the next start.
func (u *Updater) replace(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	err := u.Files.Rename(src, dest)
	if err == nil || !u.Windows || !exists(dest) {
		return err
	}
	aside := ""
	for _, candidate := range oldNames(dest) {
		if !exists(candidate) || u.Files.Remove(candidate) == nil {
			aside = candidate
			break
		}
	}
	if aside == "" {
		return err
	}
	if err := u.Files.Rename(dest, aside); err != nil {
		return err
	}
	if err := u.Files.Rename(src, dest); err != nil {
		_ = u.Files.Rename(aside, dest)
		return err
	}
	// Not in use after all (or no longer): no need to wait for the next start.
	_ = u.Files.Remove(aside)
	return nil
}

// oldNames are where a file in use goes. <name>.old may itself still be in use (by the
// supervisor of an earlier update), so there are a few more.
func oldNames(path string) []string {
	names := []string{path + ".old"}
	for i := 1; i <= 9; i++ {
		names = append(names, fmt.Sprintf("%s.%d.old", path, i))
	}
	return names
}

// RemoveOld deletes the files an earlier update moved out of the way. Call it at start.
func (u *Updater) RemoveOld() {
	if u.Root == "" {
		return
	}
	for _, rel := range u.InstalledFiles() {
		for _, old := range oldNames(u.path(rel)) {
			if st, err := os.Lstat(old); err == nil && st.Mode().IsRegular() {
				_ = u.Files.Remove(old)
			}
		}
	}
}

// InstalledFiles reads the list of files the updater installed; lines that are not a plain
// relative path are skipped.
func (u *Updater) InstalledFiles() []string {
	data, err := os.ReadFile(filepath.Join(u.Root, FilesList))
	if err != nil {
		return nil
	}
	var files []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && CheckPath(line) == nil {
			files = append(files, line)
		}
	}
	return files
}

func (u *Updater) writeList(files []string) error {
	path := filepath.Join(u.Root, FilesList)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(files, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func union(a, b []string) []string {
	out := append([]string(nil), a...)
	for _, f := range b {
		if !slices.ContainsFunc(out, func(g string) bool { return fold(g) == fold(f) }) {
			out = append(out, f)
		}
	}
	return out
}
