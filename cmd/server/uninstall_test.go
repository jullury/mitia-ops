package main

import "testing"

func TestIsUninstall(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"uninstall"}, true},
		{[]string{"uninstall", "--yes"}, true},
		{[]string{}, false},
		{[]string{"serve"}, false},
		{[]string{"up"}, false},
	}
	for _, c := range cases {
		if got := isUninstall(c.args); got != c.want {
			t.Errorf("isUninstall(%q) = %v, want %v", c.args, got, c.want)
		}
	}
}
