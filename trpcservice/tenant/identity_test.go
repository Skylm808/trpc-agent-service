package tenant

import "testing"

func TestCanonicalIdentifiersAreScopedAndEscaped(t *testing.T) {
	appA, err := CanonicalAppName("tenant-a", "support/app")
	if err != nil {
		t.Fatalf("CanonicalAppName() error = %v", err)
	}
	appB, err := CanonicalAppName("tenant-b", "support/app")
	if err != nil {
		t.Fatalf("CanonicalAppName() error = %v", err)
	}
	if appA == appB {
		t.Fatalf("app names collide: %q", appA)
	}
	if appA != "tenant/tenant-a/app/support%2Fapp" {
		t.Fatalf("appA = %q", appA)
	}

	userID, err := CanonicalUserID(ChannelTypeTelegram, "binding/1", "user/42")
	if err != nil {
		t.Fatalf("CanonicalUserID() error = %v", err)
	}
	if userID != "telegram/binding%2F1/user%2F42" {
		t.Fatalf("userID = %q", userID)
	}

	direct, err := DirectSessionID("binding/1", "user/42")
	if err != nil {
		t.Fatalf("DirectSessionID() error = %v", err)
	}
	if direct != "dm/binding%2F1/user%2F42" {
		t.Fatalf("direct = %q", direct)
	}

	group, err := GroupSessionID("binding/1", "group/7")
	if err != nil {
		t.Fatalf("GroupSessionID() error = %v", err)
	}
	thread, err := ThreadSessionID(group, "topic/9")
	if err != nil {
		t.Fatalf("ThreadSessionID() error = %v", err)
	}
	if thread != "group/binding%2F1/group%2F7/thread/topic%2F9" {
		t.Fatalf("thread = %q", thread)
	}
}

func TestCanonicalIdentifiersRejectInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "empty tenant",
			call: func() error {
				_, err := CanonicalAppName("", "app")
				return err
			},
		},
		{
			name: "untrimmed user",
			call: func() error {
				_, err := DirectSessionID("binding", " user ")
				return err
			},
		},
		{
			name: "control character",
			call: func() error {
				_, err := GroupSessionID("binding", "group\n")
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); err == nil {
				t.Fatal("error = nil, want non-nil")
			}
		})
	}
}

func TestParseCanonicalAppName(t *testing.T) {
	name, err := CanonicalAppName("tenant/a", "assistant b")
	if err != nil {
		t.Fatal(err)
	}
	tenantID, appID, err := ParseCanonicalAppName(name)
	if err != nil || tenantID != "tenant/a" || appID != "assistant b" {
		t.Fatalf("ParseCanonicalAppName(%q) = %q, %q, %v", name, tenantID, appID, err)
	}
	for _, invalid := range []string{"", "tenant/a", "tenant/a/app/b/extra", "other/a/app/b", "tenant/%ZZ/app/b"} {
		if _, _, err := ParseCanonicalAppName(invalid); err == nil {
			t.Fatalf("ParseCanonicalAppName(%q) error = nil", invalid)
		}
	}
}
