# sita-mcp

MCP-Server, der LLM-Clients wie Claude Code oder Claude Desktop **Lesezugriff auf das Simpletactics-Wiki** gibt.

Der Server spricht mit der öffentlichen API von **sita-backend** (`/api/wiki/...`). Er meldet sich mit einem **persönlichen MCP-Token** an, das jeder Nutzer auf simpletactics.de selbst erzeugt und jederzeit widerrufen kann, und bekommt dafür eine normale Session, wie das Frontend nach dem Login. Es gelten dieselben Wiki-Rechte (`WIKI_READ`) wie auf der Webseite: Jeder sieht nur, was er auch im Browser sehen darf. Sein Passwort muss niemand im MCP hinterlegen.

> **Stand:** Backend und Frontend unterstützen die MCP-Tokens noch nicht. Den Vertrag, den der MCP erwartet, beschreibt [docs/api-tokens.md](docs/api-tokens.md).

## Tools

| Tool | Zweck |
|---|---|
| `wiki_search` | Volltextsuche über Titel und Inhalte. Treffer mit Pfad, Ausschnitt und Link |
| `wiki_get_page` | Liest einen Knoten per id: Einträge als Markdown, Themen mit ihren Unterseiten, Umfragen mit Optionen und eigener Stimme |
| `wiki_list_children` | Direkte Unterseiten eines Themas (Startseite = `0`) |
| `wiki_get_tree` | Eingerückte Gliederung ab einem Thema (Standardtiefe 3, max. 6) |

Alle Tools sind read-only. Schreib-Tools sind für später geplant.

Das Backend hat keine Suche. Der Server durchläuft deshalb beim ersten `wiki_search` pro Token den Wiki-Baum und hält einen Index im Speicher, standardmäßig 10 Minuten lang (`WIKI_INDEX_TTL`).

## MCP-Token

1. Auf simpletactics.de im Profil ein MCP-Token erzeugen. Es wird nur einmal angezeigt und beginnt mit `sita_mcp_`.
2. Das Token im MCP-Client hinterlegen (Beispiele unten).
3. Wird es nicht mehr gebraucht oder ist es in falsche Hände geraten: im Profil widerrufen. Ab dann lehnt das Backend es sofort ab.

Das Token steht im Klartext in der Konfiguration deines MCP-Clients. Anders als dein Passwort gilt es nur für den MCP, läuft von selbst ab und lässt sich einzeln widerrufen, ohne dass du dein Passwort ändern musst. Behandle es trotzdem wie ein Passwort: Der MCP meldet sich damit wie du selbst an.

## Betriebsarten

Dasselbe Docker-Image kann beides:

- **`stdio` (Standard):** Claude startet den Container selbst (`docker run -i …`) und redet über stdin/stdout mit ihm. Das Token kommt aus `SITA_TOKEN`.
- **`http`:** Der Container läuft dauerhaft, z. B. im Swarm, und stellt Streamable HTTP unter `/mcp` bereit. Jeder Client schickt sein eigenes Token mit: `Authorization: Bearer <token>`.

  Ohne gültiges Token antwortet der Server mit `401`. Pro Token meldet er sich einmal beim Backend an (`POST /login/mcp`) und nutzt dann die Session, bis sie ungültig wird. Wird das Token widerrufen, löscht das Backend auch seine Sessions, und der nächste Aufruf scheitert. TLS muss der Reverse Proxy übernehmen.

  **Limiter:** Nach `MCP_AUTH_MAX_FAILURES` abgelehnten Tokens (Standard 5) innerhalb von `MCP_AUTH_BLOCK_WINDOW` (Standard 15 Minuten) antwortet der Server einer Client-IP mit `429` und `Retry-After`. Requests ganz ohne Token zählen nicht.

  Hinter einem Reverse Proxy muss `MCP_CLIENT_IP_HEADER` gesetzt sein, z. B. `X-Forwarded-For`. Sonst teilen sich alle Clients die IP des Proxys und blockieren sich gegenseitig. Den Header nur setzen, wenn der Proxy ihn auch selbst schreibt, sonst können Clients ihre IP fälschen.

### Lokal mit Claude Code (stdio, für Entwicklung; braucht Zugriff auf die Registry)

```bash
claude mcp add sita -e SITA_TOKEN=sita_mcp_… \
  -- docker run -i --rm -e SITA_TOKEN registry.simpletactics.de/sita-mcp:<version>
```

Nicht mit `--scope project` anlegen: Dann landet das Token in `.mcp.json` im Projektordner und schnell im Git-Repo.

### Lokal mit Claude Desktop (stdio, für Entwicklung)

`claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "sita": {
      "command": "docker",
      "args": ["run", "-i", "--rm", "-e", "SITA_TOKEN", "registry.simpletactics.de/sita-mcp:<version>"],
      "env": { "SITA_TOKEN": "sita_mcp_…" }
    }
  }
}
```

### Zentral auf simpletactics.de (HTTP, geplanter Normalfall)

Der MCP läuft im Swarm unter `https://simpletactics.de/mcp` (geplant, siehe [docs/umsetzung-backend-frontend.md](docs/umsetzung-backend-frontend.md#deployment-deployment-repo-und-reverse-proxy)). Docker brauchen die Nutzer dafür nicht.

**Claude Code:**

```bash
claude mcp add --transport http sita https://simpletactics.de/mcp --header "Authorization: Bearer sita_mcp_…"
```

**Claude Desktop:** Remote-Connectors können keine eigenen Header setzen. Als Umweg dient die Brücke [`mcp-remote`](https://www.npmjs.com/package/mcp-remote), die Node.js braucht. Diesen Weg habe ich noch nicht getestet.

```json
{
  "mcpServers": {
    "sita": {
      "command": "npx",
      "args": ["-y", "mcp-remote", "https://simpletactics.de/mcp", "--header", "Authorization:${AUTH_HEADER}"],
      "env": { "AUTH_HEADER": "Bearer sita_mcp_…" }
    }
  }
}
```

## Konfiguration

| Variable | Standard | Bedeutung |
|---|---|---|
| `MCP_TRANSPORT` | `stdio` | `stdio` oder `http` |
| `SITA_TOKEN` | – | MCP-Token für den stdio-Modus (Pflicht) |
| `MCP_HTTP_ADDR` | `:8080` | Listen-Adresse im HTTP-Modus |
| `MCP_AUTH_MAX_FAILURES` | `5` | Abgelehnte Tokens pro IP bis zur Sperre (HTTP), `0` schaltet den Limiter ab |
| `MCP_AUTH_BLOCK_WINDOW` | `15m` | Zeitfenster für die Fehlversuche und Dauer der Sperre |
| `MCP_CLIENT_IP_HEADER` | – | Header mit der Client-IP vom Reverse Proxy, z. B. `X-Forwarded-For` (letzter Eintrag zählt) |
| `SITA_BACKEND_URL` | `https://simpletactics.de/backend` | Basis-URL von sita-backend; im Swarm z. B. die interne Service-URL |
| `SITA_WIKI_PUBLIC_URL` | `https://simpletactics.de/wiki/?ar=` | Präfix für öffentliche Links, die id wird angehängt |
| `WIKI_INDEX_TTL` | `10m` | Wie lange ein Suchindex wiederverwendet wird |
| `WIKI_INDEX_MAX_NODES` | `2000` | Obergrenze für die Knoten pro Durchlauf |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`; Logs gehen als JSON nach stderr |

## Entwicklung

```bash
go test -race ./...
golangci-lint run
docker compose up --build   # HTTP-Modus auf http://localhost:8080/mcp
```

Ausprobieren mit dem MCP Inspector:

```bash
npx @modelcontextprotocol/inspector docker run -i --rm -e SITA_TOKEN sita-mcp:local
```

Aufbau:

- `cmd/sita-mcp`: Einstiegspunkt, wählt stdio oder HTTP
- `internal/sita`: Client für sita-backend (Login per MCP-Token, Session-Cache, Wiki-Endpunkte), dazu `sitatest`, ein Fake-Backend für Tests
- `internal/wiki`: Seiten, Breadcrumbs, Baum, Suchindex, HTML→Markdown
- `internal/auth`: Token aus dem `Authorization`-Header prüfen, Limiter (HTTP-Modus)
- `internal/mcpserver`: Tool-Definitionen

## Versionierung und Releases

Die Versionen berechnet [`SimpletacticsGaming/version-action`](https://github.com/SimpletacticsGaming/version-action) aus den **Conventional Commits** (`feat:`, `fix:`, `feat!:` …). Commit-Nachrichten und PR-Titel werden geprüft und müssen klein beginnen.

| Workflow | Auslöser | Ergebnis |
|---|---|---|
| `pipeline.yml` | jeder Push | Tests und Lint, dann Dev-Image `sita-mcp:X.Y.Z-g<sha>` |
| `prepare-release.yml` | manuell auf `main` | Branch `release/X.Y` |
| `cut-rc.yml` | manuell auf `release/*` | Image `sita-mcp:X.Y.Z-rc.N`, Tag `vX.Y.Z-rc.N`, GitHub-Prerelease |
| `promote.yml` | manuell mit `rc_tag` | Taggt das RC-Image ohne Neubau als `X.Y.Z` um, legt Tag `vX.Y.Z` und GitHub-Release an |

Benötigte Secrets: `DOCKER_REPOSITORY`, `DOCKER_REGISTRY_USER`, `DOCKER_REGISTRY_PW`, `ACTION_REPO_SECRET`.
