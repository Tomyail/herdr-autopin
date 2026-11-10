BIN ?= /tmp/herdr-autopin
LOG ?= /tmp/herdr-autopin.log
T   ?= -

.PHONY: build run dev stop log judge

build:
	go build -o $(BIN) ./cmd/herdr-autopin

# Foreground, logging to stdout: for watching it work.
run: build
	$(BIN)

# Build, restart in the background, show it came up.
dev: build stop
	@nohup $(BIN) >> $(LOG) 2>&1 < /dev/null &
	@sleep 1
	@pgrep -xl herdr-autopin | head -1
	@tail -1 $(LOG)

stop:
	@pkill -x herdr-autopin || true
	@sleep 1

log:
	@tail -f $(LOG)

# One judgment, with what gets sent: make judge T=t7, or two saved screens on stdin: make judge < screens.json
judge: build
	$(BIN) -judge $(T)
