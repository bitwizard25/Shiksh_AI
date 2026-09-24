package bootstrap

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Role is a runnable part of the system. Roles run together in one process for local
// development, or as separate deployments scaled independently (spec §19.4).
type Role string

// RoleAPI serves the stateless REST API.
const RoleAPI Role = "api"

// KnownRoles lists the roles this build can run, in start order. Plan 3 adds "worker"; Plan 4 adds "realtime".
var KnownRoles = []Role{RoleAPI}

// ParseRoles parses a comma-separated role list. An empty string means every known role.
func ParseRoles(s string) ([]Role, error) {
	if strings.TrimSpace(s) == "" {
		return slices.Clone(KnownRoles), nil
	}
	var roles []Role
	for _, part := range strings.Split(s, ",") {
		r := Role(strings.TrimSpace(part))
		if r == "" {
			continue
		}
		if !slices.Contains(KnownRoles, r) {
			known := make([]string, len(KnownRoles))
			for i, k := range KnownRoles {
				known[i] = string(k)
			}
			return nil, fmt.Errorf("unknown role %q (known: %s)", r, strings.Join(known, ", "))
		}
		if !slices.Contains(roles, r) {
			roles = append(roles, r)
		}
	}
	if len(roles) == 0 {
		return nil, errors.New("no roles given")
	}
	return roles, nil
}
