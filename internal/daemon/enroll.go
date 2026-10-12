package daemon

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/httpapi"
	"github.com/DavidVinu/uni-vpn/internal/i18n"
	"github.com/DavidVinu/uni-vpn/internal/linotp"
	"github.com/DavidVinu/uni-vpn/internal/messages"
	"github.com/DavidVinu/uni-vpn/internal/pyjson"
	"github.com/DavidVinu/uni-vpn/internal/socks"
	"github.com/DavidVinu/uni-vpn/internal/totp"
	unis "github.com/DavidVinu/uni-vpn/internal/universities"
)

// Automatic second factor (role model: Microsoft Authenticator's "sign in to add your work
// account"). With one code from the user's existing authenticator app, uni-vpn connects once,
// signs in to the university's MFA portal through the tunnel and adds itself there as a code
// generator, so the user never opens the portal (Profile.MFAEnroll). The picture guide stays
// the way in for everything this cannot do.

// Enrollment stages in status.json "enroll".
const (
	EnrollConnecting = "connecting" // signing in to the VPN with the typed code
	EnrollCode       = "code"       // the portal wants a (new) code from the user's app
	EnrollWorking    = "working"    // adding the code generator
	EnrollDone       = "done"
	EnrollFailed     = "failed"
)

var codeRE = regexp.MustCompile(`\A[0-9]{6,8}\z`)

// enrollTimeout bounds a whole run, including the user finding the next code.
var enrollTimeout = 10 * time.Minute

type enrollJob struct {
	stage  string
	err    *i18n.Text // why it failed, or a hint at the code stage
	codes  chan string
	cancel context.CancelFunc
}

func (j *enrollJob) running() bool { return j.stage != EnrollDone && j.stage != EnrollFailed }

// PortalClient opens the MFA portal; dial reaches it through the tunnel.
type PortalClient func(portal string, dial func(ctx context.Context, network, address string) (net.Conn, error)) (*linotp.Client, error)

func (d *Daemon) enrollStatus() any {
	job := d.enrollJob // mu held
	if job == nil {
		return nil
	}
	var text, key any
	if job.err != nil {
		text, key = job.err.String(), job.err.JSON()
	}
	return pyjson.O("stage", job.stage, "error", text, "error_t", key)
}

// Enroll is /api/totp/auto: start the automatic setup with a code from the user's app, give a
// waiting setup the next code, or (cancel) stop it.
func (d *Daemon) Enroll(code string, cancel bool) error {
	d.mu.Lock()
	job := d.enrollJob
	if cancel {
		d.onceCode = ""
		if job != nil && job.running() {
			job.cancel()
		}
		d.enrollJob = nil
		d.mu.Unlock()
		return nil
	}
	code = strings.Join(strings.Fields(code), "")
	if !codeRE.MatchString(code) {
		d.mu.Unlock()
		return httpapi.TextValueError(i18n.T("enroll.enter_code"))
	}
	if job != nil && job.running() {
		if job.stage == EnrollCode {
			select {
			case job.codes <- code:
				job.stage, job.err = EnrollWorking, nil
				d.changedLocked()
			default:
			}
		}
		d.mu.Unlock()
		return nil
	}
	profile, ok := unis.Get(d.cfg.University)
	if d.needsSetup || d.configError != "" || !ok || profile.MFAEnroll != "linotp" || !d.cfg.NeedsTOTP() ||
		profile.MFAPortalURL == "" {
		d.mu.Unlock()
		return httpapi.TextValueError(i18n.T("enroll.not_here"))
	}
	d.startEnrollLocked(code, profile.MFAPortalURL)
	connected := d.state == Connected
	d.mu.Unlock()
	if !connected {
		d.secretsChanged() // connects with the typed code, also instead of an attempt running
	}
	return nil
}

// startEnrollLocked runs a new setup with this code; mu held. Connected, the code signs in to
// the portal; otherwise it signs in to the VPN first.
func (d *Daemon) startEnrollLocked(code, portal string) {
	if d.enrollJob != nil && d.enrollJob.running() {
		d.enrollJob.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), enrollTimeout)
	job := &enrollJob{stage: EnrollConnecting, codes: make(chan string, 1), cancel: cancel}
	if d.state == Connected {
		job.codes <- code
		job.stage = EnrollWorking
	} else {
		d.onceCode = code
	}
	d.enrollJob = job
	d.changedLocked()
	go d.runEnroll(ctx, job, portal)
}

// changedLocked wakes status watchers without a state change; mu held.
func (d *Daemon) changedLocked() {
	close(d.changed)
	d.changed = make(chan struct{})
}

func (d *Daemon) enrollStage(job *enrollJob, stage string, err *i18n.Text) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.enrollJob != job || !job.running() {
		return
	}
	job.stage, job.err = stage, err
	d.changedLocked()
}

func textOf(key string) *i18n.Text {
	t := i18n.T(key)
	return &t
}

func messageText(m messages.Message) *i18n.Text { return textOf(messages.KeyPrefix + m.ID) }

func (d *Daemon) runEnroll(ctx context.Context, job *enrollJob, portal string) {
	defer job.cancel()
	err := d.enroll(ctx, job, portal)
	if err == nil {
		d.logf(slog.LevelInfo, "Added a code generator in the MFA portal")
		d.enrollStage(job, EnrollDone, nil)
		return
	}
	d.logf(slog.LevelWarn, "Automatic second factor setup failed: %s", err)
	d.mu.Lock()
	if d.enrollJob == job {
		d.onceCode = ""
	}
	d.mu.Unlock()
	var t *enrollError
	if errors.As(err, &t) {
		d.enrollStage(job, EnrollFailed, t.text)
	} else {
		d.enrollStage(job, EnrollFailed, textOf("enroll.failed"))
	}
}

// enrollError is a failure with the text the user sees.
type enrollError struct {
	text  *i18n.Text
	cause string
}

func (e *enrollError) Error() string { return e.cause }

func failWith(text *i18n.Text, cause string) error { return &enrollError{text, cause} }

// waitConnected waits until the tunnel is up; a refused sign-in ends the setup with the reason.
func (d *Daemon) waitConnected(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for {
		switch {
		case d.state == Connected:
			return nil
		case d.state.final():
			if d.message == messages.TOTPRejected {
				return failWith(textOf("enroll.code_wrong"), "the VPN refused the code")
			}
			return failWith(messageText(d.message), d.message.Text)
		case d.state == Idle && !d.loopRunning && d.onceCode == "":
			return failWith(textOf("enroll.failed"), "stopped before connecting")
		}
		changed := d.changed
		d.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			d.mu.Lock()
			message := d.message
			if d.state.retry() {
				return failWith(messageText(message), "not connected: "+message.Text)
			}
			return failWith(textOf("enroll.failed"), "timed out")
		}
		d.mu.Lock()
	}
}

// description names the token in the portal's list, so the user knows what it is.
func description() string {
	name, _ := os.Hostname()
	name, _, _ = strings.Cut(name, ".")
	clean := strings.Map(func(r rune) rune {
		if r < 0x80 && (r == '-' || r == ' ' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z') {
			return r
		}
		return -1
	}, name)
	if len(clean) > 30 {
		clean = clean[:30]
	}
	if clean == "" {
		return "Uni VPN"
	}
	return "Uni VPN " + clean
}

func (d *Daemon) enroll(ctx context.Context, job *enrollJob, portal string) error {
	if err := d.waitConnected(ctx); err != nil {
		return err
	}
	d.mu.Lock()
	user, socksPort := d.cfg.User, d.cfg.SocksPort
	d.mu.Unlock()
	password, err := d.opts.PasswordGetter(ctx)
	if err != nil {
		return failWith(messageText(messages.PasswordUnreadable), err.Error())
	}
	newClient := d.opts.PortalClient
	if newClient == nil {
		newClient = func(portal string, dial func(context.Context, string, string) (net.Conn, error)) (*linotp.Client, error) {
			return linotp.New(portal, dial)
		}
	}
	// Through this daemon's own SOCKS port: the portal only answers from inside, and the
	// connection counts as use, so the tunnel stays up meanwhile.
	proxy := net.JoinHostPort("127.0.0.1", strconv.Itoa(socksPort))
	client, err := newClient(portal, func(ctx context.Context, _, address string) (net.Conn, error) {
		return socks.Dial(ctx, proxy, address)
	})
	if err != nil {
		return err
	}
	if err := d.portalSignIn(ctx, job, client, user, string(password)); err != nil {
		return err
	}
	defer func() {
		logout, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		client.Logout(logout)
	}()
	d.enrollStage(job, EnrollWorking, nil)
	serial, address, err := client.Enroll(ctx, description())
	if err != nil {
		var refused *linotp.RefusedError
		if errors.As(err, &refused) {
			return failWith(textOf("enroll.refused"), err.Error())
		}
		return err
	}
	if err := d.keepToken(ctx, client, serial, address); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if derr := client.Delete(cleanup, serial); derr != nil {
			d.logf(slog.LevelWarn, "Could not remove the unused code generator %s: %s", serial, derr)
		}
		return err
	}
	return nil
}

// portalSignIn: password, then a code from the user's app when the portal asks for one. The
// code that opened the VPN is used up, so the user types the next one.
func (d *Daemon) portalSignIn(ctx context.Context, job *enrollJob, client *linotp.Client, user, password string) error {
	for tries := 0; ; tries++ {
		needsCode, err := client.Login(ctx, user, password)
		if errors.Is(err, linotp.ErrPassword) {
			// The VPN took the same password: the portal wants something else (another realm).
			return failWith(textOf("enroll.failed"), err.Error())
		}
		if err != nil {
			return err
		}
		if !needsCode {
			return nil
		}
		var code string
		select {
		case code = <-job.codes:
		default:
			hint := textOf("enroll.next_code")
			if tries > 0 {
				hint = textOf("enroll.code_wrong")
			}
			d.enrollStage(job, EnrollCode, hint)
			select {
			case code = <-job.codes:
			case <-ctx.Done():
				return failWith(textOf("enroll.failed"), "no code typed")
			}
		}
		err = client.Code(ctx, code)
		if err == nil {
			return nil
		}
		if !errors.Is(err, linotp.ErrCode) || tries >= 4 {
			return err
		}
	}
}

// keepToken tests the new code generator like the portal's "Testen" step and stores it.
func (d *Daemon) keepToken(ctx context.Context, client *linotp.Client, serial, address string) error {
	secret, err := totp.Normalize(address)
	if err != nil {
		return err
	}
	code, err := totp.CodeAt(secret, time.Now())
	if err != nil {
		return err
	}
	if _, err := client.Verify(ctx, serial, code); err != nil {
		return err
	}
	// SetTOTP notes this code window as used: the next sign-in waits for a new code.
	if err := d.SetTOTP(secret); err != nil {
		return failWith(textOf("enroll.failed"), err.Error())
	}
	return nil
}
