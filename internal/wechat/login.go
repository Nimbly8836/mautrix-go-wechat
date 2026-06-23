package wechat

import (
	"context"
	"fmt"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

// LoginFlows advertises the supported interactive login methods. Mirrors the
// bridgev2 pattern used by mautrix-telegram (which offers e.g. "qr" + "phone").
func (c *Client) LoginFlows() []remoteapi.LoginFlow {
	return []remoteapi.LoginFlow{
		{
			ID:          "qr",
			Name:        "Scan QR code",
			Description: "Log in by scanning a QR code with the WeChat app (iPad mode).",
		},
	}
}

// CreateLogin starts a login process for the chosen flow.
func (c *Client) CreateLogin(ctx context.Context, flowID string) (remoteapi.LoginProcess, error) {
	switch flowID {
	case "", "qr":
		return &qrLogin{c: c}, nil
	default:
		return nil, fmt.Errorf("wechat: unknown login flow %q", flowID)
	}
}

// qrLogin implements remoteapi.LoginProcess as a state machine:
//
//	Start -> display_and_wait(QR)
//	Wait  -> [progress...] -> complete | error
//
// This matches bridgev2's display-and-wait QR flow: the bot shows the QR and
// then blocks in Wait until the backend confirms the scan.
type qrLogin struct {
	c    *Client
	uuid string
}

var _ remoteapi.LoginProcess = (*qrLogin)(nil)

func (l *qrLogin) Start(ctx context.Context) (*remoteapi.LoginStep, error) {
	var qr getQRResp
	req := getQRReq{DeviceName: "mautrix-go-wechat", LoginType: "ipad"}
	if err := l.c.t.post(ctx, "/Login/LoginGetQR", req, nil, &qr); err != nil {
		return nil, err
	}
	if qr.Uuid == "" {
		return nil, fmt.Errorf("wechat: empty UUID from LoginGetQR")
	}
	l.uuid = qr.Uuid

	return &remoteapi.LoginStep{
		Type:         remoteapi.LoginDisplayAndWait,
		StepID:       "wechat.qr",
		Instructions: "Scan this QR code with WeChat, then confirm on your phone.",
		Display: &remoteapi.LoginDisplay{
			Type: "qr",
			Data: pick(qr.QRBase, pick(qr.QRCode, qr.BaseUrl)),
		},
	}, nil
}

// SubmitUserInput is unused for the QR flow (no input fields). Kept to satisfy
// the interface; a phone/code flow would implement it.
func (l *qrLogin) SubmitUserInput(ctx context.Context, _ map[string]string) (*remoteapi.LoginStep, error) {
	return nil, fmt.Errorf("wechat: qr flow takes no user input")
}

// Wait blocks until the QR is scanned and confirmed, then completes the login.
func (l *qrLogin) Wait(ctx context.Context) (*remoteapi.LoginStep, error) {
	if l.uuid == "" {
		return nil, fmt.Errorf("wechat: Wait called before Start")
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	deadline := time.After(4 * time.Minute)

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			return &remoteapi.LoginStep{Type: remoteapi.LoginError, StepID: "wechat.qr",
				Err: fmt.Errorf("wechat: QR login timed out")}, nil
		case <-ticker.C:
		}

		var chk checkQRResp
		if err := l.c.t.post(ctx, "/Login/LoginCheckQR", nil, q("uuid", l.uuid), &chk); err != nil {
			continue // transient; keep polling
		}

		if chk.Status >= 2 && chk.Wxid != "" {
			l.c.mu.Lock()
			l.c.wxid = chk.Wxid
			l.c.mu.Unlock()

			// prime the sync key for the message loop.
			_ = l.c.t.post(ctx, "/Login/Newinit", nil, q("wxid", chk.Wxid), nil)

			return &remoteapi.LoginStep{
				Type:         remoteapi.LoginComplete,
				StepID:       "wechat.complete",
				Instructions: "Successfully logged in.",
				Self:         remoteapi.UserID(chk.Wxid),
			}, nil
		}
		// chk.Status == 1 means "scanned, awaiting confirm"; keep waiting.
	}
}

func (l *qrLogin) Cancel() {}
