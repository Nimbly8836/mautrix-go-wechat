package wechat

import (
	"context"
	"fmt"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

type qrFlow struct {
	ID          string
	Name        string
	Description string
	Endpoint    string
	LoginType   string
}

var wechatQRFlows = []qrFlow{
	{ID: "ipad", Name: "iPad QR", Description: "iPad protocol QR login", Endpoint: "/Login/LoginGetQR", LoginType: "ipad"},
	{ID: "ipad-x", Name: "iPad QR bypass", Description: "iPad QR bypass/verification channel", Endpoint: "/Login/LoginGetQRx", LoginType: "ipad"},
	{ID: "android-pad", Name: "Android Pad QR", Description: "Android Pad protocol QR login", Endpoint: "/Login/LoginGetQRPad", LoginType: "pad"},
	{ID: "android-pad-x", Name: "Android Pad QR bypass", Description: "Android Pad bypass/verification channel", Endpoint: "/Login/LoginGetQRPadx", LoginType: "pad"},
	{ID: "win", Name: "Windows QR", Description: "Windows desktop protocol QR login", Endpoint: "/Login/LoginGetQRWin", LoginType: "win"},
	{ID: "win-unified", Name: "Windows Unified QR", Description: "Unified Windows desktop protocol QR login", Endpoint: "/Login/LoginGetQRWinUnified", LoginType: "win"},
	{ID: "win-uwp", Name: "Windows UWP QR", Description: "Windows UWP bypass/verification channel", Endpoint: "/Login/LoginGetQRWinUwp", LoginType: "win"},
	{ID: "mac", Name: "macOS QR", Description: "macOS desktop protocol QR login", Endpoint: "/Login/LoginGetQRMac", LoginType: "mac"},
	{ID: "car", Name: "Car QR", Description: "Car protocol QR login", Endpoint: "/Login/LoginGetQRCar", LoginType: "car"},
}

// LoginFlows advertises the supported interactive login methods. These are
// WeChat protocol/client variants, not network names like QQ/WeChat.
func (c *Client) LoginFlows() []remoteapi.LoginFlow {
	flows := make([]remoteapi.LoginFlow, 0, len(wechatQRFlows))
	for _, f := range wechatQRFlows {
		flows = append(flows, remoteapi.LoginFlow{ID: f.ID, Name: f.Name, Description: f.Description})
	}
	return flows
}

// CreateLogin starts a login process for the chosen flow.
func (c *Client) CreateLogin(ctx context.Context, flowID string) (remoteapi.LoginProcess, error) {
	flow, ok := findQRFlow(flowID)
	if !ok {
		return nil, fmt.Errorf("wechat: unknown login flow %q", flowID)
	}
	return &qrLogin{c: c, flow: flow}, nil
}

func findQRFlow(flowID string) (qrFlow, bool) {
	if flowID == "" || flowID == "qr" {
		flowID = "ipad"
	}
	for _, f := range wechatQRFlows {
		if f.ID == flowID {
			return f, true
		}
	}
	return qrFlow{}, false
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
	flow qrFlow
	uuid string
}

var _ remoteapi.LoginProcess = (*qrLogin)(nil)

func (l *qrLogin) Start(ctx context.Context) (*remoteapi.LoginStep, error) {
	var qr getQRResp
	req := getQRReq{DeviceName: "mautrix-go-wechat", LoginType: l.flow.LoginType}
	if err := l.c.t.post(ctx, l.flow.Endpoint, req, nil, &qr); err != nil {
		return nil, err
	}
	if qr.Uuid == "" {
		return nil, fmt.Errorf("wechat: empty UUID from %s", l.flow.Endpoint)
	}
	l.uuid = qr.Uuid

	return &remoteapi.LoginStep{
		Type:         remoteapi.LoginDisplayAndWait,
		StepID:       "wechat." + l.flow.ID,
		Instructions: fmt.Sprintf("Scan this %s login QR code with WeChat, then confirm on your phone.", l.flow.Name),
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
			return &remoteapi.LoginStep{Type: remoteapi.LoginError, StepID: "wechat." + l.flow.ID,
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
