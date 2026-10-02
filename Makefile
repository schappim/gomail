BINARY  := gomail
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64

.PHONY: build install install-skill test vet dist clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/gomail

PREFIX ?= $(HOME)/.local

install:
	mkdir -p $(PREFIX)/bin
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(PREFIX)/bin/$(BINARY) ./cmd/gomail

# Link skills/gomail into the shared Agent Skills directory (~/.agents/skills)
# and from there into each agent's own skills directory that exists.
SKILL_HOME ?= $(HOME)/.agents/skills
AGENT_SKILL_DIRS := $(HOME)/.claude/skills $(HOME)/.claude-work/skills $(HOME)/.codex/skills $(HOME)/.gemini/skills \
	$(HOME)/.cursor/skills $(HOME)/.config/opencode/skills $(HOME)/.factory/skills

install-skill:
	mkdir -p $(SKILL_HOME)
	ln -sfn $(CURDIR)/skills/gomail $(SKILL_HOME)/gomail
	@for d in $(AGENT_SKILL_DIRS); do \
		if [ -d $$d ]; then ln -sfn $(SKILL_HOME)/gomail $$d/gomail && echo "linked $$d/gomail"; fi; \
	done

test:
	go test ./...

vet:
	go vet ./...

# Static, self-contained binaries for every platform in dist/, plus the
# SHA256SUMS that install.sh verifies downloads against. Upload all of dist/
# to the GitHub release.
dist:
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" \
			-o dist/$(BINARY)-$$os-$$arch$$ext ./cmd/gomail || exit 1; \
	done
	cd dist && shasum -a 256 $(BINARY)-* > SHA256SUMS

clean:
	rm -rf bin dist
