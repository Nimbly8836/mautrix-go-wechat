package matrixas

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"maunium.net/go/mautrix/id"
)

// Claim before sending and never remove a claim on error: a timeout/crash may
// have happened after WeChat accepted the entry. O_EXCL also prevents two
// processes sharing the same state directory from replaying the same command.
// Claims are intentionally durable and unpruned. They contain no entry text.
func claimSolitaireCommand(portalPath string, room id.RoomID, command id.EventID) (bool, error) {
	if portalPath == "" || room == "" || command == "" {
		return false, fmt.Errorf("missing persistent portal path or Matrix command identity")
	}
	dir := portalPath + ".solitaire-claims"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	key := sha256.Sum256([]byte(string(room) + "\x00" + string(command)))
	file, err := os.OpenFile(filepath.Join(dir, hex.EncodeToString(key[:])+".json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	if err := json.NewEncoder(file).Encode(struct {
		Room    id.RoomID  `json:"room"`
		Command id.EventID `json:"command"`
	}{room, command}); err != nil {
		return false, err
	}
	if err := file.Sync(); err != nil {
		return false, err
	}
	if err := file.Close(); err != nil {
		return false, err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return false, err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return false, err
	}
	return true, nil
}
