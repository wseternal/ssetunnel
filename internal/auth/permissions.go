package auth

// Permission represents an action a user is allowed to perform.
type Permission string

const (
	PermAgent   Permission = "agent"   // register and maintain an agent tunnel
	PermConnect Permission = "connect" // connect to an agent via agent TCP
	PermAdmin   Permission = "admin"   // manage users, tokens, and configuration
)

// PermissionsFor returns the set of permissions granted by the given role.
// The "agent" case is kept for backward compatibility with existing bearer
// tokens in the tokens table; it is no longer a valid user role.
func PermissionsFor(role string) []Permission {
	switch role {
	case "admin":
		return []Permission{PermAgent, PermConnect, PermAdmin}
	case "user":
		return []Permission{PermConnect}
	case "agent":
		return []Permission{PermAgent}
	default:
		return nil
	}
}

// HasPermission reports whether the given role grants the specified permission.
func HasPermission(role string, perm Permission) bool {
	for _, p := range PermissionsFor(role) {
		if p == perm {
			return true
		}
	}
	return false
}

// UserHasPermission checks whether a user has the given permission.
// Admin role always grants all permissions regardless of column values.
// Otherwise, the per-user boolean flags are checked.
func UserHasPermission(role string, permConnect, permAgent bool, perm Permission) bool {
	if role == "admin" {
		return true
	}
	switch perm {
	case PermConnect:
		return permConnect
	case PermAgent:
		return permAgent
	case PermAdmin:
		return false
	}
	return false
}
