package wechat

import (
	"context"
	"fmt"
	"log"
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

var (
	qrPollInterval                = 2 * time.Second
	qrFallbackTimeout             = 4 * time.Minute
	qrMaxConsecutiveCheckFailures = 3
)

// qrLogin implements remoteapi.LoginProcess as a state machine:
//
//	Start -> display_and_wait(QR)
//	Wait  -> [progress...] -> complete | error
//
// This matches bridgev2's display-and-wait QR flow: the bot shows the QR and
// then blocks in Wait until the backend confirms the scan.
type qrLogin struct {
	c         *Client
	flow      qrFlow
	uuid      string
	expiresAt time.Time
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
	l.expiresAt = qrExpiryDeadline(qr.ExpiredTime, time.Now())

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
	ticker := time.NewTicker(qrPollInterval)
	defer ticker.Stop()
	if l.expiresAt.IsZero() {
		l.expiresAt = time.Now().Add(qrFallbackTimeout)
	}
	deadline := time.NewTimer(time.Until(l.expiresAt))
	defer deadline.Stop()
	failures := 0

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return &remoteapi.LoginStep{Type: remoteapi.LoginError, StepID: "wechat." + l.flow.ID,
				Err: fmt.Errorf("wechat: QR login expired")}, nil
		case <-ticker.C:
		}

		var chk checkQRResp
		if err := l.c.t.post(ctx, "/Login/LoginCheckQR", nil, q("uuid", l.uuid), &chk); err != nil {
			log.Printf("wechat login: check qr failed uuid=%s failures=%d err=%v", l.uuid, failures+1, err)
			if isFatalQRCheckError(err) {
				return &remoteapi.LoginStep{Type: remoteapi.LoginError, StepID: "wechat." + l.flow.ID, Err: err}, nil
			}
			failures++
			if failures >= qrMaxConsecutiveCheckFailures {
				return &remoteapi.LoginStep{Type: remoteapi.LoginError, StepID: "wechat." + l.flow.ID,
					Err: fmt.Errorf("wechat: QR status check failed %d times: %w", failures, err)}, nil
			}
			continue
		}
		failures = 0
		if chk.ExpiredTime > 0 {
			l.expiresAt = qrExpiryDeadline(int64(chk.ExpiredTime), time.Now())
			resetTimer(deadline, time.Until(l.expiresAt))
		}
		log.Printf("wechat login: check qr uuid=%s status=%d wxid=%s nickname=%s", l.uuid, chk.Status, chk.Wxid, chk.NickName)

		if chk.Status >= 2 && chk.Wxid != "" {
			l.c.mu.Lock()
			l.c.wxid = chk.Wxid
			l.c.mu.Unlock()

			if err := l.c.enablePostLoginHooks(ctx, chk.Wxid); err != nil {
				log.Printf("wechat login: post-login hooks failed wxid=%s err=%v", chk.Wxid, err)
			}

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

func (c *Client) RefreshLogin(ctx context.Context, id remoteapi.UserID) error {
	wxid := string(id)
	if wxid == "" {
		c.mu.RLock()
		wxid = c.wxid
		c.mu.RUnlock()
	}
	if wxid == "" {
		return fmt.Errorf("wechat: no account selected for twice login")
	}
	c.mu.Lock()
	c.wxid = wxid
	c.mu.Unlock()
	return c.enablePostLoginHooks(ctx, wxid)
}

func (c *Client) enablePostLoginHooks(ctx context.Context, wxid string) error {
	if err := c.t.post(ctx, "/Login/LoginTwiceAutoAuth", nil, q("wxid", wxid), nil); err != nil {
		return err
	}
	if err := c.t.post(ctx, "/Login/Newinit", nil, q("wxid", wxid), nil); err != nil {
		return err
	}
	if err := c.t.post(ctx, "/Login/AutoHeartBeat", nil, q("wxid", wxid), nil); err != nil {
		return err
	}
	log.Printf("wechat login: enabled post-login hooks for %s", wxid)
	return nil
}

func qrExpiryDeadline(expiredTime int64, now time.Time) time.Time {
	if expiredTime <= 0 {
		return now.Add(qrFallbackTimeout)
	}
	// Large values are Unix timestamps. Small values are durations in seconds.
	if expiredTime > 1_000_000_000 {
		return time.Unix(expiredTime, 0)
	}
	return now.Add(time.Duration(expiredTime) * time.Second)
}

func resetTimer(t *time.Timer, d time.Duration) {
	if d < 0 {
		d = 0
	}
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

func isFatalQRCheckError(err error) bool {
	apiErr, ok := err.(*APIError)
	if !ok {
		return false
	}
	if apiErr.Code == -8 {
		return true
	}
	return false
}

func (l *qrLogin) Cancel() {}
