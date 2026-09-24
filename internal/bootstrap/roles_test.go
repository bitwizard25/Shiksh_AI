package bootstrap

import (
	"slices"
	"strings"
	"testing"
)

func TestParseRoles(t *testing.T) {
	cases := map[string][]Role{
		"":           {RoleAPI},
		"api":        {RoleAPI},
		" api , api": {RoleAPI},
	}
	for in, want := range cases {
		got, err := ParseRoles(in)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("ParseRoles(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseRoles("api,worker"); err == nil || !strings.Contains(err.Error(), `"worker"`) || !strings.Contains(err.Error(), "api") {
		t.Errorf("unknown role err = %v, want it to name the role and list known roles", err)
	}
	if _, err := ParseRoles(" , "); err == nil {
		t.Error("blank role list accepted")
	}
}
