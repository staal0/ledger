<p align="center">
  <img src="static/logo.svg" width="96" height="96" alt="Ledger logo">
</p>

<h1 align="center">Ledger</h1>

<p align="center">A small self-hosted household budget, built to replace a yearly budget spreadsheet.</p>

Ledger is one Go binary with three dependencies (MySQL driver, go-oidc, x/oauth2) and no frontend build step.

## Features

- A yearly budget grid with income, expenses, the disposable amount and allocation, by month, with totals that update as you type
- Charts beside the grid: year totals, income vs. expenses per month, largest expenses and allocation
- Spreadsheet-style keyboard use: arrow keys or vim keys (`hjkl`, `gg`/`G`, `{`/`}`, `x`, `u`, `y`/`p`), Enter saves; press `?` for help
- New years start empty or as a copy of the previous year
- Statistics across years: totals, change from the year before and every line over time, for a chosen range of years
- Export a year to an OpenDocument spreadsheet (`.ods`) with the totals as formulas
- Login with OIDC, e.g. passkeys via [Pocket ID](https://pocket-id.org), or local mode without login for trying it out
- English and Danish, chosen per user or taken from the browser; new languages are JSON files
- Per-user settings for language, money format (`1.234,56` or `1,234.56`) and light/dark theme
- Room for plugins (pension, investments, …), see `plugin.go`

Planned: bank CSV import and budget vs. actual.

## Try it

Start a throwaway MariaDB, fill it with a fictional demo household and run Ledger in local mode (no login, only reachable from your own machine):

```sh
docker run -d --name ledger-demo-db -p 127.0.0.1:3306:3306 \
  -e MARIADB_DATABASE=ledger -e MARIADB_USER=ledger -e MARIADB_PASSWORD=ledger -e MARIADB_RANDOM_ROOT_PASSWORD=yes mariadb:lts

go build
export LEDGER_DB_DSN='ledger:ledger@tcp(127.0.0.1:3306)/ledger'
./ledger demo        # or "./ledger demo da" for Danish line names; give MariaDB ~20 s to start first
./ledger             # then open http://localhost:8080
```

`ledger demo` adds three years of budgets, ending with the current year. It refuses to run on a database that already has budgets, so it cannot mix demo data into real data. With Docker Compose: `docker compose run --rm ledger demo`.

## Configuration

| Variable | Required | Description |
|---|---|---|
| `LEDGER_DB_DSN` | yes | e.g. `ledger:secret@tcp(localhost:3306)/ledger` |
| `OIDC_ISSUER` | no | e.g. `https://id.example.com`. Without it the app runs in local mode: no login, loopback only |
| `OIDC_CLIENT_ID` | with OIDC | |
| `OIDC_CLIENT_SECRET` | no | Leave empty for a public client (PKCE is always used) |
| `LEDGER_BASE_URL` | no | Public URL, default `http://localhost:8080` |
| `LEDGER_ADDR` | no | Listen address, default `:8080` (`127.0.0.1:8080` in local mode) |
| `LEDGER_LOCALES_DIR` | no | Extra or overriding language files |
| `LEDGER_ADMIN_GROUPS` | no | Comma-separated groups whose members are admins, see [Roles](#roles) |
| `LEDGER_EDITOR_GROUPS` | no | Groups whose members are editors |
| `LEDGER_VIEWER_GROUPS` | no | Groups whose members are viewers |
| `LEDGER_GROUPS_CLAIM` | no | Claim with the user's groups, default `groups`; nested with dots, e.g. `realm_access.roles` |
| `LEDGER_GROUPS_SCOPE` | no | Scope requested for the groups claim, default `groups`; set it empty to request none |

### Database

Ledger needs an empty MariaDB database and a user that owns it. The schema is created and migrated automatically on startup.
With `compose.yaml` the `MARIADB_*` variables create both on the first start. On an existing server, run as root:

```sql
CREATE DATABASE ledger CHARACTER SET utf8mb4;
CREATE USER 'ledger'@'%' IDENTIFIED BY 'secret';
GRANT ALL PRIVILEGES ON ledger.* TO 'ledger'@'%';
```

All data is in this database, so back it up like any other MariaDB database, e.g. with `mariadb-dump`.

### Roles

| Role | Can |
|---|---|
| Viewer | Read budgets and statistics, and export |
| Editor | Also change budgets and create years |
| Admin | Also use the manage page: delete years, change history, history settings |

Roles come from the groups your identity provider sends at login: a user gets the highest role whose group they are in, and a user in none of the groups cannot log in.
The role is set at each login. Without any `LEDGER_*_GROUPS`, everyone who can log in is an admin.

Ledger reads the groups from the ID token, or from the userinfo endpoint if the ID token has none.

### Pocket ID

Create an OIDC client with the callback URL `<LEDGER_BASE_URL>/auth/callback`.
Restrict it to your household with *Allowed user groups*, and set the `LEDGER_*_GROUPS` to Pocket ID group names. Pocket ID sends the groups for the `groups` scope, which Ledger requests by default.

### Keycloak

Create a client scope named `groups` with a *Group Membership* mapper (claim name `groups`, *Full group path* off, added to the ID token) and add it to the client as an optional scope.
Keycloak rejects scopes it does not know, so without that client scope set `LEDGER_GROUPS_SCOPE` to empty.
To use realm roles instead of groups, set `LEDGER_GROUPS_CLAIM=realm_access.roles` and turn on *Add to ID token* for the *realm roles* mapper of the `roles` client scope.

## Running

Images are published to `ghcr.io/staal0/ledger`. `latest` is the newest release; each release is also tagged with its version for pinning:

```sh
docker pull ghcr.io/staal0/ledger:latest
```

To run it with Docker Compose, download [`compose.yaml`](compose.yaml), replace the `change-me` values and the URLs, and run `docker compose up -d`. Or build from source:

```sh
go build && LEDGER_DB_DSN=... OIDC_ISSUER=... OIDC_CLIENT_ID=... ./ledger
```

## Languages

Each language is a `locales/<code>.json` file. To add one, copy `locales/en.json` to e.g. `de.json`, translate it,
and either put it in `locales/` (compiled in) or in `LEDGER_LOCALES_DIR`. Missing keys fall back to English.
`_decimal` and `_thousands` control number formatting.

## Plugins

A plugin implements the `Plugin` interface in `plugin.go` and calls `register` from `init()`.
It can add migrations (tracked per plugin), routes behind login and menu entries.

## Tests

`go test ./...` runs the unit tests. The database tests also need a disposable MariaDB database; they wipe it, so its name must contain `test`:

```sh
LEDGER_TEST_DSN='ledger:ledger@tcp(127.0.0.1:3306)/ledger_test' go test ./...
```

Without `LEDGER_TEST_DSN` they are skipped. CI runs everything against a MariaDB service container.

## License

MIT, see [LICENSE](LICENSE).
