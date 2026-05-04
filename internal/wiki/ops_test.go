package wiki

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/mritunjaysharma394/llmwiki/internal/ingest"
	"github.com/mritunjaysharma394/llmwiki/internal/llm"
	"github.com/mritunjaysharma394/llmwiki/internal/schema"
)

func sourceFiles(source string) []ingest.SourceFile {
	return []ingest.SourceFile{ingest.NewSourceFile("doc", []byte(source))}
}

func TestValidateAndAttachEvidence(t *testing.T) {
	source := `Line one of source.
Line two contains the quick brown fox.
Line three.
Line four mentions kafka consumer group offset.
Line five.`

	pages := []Page{
		{
			Title: "Good page",
			Body:  "About the fox",
			Evidence: []Evidence{
				{Quote: "quick brown fox"},
				{Quote: "this string is NOT in source"},
			},
		},
		{
			Title: "Another good page",
			Body:  "Kafka",
			Evidence: []Evidence{{Quote: "kafka consumer group offset"}},
		},
		{
			Title: "Hallucinated page",
			Body:  "Made up",
			Evidence: []Evidence{{Quote: "absolutely not present anywhere"}},
		},
		{
			Title: "Empty evidence page",
			Body:  "Nope",
		},
	}

	got, dropped := ValidateAndAttachEvidence(pages, sourceFiles(source))

	if len(got) != 2 {
		t.Fatalf("got %d valid pages, want 2 (titles: %v)", len(got), pageTitles(got))
	}
	if got[0].Title != "Good page" {
		t.Errorf("got[0].Title = %q", got[0].Title)
	}
	if len(got[0].Evidence) != 1 {
		t.Errorf("good page kept %d evidence, want 1", len(got[0].Evidence))
	}
	if got[0].Evidence[0].LineStart != 2 || got[0].Evidence[0].LineEnd != 2 {
		t.Errorf("good page line range = %d-%d, want 2-2", got[0].Evidence[0].LineStart, got[0].Evidence[0].LineEnd)
	}
	if got[1].Evidence[0].LineStart != 4 {
		t.Errorf("kafka quote line_start = %d, want 4", got[1].Evidence[0].LineStart)
	}
	if dropped != 2 {
		t.Errorf("dropped = %d, want 2", dropped)
	}
}

func TestValidateAndAttachEvidenceMultilineQuote(t *testing.T) {
	source := "alpha\nbeta\ngamma\ndelta\n"
	pages := []Page{{
		Title:    "T",
		Body:     "b",
		Evidence: []Evidence{{Quote: "beta\ngamma"}},
	}}
	got, _ := ValidateAndAttachEvidence(pages, sourceFiles(source))
	if len(got) != 1 {
		t.Fatal("page dropped")
	}
	if got[0].Evidence[0].LineStart != 2 || got[0].Evidence[0].LineEnd != 3 {
		t.Errorf("multiline lines = %d-%d, want 2-3", got[0].Evidence[0].LineStart, got[0].Evidence[0].LineEnd)
	}
}

func TestValidateAndAttachEvidenceUnicode(t *testing.T) {
	source := "héllo wörld\nsecond line\n"
	pages := []Page{{
		Title:    "T",
		Body:     "b",
		Evidence: []Evidence{{Quote: "héllo wörld"}},
	}}
	got, _ := ValidateAndAttachEvidence(pages, sourceFiles(source))
	if len(got) != 1 {
		t.Fatalf("unicode quote dropped (page count=%d)", len(got))
	}
}

func pageTitles(ps []Page) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Title
	}
	return out
}

func TestExtractPagesFromToolResult(t *testing.T) {
	raw := map[string]any{
		"pages": []any{
			map[string]any{
				"title": "P1",
				"body":  "body 1",
				"links": []any{
					map[string]any{"to": "P2", "type": "supports"},
				},
				"evidence": []any{
					map[string]any{"quote": "first quote"},
					map[string]any{"quote": "second", "explanation": "ignored"},
				},
			},
			map[string]any{
				"title":    "P2",
				"body":     "body 2",
				"evidence": []any{map[string]any{"quote": "another"}},
			},
		},
	}
	pages, err := ExtractPagesFromToolResult(raw)
	if err != nil {
		t.Fatalf("ExtractPagesFromToolResult: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("got %d pages, want 2", len(pages))
	}
	if len(pages[0].Evidence) != 2 {
		t.Errorf("page 0 evidence count = %d, want 2", len(pages[0].Evidence))
	}
	if len(pages[0].Links) != 1 || pages[0].Links[0].To != "P2" {
		t.Errorf("page 0 links = %+v", pages[0].Links)
	}
}

func TestValidateAndAttachEvidencePerFile(t *testing.T) {
	files := []ingest.SourceFile{
		ingest.NewSourceFile("a.md", []byte("alpha line\nbeta line\n")),
		ingest.NewSourceFile("b.md", []byte("gamma\ndelta\n")),
	}
	pages := []Page{
		{
			Title: "Found-correctly",
			Body:  "x",
			Evidence: []Evidence{
				{Quote: "alpha line", SourceFilePath: "a.md"},
				{Quote: "delta", SourceFilePath: "b.md"},
			},
		},
		{
			Title: "Wrong-file",
			Body:  "x",
			// quote exists, but the named file doesn't contain it
			Evidence: []Evidence{{Quote: "alpha line", SourceFilePath: "b.md"}},
		},
		{
			Title: "Unknown-file",
			Body:  "x",
			Evidence: []Evidence{{Quote: "alpha line", SourceFilePath: "z.md"}},
		},
	}
	kept, dropped := ValidateAndAttachEvidence(pages, files)
	if len(kept) != 1 || kept[0].Title != "Found-correctly" {
		t.Fatalf("kept = %v", pageTitles(kept))
	}
	if dropped != 2 {
		t.Errorf("dropped = %d, want 2", dropped)
	}
	e0 := kept[0].Evidence[0]
	if e0.SourceFilePath != "a.md" || e0.LineStart != 1 || e0.LineEnd != 1 {
		t.Errorf("ev[0] = %+v", e0)
	}
	e1 := kept[0].Evidence[1]
	if e1.SourceFilePath != "b.md" || e1.LineStart != 2 || e1.LineEnd != 2 {
		t.Errorf("ev[1] = %+v", e1)
	}
}

func TestValidateAndAttachEvidenceFallbackWhenSourceFileMissing(t *testing.T) {
	files := []ingest.SourceFile{
		ingest.NewSourceFile("only.md", []byte("the answer is 42\n")),
	}
	pages := []Page{{
		Title:    "T",
		Body:     "b",
		Evidence: []Evidence{{Quote: "the answer is 42"}}, // no SourceFilePath
	}}
	kept, _ := ValidateAndAttachEvidence(pages, files)
	if len(kept) != 1 {
		t.Fatal("page dropped")
	}
	got := kept[0].Evidence[0]
	if got.SourceFilePath != "only.md" {
		t.Errorf("fallback didn't attribute: %+v", got)
	}
	if got.LineStart != 1 || got.LineEnd != 1 {
		t.Errorf("fallback line range = %d-%d, want 1-1", got.LineStart, got.LineEnd)
	}
}

func TestValidateAndAttachEvidenceQuoteNamedButNotInNamedFile(t *testing.T) {
	files := []ingest.SourceFile{
		ingest.NewSourceFile("a.md", []byte("only this line\n")),
		ingest.NewSourceFile("b.md", []byte("a different thing\n")),
	}
	// LLM names b.md but the quote is actually in a.md — must drop, not silently fix.
	pages := []Page{{
		Title:    "Misattributed",
		Body:     "x",
		Evidence: []Evidence{{Quote: "only this line", SourceFilePath: "b.md"}},
	}}
	kept, dropped := ValidateAndAttachEvidence(pages, files)
	if len(kept) != 0 {
		t.Errorf("expected page dropped, got kept=%v", pageTitles(kept))
	}
	if dropped != 1 {
		t.Errorf("dropped = %d, want 1", dropped)
	}
}

func TestExtractPagesFromToolResultReadsSourceFile(t *testing.T) {
	raw := map[string]any{
		"pages": []any{
			map[string]any{
				"title": "P",
				"body":  "b",
				"evidence": []any{
					map[string]any{"quote": "q1", "source_file": "a.md"},
					map[string]any{"quote": "q2"}, // missing source_file
				},
			},
		},
	}
	pages, err := ExtractPagesFromToolResult(raw)
	if err != nil {
		t.Fatalf("ExtractPagesFromToolResult: %v", err)
	}
	if len(pages) != 1 || len(pages[0].Evidence) != 2 {
		t.Fatalf("unexpected pages: %+v", pages)
	}
	if pages[0].Evidence[0].SourceFilePath != "a.md" {
		t.Errorf("ev[0].SourceFilePath = %q, want %q", pages[0].Evidence[0].SourceFilePath, "a.md")
	}
	if pages[0].Evidence[1].SourceFilePath != "" {
		t.Errorf("ev[1].SourceFilePath = %q, want empty", pages[0].Evidence[1].SourceFilePath)
	}
}

func TestExtractPagesMissingPagesKey(t *testing.T) {
	_, err := ExtractPagesFromToolResult(map[string]any{"foo": "bar"})
	if err == nil {
		t.Fatal("expected error for missing 'pages' key")
	}
}

func TestBuildAnswerPromptIncludesEvidence(t *testing.T) {
	pages := []Page{{
		Title: "Channels",
		Body:  "channels coordinate goroutines",
		Evidence: []Evidence{
			{Quote: "channels block when full", LineStart: 4, LineEnd: 4},
		},
	}}
	prompt := buildAnswerUserPrompt("how do channels work?", pages)
	if !strings.Contains(prompt, "Channels") {
		t.Error("prompt missing page title")
	}
	if !strings.Contains(prompt, "channels block when full") {
		t.Error("prompt missing evidence quote")
	}
	if !strings.Contains(prompt, "(lines 4-4)") {
		t.Error("prompt missing legacy line range when SourceFilePath empty")
	}
	if !strings.Contains(prompt, "Question: how do channels work?") {
		t.Error("prompt missing question")
	}
}

// TestBuildAnswerUserPromptIncludesSourceFile verifies the (file:a-b)
// annotation when evidence carries a SourceFilePath, including the
// "page-N:a-b" form used for PDF-derived evidence.
func TestBuildAnswerUserPromptIncludesSourceFile(t *testing.T) {
	pages := []Page{{
		Title: "T",
		Body:  "b",
		Evidence: []Evidence{
			{Quote: "abc", LineStart: 1, LineEnd: 1, SourceFilePath: "internal/db/db.go"},
			{Quote: "xyz", LineStart: 2, LineEnd: 2, SourceFilePath: "page-3"},
		},
	}}
	out := buildAnswerUserPrompt("q?", pages)
	if !strings.Contains(out, "(internal/db/db.go:1-1)") {
		t.Errorf("missing file annotation: %q", out)
	}
	if !strings.Contains(out, "(page-3:2-2)") {
		t.Errorf("missing pdf page annotation: %q", out)
	}
	if strings.Contains(out, "(lines 1-1)") {
		t.Errorf("legacy line annotation should not appear when SourceFilePath set: %q", out)
	}
}

// recordingLLMClient is a minimal llm.Client that records the system /
// user prompt of the first call into capturedSystem / capturedUser and
// returns a canned response. Phase B Task 5 uses it to assert that the
// schema-driven system prompt actually reaches the LLM call site.
type recordingLLMClient struct {
	capturedSystem    string
	capturedUser      string
	completeResp      string
	structuredResp    map[string]any
	streamWrittenResp string
}

func (r *recordingLLMClient) Complete(ctx context.Context, system, user string) (string, error) {
	r.capturedSystem = system
	r.capturedUser = user
	return r.completeResp, nil
}

func (r *recordingLLMClient) CompleteStructured(ctx context.Context, system, user string, ts llm.ToolSchema) (map[string]any, error) {
	r.capturedSystem = system
	r.capturedUser = user
	if r.structuredResp == nil {
		return nil, errors.New("recordingLLMClient: structuredResp not set")
	}
	return r.structuredResp, nil
}

func (r *recordingLLMClient) CompleteStream(ctx context.Context, system, user string, w io.Writer) (string, error) {
	r.capturedSystem = system
	r.capturedUser = user
	resp := r.streamWrittenResp
	if resp == "" {
		resp = r.completeResp
	}
	if _, err := w.Write([]byte(resp)); err != nil {
		return "", err
	}
	return resp, nil
}

// TestIngestSourceFilesToPages_AcceptsSchemaParam_StubLLM exercises the
// new sch parameter (Phase B Task 5): the system prompt the LLM
// receives is the byte-equal-to-v0.6 rendered Prompts.Ingest, the user
// prompt no longer carries the existing-titles preamble (it moved into
// the schema template under option (a)), the validator still gates the
// LLM output, and a clean page parses out.
func TestIngestSourceFilesToPages_AcceptsSchemaParam_StubLLM(t *testing.T) {
	source := "Line one.\nthe validator drops unverified quotes\nLine three.\n"
	files := []ingest.SourceFile{ingest.NewSourceFile("src.md", []byte(source))}

	client := &recordingLLMClient{
		structuredResp: map[string]any{
			"pages": []any{
				map[string]any{
					"title": "Validator",
					"body":  "Body about the validator.",
					"evidence": []any{
						map[string]any{
							"quote":       "the validator drops unverified quotes",
							"source_file": "src.md",
						},
					},
				},
			},
		},
	}
	pages, err := IngestSourceFilesToPages(context.Background(), client, files, []string{"Foo", "Bar"}, schema.Bundled())
	if err != nil {
		t.Fatalf("IngestSourceFilesToPages: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("got %d pages, want 1", len(pages))
	}
	if pages[0].Title != "Validator" {
		t.Errorf("page title = %q, want %q", pages[0].Title, "Validator")
	}
	if len(pages[0].Evidence) != 1 {
		t.Fatalf("evidence rows = %d, want 1", len(pages[0].Evidence))
	}
	// System prompt should be the rendered Prompts.Ingest with the
	// real existing-titles bullet list filled in. The "Existing wiki
	// pages..." preamble belongs in the SYSTEM prompt now (option a).
	if !strings.Contains(client.capturedSystem, "Existing wiki pages (titles only):") {
		t.Errorf("system prompt missing existing-titles preamble:\n%s", client.capturedSystem)
	}
	if !strings.Contains(client.capturedSystem, "- Foo") || !strings.Contains(client.capturedSystem, "- Bar") {
		t.Errorf("system prompt missing real existing titles bullet list:\n%s", client.capturedSystem)
	}
	// User prompt should NOT carry the existing-titles preamble (we
	// hoisted it into the system prompt for v0.7).
	if strings.Contains(client.capturedUser, "Existing wiki pages") {
		t.Errorf("user prompt should not contain existing-titles preamble after Task 5:\n%s", client.capturedUser)
	}
	if !strings.Contains(client.capturedUser, "SOURCE to ingest:") {
		t.Errorf("user prompt missing SOURCE block:\n%s", client.capturedUser)
	}
}

// TestAnswerQuestion_AcceptsSchemaParam_StubLLM asserts the new sch
// parameter on AnswerQuestion (Phase B Task 5): the rendered
// Prompts.Ask hits the LLM as the system prompt and the model's
// canned response is returned verbatim.
func TestAnswerQuestion_AcceptsSchemaParam_StubLLM(t *testing.T) {
	pages := []Page{{
		Title: "Goroutines",
		Body:  "Goroutines are lightweight threads.",
		Evidence: []Evidence{
			{Quote: "lightweight threads", LineStart: 1, LineEnd: 1, SourceFilePath: "g.md"},
		},
	}}
	client := &recordingLLMClient{completeResp: "goroutines are managed by the Go runtime"}
	got, err := AnswerQuestion(context.Background(), client, "what are goroutines?", pages, schema.Bundled())
	if err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}
	if got != "goroutines are managed by the Go runtime" {
		t.Errorf("answer = %q, want canned response", got)
	}
	// The bundled rendered Prompts.Ask is byte-equal to v0.6's
	// answerSystemPrompt (pinned by byte_equality_test.go).
	if client.capturedSystem != AnswerSystemPromptForTests() {
		t.Errorf("system prompt drifted from v0.6 answerSystemPrompt; first 80 bytes:\n%q",
			client.capturedSystem[:min(80, len(client.capturedSystem))])
	}
	if !strings.Contains(client.capturedUser, "Goroutines") {
		t.Errorf("user prompt missing page title:\n%s", client.capturedUser)
	}
}

// TestStreamAnswer_AcceptsSchemaParam_StubLLM mirrors AnswerQuestion's
// shape but exercises the streaming variant (Phase B Task 5). The
// stream sink receives the canned response.
func TestStreamAnswer_AcceptsSchemaParam_StubLLM(t *testing.T) {
	pages := []Page{{Title: "P", Body: "B"}}
	client := &recordingLLMClient{streamWrittenResp: "streamed answer"}
	var sink bytes.Buffer
	got, err := StreamAnswer(context.Background(), client, "q?", pages, &sink, schema.Bundled())
	if err != nil {
		t.Fatalf("StreamAnswer: %v", err)
	}
	if got != "streamed answer" {
		t.Errorf("got = %q, want canned response", got)
	}
	if sink.String() != "streamed answer" {
		t.Errorf("sink = %q, want streamed bytes", sink.String())
	}
	if client.capturedSystem != AnswerSystemPromptForTests() {
		t.Errorf("system prompt drifted from v0.6 answerSystemPrompt")
	}
}

// TestDetectContradictions_AcceptsSchemaParam_StubLLM asserts the new
// sch parameter on the whole-wiki batched lint detector (Phase B Task
// 5): the rendered Prompts.LintContradictions hits the LLM and the
// canned response is returned verbatim.
func TestDetectContradictions_AcceptsSchemaParam_StubLLM(t *testing.T) {
	pages := []Page{
		{Title: "A", Body: "first."},
		{Title: "B", Body: "second."},
	}
	client := &recordingLLMClient{completeResp: "No contradictions found."}
	got, err := DetectContradictions(context.Background(), client, pages, schema.Bundled())
	if err != nil {
		t.Fatalf("DetectContradictions: %v", err)
	}
	if got != "No contradictions found." {
		t.Errorf("result = %q, want canned response", got)
	}
	if client.capturedSystem != LintContradictionsSystemPromptForTests() {
		t.Errorf("system prompt drifted from v0.6 lintContradictionsSystemPrompt")
	}
}

// TestDetectContradictions_FewerThanTwoPages_NoLLMCall covers the
// short-circuit case: with <2 pages, no LLM call is made and an empty
// string is returned. Establishes the schema parameter does not break
// the early-return guard.
func TestDetectContradictions_FewerThanTwoPages_NoLLMCall(t *testing.T) {
	client := &recordingLLMClient{completeResp: "should-not-be-returned"}
	got, err := DetectContradictions(context.Background(), client, []Page{{Title: "Only"}}, schema.Bundled())
	if err != nil {
		t.Fatalf("DetectContradictions: %v", err)
	}
	if got != "" {
		t.Errorf("result = %q, want empty (short-circuit)", got)
	}
	if client.capturedSystem != "" {
		t.Errorf("LLM was called despite <2 pages: capturedSystem=%q", client.capturedSystem)
	}
}
