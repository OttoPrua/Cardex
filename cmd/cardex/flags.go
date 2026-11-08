package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
)

func newFlagSet(name, purpose, params string, stdout, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stdout, "%s\npurpose: %s\n\nparameters:\n", name, purpose)
		if params != "" {
			fmt.Fprintf(stdout, "  %s\n", params)
		}
		fs.SetOutput(stdout)
		fs.PrintDefaults()
		fs.SetOutput(stderr)
	}
	return fs
}

func parseSet(fs *flag.FlagSet, args []string) (int, bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, false
		}
		return 2, false
	}
	return 0, true
}

func groupHelp(stdout io.Writer, args []string, text string) bool {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		fmt.Fprint(stdout, text)
		return true
	}
	return false
}

func finishSoft(stdout, stderr io.Writer, asJSON bool, notes []string, v any, line string) int {
	for _, note := range notes {
		fmt.Fprintf(stderr, "needs correction: %s\n", note)
		if !asJSON {
			fmt.Fprintf(stdout, "needs correction: %s\n", note)
		}
	}
	return emit(stdout, asJSON, v, line)
}

func flagSeen(fs *flag.FlagSet, name string) bool {
	seen := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			seen = true
		}
	})
	return seen
}

// softNumber parses one business number without failing the flag set.
// An unparsable value keeps the previous or default number and is reported
// with the other corrections. A later valid value on the same flag still applies.
type softNumber struct {
	name string
	kind numberKind
	n    int
	i64  int64
	f64  float64
	ok   bool
	bad  []string
}

type numberKind int

const (
	numberInt numberKind = iota
	numberInt64
	numberFloat
)

func (s *softNumber) String() string {
	switch s.kind {
	case numberInt64:
		return strconv.FormatInt(s.i64, 10)
	case numberFloat:
		return strconv.FormatFloat(s.f64, 'f', -1, 64)
	default:
		return strconv.Itoa(s.n)
	}
}

func (s *softNumber) Set(raw string) error {
	switch s.kind {
	case numberInt64:
		n, err := strconv.ParseInt(raw, 0, 64)
		if err != nil {
			s.bad = append(s.bad, s.name+" "+raw)
			return nil
		}
		s.i64 = n
	case numberFloat:
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			s.bad = append(s.bad, s.name+" "+raw)
			return nil
		}
		s.f64 = n
	default:
		n, err := strconv.ParseInt(raw, 0, strconv.IntSize)
		if err != nil {
			s.bad = append(s.bad, s.name+" "+raw)
			return nil
		}
		s.n = int(n)
	}
	s.ok = true
	return nil
}
