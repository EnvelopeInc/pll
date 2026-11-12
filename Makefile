.PHONY: test verify fmt fuzz bench

test:
	go test ./... -count=1

fmt:
	gofmt -w .

# Everything the loop must leave green.
verify:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)
	go vet ./...
	go test ./... -count=1
	go test -race ./... -count=1

fuzz:
	go test -run '^$$' -fuzz '^FuzzPacer$$' -fuzztime 30s .

bench:
	go test -run '^$$' -bench . -benchmem .
