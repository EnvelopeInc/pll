# Releasing pll

This repository publishes the Go module `github.com/EnvelopeInc/pll`.

## Before releasing

- Start from a clean, up-to-date `main` checkout; release only reviewed changes.
- Confirm CI is green on `main` for both Go matrix versions.
- Choose a Go-compatible semantic version. While the API is unstable, use `v0.x.y`; use `v1.0.0` when committing to a stable v1 API. Increment the appropriate component for the change.
- Before declaring a stable v1 API, validate measured pacing accuracy with representative workloads and review the fixed-gain type-II synchronizer behavior, exact rational pacing arithmetic, and documented limits: periods below 1ns are unsupported, virtual timestamps must fit `time.Duration`, scheduler work/backpressure is bounded, and callbacks must honor cancellation. Do not describe or depend on a removed PID implementation.

## Publish a release

1. Create and push an annotated version tag from the intended commit, for example:

   ```sh
   git switch main
   git pull --ff-only
   git status --short
   git tag -a v0.1.0 -m "pll v0.1.0"
   git push origin v0.1.0
   ```

2. In GitHub, create a Release for that existing tag and add concise release notes. The tag push also runs CI; confirm it passes.
3. Do not move, delete, or retag a published version. If a release has a problem, publish a new version.

Go module major versions v0 and v1 use the module path `github.com/EnvelopeInc/pll`. For v2 and later, Go requires a matching major-version suffix in both the module path and repository source/imports, such as `github.com/EnvelopeInc/pll/v2`; publish each major line at its corresponding path rather than tagging v2 under the unsuffixed path.

## Verify publication

After publishing, confirm the exact version resolves from the Go module proxy, then test the consumer command:

```sh
GOPROXY=https://proxy.golang.org go list -m -json github.com/EnvelopeInc/pll@v0.1.0
go mod download -json github.com/EnvelopeInc/pll@v0.1.0
tmp="$(mktemp -d)"
cd "$tmp"
go mod init example.com/pll-consumer
GOPROXY=https://proxy.golang.org go get github.com/EnvelopeInc/pll@v0.1.0
```

Use the released version in place of `v0.1.0`. Check that the resolved version and module metadata match the intended tag. The Go proxy and pkg.go.dev index modules automatically; after the first successful proxy fetch, verify the version appears at `https://pkg.go.dev/github.com/EnvelopeInc/pll` (or request indexing at `https://pkg.go.dev/` if it has not appeared yet).
