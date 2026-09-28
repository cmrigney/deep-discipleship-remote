# Deep Discipleship Remote — Project Plan

Control the Deep Discipleship course video (Lifeway player) on the church computer from a phone:
play/pause, plus a full-screen discussion timer overlay (default 10 minutes) that darkens the video.

---

## Implementation Status (2026-09-24)

| Phase | Status |
|-------|--------|
| 1–2 Relay core + hardening | ✅ Done. Unit and integration tests cover auth, lockout, timeout, routing, origin check, connection caps and HTTP/message rate limits (`relay/`, `go test ./...`). |
| 3 Phone UI | ✅ Done (`relay/web/static`) |
| 4 Extension | ✅ Done. `e2e/run.mjs` passes: relay + Chrome build in Chromium + mock player + phone page. The Safari content-script bundle was also checked on the **real Lifeway player** (play, pause, seek, timer, fullscreen overlay). |
| 0 Safari background spike, 5 Safari packaging | ⏳ Needs the Mac. Run `safari/make-xcode-project.sh`, then leave it connected for 90+ min. |
| 6 Deploy, 7 Dress rehearsal | ⏳ `deploy/` has the systemd unit, env example, cloudflared config and Makefile (README §1–2) |
| 8 Chrome/Firefox | Chrome build is covered by the E2E test. Firefox builds but hasn't been run. |

---

## 0. Findings from the Real Player Page (verified 2026-09-24 via Playwright)

| Question | Answer |
|----------|--------|
| Where is the video? | Tapping a lesson on `my.lifeway.com/digital-media` opens a **new top-level tab** at `https://player.lifeway.com/player/<videoId>` (e.g. `005851990`). **Not an iframe.** The only iframe on the page is Lifeway's OpenID session checker. |
| Player | video.js v8 (`vjs-v8`), no global `window.videojs`, so we can't use the video.js API and must work with the DOM. |
| Selectors | Container `div#vjs_video_3.video-js` · video `video.vjs-tech` (id `vjs_video_3_html5_api`, `blob:` src) · play button `button.vjs-play-control`, whose class toggles `vjs-paused` ↔ `vjs-playing` · fullscreen `button.vjs-fullscreen-control` |
| Play/pause from script | ✅ `btn.click()` and `video.play()` / `video.pause()` both work, and the button state stays in sync either way. (The page already had a user interaction when tested, so the autoplay block wasn't exercised.) |
| Fullscreen | ✅ The fullscreen element is the **video.js container** (`#vjs_video_3`). An overlay appended to the container stays visible in fullscreen. |
| Overlay | ✅ Prototype (Shadow DOM, `rgba(0,0,0,.75)`, big countdown) renders correctly over the video. The container is `position: relative`. |
| Page CSP | `connect-src 'self' https://*.lifeway.com https://*.amazonaws.com …(video CDNs)`. **A WebSocket to our relay is not allowed from the page's context.** The WebSocket must live in the extension's background, not in the content script. |
| Video length | ~72.5 min for this lesson, so the phone should display `h:mm:ss`. |

---

## 1. Goals & Non-Goals

**Goals**
- Play/pause the Lifeway video from a phone browser. No app install on the phone.
- Start a countdown timer overlay from the phone (default **10:00**). The overlay darkens the video.
- Safari Web Extension on the church Mac, written so it ports to Chrome and Firefox with little effort.
- The extension can be turned on and off from its toolbar popup.
- A Go relay server on a home Raspberry Pi, reached through a Cloudflare Tunnel.
- The relay is password protected (one shared password) and rate limited.

**Non-goals (for v1)**
- Multiple churches, rooms or users. One shared "room" is enough.
- Controlling any video site other than the Lifeway player.
- Native phone apps.

---

## 2. Architecture

```
 ┌──────────────┐        wss://remote.example.com/ws        ┌───────────────────────┐
 │ Phone browser│ ───────────────┐                ┌──────── │ Church Mac (Safari)   │
 │ (controller) │                ▼                │         │  Extension            │
 └──────────────┘        ┌──────────────────┐     │         │   background ⇄ content│
                         │ Cloudflare edge  │     │         │   script (player.     │
                         │  (TLS, WAF)      │◀────┘         │   lifeway.com tab)    │
                         └────────┬─────────┘               └───────────────────────┘
                                  │ Cloudflare Tunnel (cloudflared, outbound only)
                         ┌────────▼─────────┐
                         │ Raspberry Pi     │
                         │ relay (Go)       │  listens on 127.0.0.1:8080 only
                         │  - serves phone UI│
                         │  - /ws hub        │
                         └──────────────────┘
```

**Roles on the WebSocket**
- `player`: the browser extension. It receives commands and sends back state.
- `controller`: the phone page. It sends commands and receives state.

The relay stays simple. It authenticates clients, checks and rate-limits messages, sends commands
from controllers to players and state from players to controllers, and caches the last player state
so a phone that just connected sees the current state right away.

---

## 3. Repository Layout

```
deep-discipleship-extension/
├── PLAN.md
├── README.md
├── relay/                          # Go relay server
│   ├── go.mod
│   ├── cmd/relay/main.go           # flags/env, `serve` and `hash-password` subcommands
│   ├── internal/
│   │   ├── auth/                   # bcrypt verify, failed-attempt lockout
│   │   ├── ratelimit/              # per-IP + per-connection token buckets
│   │   ├── hub/                    # client registry, routing, last-state cache
│   │   ├── protocol/               # message types + validation
│   │   └── server/                 # HTTP handlers, WS upgrade, client IP resolution
│   └── web/                        # phone UI, embedded with go:embed
│       ├── index.html
│       ├── app.js
│       ├── style.css
│       ├── manifest.webmanifest    # "Add to Home Screen"
│       └── icon-*.png
├── extension/
│   ├── src/
│   │   ├── manifest.base.json      # shared MV3 manifest
│   │   ├── lib/browser.js          # `const api = globalThis.browser ?? globalThis.chrome`
│   │   ├── lib/protocol.js         # shared message constants/validators
│   │   ├── background.js           # owns the WebSocket + enabled state
│   │   ├── content.js              # finds <video>, runs commands, draws overlay
│   │   ├── popup/popup.html|js|css # on/off toggle, relay URL, password, status
│   │   └── icons/
│   ├── platforms/
│   │   ├── safari.json             # manifest overrides per browser
│   │   ├── chrome.json
│   │   └── firefox.json
│   ├── scripts/build.mjs           # merges base + overrides → dist/<browser>/
│   └── test/mock-player.html       # offline stand-in with Lifeway's markup, for development
├── safari/                         # Xcode wrapper project (generated once, committed)
└── deploy/
    ├── relay.service               # systemd unit
    ├── cloudflared-config.yml
    └── Makefile                    # cross-compile for the Pi, scp, restart
```

---

## 4. Message Protocol (JSON over WebSocket)

Every message is `{"type": string, ...}`. The largest allowed message is 4 KB. The server rejects
unknown `type`s and malformed fields.

### 4.1 Handshake (both roles)
The password never goes in the URL, because URLs end up in Cloudflare and proxy logs. The **first
message** after the WS upgrade must be:

```json
{ "type": "auth", "password": "…", "role": "controller" | "player", "clientName": "Church Mac" }
```
Server replies:
```json
{ "type": "auth_ok" }                       // then normal traffic
{ "type": "auth_error", "reason": "bad_password" | "locked_out" | "timeout" }  // then close
```
If `auth` doesn't arrive within **5 s**, the socket closes.

### 4.2 Controller → Player (commands)
| type            | fields                          | effect |
|-----------------|---------------------------------|--------|
| `play`          | —                               | Play (no-op if already playing) |
| `pause`         | —                               | Pause (no-op if already paused) |
| `toggle`        | —                               | Toggle play/pause |
| `seek_relative` | `seconds` (−60…60)              | Optional: "back 10 s" button |
| `timer_start`   | `durationSec` (1…3600), `pauseVideo` (bool, default true) | Show overlay + countdown |
| `timer_adjust`  | `deltaSec` (−600…600)           | +1 min / −1 min |
| `timer_pause` / `timer_resume` | —                | Freeze/resume countdown |
| `timer_stop`    | —                               | Hide overlay |
| `request_state` | —                               | Ask the player to send state now |

Each command carries an `id` (random string) so the player can `ack` it and the phone can show
a brief "✓ sent" or an error.

Play and pause are sent as **explicit** commands. The phone shows one big button whose label comes
from the reported state, but it sends `play` or `pause`, not `toggle`, so a double-tap or a retried
message can't flip the video back.

### 4.3 Player → Controllers (state)
```json
{
  "type": "state",
  "videoFound": true,
  "paused": true,
  "currentTime": 754.2,
  "duration": 2710.0,
  "timer": { "running": true, "paused": false, "endsAt": 1790000000000, "remainingSec": 412, "durationSec": 600 }
}
```
The player sends state on every change (`play`, `pause`, `seeked`, timer events) and once every
5 s as a heartbeat. The server caches the latest one for new controllers.

### 4.4 Server → Controllers (presence)
```json
{ "type": "presence", "players": 1, "controllers": 2 }
```
The phone uses this to show "Church computer: connected / not connected".

### 4.5 Keepalive
The server sends WS pings every 25 s. Cloudflare closes idle WebSockets after ~100 s. Clients
reconnect with exponential backoff (1 s → 30 s max, with jitter).

---

## 5. Relay Server (Go)

### 5.1 Stack
- Go 1.23+ standard library `net/http` (the method and path patterns in `ServeMux`).
- WebSocket: `github.com/coder/websocket` (the maintained successor to nhooyr), which is context-aware.
- Rate limiting: `golang.org/x/time/rate`.
- Password hashing: `golang.org/x/crypto/bcrypt`.
- Phone UI embedded with `//go:embed web/*`, so the whole thing ships as one static binary.

### 5.2 Endpoints
| Method | Path        | Purpose |
|--------|-------------|---------|
| GET    | `/`         | Phone control page (and static assets) |
| GET    | `/ws`       | WebSocket upgrade (both roles) |
| GET    | `/healthz`  | Liveness check for systemd/monitoring (no auth, returns `ok`) |

### 5.3 Configuration (env vars / flags)
| Var | Default | Notes |
|-----|---------|-------|
| `RELAY_LISTEN` | `127.0.0.1:8080` | Loopback only. Only cloudflared can reach it. |
| `RELAY_PASSWORD_HASH` | (required) | bcrypt hash. Generate with `relay hash-password`. |
| `RELAY_TRUST_CF_HEADERS` | `true` | Use `CF-Connecting-IP` for the client IP (safe because the server listens on loopback only) |
| `RELAY_ALLOWED_ORIGINS` | the public hostname + extension schemes | See 5.6 |

`relay hash-password` reads the password from stdin with no echo and prints the bcrypt hash to put
in `/etc/relay/relay.env`.

### 5.4 Authentication
- A single shared password. Clients send it in the first WS message (§4.1).
- It is checked with `bcrypt.CompareHashAndPassword`. That comparison is constant-time and slow on
  purpose, which also makes brute force expensive.
- **Lockout**: after 5 failed attempts from one IP within 15 min, that IP is locked out for 15 min
  (`auth_error: locked_out`). There is also a **global** cap of 30 failures per 15 min across all
  IPs, which slows distributed guessing.
- To change the password, update the hash and restart. All clients are disconnected and must
  re-enter it.
- Passwords are never logged. Logs record `ip`, `role`, `result`.

### 5.5 Rate Limiting (layered)
| Layer | Limit | On exceed |
|-------|-------|-----------|
| HTTP requests per IP (`/`, `/ws`) | 5 req/s, burst 20 | 429 |
| WS upgrades per IP | 10/min | 429 |
| Concurrent connections per IP | 5 | 429 |
| Concurrent connections total | 20 | 503 |
| Messages per connection (controller) | 5 msg/s, burst 10 | Drop the message and send `{"type":"error","reason":"rate_limited"}`. Close after 3 violations in a row |
| Messages per connection (player) | 10 msg/s, burst 20 | same |
| Failed auth | see 5.4 | lockout |

Per-IP limiters live in a map that a janitor goroutine prunes every few minutes, so memory stays
bounded. The client IP comes from `CF-Connecting-IP`, falling back to `RemoteAddr`.

**Optional extra layer:** add a Cloudflare WAF rate-limiting rule on `/ws` (the free tier allows
one rule). Abuse is then dropped at the edge before it reaches the Pi.

### 5.6 Other Hardening
- **Origin check** on the WS upgrade. Allow `https://<public-host>`, `safari-web-extension://*`,
  `chrome-extension://<id>` and `moz-extension://*`. This stops a random website from opening a
  socket from a visitor's browser. The password is still the real control, because non-browser
  clients can fake `Origin`.
- Read limit of 4 KB per message. Write timeout of 5 s per message. A slow client is dropped
  instead of blocking the hub.
- Server-side schema validation: only whitelisted message types and numeric ranges pass through.
  Even an authenticated client can only play, pause, seek and run the timer.
- Security headers on `/`: a strict CSP (`default-src 'self'; connect-src 'self'`),
  `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`.
- `http.Server` timeouts: `ReadHeaderTimeout` of 5 s, plus idle timeouts.
- Graceful shutdown on SIGTERM.

### 5.7 Hub Design
- One goroutine owns the hub state (players, controllers, lastState). Clients talk to it over
  channels. There are no shared-memory locks in the routing path.
- Each client has a buffered outbound channel (size 16). If the buffer is full, the client is
  disconnected.
- Routing: command from a controller → all players. State from a player → all controllers, and
  update `lastState`. A change in the connection set → send `presence` to controllers.
- New controller → immediately send `presence` + `lastState` (if any).

### 5.8 Tests
- Unit tests for protocol validation, the auth lockout window, and limiter behavior with a fake
  clock.
- Integration tests using `httptest.Server` and real WS clients: auth success and failure, the
  auth timeout, routing, rate-limit disconnects and presence.
- Run `go vet` and `staticcheck` in a Makefile target.

---

## 6. Phone Control Page

It is served by the relay at `/`. It is plain HTML, CSS and JS with no framework and no build step,
built mobile-first.

### 6.1 Screens
**Login**
- Password field and a "Remember on this phone" checkbox (saves to `localStorage`, on by default).
- The error reads "Wrong password" or "Too many attempts. Try again in N minutes."

**Control**
```
┌───────────────────────────────┐
│ ● Church computer connected   │  ← green/amber/red status pill
│   Video found · 12:34 / 45:10 │
├───────────────────────────────┤
│                               │
│         ▶  PLAY               │  ← huge button, ~40% of screen height
│                               │     label/color reflects actual state
├───────────────────────────────┤
│  [⟲ 10s]                      │  ← optional small rewind
├───────────────────────────────┤
│  Discussion Timer             │
│  [ 5 ] [ 10 ] [ 15 ] [ 20 ]   │  ← presets, 10 selected by default
│      −1 min   10:00  +1 min   │
│  [   START TIMER   ]          │
│   (while running:)            │
│      07:12 remaining          │
│  [ −1 ] [ Pause ] [ +1 ] [Stop]│
│  ☑ Pause video when timer starts
└───────────────────────────────┘
```

### 6.2 UX Details
- Touch targets are at least 56 px. A high-contrast dark theme suits a dim room.
- **The buttons show real state.** The play button label comes from the player's reported
  `paused`, not from the last button tapped.
- A button disables while its command is in flight, and a brief "✓" shows on `ack`. If there is no
  ack within 3 s, it shows "Didn't reach the church computer".
- When no player is connected, the controls are greyed out and the page shows "Open the video on
  the church computer and make sure the extension is ON."
- When the player is connected but no video is found: "Extension is on, but no video is on screen."
- The phone runs its own timer countdown from `endsAt`, so the display is smooth. It corrects
  itself on each state message.
- The **Screen Wake Lock API** keeps the phone screen on during the session. A toggle offers it
  where supported.
- Reconnects automatically and says "Reconnecting…" when the network drops.
- Includes `manifest.webmanifest` and icons, so it can be added to the home screen and opened
  full-screen.
- Plays a light vibration (`navigator.vibrate`) on button presses where supported (not iOS Safari).

---

## 7. Browser Extension

### 7.1 Background lifetime: why Safari uses a persistent MV2 background page

The extension's one hard requirement is a WebSocket that stays open for a 60–90 minute session
while nothing else happens. Research findings:

- **Safari only allows persistent backgrounds on macOS, and only in Manifest V2.** Apple's
  compatibility docs: *"In iOS, you need to set the `persistent` attribute to `false`. With
  manifest version 3, all background pages are nonpersistent."* So `"persistent": true` is
  allowed on macOS with `manifest_version: 2` and nowhere else.
- **Safari's MV3 backgrounds are unreliable for long-lived work.** Developers report Safari
  terminating MV3 service workers or non-persistent pages after roughly 30–45 s, sometimes not
  waking them again, and background scripts that "keep getting terminated" and stop answering
  `sendMessage`. Unlike Chrome, Safari doesn't document an open WebSocket as keeping the worker
  alive.
- **Chrome** has supported WebSockets in MV3 service workers since Chrome 116: activity on the
  socket resets the 30 s idle timer, so a ping every 20 s keeps it alive. Chrome no longer runs MV2
  at all (MV2 escape hatches were removed in Chrome 151, July 2026).
- **Firefox** still supports MV2 with persistent background pages, and MV3 there uses event pages
  (`background.scripts`).
- I found no announcement of Safari dropping MV2 as of September 2026. That's the main long-term
  risk (§12).

Sources: [Apple – Assessing your Safari web extension's browser compatibility](https://developer.apple.com/documentation/safariservices/assessing-your-safari-web-extension-s-browser-compatibility),
[Apple forums – background scripts terminated in MV3](https://developer.apple.com/forums/thread/709349),
[Apple forums – Safari service worker killed](https://developer.apple.com/forums/thread/758346),
[Chrome – WebSockets in service workers](https://developer.chrome.com/docs/extensions/how-to/web-platform/websockets),
[MDN – manifest `background`](https://developer.mozilla.org/en-US/docs/Mozilla/Add-ons/WebExtensions/manifest.json/background).

**Decision:** one codebase, different manifest version per browser.

| Browser | Manifest | Background | Keepalive |
|---------|----------|------------|-----------|
| **Safari (macOS)** | **MV2** | `"scripts": ["background.js"], "persistent": true` | None needed. The page never unloads. The app-level ping stays only to detect dead connections. |
| Chrome | MV3 | `"service_worker": "background.js"` | WS ping every 20 s (Chrome 116+) |
| Firefox | MV2 | `"scripts": [...], "persistent": true` (MV3 event page + pings if MV2 ever goes away) | — |

### 7.2 Cross-browser code strategy
- Only standard WebExtension APIs, and only ones that exist in **both MV2 and MV3**.
- All code uses a tiny shim:
  ```js
  const api = globalThis.browser ?? globalThis.chrome;
  const action = api.action ?? api.browserAction;   // MV3 vs MV2 name
  ```
  Promise-style calls only. Safari and Firefox support promises on `browser.*` in MV2, and Chrome
  supports them in MV3.
- No browser-specific APIs (no Chrome offscreen documents, no Safari native messaging).
- **No ES modules in background or content scripts.** MV2 background pages and content scripts
  don't support them consistently. The build bundles or concatenates `lib/*.js` into each entry
  file (a plain concat or `esbuild --bundle --format=iife`).
- `scripts/build.mjs` builds the final manifest from `manifest.base.json` plus
  `platforms/<browser>.json`, then applies the **MV3 → MV2 transform** when the platform file
  says `"manifest_version": 2`:
  - `action` → `browser_action`
  - `host_permissions` → appended to `permissions`
  - `optional_host_permissions` → `optional_permissions`
  - `background.service_worker` → `background.scripts` + `persistent: true`
  - Firefox also gets `browser_specific_settings.gecko.id`.
- Code must not depend on the background ever restarting *or* never restarting. On connect it
  reads everything from `storage.local`, so it works in both models.

### 7.3 Manifest (base, written as MV3; the build derives MV2 for Safari)
```json
{
  "manifest_version": 3,
  "name": "Deep Discipleship Remote",
  "version": "1.0.0",
  "permissions": ["storage"],
  "host_permissions": ["https://player.lifeway.com/*"],
  "action": { "default_popup": "popup/popup.html", "default_icon": { … } },
  "content_scripts": [{
    "matches": ["https://player.lifeway.com/player/*"],
    "js": ["content.js"],
    "all_frames": false,
    "run_at": "document_idle"
  }]
}
```
The player is a top-level page (§0), so there's no need for `all_frames` or broad `*.lifeway.com`
matches. Overlay styles live inside its Shadow DOM, so no content-script CSS file is needed.
No host permission is needed for the relay. WebSockets opened from the background aren't subject
to CORS or host permissions, so the relay URL is just a setting and isn't in the manifest.

### 7.4 Components

**Popup (`popup/`)**
- A big **ON/OFF toggle**.
- Relay URL (for example `wss://remote.example.com/ws`), password field (masked, with a
  show/hide control) and a "Save" button.
- Status: `Off` / `Connecting…` / `Connected` / `Wrong password` / `Locked out` / `Server
  unreachable`, plus "Video detected: yes/no" and the number of connected phones.
- Settings are stored in `storage.local`, not `storage.sync`, so the password doesn't sync to
  other devices.

**Background (`background.js`)**: owns the connection
- Reads `{enabled, relayUrl, password}` from storage. Watches `storage.onChanged` and connects or
  disconnects when the toggle or credentials change.
- When enabled, it opens the WS, sends `auth` as `player` and reconnects with backoff. A bad
  password stops retries and shows the error in the popup and on the badge.
- Content scripts connect with `runtime.connect({name: "player-frame"})`. The background keeps a set
  of ports and remembers which one reported `videoFound: true`.
- Commands from the relay → posted to the video-owning port (or to all ports, and frames without a
  video ignore them). Acks and state from content → forwarded to the relay.
- Action badge: green dot = connected, grey = off, red "!" = error.
- **Lifetime** (see §7.1): on Safari and Firefox the persistent background page keeps the socket
  open for as long as the browser runs. On Chrome, an app-level ping every 20 s keeps the service
  worker alive (Chrome 116+). In every browser the ping also detects dead connections, and the
  content script's port reconnects if the background ever restarts.

**Content script (`content.js`)**: runs on `player.lifeway.com/player/*` tabs
- Must **not** open network connections. The page's CSP blocks them (§0). All relay traffic goes
  through the background.
- Finds the video: `document.querySelector('video.vjs-tech')`. A `MutationObserver` handles players
  that load late or change, which also covers the `vjs_video_N` ids changing.
- Uses selectors only. There's no page-global `videojs` object to call.
- When disabled (per `storage`), it does nothing and removes any overlay.
- **Play/pause**: click `button.vjs-play-control`, which keeps video.js's UI and analytics
  consistent. Only click when `video.paused` differs from the target, so the command is
  idempotent. Fall back to `video.play()` / `video.pause()` if the button is missing. `play()` is
  allowed without a user gesture because the user already interacted with the page to start the
  lesson. If `play()` rejects with `NotAllowedError`, send an error ack so the phone can say
  "Tap play once on the computer first."
- Listens to `play`, `pause`, `seeked`, `loadedmetadata` and `timeupdate` (throttled) on the video
  and sends `state` when they fire.
- **Timer overlay** (below).

### 7.5 Timer Overlay
- The overlay is attached **inside the video.js container** (`video.closest('.video-js')`). It then
  stays visible when the player is fullscreen, because anything outside the fullscreen element
  would be hidden. ✅ Verified: the container *is* the fullscreen element (§0).
- It uses a **Shadow DOM** host element, so neither the page's CSS nor ours leaks into the other.
- Look:
  - Full-size layer with `background: rgba(0,0,0,0.75)` and an optional `backdrop-filter: blur(4px)`.
    This is the "darken".
  - A large centered countdown (`clamp(6rem, 20vw, 16rem)`, tabular numerals) with a
    "Group Discussion" label above it.
  - A thin progress ring or bar showing the time left.
  - In the last 60 s, the numbers turn amber.
  - At 0:00 it shows "Time's up" with a gentle pulse, and the overlay **stays** until the phone
    sends `timer_stop` or starts playback. An optional soft chime can be turned on in the popup
    (off by default).
- `pointer-events: none` on everything except a small "×" close button, so a person at the computer
  can still use the player.
- Timing is based on `endsAt = Date.now() + remaining`, not on counting ticks. Background-tab
  timer throttling therefore can't make it drift. The display updates with `requestAnimationFrame`
  or a 250 ms interval.
- `timer_start` with `pauseVideo: true` pauses the video first.
- Starting playback from the phone while a timer is up hides the overlay automatically. Resuming the
  video means discussion is over. This can be made configurable if it gets in the way.

---

## 8. Safari Packaging (church Mac)

1. Build: `node extension/scripts/build.mjs safari` → `extension/dist/safari/` (an **MV2**
   manifest with a persistent background page, §7.1). `--macos-only` below matters, because iOS
   rejects persistent backgrounds.
2. Generate the wrapper once:
   `xcrun safari-web-extension-converter extension/dist/safari --project-location safari --app-name "Deep Discipleship Remote" --macos-only`
3. Point the Xcode project's extension resources at `dist/safari`, or add a build phase that runs the
   build script, so it updates when you rebuild.
4. Sign with an Apple ID (**Personal Team** is enough to run it on your own Mac), then build and run
   once. Enable it in Safari → Settings → Extensions, and allow it on `lifeway.com`
   and the relay host.
   - Avoid relying on "Allow Unsigned Extensions" (Develop menu). Safari **resets it every time it
     quits**, so it would break every Sunday.
   - If the Personal Team signing expires or causes trouble, a paid Apple Developer account ($99/yr)
     gives long-lived signing.
5. In the popup, set the relay URL and password, and toggle ON.

---

## 9. Deployment (Raspberry Pi + Cloudflare Tunnel)

### 9.1 Build & install
```bash
# on dev machine
cd relay && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o relay ./cmd/relay
scp relay pi:/usr/local/bin/relay
```
(Use `GOARCH=arm GOARM=7` if the Pi runs a 32-bit OS.)

### 9.2 systemd (`deploy/relay.service`)
- Runs as a dedicated `relay` user with no login shell.
- `EnvironmentFile=/etc/relay/relay.env` (mode 600, contains `RELAY_PASSWORD_HASH`).
- `Restart=always` and hardening options: `NoNewPrivileges`, `ProtectSystem=strict`,
  `ProtectHome`, `PrivateTmp`.

### 9.3 Cloudflare Tunnel (`deploy/cloudflared-config.yml`)
```yaml
tunnel: <tunnel-id>
credentials-file: /etc/cloudflared/<tunnel-id>.json
ingress:
  - hostname: remote.example.com
    service: http://127.0.0.1:8080
  - service: http_status:404
```
- `cloudflared service install` sets it up as a systemd service.
- WebSockets work through tunnels by default. Keepalive pings (§4.5) prevent idle disconnects.
- In the Cloudflare dashboard, set SSL/TLS to Full, turn on **Always Use HTTPS**, and optionally
  add the WAF rate-limit rule from §5.5.
- **Don't** put Cloudflare Access in front of `/ws`. The extension can't complete the Access login
  flow. The app password plus rate limiting covers this. (Access *could* protect `/` for the phone,
  but that's another login and not worth it.)

---

## 10. Implementation Phases

| # | Phase | Deliverable | Done when |
|---|-------|-------------|-----------|
| 0 | **Spike** (1–2 hours) | Throwaway Safari **MV2 persistent** extension | DOM, play/pause, fullscreen and overlay are already verified (§0), and research settled the background model (§7.1). **Remaining check:** an MV2 `persistent: true` background opens a `wss://` socket to a test echo server and stays connected for 90+ min idle, including with the Mac's display asleep. Also confirm Safari's Develop menu shows it as a persistent background page. |
| 1 | Relay core | `serve`, `hash-password`, auth, hub, protocol | Integration tests pass. Two `websocat` clients can relay messages. |
| 2 | Relay hardening | Rate limits, lockout, origin check, headers | Tests for every limit. Manual brute-force attempt gets locked out. |
| 3 | Phone UI | `web/` embedded | Works on the iPhone Safari and Android Chrome you'll use. Tested against a fake player script. |
| 4 | Extension | background, content, overlay, popup | Works against `test/mock-player.html` (Lifeway markup, generated silent WAV, no network) in Chrome |
| 5 | Safari packaging | Xcode project | Installed on the church Mac and survives a Safari restart and a reboot |
| 6 | Deploy | Pi + tunnel | Phone on cellular controls the church Mac through the public hostname |
| 7 | Dress rehearsal | — | Full lesson run-through: several pause → timer → resume cycles, with the phone locking and unlocking and Wi-Fi dropping |
| 8 | Chrome/Firefox | `dist/chrome`, `dist/firefox` | Load unpacked in each and repeat the Phase 4 checklist |

---

## 11. Testing Checklist (manual, pre-Sunday)
- [ ] Wrong password → clear error on both the phone and the extension. The 6th attempt is locked out.
- [ ] Extension OFF → no connection (the relay shows 0 players) and the overlay is removed.
- [ ] Play/pause from the phone. The label matches the real state even after someone clicks the
      mouse on the computer.
- [ ] Timer default is 10:00. Presets, ±1 min, pause/resume and stop all work. Darkening is visible
      in the room.
- [ ] Overlay is visible in fullscreen and windowed mode.
- [ ] Phone screen locks and unlocks → reconnects and shows the current state.
- [ ] Church Wi-Fi blip → the extension reconnects within 30 s.
- [ ] Pi reboot → relay and tunnel come back on their own.
- [ ] Two phones at once both work and see the same state.

---

## 12. Risks & Mitigations
| Risk | Mitigation |
|------|------------|
| Safari suspends the background and drops the WebSocket | Use **MV2 with `persistent: true`** on Safari (§7.1). Pings plus fast reconnect as a safety net. A content-script-owned socket is **not** an option because the page CSP blocks it |
| Safari drops MV2 support in a future release | Nothing announced as of Sept 2026. Watch Safari release notes. Plan B: MV3 non-persistent background, content-script port pings every 20 s to keep it awake, and a relay that tolerates brief player reconnects. The code is already written to work in both models (§7.2) |
| Mac sleeps during the session | Disable display/system sleep on the church Mac while presenting (System Settings → Lock Screen / Energy), or run `caffeinate -d` |
| Lifeway changes its player markup or class names | Selectors live in one constants block. Fall back from the button to `video.play()/pause()`. The phone shows "no video found" |
| Lifeway moves the player into an iframe later | The content script is self-contained. Add `all_frames: true` and widen `matches` if needed |
| `play()` blocked by autoplay policy | Clicking the video.js button counts as page-driven. Surface `NotAllowedError` to the phone with instructions |
| Password leaked | Rotate by changing the hash and restarting. Rate limits and lockout slow guessing. Commands are harmless (play/pause/timer only) |
| Home internet or Pi down on Sunday | `/healthz` check before class. The mouse/keyboard on the computer is still the backup |
| Safari unsigned-extension toggle resets | Sign with a Personal Team or a paid developer account (§8) |

---

## 13. Decisions to Confirm (defaults chosen)
1. **Pause the video when the timer starts?** Default: **yes**, as a checkbox on the phone.
2. **Hide the overlay automatically when the video is resumed?** Default: **yes**.
3. **Sound at 0:00?** Default: **off**, configurable in the popup.
4. **Remember the password on the phone?** Default: **yes** (localStorage, with a "Forget" link).
5. **"Back 10 s" button?** Default: **included** (small, secondary).
6. **Public hostname**: to be decided (for example `dd-remote.<yourdomain>`).
