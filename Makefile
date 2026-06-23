.PHONY: tidy server gui-mac gui-windows gui-linux gui-cross-linux gui-cross-windows gui-cross-all all clean

BIN_DIR := dist
APP_ID  ?= com.sherifhamad.shixo-msn

tidy:
	go mod tidy

# Server is pure Go (modernc.org/sqlite) — cross-compiles from any host.
server: tidy
	mkdir -p $(BIN_DIR)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
		go build -ldflags="-s -w" -o $(BIN_DIR)/clipsrv-linux-amd64 ./cmd/clipsrv
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
		go build -ldflags="-s -w" -o $(BIN_DIR)/clipsrv-linux-arm64 ./cmd/clipsrv

# GUI client uses Fyne (CGO). Build natively on the target OS.
# On mac: builds for your current arch (apple silicon).
gui-mac: tidy
	mkdir -p $(BIN_DIR)
	go build -ldflags="-s -w" -o $(BIN_DIR)/shixo-msn ./cmd/shixo-msn

# Run these targets ON Windows / ON Linux respectively, with Go installed there.
gui-windows: tidy
	go build -ldflags="-s -w -H=windowsgui" -o $(BIN_DIR)/shixo-msn.exe ./cmd/shixo-msn

gui-linux: tidy
	go build -ldflags="-s -w" -o $(BIN_DIR)/shixo-msn-linux ./cmd/shixo-msn

# Cross-build via fyne-cross (Docker required). Run from any host.
#   go install github.com/fyne-io/fyne-cross@latest    # lands in $(go env GOPATH)/bin
#   make gui-cross-linux gui-cross-windows
FYNECROSS := $(shell go env GOPATH)/bin/fyne-cross

gui-cross-linux:
	$(FYNECROSS) linux   -arch=amd64 -app-id $(APP_ID) -name shixo-msn ./cmd/shixo-msn

gui-cross-windows:
	$(FYNECROSS) windows -arch=amd64 -app-id $(APP_ID) -name shixo-msn ./cmd/shixo-msn

gui-cross-all: gui-cross-linux gui-cross-windows

all: server gui-mac

clean:
	rm -rf $(BIN_DIR) fyne-cross/
