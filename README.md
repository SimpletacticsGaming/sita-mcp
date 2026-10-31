# sita-mcp

MCP-Server, der LLM-Clients wie Claude Code oder Claude Desktop **Lesezugriff auf das Simpletactics-Wiki** gibt.

Der Server spricht mit der öffentlichen API von **sita-backend** (`/api/wiki/...`) und meldet sich dort **mit dem sita-Account des jeweiligen Nutzers** an. Es gelten also dieselben Wiki-Rechte (`WIKI_READ`) wie auf der Webseite: Jeder sieht nur, was er auch im Browser sehen darf.

## Tools

| Tool | Zweck |
|---|---|
| `wiki_search` | Volltextsuche über Titel und Inhalte. Treffer mit Pfad, Ausschnitt und Link |
| `wiki_get_page` | Liest einen Knoten per id: Einträge als Markdown, Themen mit ihren Unterseiten, Umfragen mit Optionen und eigener Stimme |
| `wiki_list_children` | Direkte Unterseiten eines Themas (Startseite = `0`) |
| `wiki_get_tree` | Eingerückte Gliederung ab einem Thema (Standardtiefe 3, max. 6) |

Alle Tools sind read-only. Schreib-Tools sind für später geplant.

Das Backend hat keine Suche. Der Server durchläuft deshalb beim ersten `wiki_search` pro Nutzer den Wiki-Baum und hält einen Index im Speicher, standardmäßig 10 Minuten lang (`WIKI_INDEX_TTL`).

## Betriebsarten

Dasselbe Docker-Image kann beides:

- **`stdio` (Standard):** Claude startet den Container selbst (`docker run -i …`) und redet über stdin/stdout mit ihm. Die Zugangsdaten kommen aus Umgebungsvariablen.
- **`http`:** Der Container läuft dauerhaft, z. B. im Swarm, und stellt Streamable HTTP unter `/mcp` bereit. Jeder Client schickt seine eigenen sita-Zugangsdaten im `Authorization`-Header mit:
  - `Authorization: Basic base64(benutzer:passwort)`, oder
  - `Authorization: Bearer <sita-session-id>`.

  Ohne gültige Zugangsdaten antwortet der Server mit `401`. TLS muss der Reverse Proxy übernehmen, weil sonst Passwörter im Klartext übertragen werden.

### Lokal mit Claude Code (stdio)

```bash
claude mcp add sita \
  -e SITA_USERNAME=dein-name -e SITA_PASSWORD=dein-passwort \
  -- docker run -i --rm -e SITA_USERNAME -e SITA_PASSWORD registry.simpletactics.de/sita-mcp:<version>
```

### Lokal mit Claude Desktop (stdio)

`claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "sita": {
      "command": "docker",
      "args": ["run", "-i", "--rm", "-e", "SITA_USERNAME", "-e", "SITA_PASSWORD",
               "registry.simpletactics.de/sita-mcp:<version>"],
      "env": { "SITA_USERNAME": "dein-name", "SITA_PASSWORD": "dein-passwort" }
    }
  }
}
```

### Remote (HTTP)

```bash
claude mcp add --transport http sita https://<host>/mcp \
  --header "Authorization: Basic $(printf 'dein-name:dein-passwort' | base64)"
```

## Konfiguration

| Variable | Standard | Bedeutung |
|---|---|---|
| `MCP_TRANSPORT` | `stdio` | `stdio` oder `http` |
| `MCP_HTTP_ADDR` | `:8080` | Listen-Adresse im HTTP-Modus |
| `SITA_BACKEND_URL` | `https://simpletactics.de/backend` | Basis-URL von sita-backend; im Swarm z. B. die interne Service-URL |
| `SITA_WIKI_PUBLIC_URL` | `https://simpletactics.de/wiki/?ar=` | Präfix für öffentliche Links, die id wird angehängt |
| `SITA_AUTH_HEADER` | `authentication` | Header, in dem das Backend die Session erwartet |
| `SITA_USERNAME`, `SITA_PASSWORD` | – | Login für den stdio-Modus |
| `SITA_SESSION` | – | Alternativ zum Login: eine bestehende Session-id (stdio) |
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
npx @modelcontextprotocol/inspector docker run -i --rm -e SITA_USERNAME -e SITA_PASSWORD sita-mcp:local
```

Aufbau:

- `cmd/sita-mcp`: Einstiegspunkt, wählt stdio oder HTTP
- `internal/sita`: Client für sita-backend (Login, Session-Cache, Wiki-Endpunkte), dazu `sitatest`, ein Fake-Backend für Tests
- `internal/wiki`: Seiten, Breadcrumbs, Baum, Suchindex, HTML→Markdown
- `internal/auth`: `Authorization`-Header auswerten und prüfen (HTTP-Modus)
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
