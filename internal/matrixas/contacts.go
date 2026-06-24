package matrixas

import (
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

type contactStore struct {
	mu   sync.Mutex
	path string
	data contactStoreData
}

type contactStoreData struct {
	Accounts map[string]accountContacts `json:"accounts"`
}

type accountContacts struct {
	UpdatedAt time.Time                `json:"updated_at"`
	Contacts  map[string]contactRecord `json:"contacts"`
}

type contactRecord struct {
	ID        remoteapi.UserID `json:"id"`
	Name      string           `json:"name,omitempty"`
	Remark    string           `json:"remark,omitempty"`
	AvatarURL string           `json:"avatar_url,omitempty"`
	UpdatedAt time.Time        `json:"updated_at"`
}

func newContactStore(path string) *contactStore {
	cs := &contactStore{path: path, data: contactStoreData{Accounts: map[string]accountContacts{}}}
	cs.load()
	return cs
}

func (cs *contactStore) load() {
	b, err := os.ReadFile(cs.path)
	if err != nil {
		return
	}
	if err := json.Unmarshal(b, &cs.data); err != nil {
		log.Printf("failed to parse contact store %s: %v", cs.path, err)
		cs.data = contactStoreData{Accounts: map[string]accountContacts{}}
	}
	if cs.data.Accounts == nil {
		cs.data.Accounts = map[string]accountContacts{}
	}
}

func (cs *contactStore) saveLocked() {
	b, err := json.MarshalIndent(cs.data, "", "  ")
	if err != nil {
		log.Printf("marshal contact store: %v", err)
		return
	}
	if err := os.WriteFile(cs.path, b, 0o600); err != nil {
		log.Printf("write contact store %s: %v", cs.path, err)
	}
}

func (cs *contactStore) get(account, id remoteapi.UserID) (remoteapi.Contact, bool) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	acct, ok := cs.data.Accounts[string(account)]
	if !ok {
		return remoteapi.Contact{}, false
	}
	rec, ok := acct.Contacts[string(id)]
	if !ok {
		return remoteapi.Contact{}, false
	}
	return contactFromRecord(rec), true
}

func (cs *contactStore) put(account remoteapi.UserID, contact remoteapi.Contact) {
	if account == "" || contact.ID == "" {
		return
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	acct := cs.data.Accounts[string(account)]
	if acct.Contacts == nil {
		acct.Contacts = map[string]contactRecord{}
	}
	acct.Contacts[string(contact.ID)] = contactToRecord(contact)
	acct.UpdatedAt = time.Now()
	cs.data.Accounts[string(account)] = acct
	cs.saveLocked()
}

func (cs *contactStore) putList(account remoteapi.UserID, contacts []remoteapi.Contact) {
	if account == "" {
		return
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	acct := cs.data.Accounts[string(account)]
	if acct.Contacts == nil {
		acct.Contacts = map[string]contactRecord{}
	}
	for _, contact := range contacts {
		if contact.ID == "" {
			continue
		}
		acct.Contacts[string(contact.ID)] = contactToRecord(contact)
	}
	acct.UpdatedAt = time.Now()
	cs.data.Accounts[string(account)] = acct
	cs.saveLocked()
}

func (cs *contactStore) shouldRefresh(account remoteapi.UserID, maxAge time.Duration) bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	acct, ok := cs.data.Accounts[string(account)]
	return !ok || len(acct.Contacts) == 0 || time.Since(acct.UpdatedAt) > maxAge
}

func contactToRecord(contact remoteapi.Contact) contactRecord {
	return contactRecord{ID: contact.ID, Name: contact.Name, Remark: contact.Remark, AvatarURL: contact.AvatarURL, UpdatedAt: time.Now()}
}

func contactFromRecord(rec contactRecord) remoteapi.Contact {
	return remoteapi.Contact{ID: rec.ID, Name: rec.Name, Remark: rec.Remark, AvatarURL: rec.AvatarURL}
}
