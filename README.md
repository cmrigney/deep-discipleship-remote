# Deep Discipleship Remote

Control the Deep Discipleship course video on the church computer from your phone:

- **Play / pause** and **back 10 s**
- **Discussion timer** (default 10 min) shown over the video. It darkens the video, works in
  fullscreen, and hides when you press Play.

```
Phone browser ──wss──▶ Cloudflare ──tunnel──▶ Raspberry Pi (relay, Go) ◀──wss── Safari extension on the church Mac
```

See [PLAN.md](PLAN.md) for the design, research notes and security model.

| Folder | What |
|---|---|
| `relay/` | Go relay server. Also serves the phone page (`relay/web/static`). |
| `extension/` | Browser extension source (`src/`), per-browser manifest overrides, build script, mock player |
| `safari/` | Script that wraps the extension in a macOS app with Xcode |
| `deploy/` | systemd unit, env example, cloudflared config, Makefile for the Pi |
| `e2e/` | End-to-end test (relay + extension in Chromium + mock player + phone page) |
| `tools/` | Icon generator |

---

## 1. Deploy the relay on the Raspberry Pi

You need Go 1.26+ on your dev machine and SSH access to the Pi.

```bash
cd deploy
make test                                  # optional: run the relay tests
make install-service PI=pi@raspberrypi.local   # creates the relay user + systemd unit
make deploy PI=pi@raspberrypi.local            # cross-compiles (arm64) and installs
```

Use `make deploy GOARCH=arm` if the Pi runs 32-bit Raspberry Pi OS.

Then set the password **on the Pi**:

```bash
relay hash-password               # type the password twice; prints a bcrypt hash
sudo nano /etc/relay/relay.env    # paste in the contents of deploy/relay.env.example, with the hash
sudo chmod 600 /etc/relay/relay.env
sudo systemctl restart relay
curl http://127.0.0.1:8080/healthz   # → ok
```

Pick a password of at least 8 characters that the group can type on a phone. To change it
later, run `relay hash-password` again, update `relay.env` and restart. Everyone connected is
signed out and has to enter the new password.

## 2. Cloudflare Tunnel

On the Pi, with [`cloudflared`](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/) installed:

```bash
cloudflared tunnel login
cloudflared tunnel create dd-remote          # prints the tunnel id; writes ~/.cloudflared/<tunnel-id>.json
cloudflared tunnel route dns dd-remote dd-remote.<your-domain>

# The service reads its config and credentials from /etc/cloudflared, so copy both there.
sudo mkdir -p /etc/cloudflared
sudo cp ~/.cloudflared/<tunnel-id>.json /etc/cloudflared/
sudo chmod 600 /etc/cloudflared/<tunnel-id>.json
sudo cp deploy/cloudflared-config.yml /etc/cloudflared/config.yml   # then edit tunnel id + hostname
sudo cloudflared tunnel --config /etc/cloudflared/config.yml ingress validate
sudo cloudflared service install
systemctl status cloudflared --no-pager | head -5   # should be active (running)
```

Leave the relay on `127.0.0.1`. Only `cloudflared` should reach it, which is also why it's safe
for the relay to trust the `CF-Connecting-IP` header. In the Cloudflare dashboard, turn on
**Always Use HTTPS**. You can also add a WAF rate-limiting rule on `/ws` (PLAN.md §5.5).
Don't put Cloudflare Access in front of `/ws`, because the extension can't log in through it.

Open `https://dd-remote.<your-domain>/` on your phone. You should see the password screen.

## 3. Install the Safari extension on the church Mac

On the Mac (needs Xcode and Node):

```bash
./safari/make-xcode-project.sh
```

1. Open the generated project in Xcode. Under **Signing & Capabilities**, choose a **Team** for
   both targets. The free Apple ID "Personal Team" is enough.
2. **Product → Run.** This installs the small wrapper app. Open it once.
3. In Safari, go to **Settings → Extensions** and turn on **Deep Discipleship Remote**. Allow it
   on `player.lifeway.com`.
4. Click the extension's toolbar icon, open **Connection settings**, enter the relay address
   (`dd-remote.<your-domain>` is enough) and the password, then **Save**.
5. Turn **Remote control** on. The status should say **Connected**.

Don't use Safari's "Allow Unsigned Extensions" instead of signing: Safari turns it off every
time it quits.

Before class, set the Mac so its **display doesn't sleep** while the lesson is on. The
extension stays connected, but a sleeping Mac won't respond.

After changing the extension code, rebuild (`node extension/scripts/build.mjs safari`) and
press Run in Xcode again. The Xcode project references `extension/dist/safari` rather than a
copy of it. If you add a **new** file to the extension, re-run `./safari/make-xcode-project.sh`
so the project includes it.

## 4. Using it

1. On the church Mac, open the lesson from my.lifeway.com. It opens in a
   `player.lifeway.com/player/…` tab.
2. On your phone, open `https://dd-remote.<your-domain>/` and enter the password.
   **Add to Home Screen** makes it open like an app. Turn on **Keep screen on** at the
   bottom if your phone supports it.
3. The top of the phone page shows **Church computer connected** in green when everything's
   ready.
4. **Play / Pause** is the big button. **Start Timer** pauses the video and shows the countdown
   over it. **Play** ends the timer and resumes the video.

If the phone says *"The browser blocked playback"*, click Play once on the computer. Browsers
require one real click on a page before a script can play it with sound.

---

## Development

```bash
node tools/gen-icons.mjs                     # regenerate PNG icons (already committed)
node extension/scripts/build.mjs all         # → extension/dist/{safari,chrome,firefox}
node extension/scripts/build.mjs chrome --dev   # also runs on http://localhost (mock player)

cd relay && go test ./...                    # relay unit + integration tests
RELAY_PASSWORD=devpass RELAY_TRUST_CF_HEADERS=false go run ./cmd/relay   # local relay on :8080

cd e2e && npm install && npx playwright install chromium && node run.mjs   # full end-to-end test
```

For quick manual testing in Chrome, load `extension/dist/chrome` via `chrome://extensions` →
Developer mode → Load unpacked. Then open `extension/test/mock-player.html` from a local server
and use relay address `ws://localhost:8080`.

**Firefox:** load `extension/dist/firefox/manifest.json` from `about:debugging` → This Firefox →
Load Temporary Add-on.

### Where to change things

- **Lifeway markup changes:** the `SEL` constants at the top of `extension/src/content.js`.
- **Rate limits and lockout:** `DefaultLimits()` in `relay/internal/server/server.go` and
  `DefaultConfig()` in `relay/internal/auth/auth.go`.
- **Messages:** `relay/internal/protocol/protocol.go`. The relay forwards only the fields
  defined there.
