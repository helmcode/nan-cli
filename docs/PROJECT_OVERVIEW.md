# nan CLI — Project Overview

> A terminal-based CLI for interacting with the [nan.builders](https://nan.builders) cloud platform.
> Authentication via Discord OAuth; API calls use cookie-based session tokens.

---

## Quick Reference (Java Developer's Cheat Sheet)

| Go concept | Java equivalent |
|---|---|
| `package` | `package` (directory = module) |
| `import` | `import` |
| `struct` | `class` (data carrier, no methods) |
| `func (c *Client) Method()` | `class Client { void method() }` (receiver = implicit `this`) |
| `interface{}` | `Object` / `var` / `generic<?>` |
| `map[string]any` | `Map<String, Object>` |
| `defer resp.Body.Close()` | try-with-resources (`try (var r = resp) {}`) |
| `errors.New()` / `%w` | `throw new RuntimeException()` / `Exception e` (wrapped) |
| `init()` function | static initializer block |
| `go.mod` | `pom.xml` / `build.gradle` |
| `go run .` | `mvn exec:java` / `java Main` |
| `go build -o nan .` | `mvn package` (produces fat JAR or binary) |

---

## Architecture

```
main.go                  ← Entry point. Calls cmd.Execute().
│
└── cmd/                  ← "Controllers" — CLI subcommands (Cobra)
    ├── root.go           Root command: "nan"
    ├── auth.go           "nan auth login" / "nan auth logout"
    ├── me.go             "nan me" — fetch current user profile
    └── metrics.go        "nan metrics usage" — fetch usage stats
         │
         └── internal/    ← "Service layer" (private to this module)
              ├── api/client.go     HTTP client — thin wrapper around nan.builders REST API
              ├── browser/cookies.go Browser cookie reader — reads Firefox/Chromium cookie DBs
              └── session/          Session persistence — reads/writes ~/.config/nan/session.json
```

### Directory tree

```
.
├── main.go                  # Entry point
├── go.mod                   # Module + dependency manifest (like pom.xml)
├── go.sum                   # Dependency checksums
├── nan                      # Compiled binary (after `go build`)
├── cmd/                     # CLI commands ("controllers")
│   ├── root.go              # Root command: "nan"
│   ├── auth.go              # "auth login" / "auth logout"
│   ├── me.go                # "me" — profile endpoint
│   └── metrics.go           # "metrics usage" — usage stats
└── internal/                # Private packages
    ├── api/
    │   └── client.go        # REST client (HTTP + JSON)
    ├── browser/
    │   └── cookies.go       # Firefox & Chromium cookie reader
    └── session/
        └── session.go       # Token persistence (~/.config/nan/session.json)
```

---

## 1. Entry Point & CLI Framework

### `main.go`

Minimal — just delegates to Cobra's `Execute()`.

```go
func main() { cmd.Execute() }
```

**Think of it as:** a Spring Boot `@SpringBootApplication` class with a single `public static void main` that delegates to a framework bootstrap. Cobra handles argument parsing, help text, and command routing — similar to Spring Shell or Picocli.

### `cmd/root.go`

Defines the root command `nan` and registers all subcommands. The `init()` function is like a static initializer — Cobra's command tree is built at package load time.

---

## 2. Authentication Flow

### `cmd/auth.go` — "Auth Controller"

| Subcommand | Usage | What it does |
|---|---|---|
| `nan auth login` | Opens Discord OAuth URL in browser, then waits for user to paste `nan_session` cookie value | Saves token to `~/.config/nan/session.json` |
| `nan auth login --token <value>` | Skips browser, saves token directly | Same persistence |
| `nan auth logout` | Deletes `session.json` | Clears local session |

**Login flow (step by step):**

1. `openBrowser(apiAuthURL)` — spawns `xdg-open` / `open` / `rundll32` to open Discord login in the default browser.
2. Prints instructions to manually copy the `nan_session` cookie from DevTools.
3. User pastes the token into the terminal.
4. `saveToken()` writes it to `~/.config/nan/session.json` (file permissions `0600`, dir `0700` — like a `.env` file with restrictive perms).

**No browser cookie polling anymore** — the older version auto-read Firefox/Chromium DBs; the current version uses manual paste for simplicity and security. The `browser/` package is still present but unused by the login flow.

---

## 3. Session Persistence

### `internal/session/session.go` — "Session Repository"

Analogous to a `JdbcTemplate` or `@Repository` that reads/writes a single JSON file:

```
~/.config/nan/
└── session.json    ← {"token": "abc123..."}    (mode 0600)
```

| Function | Behavior |
|---|---|
| `Load()` | Reads `session.json`; returns `ErrNotLoggedIn` if file missing |
| `Save(s *Session)` | Creates directory (if needed, `0700`) and writes `session.json` (`0600`) |
| `Delete()` | Removes `session.json` (used by `logout`) |

**Java equivalent:** a class with `load()`, `save(Session)`, and `delete()` backed by `ObjectMapper` reading a file on disk.

---

## 4. API Client

### `internal/api/client.go` — "REST Client"

A thin wrapper around `net/http` with cookie-based auth.

```
Base URL: https://cloud-api.nan.builders/api
Auth:     Cookie header → "nan_session=<token>"
```

| Method | Endpoint | Returns |
|---|---|---|
| `GetMe()` | `GET /auth/me` | `map[string]any` (user profile JSON) |
| `GetMetricsUsage()` | `GET /metrics/usage` | `map[string]any` (usage stats JSON) |
| `GetAgentsModels()` | `GET /agents/models` | `any` (agents/models list — not yet used) |

**How `get()` works (the HTTP layer):**

1. Builds a `GET` request to `BaseURL + path`.
2. Attaches `Cookie: nan_session=<token>` header if a token exists.
3. Reads the full response body.
4. On HTTP ≥ 400, tries to parse `{"error":"..."}` from the body; falls back to `"HTTP <status>"`.
5. Returns raw bytes on success (caller does the `json.Unmarshal`).

**Java equivalent:** a `@Component` using `RestTemplate` or `WebClient` — the `get()` method is like a private `executeGet(path)` helper that handles auth header injection and error parsing. The caller methods (`GetMe`, `GetMetricsUsage`) are like service-layer methods that deserialize the JSON into typed objects (currently `map[string]any` / `Map<String, Object>` because the response schemas are unknown or dynamic).

---

## 5. Browser Cookie Reader (Legacy / Unused)

### `internal/browser/cookies.go`

This package reads the `nan_session` cookie from local browser databases. It supports two engines:

#### Firefox
- Reads `~/.mozilla/firefox/<profile>/cookies.sqlite`
- Queries the `moz_cookies` table for `nan_session` where `host LIKE '%nan.builders%'`
- No decryption needed — Firefox stores cookie values in plaintext in the DB.

#### Chromium / Chrome
- Reads `~/.config/chromium/Default/Cookies` or `~/.config/google-chrome/Default/Cookies`
- Queries the `cookies` table for `encrypted_value`
- **Decryption:** Chromium on Linux encrypts cookie values with AES-128-CBC:
  - Key derived via PBKDF2: `PBKDF2("peanuts", "saltysalt", 1, 16)`
  - IV: 16 space characters
  - Supports `v10` and `v11` prefix formats
  - Strips PKCS#7 padding after decryption

**Why it exists:** The original login flow auto-pollled browser cookies every 2 seconds to detect when the user completed Discord login. It was replaced by manual paste in the current version, but the package is kept for potential future use.

**Java equivalent:** a `@Service` using an embedded SQLite driver (like `org.xerial:sqlite-jdbc`) to query browser cookie DBs on disk. The decryption logic maps to `javax.crypto.Cipher` with `AES/CBC/PKCS5Padding`.

---

## 6. Commands & Endpoints

### Full command tree

```
nan                     # Root command
├── auth                # Authentication
│   ├── login           # Discord login (paste session token)
│   │   └── --token     # Skip browser, save token directly
│   └── logout          # Delete local session
├── me                  # Show current user profile (JSON)
└── metrics             # Usage metrics
    └── usage           # Show usage stats (JSON)
```

### Example output

```bash
$ nan me
{
  "id": "123456",
  "username": "devuser",
  "email": "dev@example.com"
}

$ nan metrics usage
{
  "requests_used": 42,
  "requests_limit": 1000
}
```

---

## 7. Dependencies

| Package | Role |
|---|---|
| `github.com/spf13/cobra` | CLI framework (arg parsing, command tree, help generation) |
| `charmbracelet/bubbletea` | TUI framework (installed but not yet used in code) |
| `modernc.org/sqlite` | Pure-Go SQLite (no CGO) — for reading browser cookie DBs |
| `golang.org/x/crypto` | PBKDF2 key derivation (for Chromium cookie decryption) |
| `dustin/go-humanize` | Human-readable numbers (installed, unused) |

---

## 8. Build & Run

```bash
# Build
go build -o nan .

# Run
./nan auth login
./nan me
./nan metrics usage
./nan auth logout
```

```bash
# Single-command run (no build needed)
go run . auth login
```

---

## 9. Key Design Decisions

1. **Cookie-based auth, not JWT.** The API expects `Cookie: nan_session=<token>` — no `Authorization: Bearer` header.
2. **Dynamic response types.** All API responses are `map[string]any` — no typed DTOs. The response schemas are treated as opaque JSON for now.
3. **No persistence layer beyond a single file.** Session state is one JSON file on disk. No database, no cache.
4. **Embedded SQLite.** `modernc.org/sqlite` is a pure-Go SQLite implementation — no native libraries or CGO required. This means the binary is fully static and cross-compiles easily.
5. **Manual login flow.** The cookie-polling approach was replaced with a manual paste step for simplicity and to avoid needing `database/sql` during login.
