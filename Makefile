.PHONY: help
help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@echo "  build    Build the extension binary"
	@echo "  install  Build and install the extension locally"
	@echo "  help     Show this help message"

.PHONY: build
build:
	go build -o gh-gist-new main.go

.PHONY: install
install: build
	gh extension remove gist-new || true
	gh extension install .
