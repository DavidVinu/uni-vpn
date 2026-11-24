//go:build darwin && !ios

package guard

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/DavidVinu/uni-vpn/internal/atomicfile"
	"github.com/DavidVinu/uni-vpn/internal/sockowner"
	"github.com/DavidVinu/uni-vpn/internal/sysproxy"
)

// Run is the command "uni-vpn-core guard": no argument runs the guard (the LaunchDaemon), and
// "--app" one that removes itself once the app has gone to the Trash; "install" puts this
// program in place as the guard and starts it, "remove" switches our rule off and removes the
// guard. All of them as root.
func Run(args []string) int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "uni-vpn guard: needs root")
		return 1
	}
	var err error
	switch {
	case len(args) == 0:
		err = serve(false)
	case len(args) == 1 && args[0] == "--app":
		err = serve(true)
	case len(args) == 1 && args[0] == "install":
		err = install()
	case len(args) == 1 && args[0] == "remove":
		err = remove(false)
	default:
		err = errors.New("usage: uni-vpn guard [install|remove]")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "uni-vpn guard:", err)
		return 1
	}
	return 0
}

func world(log *slog.Logger) World {
	return World{
		Console:  console,
		Listener: sockowner.ListenerOwner,
		Run:      sysproxy.Exec,
		Now:      time.Now,
		Save: func(data []byte) error {
			if err := os.MkdirAll(filepath.Dir(StatePath), 0o755); err != nil {
				return err
			}
			return atomicfile.Write(StatePath, data)
		},
		Log: log,
	}
}

// console is the account at the screen: the owner of /dev/console (root at the login window).
func console() (uint32, bool) {
	st, err := os.Stat("/dev/console")
	if err != nil {
		return 0, false
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return sys.Uid, true
}

// peer is the account of the program at the other end of a Unix socket.
func peer(c net.Conn) (uint32, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, errors.New("not a Unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Xucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, credErr
	}
	return cred.Uid, nil
}

func isFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

// serve runs the guard; app: it came with the app, and ends once the app is in the Trash. One
// set up without the app (get.sh, a clone) ends once no account wants the rule any more.
func serve(app bool) error {
	if st, err := os.Stat(LogPath); err == nil && st.Size() > 1<<20 {
		_ = os.Truncate(LogPath, 0)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	data, _ := os.ReadFile(StatePath)
	g := New(world(log), data)
	os.Remove(sysproxy.GuardSocket)
	l, err := net.Listen("unix", sysproxy.GuardSocket)
	if err != nil {
		return err
	}
	// Every account may ask; the socket's credentials name it.
	if err := os.Chmod(sysproxy.GuardSocket, 0o666); err != nil {
		return err
	}
	log.Info("Guard started")
	go func() { _ = g.Serve(l, peer) }()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	ticker := time.NewTicker(Tick)
	defer ticker.Stop()
	var missing time.Time
	for {
		select {
		case <-stop:
			l.Close()
			os.Remove(sysproxy.GuardSocket)
			return nil
		case <-ticker.C:
		}
		g.Look()
		// Dragged to the Trash: each account's core removes itself, and so does the guard.
		switch {
		case !app:
			if g.Abandoned() {
				log.Info("No account wants the rule: removing the guard")
				l.Close()
				g.Off()
				return remove(true)
			}
		case isFile(PackageCore):
			missing = time.Time{}
		case missing.IsZero():
			missing = time.Now()
		case time.Since(missing) >= Gone:
			log.Info("The app is gone: removing the guard")
			l.Close()
			g.Off()
			return remove(true)
		}
	}
}

// install copies this program to Helper, writes the LaunchDaemon, adopts the accounts set up
// before it and starts it.
func install() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(Helper), 0o755); err != nil {
		return err
	}
	if err := copyFile(exe, Helper, 0o755); err != nil {
		return err
	}
	if err := writeFile(PlistPath, []byte(Plist(isFile(PackageCore))), 0o644); err != nil {
		return err
	}
	_ = exec.Command("/bin/launchctl", "bootout", "system/"+Label).Run()
	// Accounts set up before the guard: their cores may never ask for the rule.
	data, _ := os.ReadFile(StatePath)
	New(world(slog.New(slog.NewTextHandler(io.Discard, nil))), data).Adopt(servicesIn("/Users"))
	// Right after bootout the old guard may still be going away ("Bootstrap failed: 5"), as for
	// the service (internal/service): a .pkg over this one, or Repair replacing an older guard.
	for attempt := 0; ; attempt++ {
		out, err := exec.Command("/bin/launchctl", "bootstrap", "system", PlistPath).CombinedOutput()
		if err == nil {
			break
		}
		if attempt == 4 {
			return fmt.Errorf("launchctl bootstrap: %v: %s", err, out)
		}
		time.Sleep(time.Second)
	}
	for range 50 {
		if _, err := sysproxy.AskGuard("state"); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("the guard does not answer")
}

// remove switches our rule off and removes the guard's files and its log; running is true for
// the guard itself, which launchctl then ends.
func remove(running bool) error {
	if !running {
		_ = exec.Command("/bin/launchctl", "bootout", "system/"+Label).Run()
		data, _ := os.ReadFile(StatePath)
		New(world(slog.New(slog.NewTextHandler(io.Discard, nil))), data).Off()
	}
	for _, path := range []string{StatePath, sysproxy.GuardSocket, PlistPath, Helper, LogPath} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	os.Remove(filepath.Dir(StatePath))
	if running {
		return exec.Command("/bin/launchctl", "bootout", "system/"+Label).Run()
	}
	return nil
}

// copyFile writes src's bytes to dst in one step, owned by root.
func copyFile(src, dst string, mode os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return writeFile(dst, data, mode)
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".new"
	os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, mode)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}
