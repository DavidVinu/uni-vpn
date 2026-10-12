package linotp_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DavidVinu/uni-vpn/internal/linotp"
	"github.com/DavidVinu/uni-vpn/internal/linotp/fakelinotp"
	"github.com/DavidVinu/uni-vpn/internal/totp"
)

func client(t *testing.T, p *fakelinotp.Portal) *linotp.Client {
	t.Helper()
	c, err := linotp.New(p.URL+"/selfservice/de/", nil)
	if err != nil {
		t.Fatal(err)
	}
	c.SetHTTPClient(p.Client())
	return c
}

func TestEnrollLikeThePortalsOwnPages(t *testing.T) {
	p := fakelinotp.Start()
	defer p.Close()
	c := client(t, p)
	ctx := context.Background()
	needs, err := c.Login(ctx, "ab123", "pw")
	if err != nil || !needs {
		t.Fatal(needs, err)
	}
	if err := c.Code(ctx, "111111"); err != nil || p.Challenged != 1 {
		t.Fatal(err, p.Challenged)
	}
	serial, url, err := c.Enroll(ctx, "Uni VPN")
	if err != nil {
		t.Fatal(err)
	}
	secret, err := totp.Normalize(url)
	if err != nil || secret != p.Tokens[serial] {
		t.Fatal(secret, err)
	}
	code, _ := totp.CodeAt(secret, time.Now())
	if tested, err := c.Verify(ctx, serial, code); !tested || err != nil {
		t.Fatal(tested, err)
	}
	// LinOTP takes each code once.
	if _, err := c.Verify(ctx, serial, code); !errors.Is(err, linotp.ErrCode) {
		t.Fatal(err)
	}
	c.Logout(ctx)
	if _, _, err := c.Enroll(ctx, "x"); err == nil {
		t.Fatal("signed out")
	}
}

func TestSignInMistakes(t *testing.T) {
	p := fakelinotp.Start()
	defer p.Close()
	c := client(t, p)
	ctx := context.Background()
	if _, err := c.Login(ctx, "ab123", "wrong"); !errors.Is(err, linotp.ErrPassword) {
		t.Fatal(err)
	}
	if _, err := c.Login(ctx, "ab123", "pw"); err != nil {
		t.Fatal(err)
	}
	if err := c.Code(ctx, "222222"); !errors.Is(err, linotp.ErrCode) {
		t.Fatal(err)
	}
	// Without a token the password is enough.
	p.Code = ""
	if needs, err := c.Login(ctx, "ab123", "pw"); needs || err != nil {
		t.Fatal(needs, err)
	}
}

func TestPortalWithoutTestStepAndRefusedEnroll(t *testing.T) {
	p := fakelinotp.Start()
	defer p.Close()
	p.Code, p.NoVerify = "", true
	c := client(t, p)
	ctx := context.Background()
	c.Login(ctx, "ab123", "pw")
	serial, _, err := c.Enroll(ctx, "Uni VPN")
	if err != nil {
		t.Fatal(err)
	}
	if tested, err := c.Verify(ctx, serial, "000000"); tested || err != nil {
		t.Fatal(tested, err)
	}
	if tokens, err := c.Tokens(ctx); err != nil || len(tokens) != 1 || tokens[0] != (linotp.Token{Serial: serial, Description: "Uni VPN"}) {
		t.Fatal(tokens, err)
	}
	if err := c.Delete(ctx, serial); err != nil || len(p.Tokens) != 0 {
		t.Fatal(err, p.Tokens)
	}
	p.RefuseEnroll = "token limit reached"
	var refused *linotp.RefusedError
	if _, _, err := c.Enroll(ctx, "Uni VPN"); !errors.As(err, &refused) || refused.Message != "token limit reached" {
		t.Fatal(err)
	}
}

func TestOnlyHTTPS(t *testing.T) {
	if _, err := linotp.New("http://mfa.example.edu/", nil); err == nil {
		t.Fatal("plain http")
	}
}
