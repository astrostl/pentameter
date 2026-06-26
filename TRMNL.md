# Pentameter → TRMNL

Push live pool data to a [TRMNL](https://usetrmnl.com/) e-ink display.

> **Status: experimental.** This is the `trmnl` branch — a playground for getting
> pool info onto a TRMNL screen. Expect rough edges.
>
> **Verified end-to-end:** push accepted by a real Private Plugin webhook and
> rendered on a TRMNL device (firmware 1.8.6). The Liquid ternary shorthand
> (`{{ x ? "a" : "b" }}`) used in `trmnl/markup.liquid` works in TRMNL's engine.

**Authoritative TRMNL docs: https://docs.trmnl.com/** — consult these for plugin
setup, the Liquid template framework, webhook limits, and refresh behavior.
Anything in this file that disagrees with the official docs is stale; trust the
docs and update this file.

## How it works

TRMNL devices pull rendered screens from TRMNL's cloud — they don't fetch from
your LAN directly. So pentameter uses the **Webhook (push)** strategy of a TRMNL
**Private Plugin**:

```
pentameter (your LAN)  --HTTPS POST-->  TRMNL cloud   --renders Liquid-->  device
   own engine, reads                     stores latest
   pool snapshot                         merge_variables
```

This keeps pentameter on your local network with no inbound exposure. The pusher
runs its **own** IntelliCenter engine in the background, independent of whatever
mode pentameter is in, so it works the same in metrics or homebridge mode. It is
**skipped in listen mode** (a debugging path that already runs its own engine).

> **Not the Device API.** TRMNL also exposes a device's MAC Address + "Device API
> Key" (Devices → your device → Device Credentials). Those authenticate the *pull*
> Display API (`GET /api/display`) and are **not** used by this integration. We
> push to a Private Plugin webhook — a different thing in a different part of the
> UI. Don't paste the Device API Key as the webhook.

## Setup

1. **Create a Private Plugin on TRMNL.** In the TRMNL web UI: Plugins → Private
   Plugin → Add. Choose the **Webhook** strategy. TRMNL gives you a webhook URL
   like `https://trmnl.com/api/custom_plugins/<uuid>`.

   ⚠️ The webhook URL is a **write credential** — treat it like a password. Don't
   commit it; pentameter never logs it.

2. **Paste the markup.** Copy [`trmnl/markup.liquid`](trmnl/markup.liquid) into
   the plugin's Markup field. It renders the variables pentameter sends. Tweak the
   layout here anytime without redeploying pentameter.

3. **Point pentameter at the webhook** and run it in any supported mode:

   ```bash
   # flag
   pentameter --trmnl-webhook https://trmnl.com/api/custom_plugins/<uuid>

   # or environment variable
   export PENTAMETER_TRMNL_WEBHOOK=https://trmnl.com/api/custom_plugins/<uuid>
   pentameter
   ```

   It pushes every 5 minutes by default. Tune with `--trmnl-interval <seconds>`
   (env `PENTAMETER_TRMNL_INTERVAL`, minimum 60s) — but mind the rate limits below.

4. **Add the plugin to a playlist** on TRMNL so it shows on the device.

## Limits (from the official docs)

> Verify against https://docs.trmnl.com/go/private-plugins/webhooks — these were
> current as of this writing and may change.

- **Payload size:** 2 KB per request for standard accounts, 5 KB for TRMNL+.
  Pentameter logs a warning if a push exceeds 2 KB. A typical pool serializes to
  ~1 KB, so this is usually not a concern; very large configs could approach it.
- **Rate limit:** 12 pushes/hour standard (one per 300s), 30/hour TRMNL+ (one per
  120s). Over the limit returns HTTP `429`. The default `--trmnl-interval 300`
  sits exactly at the standard limit; **go below 300s only on TRMNL+.** Enabling
  "Debug Logs" in plugin settings temporarily raises limits during development.
- **Endpoint:** `POST https://trmnl.com/api/custom_plugins/<uuid>` with
  `Content-Type: application/json` and a top-level `merge_variables` object.

## Refresh cadence & tiers

Three independent intervals are in play; the **slowest one wins** for what you
actually see on the device:

| Stage | Free tier | TRMNL+ | Notes |
|-------|-----------|--------|-------|
| Device auto-refresh | 15 min min | 5 min min | Device Settings → Battery & Sleep (gear icon). The hard floor. |
| Webhook push rate | 12/hr (5 min) | 30/hr (2 min) | TRMNL's limit; over it → HTTP 429. |
| `--trmnl-interval` | your choice (≥60s) | same | How often pentameter pushes. Default 300s. |

Implication: on the **free tier the screen updates every 15 min at most**, no
matter how often pentameter pushes — so pushing faster than the device refreshes
just burns webhook budget. On free tier, `--trmnl-interval 900` loses nothing
on-screen. Pool temps move slowly, so 15 min is usually fine; TRMNL+ (5 min) is
the only way to go faster automatically.

On-demand updates bypass the timer: "Force Refresh" on the plugin settings page
regenerates the screen now, and the device button (OG) / center touchbar tap (X)
fetches immediately — useful for testing. Do plugin-settings Force Refresh
*before* the button so you don't land on a stale cached screen.

## Sharing with others (Recipes)

The webhook is inherently per-user (each person's pentameter pushes to their own
secret URL), so every user needs their own plugin instance. But the markup can be
shared: from the plugin settings page, **Publish as a Recipe** — either public
(listed in `/recipes`, moderated) or **unlisted** (instant shareable link, no
review). Installers get the layout in ~1 click and only paste their own webhook
URL. Caveat: as "Recipe Master" your edits push to everyone who installed it.

## Payload

Pentameter POSTs `Content-Type: application/json`:

```json
{
  "merge_variables": {
    "updated": "2023-11-14T22:13:20Z",
    "updated_epoch": 1700000000,
    "freeze_active": true,
    "bodies":   [{ "name": "Pool", "temp": 82, "on": true, "heat": "heating" }],
    "air":      [{ "name": "Air", "temp": 75 }],
    "pumps":    [{ "name": "Pump", "rpm": 2000, "on": true }],
    "heaters":  [{ "name": "UltraTemp", "subtype": "ULTRA", "status": "heating", "setpoint_low": 85 }],
    "circuits": [{ "name": "Pool Light", "subtype": "LIGHT", "on": true, "freeze": false }],
    "features": [{ "name": "Waterfall", "subtype": "GENERIC", "on": true, "freeze": false }]
  }
}
```

Interpretation mirrors the Prometheus metrics exactly (same source code paths):

- `heat` / `status`: `off` · `heating` · `idle` · `cooling` (derived from the
  body's `HTMODE` + setpoints; "idle" = heater assigned, temp within setpoints).
- Heaters are listed only if they're real devices (not "Preferred"/combo
  pseudo-objects); `setpoint_low` is the heat setpoint of the body using them.
- `circuits` excludes generic `AUX n` placeholders; `features` respects
  IntelliCenter's "Show as Feature" (`SHOMNU`) visibility flag.
- `freeze_active` is true when the freeze-protection circuit (`SUBTYP=FRZ`) is on.

Per the design philosophy, the template should key off **types** (`subtype`), not
equipment names, so it works on any pool.

## Code map

- `trmnl.go` — pusher (`runTRMNLPusher`), payload builder (`buildTRMNLVars`), POST
  (`pushTRMNL`). Self-contained; reuses `intellicenter.Snapshot` + interpretation
  helpers (`Body.HeatStatus`, `ShouldShowFeature`).
- `trmnl_test.go` — payload-shaping and POST tests (no network beyond httptest).
- `trmnl/markup.liquid` — the TRMNL-side template.
- Flags/config in `main.go`: `--trmnl-webhook`, `--trmnl-interval`,
  `determineTRMNLInterval`, and the goroutine launch in `main()`.

## Open ideas / TODO

- Body `subtype` (POOL/SPA) isn't in `intellicenter.Body` yet — bodies are shown
  by name only. Add it if the template needs to distinguish them by type.
- Optional `/trmnl` JSON endpoint for local debugging / BYOS self-hosted TRMNL.
- Watts/GPM for pumps (already in the snapshot) if a denser screen is wanted.

> **Maintenance note:** when you learn something new about TRMNL (payload limits,
> template quirks, plugin behavior) while working on this branch, update this file
> and the official link above so the next session starts ahead.
