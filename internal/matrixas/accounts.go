package matrixas

import (
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

type accountStore struct {
	mu   sync.Mutex
	path string
	data accountStoreData
}

type accountStoreData struct {
	Current  remoteapi.UserID        `json:"current,omitempty"`
	Accounts map[string]savedAccount `json:"accounts"`
}

type savedAccount struct {
	ID           remoteapi.UserID  `json:"id"`
	Protocol     string            `json:"protocol"`
	LoginFlow    string            `json:"login_flow,omitempty"`
	Session      []byte            `json:"session"`
	SpaceRoom    string            `json:"space_room,omitempty"`
	SpaceFolders map[string]string `json:"space_folders,omitempty"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

func newAccountStore(path string) *accountStore {
	as := &accountStore{path: path, data: accountStoreData{Accounts: map[string]savedAccount{}}}
	as.load()
	return as
}

func (as *accountStore) load() {
	b, err := os.ReadFile(as.path)
	if err != nil {
		return
	}
	if err := json.Unmarshal(b, &as.data); err != nil {
		log.Printf("failed to parse account store %s: %v", as.path, err)
		as.data = accountStoreData{Accounts: map[string]savedAccount{}}
	}
	if as.data.Accounts == nil {
		as.data.Accounts = map[string]savedAccount{}
	}
}

func (as *accountStore) saveLocked() {
	b, err := json.MarshalIndent(as.data, "", "  ")
	if err != nil {
		log.Printf("marshal account store: %v", err)
		return
	}
	if err := os.WriteFile(as.path, b, 0o600); err != nil {
		log.Printf("write account store %s: %v", as.path, err)
	}
}

func (as *accountStore) put(acc savedAccount) {
	as.mu.Lock()
	defer as.mu.Unlock()
	if old, ok := as.data.Accounts[string(acc.ID)]; ok {
		if acc.SpaceRoom == "" {
			acc.SpaceRoom = old.SpaceRoom
		}
		if acc.SpaceFolders == nil {
			acc.SpaceFolders = old.SpaceFolders
		}
	}
	acc.UpdatedAt = time.Now()
	as.data.Accounts[string(acc.ID)] = acc
	as.data.Current = acc.ID
	as.saveLocked()
}

func (as *accountStore) get(id remoteapi.UserID) (savedAccount, bool) {
	as.mu.Lock()
	defer as.mu.Unlock()
	acc, ok := as.data.Accounts[string(id)]
	return acc, ok
}

func (as *accountStore) current() (savedAccount, bool) {
	as.mu.Lock()
	defer as.mu.Unlock()
	if as.data.Current == "" {
		return savedAccount{}, false
	}
	acc, ok := as.data.Accounts[string(as.data.Current)]
	return acc, ok
}

func (as *accountStore) setSpace(id remoteapi.UserID, roomID string) {
	as.mu.Lock()
	defer as.mu.Unlock()
	acc, ok := as.data.Accounts[string(id)]
	if !ok {
		acc = savedAccount{ID: id}
	}
	acc.SpaceRoom = roomID
	acc.UpdatedAt = time.Now()
	as.data.Accounts[string(id)] = acc
	as.saveLocked()
}

func (as *accountStore) setSpaceFolder(id remoteapi.UserID, folder, roomID string) {
	as.mu.Lock()
	defer as.mu.Unlock()
	acc, ok := as.data.Accounts[string(id)]
	if !ok {
		acc = savedAccount{ID: id}
	}
	if acc.SpaceFolders == nil {
		acc.SpaceFolders = map[string]string{}
	}
	acc.SpaceFolders[folder] = roomID
	acc.UpdatedAt = time.Now()
	as.data.Accounts[string(id)] = acc
	as.saveLocked()
}

func (as *accountStore) replaceSpace(id remoteapi.UserID, roomID string, folders map[string]string) {
	as.mu.Lock()
	defer as.mu.Unlock()
	acc, ok := as.data.Accounts[string(id)]
	if !ok {
		acc = savedAccount{ID: id}
	}
	acc.SpaceRoom = roomID
	acc.SpaceFolders = folders
	acc.UpdatedAt = time.Now()
	as.data.Accounts[string(id)] = acc
	as.saveLocked()
}

func (as *accountStore) setCurrent(id remoteapi.UserID) bool {
	as.mu.Lock()
	defer as.mu.Unlock()
	if _, ok := as.data.Accounts[string(id)]; !ok {
		return false
	}
	as.data.Current = id
	as.saveLocked()
	return true
}

func (as *accountStore) list() []savedAccount {
	as.mu.Lock()
	defer as.mu.Unlock()
	out := make([]savedAccount, 0, len(as.data.Accounts))
	for _, acc := range as.data.Accounts {
		out = append(out, acc)
	}
	return out
}
