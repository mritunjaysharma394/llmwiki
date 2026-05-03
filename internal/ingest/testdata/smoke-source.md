# The Smokehouse

This file is the fixture used by `llmwiki`'s end-to-end smoke test.

The smoke test exists to prove three properties of the launch surface:

1. **The trust property.** Every wiki page is produced from evidence that
   quotes verbatim spans from a source file; the smoke run confirms this
   contract holds against a tiny, hand-written input.
2. **Cassettes.** The smoke run replays a recorded LLM exchange from
   `internal/llm/testdata/cassettes/smoke__*.json`, so it works without
   `ANTHROPIC_API_KEY` set.
3. **Incremental re-ingest.** Running `llmwiki ingest` twice on the same
   smoke source should be a no-op once the content hash matches.

Topics: trust, cassettes, incremental re-ingest.
