package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/OttoPrua/Cardex/internal/apiusage"
	"github.com/OttoPrua/Cardex/internal/runner"
)

// isOfflineChannelQuote recognizes global-flag-stripped command arguments.
// Executable entry and Main use the same predicate before runtime setup.
func isOfflineChannelQuote(args []string) bool {
	return len(args) >= 2 && args[0] == "channel" && args[1] == "quote-fixture"
}

// channelQuoteFixture is offline and read-only, regardless of host HOME or
// status: it never opens a store, loads engine config, resolves credentials,
// contacts a provider, or treats a CLI cost field as supplier money.
func channelQuoteFixture(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("channel quote-fixture", "inspect offline metering; numerical estimates are synthetic only",
		"--usage FILE --channel ID --model ID --binding SHA256 --at RFC3339 [--price FILE] [--show-synthetic-estimate]", stdout, stderr)
	usagePath := fs.String("usage", "", "bounded local JSON: a synthetic Messages response or archived CLI terminal result")
	pricePath := fs.String("price", "", "optional local synthetic price snapshot JSON")
	format := fs.String("format", "messages-fixture", "messages-fixture or recorded-claude-cli")
	id := fs.String("id", "offline-fixture", "offline receipt identifier, not a live attempt")
	channel := fs.String("channel", "", "exact fixture channel")
	model := fs.String("model", "", "exact fixture model")
	binding := fs.String("binding", "", "exact nonsecret fixture binding digest")
	at := fs.String("at", "", "explicit fixture operation time (RFC3339)")
	mode := fs.String("operation", "attempt", "attempt, comparison, or probe")
	show := fs.Bool("show-synthetic-estimate", false, "display arithmetic from this synthetic price version; never real cost")
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	invalid := func(reason string) int {
		fmt.Fprintln(stderr, reason)
		return 2
	}
	if fs.NArg() != 0 || *usagePath == "" {
		return invalid("quote-fixture requires a local usage file and exact attribution")
	}
	recordedAt, err := time.Parse(time.RFC3339Nano, *at)
	if err != nil {
		return invalid("quote-fixture requires a valid RFC3339 operation time")
	}
	raw, err := readAPIFixtureFile(*usagePath)
	if err != nil {
		return invalid("quote-fixture usage file must be bounded readable JSON")
	}
	var metering apiusage.Measurement
	switch *format {
	case "messages-fixture":
		metering, err = runner.ParseSyntheticMessagesUsage(raw, *model)
	case "recorded-claude-cli":
		metering, err = runner.ParseRecordedClaudeMetering(raw, *model)
	default:
		return invalid("quote-fixture format must be messages-fixture or recorded-claude-cli")
	}
	if err != nil {
		return invalid("quote-fixture usage is invalid or ambiguous")
	}
	evidence := apiusage.Evidence{ID: *id, Operation: apiusage.Operation(*mode), Channel: *channel, Model: *model,
		Binding: *binding, RecordedAt: recordedAt.UTC(), Metering: metering}
	var price *apiusage.PriceSnapshot
	if *pricePath != "" {
		raw, err := readAPIFixtureFile(*pricePath)
		if err != nil {
			return invalid("quote-fixture price file must be bounded readable JSON")
		}
		p, err := apiusage.ReadPriceFixture(raw)
		if err != nil {
			return invalid("quote-fixture price snapshot must use the explicit synthetic schema")
		}
		if metering.Source == apiusage.FixtureResponse {
			_, hash, err := p.Canonical()
			if err != nil {
				return invalid("quote-fixture cannot fingerprint price snapshot")
			}
			evidence.Price, evidence.PriceVersion = hash, p.Version
		}
		price = &p
	}
	if err := evidence.Validate(); err != nil {
		return invalid("quote-fixture requires valid, explicit fixture attribution and operation")
	}
	return writeJSON(stdout, struct {
		Metering apiusage.View     `json:"metering"`
		Estimate apiusage.Estimate `json:"estimate"`
	}{evidence.View(), apiusage.Quote(evidence, price, *show)})
}

func readAPIFixtureFile(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() <= 0 || before.Size() > apiusage.MaxEvidenceBytes {
		return nil, apiusage.ErrEvidence
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !os.SameFile(before, st) || !st.Mode().IsRegular() || st.Size() <= 0 || st.Size() > apiusage.MaxEvidenceBytes {
		return nil, apiusage.ErrEvidence
	}
	raw, err := io.ReadAll(io.LimitReader(f, apiusage.MaxEvidenceBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > apiusage.MaxEvidenceBytes {
		return nil, apiusage.ErrEvidence
	}
	return raw, nil
}
