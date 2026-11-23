package proc

import "testing"

func TestGoCommand(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"test"}, "test"},
		{[]string{"test", "./..."}, "test"},
		{[]string{"-C", "/tmp/mod", "test", "-count=1"}, "test"},
		{[]string{"-C=/tmp/mod", "test"}, "test"},
		{[]string{"--C", "/tmp/mod", "build"}, "build"},
		{[]string{"help", "test"}, "help"},
		{[]string{"-n", "test"}, ""},
		{nil, ""},
	}
	for _, tc := range cases {
		if got, _ := goCommand(tc.args); got != tc.want {
			t.Errorf("goCommand(%q) = %q, want %q", tc.args, got, tc.want)
		}
	}
}
