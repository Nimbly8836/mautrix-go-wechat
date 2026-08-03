package matrixas

import (
	"encoding/json"
	"io/fs"
	"log"
	"os"
	"sync"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

type accountStorePersist func(path string, data []byte, mode fs.FileMode) error

type accountStore struct {
	mu      sync.Mutex
	path    string
	data    accountStoreData
	persist accountStorePersist
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
	as := &accountStore{
		path:    path,
		data:    accountStoreData{Accounts: map[string]savedAccount{}},
		persist: writeFileAtomic,
	}
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

func cloneSavedAccount(acc savedAccount) savedAccount {
	acc.Session = append([]byte(nil), acc.Session...)
	if acc.SpaceFolders != nil {
		folders := make(map[string]string, len(acc.SpaceFolders))
		for key, room := range acc.SpaceFolders {
			folders[key] = room
		}
		acc.SpaceFolders = folders
	}
	return acc
}

func cloneAccountStoreData(data accountStoreData) accountStoreData {
	clone := accountStoreData{Current: data.Current, Accounts: make(map[string]savedAccount, len(data.Accounts))}
	for key, acc := range data.Accounts {
		clone.Accounts[key] = cloneSavedAccount(acc)
	}
	return clone
}

// mutateLocked performs the account store transaction while the caller holds
// as.mu: mutate a complete clone, atomically persist it, then publish it.
func (as *accountStore) mutateLocked(mutate func(*accountStoreData) error) error {
	clone := cloneAccountStoreData(as.data)
	if err := mutate(&clone); err != nil {
		return err
	}
	data, err := json.MarshalIndent(clone, "", "  ")
	if err != nil {
		return err
	}
	persist := as.persist
	if persist == nil {
		persist = writeFileAtomic
	}
	if err := persist(as.path, data, 0o600); err != nil {
		return err
	}
	as.data = clone
	return nil
}

func (as *accountStore) put(acc savedAccount) error {
	as.mu.Lock()
	defer as.mu.Unlock()
	acc = cloneSavedAccount(acc)
	return as.mutateLocked(func(data *accountStoreData) error {
		if old, ok := data.Accounts[string(acc.ID)]; ok {
			if acc.SpaceRoom == "" {
				acc.SpaceRoom = old.SpaceRoom
			}
			if acc.SpaceFolders == nil {
				acc.SpaceFolders = cloneSavedAccount(old).SpaceFolders
			}
		}
		acc.UpdatedAt = time.Now()
		data.Accounts[string(acc.ID)] = cloneSavedAccount(acc)
		data.Current = acc.ID
		return nil
	})
}

func (as *accountStore) get(id remoteapi.UserID) (savedAccount, bool) {
	as.mu.Lock()
	defer as.mu.Unlock()
	acc, ok := as.data.Accounts[string(id)]
	return cloneSavedAccount(acc), ok
}

func (as *accountStore) current() (savedAccount, bool) {
	as.mu.Lock()
	defer as.mu.Unlock()
	if as.data.Current == "" {
		return savedAccount{}, false
	}
	acc, ok := as.data.Accounts[string(as.data.Current)]
	return cloneSavedAccount(acc), ok
}

func (as *accountStore) setSpace(id remoteapi.UserID, roomID string) error {
	as.mu.Lock()
	defer as.mu.Unlock()
	return as.mutateLocked(func(data *accountStoreData) error {
		acc, ok := data.Accounts[string(id)]
		if !ok {
			acc = savedAccount{ID: id}
		}
		acc.SpaceRoom = roomID
		acc.UpdatedAt = time.Now()
		data.Accounts[string(id)] = acc
		return nil
	})
}

func (as *accountStore) setSpaceFolder(id remoteapi.UserID, folder, roomID string) error {
	as.mu.Lock()
	defer as.mu.Unlock()
	return as.mutateLocked(func(data *accountStoreData) error {
		acc, ok := data.Accounts[string(id)]
		if !ok {
			acc = savedAccount{ID: id}
		}
		if acc.SpaceFolders == nil {
			acc.SpaceFolders = map[string]string{}
		}
		acc.SpaceFolders[folder] = roomID
		acc.UpdatedAt = time.Now()
		data.Accounts[string(id)] = acc
		return nil
	})
}

func (as *accountStore) replaceSpace(id remoteapi.UserID, roomID string, folders map[string]string) error {
	as.mu.Lock()
	defer as.mu.Unlock()
	folders = cloneSavedAccount(savedAccount{SpaceFolders: folders}).SpaceFolders
	return as.mutateLocked(func(data *accountStoreData) error {
		acc, ok := data.Accounts[string(id)]
		if !ok {
			acc = savedAccount{ID: id}
		}
		acc.SpaceRoom = roomID
		acc.SpaceFolders = folders
		acc.UpdatedAt = time.Now()
		data.Accounts[string(id)] = acc
		return nil
	})
}

func (as *accountStore) setCurrent(id remoteapi.UserID) (bool, error) {
	as.mu.Lock()
	defer as.mu.Unlock()
	if _, ok := as.data.Accounts[string(id)]; !ok {
		return false, nil
	}
	if err := as.mutateLocked(func(data *accountStoreData) error {
		data.Current = id
		return nil
	}); err != nil {
		return false, err
	}
	return true, nil
}

func (as *accountStore) list() []savedAccount {
	as.mu.Lock()
	defer as.mu.Unlock()
	out := make([]savedAccount, 0, len(as.data.Accounts))
	for _, acc := range as.data.Accounts {
		out = append(out, cloneSavedAccount(acc))
	}
	return out
}
