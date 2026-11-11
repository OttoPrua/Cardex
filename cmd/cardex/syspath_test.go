package main

import "testing"

func TestWithSystemPath(t *testing.T) {
	cases := []struct{ in, goos, want string }{
		{"/opt/homebrew/bin:/usr/bin:/bin", "darwin", "/opt/homebrew/bin:/usr/bin:/bin:/usr/local/bin:/usr/sbin:/sbin"},
		{"/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin", "linux", "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"},
		{"", "darwin", "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"},
		{"/sbin/:/x", "darwin", "/sbin/:/x:/usr/local/bin:/usr/bin:/bin:/usr/sbin"},
		{`C:\x`, "windows", `C:\x`},
	}
	for _, c := range cases {
		if got := withSystemPath(c.in, c.goos); got != c.want {
			t.Errorf("withSystemPath(%q,%s)=%q want %q", c.in, c.goos, got, c.want)
		}
	}
}
