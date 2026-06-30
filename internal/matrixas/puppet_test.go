package matrixas

import (
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func TestShouldJoinPuppetSkipsCurrentAccount(t *testing.T) {
	if shouldJoinPuppet(remoteapi.UserID("wxid_self"), remoteapi.UserID("wxid_self")) {
		t.Fatalf("current account puppet should not be joined into portal rooms")
	}
	if !shouldJoinPuppet(remoteapi.UserID("wxid_self"), remoteapi.UserID("wxid_friend")) {
		t.Fatalf("remote sender puppet should be joined into portal rooms")
	}
}

func TestPuppetProfileUpdatePolicy(t *testing.T) {
	if shouldUpdatePuppetProfile(puppetProfileUpdateAuto) {
		t.Fatalf("automatic message bridging should not update puppet profile")
	}
	if !shouldUpdatePuppetProfile(puppetProfileUpdateStartup) {
		t.Fatalf("startup should update puppet profile")
	}
	if !shouldUpdatePuppetProfile(puppetProfileUpdateCommand) {
		t.Fatalf("explicit commands should update puppet profile")
	}
}
