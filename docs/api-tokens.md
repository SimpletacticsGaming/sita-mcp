# MCP-Tokens: Vertrag zwischen sita-mcp, sita-backend und sita-frontend

Dieses Dokument beschreibt, was sita-mcp vom Backend erwartet und was dafür in Backend und Frontend gebaut werden muss. Der MCP ist bereits auf diesen Vertrag umgestellt (`internal/sita`, `internal/auth`).

## Ziel

- Nutzer erzeugen auf simpletactics.de selbst **persönliche MCP-Tokens** und hinterlegen sie in ihrem MCP-Client statt ihres Passworts.
- Ein Token handelt **im Namen seines Nutzers**: Es gelten dessen Wiki-Rechte, aber **nur lesend**.
- Tokens lassen sich einzeln widerrufen und können ablaufen.

## Token

- **Format:** `sita_mcp_` gefolgt von 32 kryptografisch zufälligen Bytes in Base64url ohne Padding (43 Zeichen), z. B. `sita_mcp_Zx3…`.
  - Am Präfix erkennt der Auth-Filter Tokens sofort und kann sie von Session-UUIDs unterscheiden. Außerdem findet Secret Scanning (z. B. eine eigene GitHub-Regel) versehentlich eingecheckte Tokens.
- **Speicherung:** nur der **SHA-256-Hash** des Tokens, niemals der Klartext.
  - Bei 256 Bit Zufall ist ein langsamer Hash wie BCrypt unnötig. SHA-256 lässt sich direkt per Index nachschlagen.
- **Klartext** gibt das Backend genau einmal zurück, in der Antwort auf das Erstellen.

## Datenmodell (Vorschlag, Flyway-Migration in sita-backend)

```sql
CREATE TABLE mcp_token (
    id           SERIAL PRIMARY KEY,
    user_id      INTEGER      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         VARCHAR(100) NOT NULL,            -- vom Nutzer vergeben, z. B. "Laptop Claude Code"
    token_hash   CHAR(64)     NOT NULL UNIQUE,     -- hex(SHA-256(token))
    scopes       TEXT[]       NOT NULL DEFAULT '{wiki:read}',
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ,
    expires_at   TIMESTAMPTZ,                      -- NULL = läuft nie ab
    revoked_at   TIMESTAMPTZ                       -- gesetzt statt Löschen, für die Nachvollziehbarkeit
);
CREATE INDEX mcp_token_user_idx ON mcp_token (user_id);
```

`scopes` ist für später gedacht: Kommen Schreib-Tools dazu, bekommen neue Tokens `wiki:write`, ohne dass die Tabelle umgebaut werden muss.

## Authentifizierung im Backend

Jeder Request des MCP trägt:

```
Authorization: Bearer sita_mcp_…
```

Das Backend erweitert seine Request-Authentifizierung (heute `SessionAuthenticationFilter`, Header `authentication`):

1. Steht im `Authorization`-Header `Bearer sita_mcp_…`, wird der Hash berechnet und in `mcp_token` gesucht.
2. **Ungültig** (unbekannt, `revoked_at` gesetzt oder `expires_at` vorbei): **401**, nicht 403, damit der MCP „Token kaputt“ von „keine Rechte“ unterscheiden kann.
3. **Gültig:**
   - Der Principal ist der Benutzername des Besitzers, wie bei einer Session. Damit greift `WikiPermissionEvaluator` unverändert.
   - Die globalen Authorities werden auf die Rechte beschränkt, die zum Scope passen. Für `wiki:read` ist das `WIKI_READ`, keine Admin-, Blog- oder sonstigen Rechte.
   - Als Merkmal am Authentication-Objekt markieren, dass der Request per Token kam, z. B. eine eigene `McpTokenAuthentication`.
   - `last_used_at` aktualisieren, gedrosselt auf höchstens einmal pro Minute und Token.
4. **Erlaubte Endpunkte per Token**, alle anderen beantwortet das Backend mit **403**:

   | Methode | Pfad |
   |---|---|
   | GET | `/api/tokens/current` |
   | GET | `/api/wiki/types/{id}` |
   | GET | `/api/wiki/topics/{id}` |
   | GET | `/api/wiki/entries/{id}` |
   | GET | `/api/wiki/buttons/main-panel/{id}` |
   | GET | `/api/wiki/buttons/left-panel/{id}` |
   | GET | `/api/poll/get/{id}` |
   | GET | `/api/poll/closed/{id}` |

   Die Allowlist sorgt dafür, dass ein Token auch dann nichts verändern kann, wenn einzelne Endpunkte ihre Rechte zu großzügig prüfen. Ein Beispiel ist `submitPollVotes`, das nur `WIKI_READ` verlangt. Ein Token darf außerdem keine Tokens verwalten.

5. Bestehende Logins per Session bleiben unverändert.

## Endpunkt für den MCP

### `GET /api/tokens/current`

Mit dem Token selbst aufgerufen. Prüft es und liefert seine Eckdaten. Der MCP ruft das im HTTP-Modus auf, bevor er einen Client durchlässt.

```json
200 OK
{ "name": "Laptop Claude Code", "username": "mirko", "expiresAt": "2027-01-31T12:00:00Z" }
```

`expiresAt` ist `null`, wenn das Token nicht abläuft. Bei ungültigem Token: **401**.

### Statuscodes, die der MCP auswertet

| Status | Bedeutung für den MCP |
|---|---|
| 401 | Token ungültig, abgelaufen oder widerrufen; der Nutzer braucht ein neues |
| 403 | Keine Leserechte auf diesen Knoten (oder Endpunkt nicht per Token erlaubt) |
| 404 | Knoten existiert nicht |

## Verwaltung (für das Frontend)

Nur mit einer normalen **Session** (`authentication`-Header) nutzbar. Mit einem Token antworten diese Endpunkte **403**: Ein geleaktes Token darf sich nicht selbst verlängern oder weitere Tokens erzeugen. Jeder Nutzer sieht und verwaltet nur seine eigenen Tokens.

| Methode | Pfad | Body | Antwort |
|---|---|---|---|
| GET | `/api/tokens` | – | `200 [{ id, name, scopes, createdAt, lastUsedAt, expiresAt }]`, nur nicht widerrufene; nie der Token-Klartext |
| POST | `/api/tokens` | `{ "name": "Laptop", "expiresInDays": 90 }` (`expiresInDays` optional, `null` = kein Ablauf) | `201 { id, name, scopes, createdAt, expiresAt, token }`, `token` nur hier |
| DELETE | `/api/tokens/{id}` | – | `204`; setzt `revoked_at`. Fremde oder unbekannte id: `404` |

Regeln, die das Backend durchsetzen sollte:

- Höchstens 10 aktive Tokens pro Nutzer; darüber `409`.
- `name`: 1–100 Zeichen, Pflicht.
- Wird ein Nutzer gelöscht, verschwinden seine Tokens (`ON DELETE CASCADE`). Beim Zurücksetzen des Passworts durch einen Admin sollten auch seine Tokens widerrufen werden.
- Optional: ein neues Recht `MCP_TOKEN_CREATE`, falls nicht jeder Nutzer Tokens anlegen darf.

## Frontend

Im Profil ein Bereich **„MCP-Tokens“**:

- **Liste** der eigenen Tokens mit Name, erstellt am, zuletzt genutzt, läuft ab am, jeweils mit **Widerrufen**-Button und Rückfrage.
- **Neues Token:** Dialog mit Name und Ablauf (30 Tage / 90 Tage / 1 Jahr / nie, Standard 90 Tage).
- **Nach dem Erstellen:** Das Token einmalig anzeigen, mit Kopieren-Button und dem Hinweis, dass es nicht noch einmal angezeigt wird. Darunter fertige Snippets für Claude Code und Claude Desktop zum Kopieren (siehe README), mit dem Token schon eingesetzt.

## Verhalten des MCP

- **stdio:** Das Token kommt aus `SITA_TOKEN` und geht bei jeder Abfrage an das Backend.
- **HTTP:** Clients schicken `Authorization: Bearer <token>`. Der MCP prüft es per `GET /api/tokens/current` und merkt sich ein akzeptiertes Token für `MCP_TOKEN_CACHE_TTL` (Standard 1 Minute).
  - Die Wiki-Abfragen gehen trotzdem immer mit dem Token ans Backend. Ein widerrufenes Token liefert also sofort keine Daten mehr, nur die Vorprüfung kann bis zu einer Minute lang noch durchgehen.
  - Clients, die wiederholt ungültige Tokens schicken, sperrt der MCP pro IP (`MCP_AUTH_MAX_FAILURES`, `MCP_AUTH_BLOCK_WINDOW`).
- Der MCP loggt Tokens nie, auch nicht gekürzt. Für seine Caches nutzt er nur deren SHA-256.

## Später: OAuth

Remote-Connectors in Claude Desktop und claude.ai können keine eigenen Header setzen, sondern erwarten OAuth 2.1. Dann würde das Backend (oder ein vorgeschalteter Auth-Server) über einen OAuth-Flow Tokens nach demselben Modell ausstellen. `mcp_token` mit `scopes` und `expires_at` passt dazu.
