BINARY := llmwiki
PREFIX ?= $(HOME)/.local/bin

.PHONY: build install clean smoke

build:
	go build -o $(BINARY) .

install: build
	mkdir -p $(PREFIX)
	cp $(BINARY) $(PREFIX)/$(BINARY)
	@echo "Installed to $(PREFIX)/$(BINARY)"
	@echo "Make sure $(PREFIX) is in your PATH."

clean:
	rm -f $(BINARY)

smoke: build
	@TMP=$$(mktemp -d) && \
	  cd $$TMP && \
	  $(CURDIR)/$(BINARY) init --provider=ollama && \
	  cp $(CURDIR)/internal/ingest/testdata/smoke-source.md . && \
	  LLMWIKI_CASSETTE=smoke $(CURDIR)/$(BINARY) ingest smoke-source.md && \
	  LLMWIKI_CASSETTE=smoke $(CURDIR)/$(BINARY) ask "what is the smoke source about?" --no-save && \
	  $(CURDIR)/$(BINARY) status && \
	  rm -rf $$TMP
