.PHONY: test build smoke clean

DAEMON_DIR := daemon
BIN_DIR := bin

test:
	cd $(DAEMON_DIR) && go test ./...

build:
	mkdir -p $(BIN_DIR)
	cd $(DAEMON_DIR) && go build -o ../$(BIN_DIR)/deconstructd ./cmd/deconstructd

smoke: build
	./$(BIN_DIR)/deconstructd --version

clean:
	rm -rf $(BIN_DIR)

