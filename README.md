# nearby-fire-alert

A deterministic, containerized Go service that watches publicly available fire
data, establishes when a fire is genuinely nearby, and notifies a human through
an **OpenClaw Gateway HTTP hook**. It is a monitoring daemon — no LLM, no model
calls. All logic is deterministic and auditable.

## How it works

1. **Providers** poll active-fire detection (NASA FIRMS VIIRS/MODIS) and
   incident-perimeter (NIFC) sources.
2. **Geo-fence** filters detections against a configured home location + radius.
3. **Dedup + escalation** ensure one fire is reported once, re-alerting only on a
   material change (confidence/FRP rise or persistence).
4. **OpenClaw client** POSTs a compact JSON "fire event" payload to the hook,
   using an `Idempotency-Key` to prevent double alerts.

## Setup (FIRMS MAP_KEY)

NASA FIRMS requires a free API key. Get one at
<https://earthdata.nasa.gov/firms> — you must log in with an Earthdata account,
then copy the key from the FIRMS dashboard ("MAP_KEY").

Inject the key at runtime via env or a mounted secret file — never inline it:

```bash
# env
export FIRMS_MAP_KEY='your_key'

# or secret file (preferred for containers / K8s)
echo -n 'your_key' > ./secrets/firms_map_key && chmod 0600 ./secrets/firms_map_key
```

## Building and running (local)

```bash
go build -o nearby-fire-alert ./cmd/nearby-fire-alert
./nearby-fire-alert --help
./nearby-fire-alert --version

# single poll, preview only (no post)
FIRMS_MAP_KEY=$KEY ./nearby-fire-alert --once --dry-run

# run as a daemon
FIRMS_MAP_KEY=$KEY ./nearby-fire-alert
```

Flags:

| Flag | Meaning |
|------|---------|
| `-config` | path to a YAML/JSON/TOML config file (overrides `CONFIG_FILE`) |
| `--dry-run` | print what would be posted without sending |
| `--once` | run a single poll cycle and exit |
| `--migrate` | apply schema migrations and exit |
| `--version` | print version/commit/build info |

## Configuration reference

Configuration is resolved through **four tiers**, lowest to highest precedence:

1. Built-in code defaults
2. Config file (`-config` / `CONFIG_FILE`)
3. Environment variables
4. Secret files (`<NAME>_FILE`)

Secret files win over env and over the config file. For any value, setting
`<NAME>_FILE` points to a mounted file whose **trimmed** contents become the
value.

### Root (geofence / openclaw / db / runtime)

| Field | Env var | Default | Secret file |
|-------|---------|---------|-------------|
| `geofence.home_lat` | `GEOFENCE_HOME_LAT` | `39.5` | `GEOFENCE_HOME_LAT_FILE` |
| `geofence.home_lon` | `GEOFENCE_HOME_LON` | `-97.0` | `GEOFENCE_HOME_LON_FILE` |
| `geofence.radius_miles` | `GEOFENCE_RADIUS_MILES` | `5` | `..._FILE` |
| `geofence.buffer_miles` | `GEOFENCE_BUFFER_MILES` | `25` | `..._FILE` |
| `openclaw.hook_url` | `OPENCLAW_HOOK_URL` | — | `OPENCLAW_HOOK_URL_FILE` |
| `openclaw.hook_token` | `OPENCLAW_HOOK_TOKEN` | — | `OPENCLAW_HOOK_TOKEN_FILE` |
| `openclaw.agent_id` | `OPENCLAW_AGENT_ID` | `main` | `..._FILE` |
| `openclaw.channel` | `OPENCLAW_CHANNEL` | `signal` | `..._FILE` |
| `openclaw.to` | `OPENCLAW_TO` | — | `..._FILE` |
| `openclaw.account_id` | `OPENCLAW_ACCOUNT_ID` | — | `..._FILE` |
| `openclaw.timeout_seconds` | `OPENCLAW_TIMEOUT_SECONDS` | `15` | `..._FILE` |
| `db.driver` | `DB_DRIVER` | `sqlite` | `..._FILE` |
| `db.dsn` | `DB_DSN` | — | `..._FILE` |
| `db.sqlite_path` | `DB_SQLITE_PATH` | `./state.db` | `..._FILE` |
| `runtime.event_ttl_hours` | `RUNTIME_EVENT_TTL_HOURS` | `24` | `..._FILE` |
| `runtime.log_level` | `RUNTIME_LOG_LEVEL` | `info` | `..._FILE` |
| `runtime.escalation_factor` | `RUNTIME_ESCALATION_FACTOR` | `0.20` | `..._FILE` |
| `runtime.escalation_persist_hours` | `RUNTIME_ESCALATION_PERSIST_HOURS` | `24` | `..._FILE` |
| `runtime.escalation_cooldown_minutes` | `RUNTIME_ESCALATION_COOLDOWN_MINUTES` | `60` | `..._FILE` |

### Providers

Provider items use `<PROVIDER>_<ITEM>` env keys and a `sources.<name>` config
block.

**FIRMS** (`sources.firms`)

| Field | Env var | Default |
|-------|---------|---------|
| `enabled` | `FIRMS_ENABLED` | `false` |
| `map_key` | `FIRMS_MAP_KEY` / `FIRMS_MAP_KEY_FILE` | — |
| `sensors` | `FIRMS_SENSORS` (comma-separated) | `VIIRS_SNPP,VIIRS_NOAA20,VIIRS_NOAA21` |
| `interval_seconds` | `FIRMS_INTERVAL_SECONDS` | `300` |
| `min_confidence` | `FIRMS_MIN_CONFIDENCE` | `75` |
| `api_url` | `FIRMS_API_URL` | `https://firms.modaps.eosdis.nasa.gov/api/area/csv` |
| `area` | `FIRMS_AREA` | `-125,24,-66,50` (WestLon,SouthLat,EastLon,NorthLat) |
| `days` | `FIRMS_DAYS` | `1` |

**NIFC** (`sources.nifc`)

| Field | Env var | Default |
|-------|---------|---------|
| `enabled` | `NIFC_ENABLED` | `true` |
| `interval_seconds` | `NIFC_INTERVAL_SECONDS` | `900` |
| `perimeter_url` | `NIFC_PERIMETER_URL` | WFIGS Current Interagency Fire Perimeters (public GeoJSON) |

**NGFS** (`sources.ngfs`) — registered, disabled stub; no-op.

## Detection & de-duplication

A FIRMS hotspot is **alerted** only if **all** of:
- within `radius_miles` of home (haversine), **and**
- `confidence >= min_confidence`, **and**
- the event id is not already known.

A NIFC **perimeter** is alerted when it intersects the home buffer or its
representative point is within the alert radius. Perimeters are treated as
"confirmed" and alert even if satellite confidence was low.

**Dedupe** key is the stable `EventID`. **Escalation** re-alerts the same event
only on a material change (confidence/FRP rise by `escalation_factor`, or the
fire persists beyond `escalation_persist_hours`, respecting
`escalation_cooldown_minutes`). Known events survive restarts and are never
re-alerted.

## Running in Docker

```bash
docker build -t nearby-fire-alert .

# SQLite, env-driven, secrets mounted
docker run --rm -e FIRMS_MAP_KEY_FILE=/run/secrets/firms_map_key \
  -e OPENCLAW_HOOK_TOKEN_FILE=/run/secrets/openclaw_hook_token \
  -e OPENCLAW_HOOK_URL=https://gateway:18789/hooks/fire \
  -e OPENCLAW_TO=+15550100400 \
  -v /tmp/data:/data \
  -v ./secrets:/run/secrets:ro \
  nearby-fire-alert
```

Or use `docker-compose.example.yml` for the full setup (SQLite and Postgres
profiles).

## Adding a new provider

Adding a source requires **only**:

1. A new package implementing `providerapi.Provider`.
2. One `reg.Register(...)` line in `cmd/nearby-fire-alert/main.go`.
3. A `sources.<name>` config block + `<NAME>_*` env keys.

No changes to the core detection, geofence, dedup, store, or OpenClaw logic.
See `internal/providers/ngfs` for the disabled stub that demonstrates this
(and documents the intended AWS S3 / GOES consumer).

## OpenClaw payload schema

`POST` to the hook URL:

- Headers:
  - `Content-Type: application/json`
  - `Authorization: Bearer <hook_token>`
  - `Idempotency-Key: <event_id>`
- Body:

```json
{
  "event": {
    "event_id": "<stable>",
    "kind": "hotspot|perimeter",
    "source": "viirs-snpp|viirs-noaa20|viirs-noaa21|modis|nifc",
    "detected_at": "<UTC ISO-8601>",
    "latitude": 35.1,
    "longitude": -96.9,
    "distance_miles": 2.3,
    "confidence": 95,
    "frp": 25.0,
    "brightness_temp_k": 350.1,
    "incident_name": "Test Fire"
  },
  "summary": "ALERT: hotspot fire Test Fire detected 2.3 mi from home (...)"
}
```

- **Any HTTP `2xx`** is treated as **admission** (accepted), not delivery; the
  service logs the `runId` and does not retry a 2xx. The response body is not
  inspected for `ok`/`enriched`/`deduplicated` booleans.
- `401`/`429`/`5xx` are retried with exponential backoff, reusing the same
  `Idempotency-Key`.
- **Durable dedup is the service's job.** Known events live in the SQLite/Postgres
  store and survive restarts; the `Idempotency-Key` header is a best-effort
  supplementary safety net on the Gateway side only (its replay cache is
  in-memory and expires after ~5 min, and is cleared on Gateway restart), so it
  should never be the source of truth for "only once".
- When posting directly to `/hooks/agent` (vs a mapped `/hooks/fire`), the body
  also carries delivery routing (`channel`, `to`, `account_id`).

## Database

- **SQLite** (default): `db.driver=sqlite`, `db.sqlite_path` points to a durable
  file. Zero-config.
- **Postgres**: `db.driver=postgres`, `db.dsn` is the URL. Run `--migrate` once
  (or auto-migrate on start). All queries are parameterized; never interpolate
  values into SQL.

## Logging

Structured JSON logs at `debug/info/warn/error`. Every delivered alert is logged
at `info`; every filtered/skipped detection is logged at `debug`. Timestamps are
UTC ISO-8601.
