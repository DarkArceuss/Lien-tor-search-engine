# Lien audit report

## Changes in web6 revision

- The original front-page CSS has been embedded directly into `index.html`. The original `styles/style.css` source file is retained byte-for-byte.
- The search backend no longer relies only on OnionLand. Lien queries the current TorDex onion endpoint through the local Tor SOCKS5 listener and queries OnionLand over HTTPS in parallel.
- Provider failures are isolated: one unavailable provider does not prevent results from the other provider from being shown. A gateway error is returned only when both providers fail.
- `.onion` URLs are validated as v2/v3-format hostnames, fragments are removed, and duplicate host/path results are merged.
- Queries are capped, provider responses are size-limited, requests have deadlines, results are cached for two minutes, and results are locally ranked.

## Verification

- `go test ./scripts` must pass before release.
- The packaged archive is checked with `unzip -t`.
