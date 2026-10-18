.PHONY: build test web web-dev clean

build:
	go build ./...

test:
	go test -race ./...

web:
	cd web && $(PNPM) build
	mkdir -p internal/server/webout
	rsync -a --delete web/out/ internal/server/webout/
	touch internal/server/webout/.keep

PNPM := $(shell command -v pnpm 2>/dev/null || echo $(HOME)/.local/bin/pnpm)

web-dev:
	cd web && VPSTUI_DEV=1 $(PNPM) dev

clean:
	rm -rf web/out web/.next internal/server/webout
