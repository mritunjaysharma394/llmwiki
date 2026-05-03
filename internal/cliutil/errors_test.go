package cliutil

import (
	"errors"
	"strings"
	"testing"
)

func TestUserErrorFormat(t *testing.T) {
	ue := &UserError{
		Cause:       "ingest failed",
		Wrapped:     errors.New("HTTP 503 from https://example.com/feed.xml"),
		Remediation: "re-run with --max-retries=3, or check the feed in a browser",
	}
	got := ue.Error()
	for _, want := range []string{"ingest failed", "HTTP 503", "re-run with --max-retries=3"} {
		if !strings.Contains(got, want) {
			t.Errorf("Error() = %q, missing %q", got, want)
		}
	}
}

func TestUserErrorUnwrap(t *testing.T) {
	inner := errors.New("inner")
	ue := &UserError{Cause: "x", Wrapped: inner, Remediation: "y"}
	if !errors.Is(ue, inner) {
		t.Errorf("errors.Is failed for wrapped error")
	}
}

func TestUserErrorAs(t *testing.T) {
	ue := &UserError{Cause: "x", Remediation: "y"}
	wrapped := errors.New("outer: " + ue.Error())
	_ = wrapped // placeholder
	var got *UserError
	if !errors.As(ue, &got) {
		t.Errorf("errors.As failed on direct UserError")
	}
	if got.Cause != "x" {
		t.Errorf("As round-trip lost Cause: %+v", got)
	}
}

func TestRenderFormatsMultiline(t *testing.T) {
	ue := &UserError{
		Cause:       "ingest failed",
		Wrapped:     errors.New("HTTP 503"),
		Remediation: "check the URL",
	}
	got := Render(ue)
	for _, want := range []string{"Error: ingest failed", "cause: HTTP 503", "try:   check the URL"} {
		if !strings.Contains(got, want) {
			t.Errorf("Render() = %q, missing %q", got, want)
		}
	}
}

func TestRenderPlainError(t *testing.T) {
	plain := errors.New("boom")
	got := Render(plain)
	if !strings.Contains(got, "Error: boom") {
		t.Errorf("Render(plain) = %q, want Error: boom", got)
	}
}
