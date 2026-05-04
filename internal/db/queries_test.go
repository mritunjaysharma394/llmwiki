package db

import (
	"errors"
	"testing"
	"time"
)

func TestDeleteEvidenceForPage_RemovesAllRowsForThatPage(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("u", "h")
	d.UpsertPage(PageRecord{Title: "P1", Path: "p1.md", Body: "b", ContentHash: "h", SourceIDs: []int64{srcID}})
	d.UpsertPage(PageRecord{Title: "P2", Path: "p2.md", Body: "b", ContentHash: "h", SourceIDs: []int64{srcID}})
	p1, _ := d.GetPage("P1")
	p2, _ := d.GetPage("P2")
	if err := d.InsertEvidence(p1.ID, srcID, []Evidence{{Quote: "q1a"}, {Quote: "q1b"}}); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertEvidence(p2.ID, srcID, []Evidence{{Quote: "q2a"}, {Quote: "q2b"}}); err != nil {
		t.Fatal(err)
	}

	if err := d.DeleteEvidenceForPage(p1.ID); err != nil {
		t.Fatalf("DeleteEvidenceForPage: %v", err)
	}

	got1, _ := d.GetEvidenceForPage(p1.ID)
	if len(got1) != 0 {
		t.Errorf("page1 evidence not deleted: got %d rows", len(got1))
	}
	got2, _ := d.GetEvidenceForPage(p2.ID)
	if len(got2) != 2 {
		t.Errorf("page2 evidence got %d rows, want 2", len(got2))
	}
}

func TestDeleteEvidenceForPage_NoErrorOnEmptyPage(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("u", "h")
	d.UpsertPage(PageRecord{Title: "Empty", Path: "e.md", Body: "b", ContentHash: "h", SourceIDs: []int64{srcID}})
	d.UpsertPage(PageRecord{Title: "Full", Path: "f.md", Body: "b", ContentHash: "h", SourceIDs: []int64{srcID}})
	empty, _ := d.GetPage("Empty")
	full, _ := d.GetPage("Full")
	d.InsertEvidence(full.ID, srcID, []Evidence{{Quote: "q"}})

	if err := d.DeleteEvidenceForPage(empty.ID); err != nil {
		t.Errorf("DeleteEvidenceForPage on empty page: %v", err)
	}
	got, _ := d.GetEvidenceForPage(full.ID)
	if len(got) != 1 {
		t.Errorf("full page evidence count = %d, want 1", len(got))
	}
}

func TestDeleteEvidenceForPage_DeletesFTSRowsToo(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("u", "h")
	d.UpsertPage(PageRecord{Title: "P", Path: "p.md", Body: "b", ContentHash: "h", SourceIDs: []int64{srcID}})
	p, _ := d.GetPage("P")
	d.InsertEvidence(p.ID, srcID, []Evidence{{Quote: "uniquetokenfoo"}})

	if err := d.DeleteEvidenceForPage(p.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM evidence_fts WHERE evidence_fts MATCH 'uniquetokenfoo'`).Scan(&n); err != nil {
		t.Fatalf("evidence_fts count: %v", err)
	}
	if n != 0 {
		t.Errorf("evidence_fts row not removed: count=%d", n)
	}
}

func TestInsertPageUpdateLog_HappyPath(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("u", "h")
	d.UpsertPage(PageRecord{Title: "P", Path: "p.md", Body: "b", ContentHash: "abc", SourceIDs: []int64{srcID}})
	p, _ := d.GetPage("P")

	if err := d.InsertPageUpdateLog(PageUpdateLogEntry{
		PageID:           p.ID,
		SourceID:         srcID,
		PriorContentHash: "abc",
		NewContentHash:   "def",
		Outcome:          "updated",
		EvidenceAdded:    2,
		EvidenceRemoved:  1,
	}); err != nil {
		t.Fatalf("InsertPageUpdateLog: %v", err)
	}

	rows, _ := d.GetPageUpdateLog(p.ID, 10)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	got := rows[0]
	if got.PageID != p.ID || got.SourceID != srcID {
		t.Errorf("ids: page=%d source=%d, want %d/%d", got.PageID, got.SourceID, p.ID, srcID)
	}
	if got.PriorContentHash != "abc" || got.NewContentHash != "def" {
		t.Errorf("hashes: prior=%q new=%q", got.PriorContentHash, got.NewContentHash)
	}
	if got.Outcome != "updated" {
		t.Errorf("outcome=%q", got.Outcome)
	}
	if got.EvidenceAdded != 2 || got.EvidenceRemoved != 1 {
		t.Errorf("evidence counts: added=%d removed=%d", got.EvidenceAdded, got.EvidenceRemoved)
	}
	if got.CreatedAt.IsZero() {
		t.Errorf("CreatedAt is zero")
	}
}

func TestInsertPageUpdateLog_NullableSourceID(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("u", "h")
	d.UpsertPage(PageRecord{Title: "P", Path: "p.md", Body: "b", ContentHash: "abc", SourceIDs: []int64{srcID}})
	p, _ := d.GetPage("P")

	if err := d.InsertPageUpdateLog(PageUpdateLogEntry{
		PageID:           p.ID,
		SourceID:         0, // sentinel for "no source"
		PriorContentHash: "abc",
		NewContentHash:   "def",
		Outcome:          "updated",
	}); err != nil {
		t.Fatalf("InsertPageUpdateLog: %v", err)
	}
	var nullCount int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM page_update_log WHERE source_id IS NULL`).Scan(&nullCount); err != nil {
		t.Fatal(err)
	}
	if nullCount != 1 {
		t.Errorf("rows with NULL source_id = %d, want 1", nullCount)
	}
}

func TestInsertPageUpdateLog_NullableNewContentHash(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("u", "h")
	d.UpsertPage(PageRecord{Title: "P", Path: "p.md", Body: "b", ContentHash: "abc", SourceIDs: []int64{srcID}})
	p, _ := d.GetPage("P")

	if err := d.InsertPageUpdateLog(PageUpdateLogEntry{
		PageID:           p.ID,
		SourceID:         srcID,
		PriorContentHash: "abc",
		NewContentHash:   "",
		Outcome:          "failed",
	}); err != nil {
		t.Fatalf("InsertPageUpdateLog: %v", err)
	}
	var n int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM page_update_log WHERE new_content_hash IS NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("rows with NULL new_content_hash = %d, want 1", n)
	}
}

func TestInsertPageUpdateLog_RejectsUnknownOutcome(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("u", "h")
	d.UpsertPage(PageRecord{Title: "P", Path: "p.md", Body: "b", ContentHash: "abc", SourceIDs: []int64{srcID}})
	p, _ := d.GetPage("P")

	err := d.InsertPageUpdateLog(PageUpdateLogEntry{
		PageID:           p.ID,
		SourceID:         srcID,
		PriorContentHash: "abc",
		Outcome:          "wat",
	})
	if err == nil {
		t.Fatal("expected error for unknown outcome, got nil")
	}
	if !errors.Is(err, ErrInvalidOutcome) {
		t.Errorf("error %v does not wrap ErrInvalidOutcome", err)
	}
}

func TestGetPageUpdateLog_ReturnsRowsOrderedByCreatedAtDesc(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("u", "h")
	d.UpsertPage(PageRecord{Title: "P", Path: "p.md", Body: "b", ContentHash: "abc", SourceIDs: []int64{srcID}})
	p, _ := d.GetPage("P")

	for i, oc := range []string{"updated", "body_only", "skipped"} {
		if err := d.InsertPageUpdateLog(PageUpdateLogEntry{
			PageID: p.ID, SourceID: srcID,
			PriorContentHash: "h", NewContentHash: "h2",
			Outcome: oc, EvidenceAdded: i,
		}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}

	rows, err := d.GetPageUpdateLog(p.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	// Newest first: skipped, body_only, updated.
	want := []string{"skipped", "body_only", "updated"}
	for i, w := range want {
		if rows[i].Outcome != w {
			t.Errorf("rows[%d].Outcome = %q, want %q", i, rows[i].Outcome, w)
		}
	}
}

func TestGetPageUpdateLog_RespectsLimit(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("u", "h")
	d.UpsertPage(PageRecord{Title: "P", Path: "p.md", Body: "b", ContentHash: "abc", SourceIDs: []int64{srcID}})
	p, _ := d.GetPage("P")

	for i := 0; i < 10; i++ {
		if err := d.InsertPageUpdateLog(PageUpdateLogEntry{
			PageID: p.ID, SourceID: srcID,
			PriorContentHash: "h", NewContentHash: "h2",
			Outcome: "updated",
		}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := d.GetPageUpdateLog(p.ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Errorf("got %d rows, want 3", len(rows))
	}
}

func TestCountPageUpdateLogByOutcome_BucketsCorrectly(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("u", "h")
	d.UpsertPage(PageRecord{Title: "P", Path: "p.md", Body: "b", ContentHash: "abc", SourceIDs: []int64{srcID}})
	p, _ := d.GetPage("P")

	for _, oc := range []string{"updated", "updated", "failed", "body_only", "skipped"} {
		if err := d.InsertPageUpdateLog(PageUpdateLogEntry{
			PageID: p.ID, SourceID: srcID,
			PriorContentHash: "h", NewContentHash: "h2",
			Outcome: oc,
		}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := d.CountPageUpdateLogByOutcome()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"updated": 2, "failed": 1, "body_only": 1, "skipped": 1}
	if len(got) != len(want) {
		t.Errorf("len(got) = %d, want %d; got=%v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("got[%q] = %d, want %d", k, got[k], v)
		}
	}
}
