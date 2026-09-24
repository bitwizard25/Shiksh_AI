package main

import (
	"strings"
	"testing"
)

func TestRunRejectsBadInvocations(t *testing.T) {
	cases := map[string]struct {
		args []string
		want string
	}{
		"no command":      {nil, "missing command"},
		"unknown command": {[]string{"launch"}, `unknown command "launch"`},
		"unknown role":    {[]string{"serve", "--roles=api,teleport"}, `unknown role "teleport"`},
		"bad flag":        {[]string{"serve", "--nope"}, "flag provided but not defined"},
	}
	for name, tc := range cases {
		err := run(tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: run(%q) = %v, want error containing %q", name, tc.args, err, tc.want)
		}
	}
}

func TestRunHelp(t *testing.T) {
	if err := run([]string{"help"}); err != nil {
		t.Fatalf("run(help) = %v", err)
	}
}
