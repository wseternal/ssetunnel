package auth

// Permission represents an action a user is allowed to perform.
type Permission string

const (
	PermAgent   Permission = "agent"   // register and maintain an agent tunnel
	PermConnect Permission = "connect" // connect to an agent via entry TCP
	PermAdmin   Permission = "admin"   // manage users, tokens, and configuration
)

// PermissionsFor returns the set of permissions granted by the given role.
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
