package db

import (
	"errors"
	"fmt"
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

// --- Sub-project 7 (v0.7) — Phase D / Task 8 — schema_hash queries ---

func TestUpdateSchemaHash_StampsRow(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("u", "h")
	d.UpsertPage(PageRecord{Title: "P", Path: "p.md", Body: "b", ContentHash: "h", SourceIDs: []int64{srcID}})
	p, _ := d.GetPage("P")

	if err := d.UpdateSchemaHash(p.ID, "abc123"); err != nil {
		t.Fatalf("UpdateSchemaHash: %v", err)
	}
	var got string
	if err := d.sql.QueryRow(`SELECT schema_hash FROM pages WHERE id = ?`, p.ID).Scan(&got); err != nil {
		t.Fatalf("SELECT schema_hash: %v", err)
	}
	if got != "abc123" {
		t.Errorf("schema_hash = %q, want abc123", got)
	}
}

func TestUpdateSchemaHash_IdempotentOnSameHash(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("u", "h")
	d.UpsertPage(PageRecord{Title: "P", Path: "p.md", Body: "b", ContentHash: "h", SourceIDs: []int64{srcID}})
	p, _ := d.GetPage("P")

	if err := d.UpdateSchemaHash(p.ID, "abc123"); err != nil {
		t.Fatalf("first UpdateSchemaHash: %v", err)
	}
	if err := d.UpdateSchemaHash(p.ID, "abc123"); err != nil {
		t.Fatalf("second UpdateSchemaHash: %v", err)
	}
	var got string
	d.sql.QueryRow(`SELECT schema_hash FROM pages WHERE id = ?`, p.ID).Scan(&got)
	if got != "abc123" {
		t.Errorf("schema_hash = %q, want abc123", got)
	}
}

func TestUpdateSchemaHash_NonexistentPageID_ReturnsErr(t *testing.T) {
	d := mustOpen(t)
	err := d.UpdateSchemaHash(99999, "abc123")
	if err == nil {
		t.Fatal("expected error for nonexistent page ID, got nil")
	}
	if !errors.Is(err, ErrPageNotFound) {
		t.Errorf("error %v does not wrap ErrPageNotFound", err)
	}
}

func TestCountPagesByHashState_ReturnsCurrentAndPriorTuple(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("u", "h")
	for i, h := range []string{"X", "X", "X", "Y", "Y"} {
		title := "P" + string(rune('A'+i))
		d.UpsertPage(PageRecord{Title: title, Path: title + ".md", Body: "b", ContentHash: "ch", SourceIDs: []int64{srcID}})
		p, _ := d.GetPage(title)
		if err := d.UpdateSchemaHash(p.ID, h); err != nil {
			t.Fatalf("stamp %s: %v", title, err)
		}
	}
	current, prior, err := d.CountPagesByHashState("X")
	if err != nil {
		t.Fatalf("CountPagesByHashState: %v", err)
	}
	if current != 3 || prior != 2 {
		t.Errorf("current=%d, prior=%d; want 3/2", current, prior)
	}
}

func TestCountPagesByHashState_AllAtActive_PriorIsZero(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("u", "h")
	for i := 0; i < 5; i++ {
		title := "P" + string(rune('A'+i))
		d.UpsertPage(PageRecord{Title: title, Path: title + ".md", Body: "b", ContentHash: "ch", SourceIDs: []int64{srcID}})
		p, _ := d.GetPage(title)
		d.UpdateSchemaHash(p.ID, "X")
	}
	current, prior, err := d.CountPagesByHashState("X")
	if err != nil {
		t.Fatalf("CountPagesByHashState: %v", err)
	}
	if current != 5 || prior != 0 {
		t.Errorf("current=%d, prior=%d; want 5/0", current, prior)
	}
}

func TestCountPagesByHashState_NoneAtActive_CurrentIsZero(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("u", "h")
	for i := 0; i < 5; i++ {
		title := "P" + string(rune('A'+i))
		d.UpsertPage(PageRecord{Title: title, Path: title + ".md", Body: "b", ContentHash: "ch", SourceIDs: []int64{srcID}})
		p, _ := d.GetPage(title)
		d.UpdateSchemaHash(p.ID, "X")
	}
	current, prior, err := d.CountPagesByHashState("Z")
	if err != nil {
		t.Fatalf("CountPagesByHashState: %v", err)
	}
	if current != 0 || prior != 5 {
		t.Errorf("current=%d, prior=%d; want 0/5", current, prior)
	}
}

func TestListPagesByHash_ReturnsMatchingRows(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("u", "h")
	for i, h := range []string{"X", "X", "X", "Y", "Y"} {
		title := "P" + string(rune('A'+i))
		d.UpsertPage(PageRecord{Title: title, Path: title + ".md", Body: "b", ContentHash: "ch", SourceIDs: []int64{srcID}})
		p, _ := d.GetPage(title)
		d.UpdateSchemaHash(p.ID, h)
	}
	gotX, err := d.ListPagesByHash("X", 10)
	if err != nil {
		t.Fatalf("ListPagesByHash X: %v", err)
	}
	if len(gotX) != 3 {
		t.Errorf("X count = %d, want 3", len(gotX))
	}
	gotY, err := d.ListPagesByHash("Y", 10)
	if err != nil {
		t.Fatalf("ListPagesByHash Y: %v", err)
	}
	if len(gotY) != 2 {
		t.Errorf("Y count = %d, want 2", len(gotY))
	}
}

func TestListPagesByHash_RespectsLimit(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("u", "h")
	for i := 0; i < 10; i++ {
		title := fmt.Sprintf("P%02d", i)
		d.UpsertPage(PageRecord{Title: title, Path: title + ".md", Body: "b", ContentHash: "ch", SourceIDs: []int64{srcID}})
		p, _ := d.GetPage(title)
		d.UpdateSchemaHash(p.ID, "X")
	}
	got, err := d.ListPagesByHash("X", 3)
	if err != nil {
		t.Fatalf("ListPagesByHash: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("count = %d, want 3", len(got))
	}
}

func TestListPagesNotAtHash_ReturnsNonMatchingRows(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("u", "h")
	for i, h := range []string{"X", "X", "X", "Y", "Y"} {
		title := "P" + string(rune('A'+i))
		d.UpsertPage(PageRecord{Title: title, Path: title + ".md", Body: "b", ContentHash: "ch", SourceIDs: []int64{srcID}})
		p, _ := d.GetPage(title)
		d.UpdateSchemaHash(p.ID, h)
	}
	got, err := d.ListPagesNotAtHash("X", 10)
	if err != nil {
		t.Fatalf("ListPagesNotAtHash: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("count = %d, want 2 (the Y pages)", len(got))
	}
	for _, p := range got {
		if p.Title == "PA" || p.Title == "PB" || p.Title == "PC" {
			t.Errorf("ListPagesNotAtHash X returned an X page: %q", p.Title)
		}
	}
}

func TestListPagesNotAtHash_RespectsLimit(t *testing.T) {
	d := mustOpen(t)
	srcID, _ := d.UpsertSource("u", "h")
	for i := 0; i < 10; i++ {
		title := fmt.Sprintf("P%02d", i)
		d.UpsertPage(PageRecord{Title: title, Path: title + ".md", Body: "b", ContentHash: "ch", SourceIDs: []int64{srcID}})
		p, _ := d.GetPage(title)
		d.UpdateSchemaHash(p.ID, "Y") // every page is "not X"
	}
	got, err := d.ListPagesNotAtHash("X", 4)
	if err != nil {
		t.Fatalf("ListPagesNotAtHash: %v", err)
	}
	if len(got) != 4 {
		t.Errorf("count = %d, want 4", len(got))
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
