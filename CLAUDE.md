# Goldbach Prebid Server Fork — Agent Instructions

This is **tamedia-adtec/prebid-server**, Goldbach's production fork of
[prebid/prebid-server](https://github.com/prebid/prebid-server) (Go). It runs as the PBS that
goldlayer-api calls for every auction (k8s namespace `prebid-server`, service
`prebid-server.prebid-server.svc`). Upstream documentation covers PBS itself — this file covers
only what is fork-specific.

## The prime directive: never upstream

**Never push to, or open PRs against, prebid/prebid-server.** The fork carries custom bidders and
config that exist in coordination with partners (e.g. Magnite) and must stay private. Guards in
place:

- The `upstream` remote's push URL is deliberately set to `DISABLED-never-push-to-prebid-upstream`.
  Do not "fix" it.
- `gh` pitfall: on forks, `gh pr create` targets the **parent repo by default**. If `gh` is ever
  used here, run `gh repo set-default tamedia-adtec/prebid-server` first and always pass
  `--repo tamedia-adtec/prebid-server`.

The one exception is code that was intentionally contributed upstream already: the `goldbach`
adapter (`adapters/goldbach/`) is **upstream-owned** — external PBS hosts need it to reach
goldlayer. Do not patch it here; changes to it must go through the normal upstream contribution
process (from a separate clean clone, never from this fork's branches).

## Fork-custom surface (what must survive upstream merges)

Everything the fork adds/changes relative to the last merged upstream tag. Check with
`git diff --stat <last-upstream-tag>..develop`. Currently:

| Area | Files | Purpose |
| --- | --- | --- |
| **Runtime config** | `pbs.yml` (committed; `.gitignore` un-ignores `pbs.*`) | EMEA endpoints: appnexus + msft → `https://goldbach-emea.adnxs.com/openrtb2` (dedicated Goldbach Xandr endpoint), rubicon → `exapi-eu.rubiconproject.com`. GDPR enabled with `default_value: 1` and `host_vendor_id: 580` (Goldbach GVL). Prometheus :8001, request+response gzip, HTTP-client tuning, `account_defaults.debug_allow: true` |
| **Custom bidder: magnitectv** | `adapters/magnitectv/`, `openrtb_ext/imp_magnitectv.go`, `static/bidder-info/magnitectv.yaml`, `static/bidder-params/magnitectv.json`, registration lines in `openrtb_ext/bidders.go` + `exchange/adapter_builders.go` | Magnite CTV / SpringServe "Publisher OpenRTB Connect" (DASB-6076). Private Goldbach↔Magnite integration — **never upstream** |
| **Test hardening** | `config/bidderinfo_test.go` → `TestBidderInfoFilesValidate` | Runs startup-equivalent validation (endpoint template resolution) over the real bidder-info files. Upstream's `TestBidderInfoFiles` only parses — see "endpoint macro" pitfall below |
| **Build/deploy** | `Dockerfile`, `tdaci.env`, `tdaci.yml` | Single-stage `golang:1.26-alpine` CGO build; TDA CI descriptors |

## Updating from upstream

```
git fetch upstream --tags
git checkout develop && git merge vX.Y.Z     # merge the upstream release TAG, not master
```

Conflict hotspots and how to resolve them:

- `openrtb_ext/bidders.go` and `exchange/adapter_builders.go`: keep the `magnitectv` entries
  (alphabetical position: after `madvertise`, before `marsmedia`). Any future custom bidder adds
  more of these one-line inserts.
- `Dockerfile`, `.gitignore`, `config/bidderinfo_test.go`: keep ours (re-apply the
  `TestBidderInfoFilesValidate` block if upstream rewrote the file).
- `pbs.yml`, `tdaci.*`: upstream doesn't have them; they merge clean.
- Bump the `golang:X.Y-alpine` base image if upstream's `go.mod` `go` directive moved past it.

After every upstream merge, run the full verification below — upstream merges are exactly when
fork-custom code silently breaks.

## Release & deploy

- `develop` is the release branch. Direct commits/merges — this fork does not use internal PRs.
- Release = bump `MY_SERVICE_VERSION` in `tdaci.env` + commit `release - X.Y.Z (short reason)` +
  push. TDA CI builds and deploys to the `prebid-server` k8s namespace from develop.
- Committing/pushing to develop **without** a version bump does not roll prod.
- ⚠️ `tdaci.yml` runs tests as `go test -v ./... || true` — **CI ignores test failures**. Local
  verification is the only real gate.

## Verification checklist (before any release)

1. `go test ./adapters/<changed>/... ./config/... ./openrtb_ext/... ./exchange/` — then the full
   suite for releases. (`analytics/pubstack` has a known timing-flaky test; verify in isolation
   before blaming your change.)
2. **Boot the image**: `docker build -t pbs-test . && docker run -e PBS_GDPR_DEFAULT_VALUE=0 pbs-test`
   and confirm `Main server starting on: :8000` with no `F…` fatal line. Config validation runs at
   startup only — green unit tests do not prove the server boots (see incident below).
3. Local manual run without Docker: `go run . -v 1 -logtostderr` picks up `pbs.yml` from the repo
   root; env overrides use the `PBS_` prefix with `_` for `.` (e.g.
   `PBS_ADAPTERS_MAGNITECTV_ENDPOINT=…`).

## Pitfalls (learned the hard way)

- **Endpoint template macros**: bidder-info endpoint templates may only use macros present in
  `testEndpointTemplateParams` (`config/bidderinfo.go`). Startup validation resolves the template
  with that fixed set; an unpopulated macro in the host position produces an invalid URL and PBS
  **fatals at boot** — this crashed prod release 1.7.0 (magnitectv used `{{.SeatID}}`, which is not
  in the set; fixed to `{{.AccountID}}` in 1.7.1). `TestBidderInfoFilesValidate` now catches this
  in tests, but keep the rule in mind for aliases/env overrides too.
- **Go toolchain**: `go.mod` requires a recent Go (check the `go` directive). `/usr/local/go` on
  dev machines may be stale — use Homebrew Go (`/opt/homebrew/bin/go`).
- **No amd64 builds on ARM machines**: building or running the linux/amd64 image under qemu on an
  aarch64 host crashes the Go runtime/GC (known qemu bug, "lfstack.push invalid packing" /
  SIGSEGV in `go mod download`). Build natively per-arch; don't chase these crashes as code bugs.
- **CGO + alpine/musl**: the image builds with `CGO_ENABLED=1` on alpine. Works today; if unexplained
  runtime crashes appear under load, this combination is the first suspect (musl thread stacks).
- **204 from bidder endpoints proves nothing**: e.g. Magnite's endpoint answers 204 to well-formed,
  malformed, and even bogus-seat requests alike. Use PBS debug (`test: 1` on the request →
  `ext.debug.httpcalls`) to see the actual outbound call and raw answer.

## Custom bidder: magnitectv (Magnite CTV / SpringServe)

- Sends OpenRTB 2.5 to `https://{{.AccountID}}.{{.Region}}.eb.tremorhub.com/ad/rtb/pub`
  (`AccountID` = Magnite seat code — Goldbach's is `4lqsb`; region defaults `eu-west-1`).
  Headers: `x-openrtb-version: 2.5`.
- Params (`imp.ext.prebid.bidder.magnitectv`): `seatCode` (required), `region` (enum), `tagid`
  (Magnite ad-unit code for supply routing, e.g. RTL `4lqsb-gr472`).
- Request shaping in `MakeRequests`: imps grouped per seat+region; schain relocated from the PBS
  2.5 location (`source.ext.schain`) to `source.schain` per the Magnite spec; OpenRTB 2.6 pod
  fields (`poddur`/`maxseq`/`podseq`) translated to `imp.video.ext.podduration`/`maxseq`/`podsequence`
  (explicit `video.ext` values win); `request.ext` stripped to only `ext.extra`; `imp.ext` removed.
- `MakeBids`: video-only; `bid.ext.tier` (Magnite waterfall priority 1–16) → `DealPriority`; raw
  `bid.ext` passed through. 204 = no bid; gvlVendorID 202 (Magnite CTV / Telaria).
- Spec: "Magnite CTV Publisher OpenRTB Connect" PDF (attached to DASB-6076). Caller is goldlayer
  (its `magnitectv` param-builder adapter, or VTM per-request configuration overrides).

## Upstream-owned adapter of special interest: goldbach

`adapters/goldbach/` receives requests from external PBS hosts and forwards to goldlayer prod
(`goldlayer-api.prod.gbads.net/openrtb/2.5/auction`, gzip, GVL 580). Its `MakeBids` requires HTTP
**201** (`http.StatusCreated`) — goldlayer answers 201 by design; don't "normalize" either side to
200. Fix bugs upstream, not here.
