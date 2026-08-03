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
	"time"

	"github.com/leat/mautrix-go-wechat/internal/wechat/textemoji"
)

const (
	builtinEmojiAssetScheme         = "https"
	builtinEmojiAssetHost           = "raw.githubusercontent.com"
	builtinEmojiAssetOwner          = "airinghost"
	builtinEmojiAssetRepo           = "wechat-emoji"
	builtinEmojiDefaultFetchTimeout = 30 * time.Second
	builtinEmojiMaxBodySize         = 256 << 10
	builtinEmojiMaxRedirects        = 3
)

var (
	errBuiltinEmojiRedirectRejected = errors.New("builtin emoji redirect rejected")
	errBuiltinEmojiWebPTooShort     = errors.New("webp data is too short")
	errBuiltinEmojiWebPMissingRIFF  = errors.New("webp data has no RIFF signature")
	errBuiltinEmojiWebPMissingWEBP  = errors.New("webp data has no WEBP signature")
)

type builtinEmojiFetchErrorCode string

const (
	builtinEmojiFetchInvalidAsset       builtinEmojiFetchErrorCode = "invalid_asset"
	builtinEmojiFetchTimeout            builtinEmojiFetchErrorCode = "timeout"
	builtinEmojiFetchCanceled           builtinEmojiFetchErrorCode = "canceled"
	builtinEmojiFetchRedirectRejected   builtinEmojiFetchErrorCode = "redirect_rejected"
	builtinEmojiFetchRequestFailed      builtinEmojiFetchErrorCode = "request_failed"
	builtinEmojiFetchInvalidResponseURL builtinEmojiFetchErrorCode = "invalid_response_url"
	builtinEmojiFetchHTTPStatus         builtinEmojiFetchErrorCode = "http_status"
	builtinEmojiFetchInvalidContentType builtinEmojiFetchErrorCode = "invalid_content_type"
	builtinEmojiFetchReadFailed         builtinEmojiFetchErrorCode = "read_failed"
	builtinEmojiFetchEmptyBody          builtinEmojiFetchErrorCode = "empty_body"
	builtinEmojiFetchBodyTooLarge       builtinEmojiFetchErrorCode = "body_too_large"
	builtinEmojiFetchInvalidWebP        builtinEmojiFetchErrorCode = "invalid_webp"
)

type builtinEmojiFetchError struct {
	code   builtinEmojiFetchErrorCode
	status int
}

func (err *builtinEmojiFetchError) Error() string {
	if err.status != 0 {
		return fmt.Sprintf("builtin emoji fetch failed: %s (status %d)", err.code, err.status)
	}
	return "builtin emoji fetch failed: " + string(err.code)
}

type githubBuiltinEmojiFetcher struct {
	client  *http.Client
	timeout time.Duration
}

func builtinEmojiAssetURL(entry textemoji.Entry) (*url.URL, error) {
	if !isCanonicalBuiltinEmojiEntry(entry) {
		return nil, errors.New("builtin emoji asset is not in the registry")
	}

	decodedPath, _ := builtinEmojiAssetPaths(entry)
	assetURL := &url.URL{
		Scheme: builtinEmojiAssetScheme,
		Host:   builtinEmojiAssetHost,
		Path:   decodedPath,
	}
	if _, err := validateBuiltinEmojiAssetURL(assetURL); err != nil {
		return nil, errors.New("builtin emoji asset URL is invalid")
	}
	return assetURL, nil
}

func isCanonicalBuiltinEmojiEntry(entry textemoji.Entry) bool {
	entries := textemoji.Entries()
	if entry.Index < 1 || entry.Index > len(entries) {
		return false
	}
	return entries[entry.Index-1] == entry
}

func builtinEmojiAssetPaths(entry textemoji.Entry) (decoded, escaped string) {
	filename := fmt.Sprintf("%03d_%s.webp", entry.Index, entry.Chinese)
	decoded = "/" + builtinEmojiAssetOwner + "/" + builtinEmojiAssetRepo + "/" + builtinEmojiSourceCommit + "/" + builtinEmojiSourcePath + "/" + filename
	escaped = "/" + builtinEmojiAssetOwner + "/" + builtinEmojiAssetRepo + "/" + builtinEmojiSourceCommit + "/" + builtinEmojiSourcePath + "/" + url.PathEscape(filename)
	return decoded, escaped
}

func validateBuiltinEmojiAssetURL(assetURL *url.URL) (string, error) {
	if assetURL == nil || assetURL.Scheme != builtinEmojiAssetScheme || assetURL.Host != builtinEmojiAssetHost {
		return "", errors.New("invalid origin")
	}
	if assetURL.Opaque != "" || assetURL.User != nil || assetURL.Port() != "" {
		return "", errors.New("invalid authority")
	}
	if assetURL.RawQuery != "" || assetURL.ForceQuery || assetURL.Fragment != "" || assetURL.RawFragment != "" {
		return "", errors.New("query and fragment are forbidden")
	}

	for _, entry := range textemoji.Entries() {
		decodedPath, escapedPath := builtinEmojiAssetPaths(entry)
		if assetURL.Path != decodedPath {
			continue
		}
		if assetURL.EscapedPath() != escapedPath {
			return "", errors.New("asset path is not canonically escaped")
		}
		if assetURL.RawPath != "" && assetURL.RawPath != escapedPath {
			return "", errors.New("asset raw path is not canonical")
		}
		return decodedPath, nil
	}
	return "", errors.New("asset path is not in the registry")
}

func newBuiltinEmojiHTTPClient(roundTripper http.RoundTripper) *http.Client {
	if roundTripper == nil {
		roundTripper = http.DefaultTransport
	}
	return &http.Client{
		Transport: roundTripper,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) == 0 || len(via) > builtinEmojiMaxRedirects {
				return errBuiltinEmojiRedirectRejected
			}

			originalPath, err := validateBuiltinEmojiAssetURL(via[0].URL)
			if err != nil {
				return errBuiltinEmojiRedirectRejected
			}
			for _, previous := range via[1:] {
				path, validateErr := validateBuiltinEmojiAssetURL(previous.URL)
				if validateErr != nil || path != originalPath {
					return errBuiltinEmojiRedirectRejected
				}
			}
			redirectPath, err := validateBuiltinEmojiAssetURL(req.URL)
			if err != nil || redirectPath != originalPath {
				return errBuiltinEmojiRedirectRejected
			}
			return nil
		},
	}
}

func validateWebP(data []byte) error {
	if len(data) < 12 {
		return errBuiltinEmojiWebPTooShort
	}
	if !bytes.Equal(data[0:4], []byte("RIFF")) {
		return errBuiltinEmojiWebPMissingRIFF
	}
	if !bytes.Equal(data[8:12], []byte("WEBP")) {
		return errBuiltinEmojiWebPMissingWEBP
	}
	return nil
}

func (f *githubBuiltinEmojiFetcher) Fetch(ctx context.Context, entry textemoji.Entry) ([]byte, error) {
	assetURL, err := builtinEmojiAssetURL(entry)
	if err != nil {
		return nil, newBuiltinEmojiFetchError(builtinEmojiFetchInvalidAsset)
	}
	initialPath, err := validateBuiltinEmojiAssetURL(assetURL)
	if err != nil {
		return nil, newBuiltinEmojiFetchError(builtinEmojiFetchInvalidAsset)
	}

	timeout := builtinEmojiDefaultFetchTimeout
	client := (*http.Client)(nil)
	if f != nil {
		if f.timeout > 0 {
			timeout = f.timeout
		}
		client = f.client
	}
	if client == nil {
		client = newBuiltinEmojiHTTPClient(nil)
	}

	requestContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, assetURL.String(), nil)
	if err != nil {
		return nil, newBuiltinEmojiFetchError(builtinEmojiFetchInvalidAsset)
	}

	response, err := client.Do(request)
	if err != nil {
		return nil, classifyBuiltinEmojiRequestError(ctx, requestContext, err)
	}
	if response.Body == nil {
		return nil, newBuiltinEmojiFetchError(builtinEmojiFetchReadFailed)
	}
	defer response.Body.Close()

	if response.Request == nil || response.Request.URL == nil {
		return nil, newBuiltinEmojiFetchError(builtinEmojiFetchInvalidResponseURL)
	}
	finalPath, err := validateBuiltinEmojiAssetURL(response.Request.URL)
	if err != nil || finalPath != initialPath {
		return nil, newBuiltinEmojiFetchError(builtinEmojiFetchInvalidResponseURL)
	}
	if response.StatusCode != http.StatusOK {
		return nil, &builtinEmojiFetchError{code: builtinEmojiFetchHTTPStatus, status: response.StatusCode}
	}

	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "image/webp" && mediaType != "application/octet-stream" {
		return nil, newBuiltinEmojiFetchError(builtinEmojiFetchInvalidContentType)
	}

	data, err := io.ReadAll(io.LimitReader(response.Body, builtinEmojiMaxBodySize+1))
	if err != nil {
		return nil, newBuiltinEmojiFetchError(builtinEmojiFetchReadFailed)
	}
	if len(data) == 0 {
		return nil, newBuiltinEmojiFetchError(builtinEmojiFetchEmptyBody)
	}
	if len(data) > builtinEmojiMaxBodySize {
		return nil, newBuiltinEmojiFetchError(builtinEmojiFetchBodyTooLarge)
	}
	if err = validateWebP(data); err != nil {
		return nil, newBuiltinEmojiFetchError(builtinEmojiFetchInvalidWebP)
	}
	return data, nil
}

func classifyBuiltinEmojiRequestError(parent, requestContext context.Context, err error) error {
	if errors.Is(requestContext.Err(), context.DeadlineExceeded) || errors.Is(parent.Err(), context.DeadlineExceeded) {
		return newBuiltinEmojiFetchError(builtinEmojiFetchTimeout)
	}
	if errors.Is(requestContext.Err(), context.Canceled) || errors.Is(parent.Err(), context.Canceled) {
		return newBuiltinEmojiFetchError(builtinEmojiFetchCanceled)
	}
	if errors.Is(err, errBuiltinEmojiRedirectRejected) {
		return newBuiltinEmojiFetchError(builtinEmojiFetchRedirectRejected)
	}
	return newBuiltinEmojiFetchError(builtinEmojiFetchRequestFailed)
}

func newBuiltinEmojiFetchError(code builtinEmojiFetchErrorCode) *builtinEmojiFetchError {
	return &builtinEmojiFetchError{code: code}
}

// safeBuiltinEmojiFetchDetails extracts only the closed fetch-reason enum and
// numeric HTTP status. Wrapped error text is deliberately never returned.
func safeBuiltinEmojiFetchDetails(err error) (builtinEmojiFetchErrorCode, int) {
	var fetchErr *builtinEmojiFetchError
	if !errors.As(err, &fetchErr) || fetchErr == nil || !isSafeBuiltinEmojiFetchErrorCode(fetchErr.code) {
		return "", 0
	}
	status := 0
	if fetchErr.code == builtinEmojiFetchHTTPStatus && fetchErr.status >= 100 && fetchErr.status <= 599 {
		status = fetchErr.status
	}
	return fetchErr.code, status
}

func isSafeBuiltinEmojiFetchErrorCode(code builtinEmojiFetchErrorCode) bool {
	switch code {
	case builtinEmojiFetchInvalidAsset,
		builtinEmojiFetchTimeout,
		builtinEmojiFetchCanceled,
		builtinEmojiFetchRedirectRejected,
		builtinEmojiFetchRequestFailed,
		builtinEmojiFetchInvalidResponseURL,
		builtinEmojiFetchHTTPStatus,
		builtinEmojiFetchInvalidContentType,
		builtinEmojiFetchReadFailed,
		builtinEmojiFetchEmptyBody,
		builtinEmojiFetchBodyTooLarge,
		builtinEmojiFetchInvalidWebP:
		return true
	default:
		return false
	}
}
