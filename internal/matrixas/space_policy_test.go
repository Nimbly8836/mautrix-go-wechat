package matrixas

import "testing"

func TestNormalizeOfficialAccountMode(t *testing.T) {
	cases := map[string]string{
		"":       officialAccountModeInvite,
		"invite": officialAccountModeInvite,
		"mute":   officialAccountModeMute,
		"reject": officialAccountModeReject,
		"bad":    officialAccountModeInvite,
	}
	for input, want := range cases {
		if got := normalizeOfficialAccountMode(input); got != want {
			t.Fatalf("normalizeOfficialAccountMode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestOfficialAccountModeInviteOwner(t *testing.T) {
	if !officialAccountModeInvitesOwner(officialAccountModeInvite, true) {
		t.Fatalf("invite mode should invite official portals")
	}
	if officialAccountModeInvitesOwner(officialAccountModeMute, true) {
		t.Fatalf("mute mode should not invite official portals")
	}
	if !officialAccountModeInvitesOwner(officialAccountModeMute, false) {
		t.Fatalf("mute mode should still invite non-official portals")
	}
	if officialAccountModeInvitesOwner(officialAccountModeReject, true) {
		t.Fatalf("reject mode should not invite official portals")
	}
}

func TestOfficialAccountModeCreatesPortal(t *testing.T) {
	if !officialAccountModeCreatesPortal(officialAccountModeInvite, true) {
		t.Fatalf("invite mode should create official portals")
	}
	if !officialAccountModeCreatesPortal(officialAccountModeMute, true) {
		t.Fatalf("mute mode should create official portals")
	}
	if officialAccountModeCreatesPortal(officialAccountModeReject, true) {
		t.Fatalf("reject mode should not create official portals")
	}
	if !officialAccountModeCreatesPortal(officialAccountModeReject, false) {
		t.Fatalf("reject mode should still create non-official portals")
	}
}
