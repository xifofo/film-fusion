// plutofish-probe checks the notification protocol without running any transfer
// business or calling any storage provider. It keeps its cursor only in memory;
// it is a diagnostic, not the production consumer.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"film-fusion/app/plutofish"
)

type diagnosticSink struct {
	cursor  string
	seen    map[string]bool
	types   map[string]int
	commits int
}

func (s *diagnosticSink) Cursor(context.Context) (string, error) { return s.cursor, nil }

func (s *diagnosticSink) Commit(_ context.Context, events []plutofish.Event, cursor string) error {
	for _, event := range events {
		if s.seen[event.ID] {
			continue
		}
		s.seen[event.ID] = true
		// Only fixed, known category names leave the process; future event type
		// values might contain user data and are never printed verbatim.
		typ := "unrecognized"
		switch event.Type {
		case "transfer.succeeded", "transfer.failed", "transfer.unknown":
			typ = event.Type
		}
		s.types[typ]++
	}
	if cursor != "" {
		s.cursor = cursor
		s.commits++
	}
	return nil
}

type diagnosticResult struct {
	DiagnosticOnly       bool           `json:"diagnosticOnly"`
	PersistedProgress    bool           `json:"persistedProgress"`
	State                string         `json:"state"`
	HTTPStatus           int            `json:"httpStatus"`
	ErrorCode            string         `json:"errorCode"`
	EventCount           int            `json:"eventCount"`
	EventTypes           map[string]int `json:"eventTypes"`
	PagesOrCursorCommits int            `json:"pagesOrCursorCommits"`
	StreamConnected      bool           `json:"streamConnected"`
}

func main() { os.Exit(run()) }

func run() int {
	baseURL := flag.String("base-url", plutofish.DefaultBaseURL, "PlutoFish service base URL (HTTPS, except loopback mocks)")
	duration := flag.Duration("duration", 20*time.Second, "maximum diagnostic connection duration")
	flag.Parse()
	result := diagnosticResult{DiagnosticOnly: true, State: "blocked", EventTypes: map[string]int{}}
	if *duration <= 0 || flag.NArg() != 0 {
		result.ErrorCode = "configuration"
		printResult(result)
		return 1
	}
	// Read only stdin, with a fixed bound. Do not accept credentials through
	// argv, environment, URL, files, logging, or diagnostic output.
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 8193))
	if err != nil || len(data) > 8192 {
		result.ErrorCode = "key_input"
		printResult(result)
		return 1
	}
	key := strings.TrimSpace(string(data))
	sink := &diagnosticSink{seen: map[string]bool{}, types: map[string]int{}}
	client, err := plutofish.NewClient(plutofish.Config{BaseURL: *baseURL, APIKey: key}, sink)
	if err != nil {
		result.ErrorCode = "configuration"
		printResult(result)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()
	err = client.Run(ctx)
	status := client.Snapshot()
	result.State = status.State
	result.HTTPStatus = status.HTTPStatus
	result.ErrorCode = status.ErrorCode
	result.EventCount = len(sink.seen)
	result.EventTypes = sink.types
	result.PagesOrCursorCommits = sink.commits
	result.StreamConnected = status.LastConnectedAt != nil
	if !result.StreamConnected && result.ErrorCode == "" {
		result.ErrorCode = "stream_not_connected_before_deadline"
	}
	printResult(result)
	if err != nil || !result.StreamConnected || result.ErrorCode != "" {
		return 1
	}
	return 0
}

func printResult(result diagnosticResult) {
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "could not write diagnostic result")
	}
}
