# Deploying mautrix-go-wechat (Docker)

This deployment follows the same pattern as
[mautrix-telegram](https://github.com/mautrix/telegram): a bridge container that
talks to your Matrix homeserver via an **appservice registration**, plus a
database. All persistent state lives in `./data`.

## Files

| File                  | Purpose |
|-----------------------|---------|
| `Dockerfile`          | builds the bridge image |
| `docker-run.sh`       | container entrypoint (config bootstrap + `-g` + run) |
| `docker-compose.yml`  | bridge + Postgres (+ optional Synapse / WeChat API) |
| `config.example.yaml` | template copied to `./data/config.yaml` on first run |
| `./data/config.yaml`  | your live config (created on first run) |
| `./data/registration.yaml` | appservice registration for the homeserver (generated) |

## First-run sequence

```bash
# 0. pull/build the image
# If using Nexus, make sure docker-compose.yml image is 10.0.0.214:28051/mautrix-go-wechat:latest
# If building locally, uncomment the build: block in docker-compose.yml.
docker compose pull mautrix-wechat || docker compose build mautrix-wechat

# 1. bootstrap config: writes ./data/config.yaml from the example, then exits
docker compose run --rm mautrix-wechat

# 2. edit ./data/config.yaml:
#    - homeserver.address / homeserver.domain
#    - network.base_url  -> YOUR WeChat API (swagger.json service)
#    - bridge.permissions -> your Matrix user/domain
nano ./data/config.yaml

# 3. generate registration + tokens: writes ./data/registration.yaml, exits
docker compose run --rm mautrix-wechat

# 4. register the appservice with your homeserver.
#    Synapse: copy ./data/registration.yaml somewhere Synapse can read it and add:
#
#      app_service_config_files:
#        - /data/registration.yaml
#
#    then restart Synapse.

# 5. start the bridge for real
docker compose up -d mautrix-wechat
docker compose logs -f mautrix-wechat
```

## Using the bridge

Once running, start a chat with the bridge bot `@wechatbot:example.com` in your
Matrix client (or invite it to a private room), then send:

```text
help
login
```

The bot can use different WeChat login protocols/client types. First send
`login` to list available flows, then use one, e.g. `login ipad`, `login win`,
`login mac`, `login android-pad`. The bot will send the QR code in the Matrix
room. Scan and confirm in WeChat.

After login, incoming WeChat messages automatically create Matrix portal rooms
(one room per WeChat chat), similar to mautrix-telegram. In a portal room, plain
Matrix text messages are sent back to WeChat.

Management-room commands:

```text
login
login ipad
login win
accounts
account <wxid>
use <wxid>
status
portals
send <wxid-or-chatroom-id> hello
```

Portal creation can be automatic or manual:

```yaml
bridge:
    portal_creation: auto   # or manual
```

Bot commands for portal control:

```text
portal-mode
portal-mode auto
portal-mode manual
pending
create <chat>
create all
```

When `portal_creation: manual`, new WeChat chats do not immediately create Matrix
rooms. The bot posts a notification in the management room and waits for
`create <chat>`.

Portal-room commands:

```text
!info
!sync
```

`account <wxid>` calls the backend's `/Login/GetCacheInfo?wxid=...` endpoint.
If the API returns login cache for that wxid, the bridge activates it as the
current logged-in account, saves it to `/data/session.json` and `/data/accounts.json`,
then starts the sync loop. Sensitive session/key/token fields are not printed in
Matrix.

If commands don't respond, check whether the room is encrypted. This MVP does
not decrypt Matrix E2EE rooms yet; use an unencrypted management/portal room.

## Switching the database to Postgres

The compose file ships a `postgres` service. To use it instead of SQLite, set in
`config.yaml`:

```yaml
database:
    type: postgres
    uri: postgres://bridge:bridge@postgres/mautrix_wechat?sslmode=disable
```

For SQLite only, you may delete the `postgres` service and the `depends_on`
block in `docker-compose.yml`; the default SQLite db is stored at
`/data/mautrix-wechat.db`.

## Bot not visible in Matrix?

Check these in order:

1. `./data/registration.yaml` is listed in Synapse `app_service_config_files`.
2. Synapse was restarted after adding the registration.
3. The bridge is reachable by Synapse at `appservice.address`, e.g.
   `http://mautrix-wechat:29318`.
4. Logs show the appservice listener started:
   `docker compose logs -f mautrix-wechat`.
5. In Matrix, invite or DM `@wechatbot:<homeserver.domain>`.

## Pointing at your WeChat API

The bridge calls your HTTP API at `network.base_url`. Options:

- **External API** (most common): set `base_url` to wherever it runs, e.g.
  `http://host.docker.internal:8080/api` or a hostname on a shared Docker network.
- **Same compose**: uncomment the `wechat-api` service in `docker-compose.yml`
  and set `base_url: http://wechat-api:8080/api`.

## Switching protocol (e.g. QQ)

Implement a new backend under `internal/<proto>` (see README), then just change:

```yaml
network:
    protocol: qq
    base_url: http://your-qq-api/...
```

No other deployment change is needed — same image, same compose.

## Reverse proxy / networks

If your homeserver runs in a different Docker network, uncomment the `matrix`
external network in `docker-compose.yml` and attach the bridge to it so the
homeserver can reach `appservice.address` and the bridge can reach
`homeserver.address`.
