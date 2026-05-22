PREFIX ?= $(HOME)/.local/bin
BIN    := wsg

build:
	go build -o $(BIN) ./cmd/wsg

install: build
	install -m 755 $(BIN) $(PREFIX)/$(BIN)
	ln -sf $(BIN) $(PREFIX)/qwe

test:
	go test ./cmd/wsg/ -v

clean:
	rm -f $(BIN)

.PHONY: build install test clean
