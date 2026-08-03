package matrixas

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/wechat/textemoji"
)

const testBuiltinEmojiAssetPrefix = "/airinghost/wechat-emoji/2896eb55348041f5c5a66e4eccfb4882ab9cda0c/web.wechat.com/compressed-cwebp/lossless-z9/"

var expectedBuiltinEmojiAssetFilenames = []string{
	"001_微笑.webp",
	"002_撇嘴.webp",
	"003_色.webp",
	"004_发呆.webp",
	"005_得意.webp",
	"006_流泪.webp",
	"007_害羞.webp",
	"008_闭嘴.webp",
	"009_睡.webp",
	"010_大哭.webp",
	"011_尴尬.webp",
	"012_发怒.webp",
	"013_调皮.webp",
	"014_呲牙.webp",
	"015_惊讶.webp",
	"016_难过.webp",
	"017_囧.webp",
	"018_抓狂.webp",
	"019_吐.webp",
	"020_偷笑.webp",
	"021_愉快.webp",
	"022_白眼.webp",
	"023_傲慢.webp",
	"024_困.webp",
	"025_惊恐.webp",
	"026_憨笑.webp",
	"027_悠闲.webp",
	"028_咒骂.webp",
	"029_疑问.webp",
	"030_嘘.webp",
	"031_晕.webp",
	"032_衰.webp",
	"033_骷髅.webp",
	"034_敲打.webp",
	"035_再见.webp",
	"036_擦汗.webp",
	"037_抠鼻.webp",
	"038_鼓掌.webp",
	"039_坏笑.webp",
	"040_右哼哼.webp",
	"041_鄙视.webp",
	"042_委屈.webp",
	"043_快哭了.webp",
	"044_阴险.webp",
	"045_亲亲.webp",
	"046_可怜.webp",
	"047_笑脸.webp",
	"048_生病.webp",
	"049_脸红.webp",
	"050_破涕为笑.webp",
	"051_恐惧.webp",
	"052_失望.webp",
	"053_无语.webp",
	"054_嘿哈.webp",
	"055_捂脸.webp",
	"056_奸笑.webp",
	"057_机智.webp",
	"058_皱眉.webp",
	"059_耶.webp",
	"060_吃瓜.webp",
	"061_加油.webp",
	"062_汗.webp",
	"063_天啊.webp",
	"064_Emm.webp",
	"065_社会社会.webp",
	"066_旺柴.webp",
	"067_好的.webp",
	"068_打脸.webp",
	"069_哇.webp",
	"070_翻白眼.webp",
	"071_666.webp",
	"072_让我看看.webp",
	"073_叹气.webp",
	"074_苦涩.webp",
	"075_裂开.webp",
	"076_嘴唇.webp",
	"077_爱心.webp",
	"078_心碎.webp",
	"079_拥抱.webp",
	"080_强.webp",
	"081_弱.webp",
	"082_握手.webp",
	"083_胜利.webp",
	"084_抱拳.webp",
	"085_勾引.webp",
	"086_拳头.webp",
	"087_OK.webp",
	"088_合十.webp",
	"089_啤酒.webp",
	"090_咖啡.webp",
	"091_蛋糕.webp",
	"092_玫瑰.webp",
	"093_凋谢.webp",
	"094_菜刀.webp",
	"095_炸弹.webp",
	"096_便便.webp",
	"097_月亮.webp",
	"098_太阳.webp",
	"099_庆祝.webp",
	"100_礼物.webp",
	"101_红包.webp",
	"102_發.webp",
	"103_福.webp",
	"104_烟花.webp",
	"105_爆竹.webp",
	"106_猪头.webp",
	"107_跳跳.webp",
	"108_发抖.webp",
	"109_转圈.webp",
}

func TestBuiltinEmojiAssetURLAllRegistryEntries(t *testing.T) {
	entries := textemoji.Entries()
	if len(entries) != 109 || len(expectedBuiltinEmojiAssetFilenames) != 109 {
		t.Fatalf("registry/test fixture lengths = %d/%d, want 109/109", len(entries), len(expectedBuiltinEmojiAssetFilenames))
	}

	for i, entry := range entries {
		entry := entry
		filename := expectedBuiltinEmojiAssetFilenames[i]
		t.Run(fmt.Sprintf("%03d_%s", entry.Index, entry.Chinese), func(t *testing.T) {
			if entry.Filename != filename {
				t.Fatalf("registry filename = %q, want exact %q", entry.Filename, filename)
			}

			got, err := builtinEmojiAssetURL(entry)
			if err != nil {
				t.Fatalf("builtinEmojiAssetURL() error: %v", err)
			}

			wantPath := testBuiltinEmojiAssetPrefix + filename
			wantEscapedPath := testBuiltinEmojiAssetPrefix + url.PathEscape(filename)
			wantURL := "https://raw.githubusercontent.com" + wantEscapedPath
			if got.Path != wantPath {
				t.Fatalf("decoded path = %q, want %q", got.Path, wantPath)
			}
			if got.EscapedPath() != wantEscapedPath {
				t.Fatalf("escaped path = %q, want %q", got.EscapedPath(), wantEscapedPath)
			}
			if got.String() != wantURL {
				t.Fatalf("URL = %q, want %q", got.String(), wantURL)
			}
			if strings.Contains(got.String(), "%25") {
				t.Fatalf("URL path was escaped more than once: %q", got.String())
			}
			if got.Scheme != "https" || got.Host != "raw.githubusercontent.com" || got.Hostname() != "raw.githubusercontent.com" {
				t.Fatalf("unexpected fixed origin: scheme=%q host=%q hostname=%q", got.Scheme, got.Host, got.Hostname())
			}
			if got.Port() != "" || got.User != nil || got.RawQuery != "" || got.ForceQuery || got.Fragment != "" || got.RawFragment != "" {
				t.Fatalf("URL contains forbidden authority/query/fragment fields: %#v", got)
			}
		})
	}
}

func TestBuiltinEmojiAssetURLRejectsNonRegistryEntries(t *testing.T) {
	valid := textemoji.Entries()[0]
	tests := map[string]textemoji.Entry{
		"zero index":       withBuiltinEmojiEntryIndex(valid, 0),
		"wrong index":      withBuiltinEmojiEntryIndex(valid, 2),
		"hostile Chinese":  withBuiltinEmojiEntryChinese(valid, "../secret?token=x"),
		"wrong English":    withBuiltinEmojiEntryEnglish(valid, "Not Smile"),
		"wrong shortcode":  withBuiltinEmojiEntryShortcode(valid, "wechat_not_smile"),
		"hostile filename": withBuiltinEmojiEntryFilename(valid, "../../secret.webp"),
	}
	for name, entry := range tests {
		t.Run(name, func(t *testing.T) {
			if got, err := builtinEmojiAssetURL(entry); err == nil {
				t.Fatalf("builtinEmojiAssetURL(%#v) = %v, want error", entry, got)
			}
		})
	}
}

func TestBuiltinEmojiRedirectPolicyRejectsUnsafeTargets(t *testing.T) {
	initial := mustBuiltinEmojiAssetURL(t, textemoji.Entries()[0])
	otherAsset := mustBuiltinEmojiAssetURL(t, textemoji.Entries()[1])
	tests := map[string]string{
		"HTTP downgrade":  "http://raw.githubusercontent.com" + initial.EscapedPath(),
		"hostile host":    "https://evil.example" + initial.EscapedPath(),
		"unexpected port": "https://raw.githubusercontent.com:443" + initial.EscapedPath(),
		"userinfo":        "https://user:secret@raw.githubusercontent.com" + initial.EscapedPath(),
		"query":           initial.String() + "?token=secret",
		"fragment":        initial.String() + "#secret",
		"wrong path":      "https://raw.githubusercontent.com/airinghost/wechat-emoji/main/README.md",
		"other asset":     otherAsset.String(),
	}
	for name, location := range tests {
		t.Run(name, func(t *testing.T) {
			var redirectBody *trackedReadCloser
			calls := 0
			client := newBuiltinEmojiHTTPClient(builtinEmojiRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				redirectBody = newTrackedReadCloser(nil)
				return redirectResponse(req, location, redirectBody), nil
			}))

			resp, err := client.Get(initial.String())
			if resp != nil {
				resp.Body.Close()
			}
			if err == nil {
				t.Fatalf("client accepted unsafe redirect %q", location)
			}
			if calls != 1 {
				t.Fatalf("RoundTrip calls = %d, want 1 before redirect rejection", calls)
			}
			if redirectBody == nil || !redirectBody.closed.Load() {
				t.Fatal("redirect response body was not closed")
			}
		})
	}
}

func TestBuiltinEmojiRedirectPolicyValidatesEveryTarget(t *testing.T) {
	initial := mustBuiltinEmojiAssetURL(t, textemoji.Entries()[0])
	calls := 0
	client := newBuiltinEmojiHTTPClient(builtinEmojiRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return redirectResponse(req, initial.String(), newTrackedReadCloser(nil)), nil
		}
		return redirectResponse(req, "https://evil.example"+initial.EscapedPath(), newTrackedReadCloser(nil)), nil
	}))

	resp, err := client.Get(initial.String())
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("client accepted hostile second redirect")
	}
	if calls != 2 {
		t.Fatalf("RoundTrip calls = %d, want 2", calls)
	}
}

func TestBuiltinEmojiRedirectPolicyAllowsThreeRedirects(t *testing.T) {
	initial := mustBuiltinEmojiAssetURL(t, textemoji.Entries()[0])
	calls := 0
	client := newBuiltinEmojiHTTPClient(builtinEmojiRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls <= 3 {
			return redirectResponse(req, initial.String(), newTrackedReadCloser(nil)), nil
		}
		return testHTTPResponse(req, http.StatusOK, "image/webp", validWebP(12), nil), nil
	}))

	resp, err := client.Get(initial.String())
	if err != nil {
		t.Fatalf("three redirects returned error: %v", err)
	}
	defer resp.Body.Close()
	if calls != 4 {
		t.Fatalf("RoundTrip calls = %d, want 4", calls)
	}
}

func TestBuiltinEmojiRedirectPolicyRejectsFourthRedirect(t *testing.T) {
	initial := mustBuiltinEmojiAssetURL(t, textemoji.Entries()[0])
	calls := 0
	client := newBuiltinEmojiHTTPClient(builtinEmojiRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return redirectResponse(req, initial.String(), newTrackedReadCloser(nil)), nil
	}))

	resp, err := client.Get(initial.String())
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("client accepted a fourth redirect")
	}
	if calls != 4 {
		t.Fatalf("RoundTrip calls = %d, want 4 before rejection", calls)
	}
}

func TestBuiltinEmojiAssetFetchClassifiesRedirectRejectionWithoutLeakingURL(t *testing.T) {
	entry := textemoji.Entries()[0]
	initial := mustBuiltinEmojiAssetURL(t, entry)
	redirectBody := newTrackedReadCloser(nil)
	fetcher := newTestBuiltinEmojiFetcher(builtinEmojiRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return redirectResponse(req, "https://evil.example"+initial.EscapedPath()+"?token=secret", redirectBody), nil
	}), time.Second)

	_, err := fetcher.Fetch(context.Background(), entry)
	requireBuiltinEmojiFetchError(t, err, builtinEmojiFetchRedirectRejected)
	assertFetchErrorExcludes(t, err, "evil.example", "token", "secret")
	if !redirectBody.closed.Load() {
		t.Fatal("redirect response body was not closed")
	}
}

func TestBuiltinEmojiAssetFetchUsesGETAndIndependentDeadline(t *testing.T) {
	entry := textemoji.Entries()[0]
	body := validWebP(24)
	var sawDeadline time.Duration
	fetcher := newTestBuiltinEmojiFetcher(builtinEmojiRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			t.Fatalf("method = %q, want GET", req.Method)
		}
		assertProductionBuiltinEmojiRequest(t, req, entry)
		deadline, ok := req.Context().Deadline()
		if !ok {
			t.Fatal("request context has no deadline")
		}
		sawDeadline = time.Until(deadline)
		return testHTTPResponse(req, http.StatusOK, "image/webp", body, nil), nil
	}), 2*time.Second)

	got, err := fetcher.Fetch(context.Background(), entry)
	if err != nil {
		t.Fatalf("Fetch() error: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("Fetch() body = %x, want %x", got, body)
	}
	if sawDeadline <= 0 || sawDeadline > 2*time.Second {
		t.Fatalf("child deadline remaining = %v, want within (0, 2s]", sawDeadline)
	}
}

func TestBuiltinEmojiAssetFetchDefaultDeadlineIsThirtySeconds(t *testing.T) {
	entry := textemoji.Entries()[0]
	fetcher := &githubBuiltinEmojiFetcher{
		client: newBuiltinEmojiHTTPClient(builtinEmojiRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			deadline, ok := req.Context().Deadline()
			if !ok {
				t.Fatal("request context has no deadline")
			}
			remaining := time.Until(deadline)
			if remaining < 29*time.Second || remaining > 30*time.Second {
				t.Fatalf("default deadline remaining = %v, want [29s, 30s]", remaining)
			}
			return testHTTPResponse(req, http.StatusOK, "image/webp", validWebP(12), nil), nil
		})),
	}

	if _, err := fetcher.Fetch(context.Background(), entry); err != nil {
		t.Fatalf("Fetch() error: %v", err)
	}
}

func TestBuiltinEmojiAssetFetchStalledResponseHitsChildDeadline(t *testing.T) {
	entry := textemoji.Entries()[0]
	started := make(chan struct{})
	fetcher := newTestBuiltinEmojiFetcher(builtinEmojiRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		close(started)
		<-req.Context().Done()
		return nil, req.Context().Err()
	}), 20*time.Millisecond)

	_, err := fetcher.Fetch(context.Background(), entry)
	<-started
	requireBuiltinEmojiFetchError(t, err, builtinEmojiFetchTimeout)
}

func TestBuiltinEmojiAssetFetchRejectsNonCanonicalInitialAsset(t *testing.T) {
	entry := withBuiltinEmojiEntryChinese(textemoji.Entries()[0], "../secret")
	var calls atomic.Int32
	fetcher := newTestBuiltinEmojiFetcher(builtinEmojiRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("must not be called")
	}), time.Second)

	_, err := fetcher.Fetch(context.Background(), entry)
	requireBuiltinEmojiFetchError(t, err, builtinEmojiFetchInvalidAsset)
	if calls.Load() != 0 {
		t.Fatalf("RoundTrip calls = %d, want 0", calls.Load())
	}
}

func TestBuiltinEmojiAssetFetchRevalidatesFinalResponseURL(t *testing.T) {
	entry := textemoji.Entries()[0]
	initial := mustBuiltinEmojiAssetURL(t, entry)
	otherAsset := mustBuiltinEmojiAssetURL(t, textemoji.Entries()[1])
	tests := map[string]string{
		"HTTP downgrade":  "http://raw.githubusercontent.com" + initial.EscapedPath(),
		"hostile host":    "https://evil.example" + initial.EscapedPath(),
		"unexpected port": "https://raw.githubusercontent.com:443" + initial.EscapedPath(),
		"userinfo":        "https://user:secret@raw.githubusercontent.com" + initial.EscapedPath(),
		"query":           initial.String() + "?token=secret",
		"fragment":        initial.String() + "#secret",
		"wrong path":      "https://raw.githubusercontent.com/airinghost/wechat-emoji/main/README.md",
		"other asset":     otherAsset.String(),
	}
	for name, finalURL := range tests {
		t.Run(name, func(t *testing.T) {
			parsed, err := url.Parse(finalURL)
			if err != nil {
				t.Fatalf("parse test final URL: %v", err)
			}
			body := newTrackedReadCloser(validWebP(12))
			fetcher := newTestBuiltinEmojiFetcher(builtinEmojiRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				return testHTTPResponse(req, http.StatusOK, "image/webp", nil, body, withResponseURL(parsed)), nil
			}), time.Second)

			_, err = fetcher.Fetch(context.Background(), entry)
			requireBuiltinEmojiFetchError(t, err, builtinEmojiFetchInvalidResponseURL)
			if !body.closed.Load() {
				t.Fatal("response body was not closed after final URL rejection")
			}
			assertFetchErrorExcludes(t, err, "secret", "evil.example", "token")
		})
	}
}

func TestBuiltinEmojiAssetFetchRequiresHTTP200(t *testing.T) {
	entry := textemoji.Entries()[0]
	for _, status := range []int{http.StatusContinue, http.StatusNoContent, http.StatusMovedPermanently, http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			body := newTrackedReadCloser([]byte("response-body-secret"))
			fetcher := newTestBuiltinEmojiFetcher(builtinEmojiRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				return testHTTPResponse(req, status, "image/webp", nil, body), nil
			}), time.Second)

			_, err := fetcher.Fetch(context.Background(), entry)
			requireBuiltinEmojiFetchStatusError(t, err, status)
			if !body.closed.Load() {
				t.Fatal("response body was not closed after status rejection")
			}
			assertFetchErrorExcludes(t, err, "response-body-secret")
		})
	}
}

func TestBuiltinEmojiAssetFetchAcceptsOnlyParsedAllowedContentTypes(t *testing.T) {
	entry := textemoji.Entries()[0]
	validTypes := []string{
		"image/webp",
		"image/webp; name=emoji.webp",
		"application/octet-stream",
		"application/octet-stream; name=emoji.webp",
	}
	for _, contentType := range validTypes {
		t.Run("valid_"+contentType, func(t *testing.T) {
			fetcher := fetcherReturning(t, http.StatusOK, contentType, validWebP(12), nil)
			if _, err := fetcher.Fetch(context.Background(), entry); err != nil {
				t.Fatalf("Fetch() rejected %q: %v", contentType, err)
			}
		})
	}

	invalidTypes := []string{
		"",
		"image/png",
		"text/plain",
		"image/webp, text/plain",
		"image/webp; bad parameter",
		"application/octet-streamx",
	}
	for _, contentType := range invalidTypes {
		t.Run("invalid_"+contentType, func(t *testing.T) {
			body := newTrackedReadCloser(validWebP(12))
			fetcher := fetcherReturning(t, http.StatusOK, contentType, nil, body)
			_, err := fetcher.Fetch(context.Background(), entry)
			requireBuiltinEmojiFetchError(t, err, builtinEmojiFetchInvalidContentType)
			if !body.closed.Load() {
				t.Fatal("response body was not closed after Content-Type rejection")
			}
		})
	}
}

func TestBuiltinEmojiAssetFetchBodySizeBoundaries(t *testing.T) {
	entry := textemoji.Entries()[0]
	t.Run("empty", func(t *testing.T) {
		fetcher := fetcherReturning(t, http.StatusOK, "image/webp", nil, nil)
		_, err := fetcher.Fetch(context.Background(), entry)
		requireBuiltinEmojiFetchError(t, err, builtinEmojiFetchEmptyBody)
	})

	t.Run("exactly 256 KiB", func(t *testing.T) {
		body := validWebP(256 << 10)
		fetcher := fetcherReturning(t, http.StatusOK, "image/webp", body, nil)
		got, err := fetcher.Fetch(context.Background(), entry)
		if err != nil {
			t.Fatalf("Fetch() error at exact limit: %v", err)
		}
		if len(got) != 256<<10 {
			t.Fatalf("Fetch() length = %d, want %d", len(got), 256<<10)
		}
	})

	t.Run("256 KiB plus one", func(t *testing.T) {
		body := newTrackedReadCloser(validWebP((256 << 10) + 1))
		fetcher := fetcherReturning(t, http.StatusOK, "image/webp", nil, body)
		_, err := fetcher.Fetch(context.Background(), entry)
		requireBuiltinEmojiFetchError(t, err, builtinEmojiFetchBodyTooLarge)
		if body.bytesRead.Load() != (256<<10)+1 {
			t.Fatalf("bytes read = %d, want bounded %d", body.bytesRead.Load(), (256<<10)+1)
		}
		if !body.closed.Load() {
			t.Fatal("oversized response body was not closed")
		}
	})
}

func TestValidateWebPBoundaries(t *testing.T) {
	valid := validWebP(12)
	if err := validateWebP(valid); err != nil {
		t.Fatalf("validateWebP(valid minimum) error: %v", err)
	}
	if err := validateWebP(validWebP(256 << 10)); err != nil {
		t.Fatalf("validateWebP(valid large) error: %v", err)
	}

	tests := map[string][]byte{
		"nil":          nil,
		"eleven bytes": valid[:11],
		"bad RIFF":     append([]byte("NOPE"), valid[4:]...),
		"bad WEBP":     append(append([]byte(nil), valid[:8]...), []byte("NOPE")...),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateWebP(body); err == nil {
				t.Fatalf("validateWebP(%x) returned nil", body)
			}
		})
	}
}

func TestBuiltinEmojiAssetFetchRejectsInvalidWebP(t *testing.T) {
	entry := textemoji.Entries()[0]
	valid := validWebP(12)
	tests := map[string][]byte{
		"too short": valid[:11],
		"bad RIFF":  append([]byte("NOPE"), valid[4:]...),
		"bad WEBP":  append(append([]byte(nil), valid[:8]...), []byte("NOPE")...),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			fetcher := fetcherReturning(t, http.StatusOK, "application/octet-stream", body, nil)
			_, err := fetcher.Fetch(context.Background(), entry)
			requireBuiltinEmojiFetchError(t, err, builtinEmojiFetchInvalidWebP)
		})
	}
}

func TestBuiltinEmojiAssetFetchClosesBodyAndClassifiesReadErrors(t *testing.T) {
	entry := textemoji.Entries()[0]
	body := &errorReadCloser{err: errors.New("read-error-secret")}
	fetcher := newTestBuiltinEmojiFetcher(builtinEmojiRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return testHTTPResponse(req, http.StatusOK, "image/webp", nil, body), nil
	}), time.Second)

	_, err := fetcher.Fetch(context.Background(), entry)
	requireBuiltinEmojiFetchError(t, err, builtinEmojiFetchReadFailed)
	if !body.closed.Load() {
		t.Fatal("body was not closed after read error")
	}
	assertFetchErrorExcludes(t, err, "read-error-secret")
}

func TestBuiltinEmojiAssetFetchErrorsAreBoundedAndDoNotLeakSecrets(t *testing.T) {
	entry := textemoji.Entries()[0]
	secret := strings.Repeat("token-secret-", 100)
	fetcher := newTestBuiltinEmojiFetcher(builtinEmojiRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New(secret)
	}), time.Second)

	_, err := fetcher.Fetch(context.Background(), entry)
	requireBuiltinEmojiFetchError(t, err, builtinEmojiFetchRequestFailed)
	assertFetchErrorExcludes(t, err, "token-secret")
	if len(err.Error()) > 128 {
		t.Fatalf("error length = %d, want <= 128: %q", len(err.Error()), err)
	}
}

func TestBuiltinEmojiAssetFetchHonorsParentCancellation(t *testing.T) {
	entry := textemoji.Entries()[0]
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fetcher := newTestBuiltinEmojiFetcher(builtinEmojiRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, req.Context().Err()
	}), time.Second)

	_, err := fetcher.Fetch(ctx, entry)
	requireBuiltinEmojiFetchError(t, err, builtinEmojiFetchCanceled)
}

type builtinEmojiRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn builtinEmojiRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type trackedReadCloser struct {
	reader    io.Reader
	closed    atomic.Bool
	bytesRead atomic.Int64
}

func newTrackedReadCloser(body []byte) *trackedReadCloser {
	return &trackedReadCloser{reader: bytes.NewReader(body)}
}

func (body *trackedReadCloser) Read(p []byte) (int, error) {
	n, err := body.reader.Read(p)
	body.bytesRead.Add(int64(n))
	return n, err
}

func (body *trackedReadCloser) Close() error {
	body.closed.Store(true)
	return nil
}

type errorReadCloser struct {
	err    error
	closed atomic.Bool
}

func (body *errorReadCloser) Read([]byte) (int, error) {
	return 0, body.err
}

func (body *errorReadCloser) Close() error {
	body.closed.Store(true)
	return nil
}

type responseOption func(*http.Response)

func withResponseURL(responseURL *url.URL) responseOption {
	return func(resp *http.Response) {
		resp.Request = &http.Request{Method: http.MethodGet, URL: responseURL}
	}
}

func testHTTPResponse(req *http.Request, status int, contentType string, body []byte, bodyOverride io.ReadCloser, options ...responseOption) *http.Response {
	if bodyOverride == nil {
		bodyOverride = newTrackedReadCloser(body)
	}
	resp := &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     make(http.Header),
		Body:       bodyOverride,
		Request:    req,
	}
	if contentType != "" {
		resp.Header.Set("Content-Type", contentType)
	}
	for _, option := range options {
		option(resp)
	}
	return resp
}

func redirectResponse(req *http.Request, location string, body io.ReadCloser) *http.Response {
	resp := testHTTPResponse(req, http.StatusFound, "text/plain", nil, body)
	resp.Header.Set("Location", location)
	return resp
}

func validWebP(size int) []byte {
	if size < 12 {
		panic("validWebP size must be at least 12")
	}
	body := make([]byte, size)
	copy(body[0:4], "RIFF")
	copy(body[8:12], "WEBP")
	return body
}

func mustBuiltinEmojiAssetURL(t *testing.T, entry textemoji.Entry) *url.URL {
	t.Helper()
	assetURL, err := builtinEmojiAssetURL(entry)
	if err != nil {
		t.Fatalf("builtinEmojiAssetURL() error: %v", err)
	}
	return assetURL
}

func newTestBuiltinEmojiFetcher(roundTripper http.RoundTripper, timeout time.Duration) *githubBuiltinEmojiFetcher {
	return &githubBuiltinEmojiFetcher{
		client:  newBuiltinEmojiHTTPClient(roundTripper),
		timeout: timeout,
	}
}

func fetcherReturning(t *testing.T, status int, contentType string, body []byte, bodyOverride io.ReadCloser) *githubBuiltinEmojiFetcher {
	t.Helper()
	return newTestBuiltinEmojiFetcher(builtinEmojiRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return testHTTPResponse(req, status, contentType, body, bodyOverride), nil
	}), time.Second)
}

func assertProductionBuiltinEmojiRequest(t *testing.T, req *http.Request, entry textemoji.Entry) {
	t.Helper()
	want := mustBuiltinEmojiAssetURL(t, entry)
	if req.URL.String() != want.String() {
		t.Fatalf("request URL = %q, want production URL %q", req.URL, want)
	}
	if req.URL.Scheme != "https" || req.URL.Host != "raw.githubusercontent.com" {
		t.Fatalf("request origin = %s://%s, want production HTTPS origin", req.URL.Scheme, req.URL.Host)
	}
}

func requireBuiltinEmojiFetchError(t *testing.T, err error, code builtinEmojiFetchErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("Fetch() error = nil, want code %q", code)
	}
	var fetchErr *builtinEmojiFetchError
	if !errors.As(err, &fetchErr) {
		t.Fatalf("Fetch() error type = %T (%v), want *builtinEmojiFetchError", err, err)
	}
	if fetchErr.code != code {
		t.Fatalf("Fetch() error code = %q, want %q (%v)", fetchErr.code, code, err)
	}
	if len(err.Error()) > 128 {
		t.Fatalf("Fetch() error length = %d, want <= 128: %q", len(err.Error()), err)
	}
}

func requireBuiltinEmojiFetchStatusError(t *testing.T, err error, status int) {
	t.Helper()
	requireBuiltinEmojiFetchError(t, err, builtinEmojiFetchHTTPStatus)
	var fetchErr *builtinEmojiFetchError
	if !errors.As(err, &fetchErr) {
		t.Fatalf("Fetch() error type = %T, want *builtinEmojiFetchError", err)
	}
	if fetchErr.status != status {
		t.Fatalf("Fetch() status = %d, want %d", fetchErr.status, status)
	}
}

func assertFetchErrorExcludes(t *testing.T, err error, forbidden ...string) {
	t.Helper()
	for _, value := range forbidden {
		if strings.Contains(err.Error(), value) {
			t.Fatalf("Fetch() error leaked %q: %q", value, err)
		}
	}
}

func withBuiltinEmojiEntryIndex(entry textemoji.Entry, index int) textemoji.Entry {
	entry.Index = index
	return entry
}

func withBuiltinEmojiEntryChinese(entry textemoji.Entry, chinese string) textemoji.Entry {
	entry.Chinese = chinese
	return entry
}

func withBuiltinEmojiEntryEnglish(entry textemoji.Entry, english string) textemoji.Entry {
	entry.English = english
	return entry
}

func withBuiltinEmojiEntryShortcode(entry textemoji.Entry, shortcode string) textemoji.Entry {
	entry.Shortcode = shortcode
	return entry
}

func withBuiltinEmojiEntryFilename(entry textemoji.Entry, filename string) textemoji.Entry {
	entry.Filename = filename
	return entry
}

func TestBuiltinEmojiTestContentTypesAreParseable(t *testing.T) {
	// Guard the valid fixtures so the Fetch tests exercise the intended parsed media types.
	for _, value := range []string{"image/webp; name=emoji.webp", "application/octet-stream; name=emoji.webp"} {
		if _, _, err := mime.ParseMediaType(value); err != nil {
			t.Fatalf("invalid test fixture %q: %v", value, err)
		}
	}
}
