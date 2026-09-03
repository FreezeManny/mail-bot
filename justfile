# Lint + vet (check-only). CI runs this too.
check:
    go vet ./...
    go build ./...

# Fail if anything is unformatted. Separate from `check` so CI can report a
# formatting problem distinctly from a real defect; `gofmt -l` exits 0 either

# way, hence the explicit test on its output.
format-check:
    @out=$(gofmt -l .); if [ -n "$out" ]; then echo "not gofmt'd:"; echo "$out"; exit 1; fi

# Auto-format in place. Run manually when you want it.
format:
    gofmt -w .

# Run the unit-test suite (pure logic; no network, no live mailbox).
test:
    go test ./...

# Build the binary into ./mailsorter (gitignored).
build: check
    go build -trimpath -o mailsorter ./cmd/mailsorter

# Build the container image the way CI does, minus the push.
docker-build:
    docker build -t mail-bot:dev .
