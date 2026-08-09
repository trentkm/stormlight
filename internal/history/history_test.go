package history

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trentkm/stormlight/internal/agent"
)

func testLog(t *testing.T) *Log {
	t.Helper()
	return NewLogAt(filepath.Join(t.TempDir(), "sessions.jsonl"))
}

func record(id string, updatedAt time.Time) Record {
	return Record{
		SessionID: id,
		Provider:  agent.ProviderClaude,
		Task:      "task for " + id,
		CreatedAt: updatedAt.Add(-time.Minute),
		UpdatedAt: updatedAt,
	}
}

func TestRecordsCoalesceBySessionNewestFirst(t *testing.T) {
	log := testLog(t)
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

	first := record("session-a", base)
	if err := log.Append(first); err != nil {
		t.Fatal(err)
	}
	if err := log.Append(record("session-b", base.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	updated := record("session-a", base.Add(2*time.Minute))
	updated.Summary = "second turn"
	if err := log.Append(updated); err != nil {
		t.Fatal(err)
	}

	records, err := log.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %#v", records)
	}
	if records[0].SessionID != "session-a" || records[0].Summary != "second turn" {
		t.Fatalf("newest record = %#v", records[0])
	}
	if records[1].SessionID != "session-b" {
		t.Fatalf("second record = %#v", records[1])
	}
}

func TestRecordsSkipTornLines(t *testing.T) {
	log := testLog(t)
	if err := log.Append(record("session-a", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	// A hook process killed mid-write leaves a truncated line; the log
	// must keep serving every intact record around it.
	file, err := os.OpenFile(log.Path(), os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"session_id":"session-b","upd`); err != nil {
		t.Fatal(err)
	}
	file.Close()

	records, err := log.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].SessionID != "session-a" {
		t.Fatalf("records = %#v", records)
	}
}

func TestMissingLogReadsAsEmpty(t *testing.T) {
	records, err := testLog(t).Records()
	if err != nil || len(records) != 0 {
		t.Fatalf("records = %#v, err = %v", records, err)
	}
}

func TestAppendRejectsRecordWithoutSessionID(t *testing.T) {
	if err := testLog(t).Append(Record{Task: "orphan"}); err == nil {
		t.Fatal("expected error for record without session id")
	}
}

func TestConcurrentAppendsAllLand(t *testing.T) {
	log := testLog(t)
	base := time.Now().UTC()
	var group sync.WaitGroup
	for worker := range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			for turn := range 16 {
				id := string(rune('a' + worker))
				if err := log.Append(
					record("session-"+id, base.Add(time.Duration(turn)*time.Second)),
				); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	group.Wait()

	records, err := log.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 8 {
		t.Fatalf("expected 8 sessions, got %d", len(records))
	}
	data, err := os.ReadFile(log.Path())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 8*16 {
		t.Fatalf("expected %d intact lines, got %d", 8*16, len(lines))
	}
}

func TestCompactRewritesOnlyWhenBloated(t *testing.T) {
	log := testLog(t)
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	for turn := range compactionSlack + 10 {
		if err := log.Append(
			record("session-a", base.Add(time.Duration(turn)*time.Second)),
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Append(record("session-b", base)); err != nil {
		t.Fatal(err)
	}

	if err := log.Compact(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log.Path())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 compacted lines, got %d", len(lines))
	}

	records, err := log.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].SessionID != "session-a" {
		t.Fatalf("records after compact = %#v", records)
	}

	// Below the slack threshold a rewrite saves less than it costs, so the
	// file must be left alone.
	stat, err := os.Stat(log.Path())
	if err != nil {
		t.Fatal(err)
	}
	before := stat.ModTime()
	if err := log.Compact(); err != nil {
		t.Fatal(err)
	}
	stat, err = os.Stat(log.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !stat.ModTime().Equal(before) {
		t.Fatal("compact rewrote a file below the slack threshold")
	}
}
