package matrixas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

var errImagePackStateNotFound = errors.New("image pack state not found")

type rawImagePackState interface {
	Load(context.Context, id.RoomID, event.Type, string) (json.RawMessage, error)
	Save(context.Context, id.RoomID, event.Type, string, json.RawMessage) error
}

type matrixRawImagePackState struct {
	server *Server
}

func (m matrixRawImagePackState) Load(ctx context.Context, room id.RoomID, eventType event.Type, stateKey string) (json.RawMessage, error) {
	if m.server == nil || m.server.as == nil || m.server.as.BotIntent() == nil {
		return nil, fmt.Errorf("Matrix bot intent unavailable")
	}
	var raw json.RawMessage
	if err := m.server.as.BotIntent().StateEvent(ctx, room, eventType, stateKey, &raw); err != nil {
		if errors.Is(err, mautrix.MNotFound) {
			return nil, errImagePackStateNotFound
		}
		return nil, err
	}
	return raw, nil
}

func (m matrixRawImagePackState) Save(ctx context.Context, room id.RoomID, eventType event.Type, stateKey string, raw json.RawMessage) error {
	if m.server == nil || m.server.as == nil || m.server.as.BotIntent() == nil {
		return fmt.Errorf("Matrix bot intent unavailable")
	}
	_, err := m.server.as.BotIntent().SendStateEvent(ctx, room, eventType, stateKey, raw)
	return err
}

type packImage struct {
	URL  id.ContentURIString
	Body string
}

// mergePackImages applies image-level updates without decoding and re-encoding
// any unrelated part of the state event. Image replacements are object patches:
// fields supplied by the replacement win, while extension fields already in the
// image are retained. A nil replacement removes the image.
func mergePackImages(raw json.RawMessage, replacements map[string]json.RawMessage) (json.RawMessage, error) {
	state := make(map[string]json.RawMessage)
	if err := json.Unmarshal(raw, &state); err != nil || state == nil {
		state = make(map[string]json.RawMessage)
	}

	images := make(map[string]json.RawMessage)
	imagesWasObject := false
	if rawImages, ok := state["images"]; ok {
		if err := json.Unmarshal(rawImages, &images); err == nil && images != nil {
			imagesWasObject = true
		} else {
			images = make(map[string]json.RawMessage)
		}
	}

	imagesChanged := false
	for shortcode, replacement := range replacements {
		if replacement == nil {
			if _, exists := images[shortcode]; exists {
				delete(images, shortcode)
				imagesChanged = true
			}
			continue
		}

		var replacementObject map[string]json.RawMessage
		if err := json.Unmarshal(replacement, &replacementObject); err != nil || replacementObject == nil {
			if err == nil {
				err = fmt.Errorf("image replacement must be an object")
			}
			return nil, fmt.Errorf("invalid replacement for image %q: %w", shortcode, err)
		}

		mergedImage := make(map[string]json.RawMessage)
		if existing, ok := images[shortcode]; ok {
			// A malformed/non-object existing image has no fields worth retaining.
			_ = json.Unmarshal(existing, &mergedImage)
			if mergedImage == nil {
				mergedImage = make(map[string]json.RawMessage)
			}
		}
		for key, value := range replacementObject {
			mergedImage[key] = value
		}
		encoded, err := json.Marshal(mergedImage)
		if err != nil {
			return nil, fmt.Errorf("marshal replacement for image %q: %w", shortcode, err)
		}
		images[shortcode] = encoded
		imagesChanged = true
	}

	if imagesWasObject || imagesChanged {
		encodedImages, err := json.Marshal(images)
		if err != nil {
			return nil, fmt.Errorf("marshal image pack images: %w", err)
		}
		state["images"] = encodedImages
	}
	encodedState, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("marshal image pack state: %w", err)
	}
	return encodedState, nil
}

func parsePackImages(raw json.RawMessage) map[string]packImage {
	var state struct {
		Images map[string]json.RawMessage `json:"images"`
	}
	if err := json.Unmarshal(raw, &state); err != nil || state.Images == nil {
		return map[string]packImage{}
	}

	images := make(map[string]packImage)
	for shortcode, rawImage := range state.Images {
		var image struct {
			URL  string `json:"url"`
			Body string `json:"body"`
		}
		if err := json.Unmarshal(rawImage, &image); err != nil {
			continue
		}
		uri, ok := validMXC(image.URL)
		if !ok {
			continue
		}
		images[shortcode] = packImage{URL: uri, Body: image.Body}
	}
	return images
}

func validMXC(raw string) (id.ContentURIString, bool) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", false
	}
	parsed, err := id.ParseContentURI(raw)
	if err != nil || !validMatrixServerName(parsed.Homeserver) || !validMediaID(parsed.FileID) {
		return "", false
	}
	return id.ContentURIString(raw), true
}

func validMediaID(mediaID string) bool {
	return validRFC3986Component(mediaID, true)
}

func validMatrixServerName(serverName string) bool {
	if serverName == "" {
		return false
	}
	if strings.HasPrefix(serverName, "[") {
		closing := strings.IndexByte(serverName, ']')
		if closing < 0 {
			return false
		}
		address := serverName[1:closing]
		if !strings.Contains(address, ":") || net.ParseIP(address) == nil {
			return false
		}
		return validOptionalPort(serverName[closing+1:])
	}

	host := serverName
	if colon := strings.LastIndexByte(serverName, ':'); colon >= 0 {
		if strings.Contains(serverName[:colon], ":") || !validOptionalPort(serverName[colon:]) {
			return false
		}
		host = serverName[:colon]
	}
	return validRFC3986Component(host, false)
}

func validRFC3986Component(value string, allowPCharExtras bool) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); {
		char := value[index]
		if char == '%' {
			if index+2 >= len(value) || !isHexDigit(value[index+1]) || !isHexDigit(value[index+2]) {
				return false
			}
			index += 3
			continue
		}
		if isRFC3986Unreserved(char) || isRFC3986SubDelim(char) || allowPCharExtras && (char == ':' || char == '@') {
			index++
			continue
		}
		return false
	}
	return true
}

func isRFC3986Unreserved(char byte) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("-._~", rune(char))
}

func isRFC3986SubDelim(char byte) bool {
	return strings.ContainsRune("!$&'()*+,;=", rune(char))
}

func isHexDigit(char byte) bool {
	return char >= '0' && char <= '9' || char >= 'a' && char <= 'f' || char >= 'A' && char <= 'F'
}

func validOptionalPort(suffix string) bool {
	if suffix == "" {
		return true
	}
	if len(suffix) < 2 || suffix[0] != ':' {
		return false
	}
	port, err := strconv.ParseUint(suffix[1:], 10, 16)
	return err == nil && port > 0
}
