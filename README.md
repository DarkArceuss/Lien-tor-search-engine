# Lien

Lien is a small private search front end with a local result collector for public `.onion` search results.

## Search providers

Lien uses two independent providers. TorDex is queried through the local Tor SOCKS5 listener (default `127.0.0.1:9050`), and OnionLand is queried over HTTPS as a fallback/second source. Results are merged, deduplicated, ranked locally, cached briefly, and paginated.

## Termux / Onion Hoster

The project panel uses Onion Hoster's Termux Edition with the custom-port method. Lien listens on `127.0.0.1:3000`; Onion Hoster maps its Tor hidden service port 80 to that local port.

Android shared storage can be mounted with `noexec`, so `onion-hoster/termux.sh` is invoked through Bash instead of being executed directly. The compiled Lien binary is built in the Termux private runtime directory for the same reason.

Start the panel from the project directory:

```sh
bash panel.sh
```

Standalone hoster launcher:

```sh
bash tools/start-onion-host.sh
```

## Direct local run

```sh
go run ./scripts
```

Open `http://127.0.0.1:3000`.

## Optional environment variables

```sh
export LIEN_PORT=3000
export LIEN_TOR_SOCKS=127.0.0.1:9050
export LIEN_TORDEX_URL='http://tordexu73joywapk2txdr54jed4imqledpcvcuf75qsas2gwdgksvnyd.onion/search?query=%s&page=%d'
export LIEN_ONIONLAND_URL='https://www.onionland.to/search?q=%s&page=%d'
```

## Front page CSS

The original Lien stylesheet is embedded directly into `index.html`, so the front page no longer depends on a separate stylesheet request. `styles/style.css` is retained as the original source copy.
