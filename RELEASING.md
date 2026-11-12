# Releasing pll

The module is `github.com/version2beta/pll`; the repository is
[github.com/Version2beta/pll](https://github.com/Version2beta/pll).

## Before releasing

1. Start from a clean, up-to-date `main`: `git switch main && git pull --ff-only && git status --short`.
2. Choose the version. While the API may still change, use `v0.x.y`; use `v1.0.0` only when committing to a stable API. Never reuse or move a tag.
3. Add an entry to `CHANGELOG.md` for the version, with the date, through a pull request. Update the version in the README's "At a glance" table in the same pull request.
4. Confirm CI passed on the exact commit you will tag: `gh run list --branch main -L 1` shows the merge commit, green.
5. Run locally: `make verify && make fuzz`.

## Publish

```sh
v=v0.3.1
git tag -a "$v" -m "pll $v"
git push origin "$v"
gh release create "$v" --title "pll $v" --notes-file <(awk "/^## $v /{f=1;next} /^## /{f=0} f" CHANGELOG.md)
```

## Check publication

```sh
GOPROXY=https://proxy.golang.org go list -m -json github.com/version2beta/pll@$v
tmp=$(mktemp -d) && cd "$tmp" && go mod init example.com/check && go get github.com/version2beta/pll@$v
```

Then open [pkg.go.dev/github.com/version2beta/pll](https://pkg.go.dev/github.com/version2beta/pll) and confirm the version appears. If it has not appeared after a few minutes, request it from that page.

If a release has a problem, publish a new version; do not delete or move the tag.

For v2 and later, Go requires the major version in the module path (`github.com/version2beta/pll/v2`) and in every import.
