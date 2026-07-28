package auth

import "testing"

func TestPermissionsFor(t *testing.T) {
	tests := []struct {
		role string
		want []Permission
	}{
		{"admin", []Permission{PermAgent, PermConnect, PermAdmin}},
		{"user", []Permission{PermConnect}},
		{"agent", []Permission{PermAgent}},
		{"unknown", nil},
		{"", nil},
	}
	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			got := PermissionsFor(tt.role)
			if len(got) != len(tt.want) {
				t.Fatalf("PermissionsFor(%q): got %v, want %v", tt.role, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("PermissionsFor(%q)[%d]: got %q, want %q", tt.role, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestHasPermission(t *testing.T) {
	tests := []struct {
		role string
		perm Permission
		want bool
	}{
		{"admin", PermAdmin, true},
		{"admin", PermConnect, true},
		{"admin", PermAgent, true},
		{"user", PermConnect, true},
		{"user", PermAgent, false},
		{"user", PermAdmin, false},
		{"agent", PermAgent, true},
		{"agent", PermConnect, false},
		{"agent", PermAdmin, false},
		{"unknown", PermConnect, false},
		{"unknown", PermAdmin, false},
		{"", PermConnect, false},
	}
	for _, tt := range tests {
		t.Run(tt.role+"_"+string(tt.perm), func(t *testing.T) {
			got := HasPermission(tt.role, tt.perm)
			if got != tt.want {
				t.Fatalf("HasPermission(%q, %q): got %v, want %v", tt.role, tt.perm, got, tt.want)
			}
		})
	}
}

func TestUserHasPermission(t *testing.T) {
	tests := []struct {
		name        string
		role        string
		permConnect bool
		permAgent   bool
		perm        Permission
		want        bool
	}{
		// Admin always gets everything regardless of flags.
		{"admin_connect_true", "admin", false, false, PermConnect, true},
		{"admin_agent_true", "admin", false, false, PermAgent, true},
		{"admin_admin_true", "admin", false, false, PermAdmin, true},

		// User role checks flags.
		{"user_connect_allowed", "user", true, false, PermConnect, true},
		{"user_connect_denied", "user", false, false, PermConnect, false},
		{"user_agent_allowed", "user", false, true, PermAgent, true},
		{"user_agent_denied", "user", false, false, PermAgent, false},

		// Non-admin never gets admin permission.
		{"user_admin_denied", "user", true, true, PermAdmin, false},

		// Unknown role: still checks flags (role validation is the caller's job).
		{"unknown_connect_allowed", "unknown", true, true, PermConnect, true},
		{"unknown_connect_denied", "unknown", false, true, PermConnect, false},
		{"unknown_agent_allowed", "unknown", true, true, PermAgent, true},
		{"unknown_agent_denied", "unknown", true, false, PermAgent, false},
		{"unknown_admin_denied", "unknown", true, true, PermAdmin, false},

		// Empty role: same as unknown — checks flags.
		{"empty_connect_allowed", "", true, true, PermConnect, true},
		{"empty_connect_denied", "", false, true, PermConnect, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := UserHasPermission(tt.role, tt.permConnect, tt.permAgent, tt.perm)
			if got != tt.want {
				t.Fatalf("UserHasPermission(%q, %v, %v, %q): got %v, want %v",
					tt.role, tt.permConnect, tt.permAgent, tt.perm, got, tt.want)
			}
		})
	}
}
