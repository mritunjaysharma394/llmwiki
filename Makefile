BINARY := llmwiki
PREFIX ?= $(HOME)/.local/bin

.PHONY: build install clean

build:
	go build -o $(BINARY) .

install: build
	mkdir -p $(PREFIX)
	cp $(BINARY) $(PREFIX)/$(BINARY)
	@echo "Installed to $(PREFIX)/$(BINARY)"
	@echo "Make sure $(PREFIX) is in your PATH."

clean:
	rm -f $(BINARY)
