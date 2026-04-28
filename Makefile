.PHONY: test build smoke clean

DAEMON_DIR := daemon
BIN_DIR := bin

test:
	cd $(DAEMON_DIR) && go test ./...

build:
	mkdir -p $(BIN_DIR)
	cd $(DAEMON_DIR) && go build -o ../$(BIN_DIR)/deconstructd ./cmd/deconstructd
	cd $(DAEMON_DIR) && go build -o ../$(BIN_DIR)/deconstruct-eval ./cmd/deconstruct-eval

smoke: build
	./$(BIN_DIR)/deconstructd --version
	./$(BIN_DIR)/deconstruct-eval -har tests/fixtures/hars/csrf-create-item.har -out /tmp/deconstruct-eval-smoke >/tmp/deconstruct-eval-smoke.json

clean:
	rm -rf $(BIN_DIR)
