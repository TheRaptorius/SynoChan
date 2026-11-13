VERSION ?= 0.0.0
BUILD ?= 1

.PHONY: test spk run clean

test:
	go vet ./...
	go test ./...

spk:
	scripts/build-spk.sh $(VERSION) $(BUILD)

# Runs the service locally with the UI on http://127.0.0.1:8580 (no login, for development only).
run:
	go run ./cmd/synochan daemon --data ./.dev --default-save-path $(CURDIR)/.dev/downloads --listen 127.0.0.1:8580 --log-stderr

clean:
	rm -rf dist site .dev
