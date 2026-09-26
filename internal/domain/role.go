package domain

// Role is what a principal is: a DM, or a player. There is no editor, because
// edit rights come from ownership rather than from a role — a player who may
// edit their own character page is not thereby allowed to edit the town, and a
// role that could express that would be a role with a foot in two doors.
//
// A second DM is another principal with RoleDM, not a separate concept.
type Role string

// The two roles, matching the CHECK constraint on principals.role.
const (
	RoleDM     Role = "dm"
	RolePlayer Role = "player"
)

// roles is the closed set.
var roles = []Role{RoleDM, RolePlayer}

// ParseRole returns the role for a stored value, or an error naming the values
// that would have been accepted.
func ParseRole(s string) (Role, error) {
	switch role := Role(s); role {
	case RoleDM, RolePlayer:
		return role, nil
	default:
		return "", oneOf("role", s, roleNames(roles)...)
	}
}

// Valid reports whether r is one of the two roles.
func (r Role) Valid() bool {
	return r == RoleDM || r == RolePlayer
}

// IsDM reports whether the role may see another DM's content. The one method
// on Role, because every access decision eventually asks it.
func (r Role) IsDM() bool {
	return r == RoleDM
}

// String returns the role as it appears in the database.
func (r Role) String() string {
	return string(r)
}

func roleNames(set []Role) []string {
	names := make([]string, 0, len(set))
	for _, role := range set {
		names = append(names, string(role))
	}
	return names
}
