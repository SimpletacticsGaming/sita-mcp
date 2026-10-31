# MCP-Tokens: Vertrag zwischen sita-mcp, sita-backend und sita-frontend

Dieses Dokument beschreibt, was sita-mcp vom Backend erwartet. Der MCP ist bereits auf diesen Vertrag umgestellt (`internal/sita`). Wie Backend und Frontend ihn umsetzen, steht in [umsetzung-backend-frontend.md](umsetzung-backend-frontend.md).

## Idee

Der MCP meldet sich **wie das Frontend** an, nur mit einem MCP-Token statt Benutzername und Passwort:

1. Der Nutzer erzeugt im Profil auf simpletactics.de ein MCP-Token und trägt es in seinem MCP-Client ein.
2. Der MCP tauscht das Token per `POST /login/mcp` gegen eine **normale Session**.
3. Ab dann schickt er `authentication: <sessionId>` mit, genau wie das Frontend. Session-Filter, Rechteprüfung und Endpunkte bleiben unverändert.

So muss im Backend möglichst wenig angepasst werden. Eine Token-Session hat dieselben Rechte wie eine Login-Session des Nutzers. Der MCP nutzt davon nur lesende Endpunkte.

## Token

- **Format:** `sita_mcp_` gefolgt von 32 kryptografisch zufälligen Bytes in Base64url ohne Padding (43 Zeichen). Am Präfix sind Tokens in Configs, Logs und beim Secret Scanning sofort erkennbar.
- **Speicherung:** nur der **SHA-256-Hash** (hex), nie der Klartext. Bei 256 Bit Zufall ist ein langsamer Hash wie BCrypt unnötig, und SHA-256 lässt sich direkt per Index nachschlagen.
- **Klartext** gibt das Backend genau einmal zurück, beim Erstellen.
- **Ablauf:** immer 1 Jahr nach dem Erstellen. Tokens ohne Ablauf gibt es nicht, damit vergessene Tokens von selbst auslaufen.
- **Widerruf:** Das Token wird gelöscht.

## Datenmodell

Neue Tabelle im Stil von `V008__create_sessions.sql`, dazu eine Spalte in `sessions`:

```sql
CREATE TABLE IF NOT EXISTS mcp_tokens
(
    id           serial PRIMARY KEY,
    user_id      integer                NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
    name         character varying(100) NOT NULL,        -- vom Nutzer vergeben, z. B. "Laptop Claude Code"
    token_hash   character(64)          NOT NULL UNIQUE, -- hex(SHA-256(token))
    created_at   timestamp              NOT NULL,
    last_used_at timestamp,
    expires_at   timestamp              NOT NULL
);

CREATE INDEX IF NOT EXISTS mcp_tokens_user_id_idx ON mcp_tokens USING btree (user_id);

-- Sessions, die per MCP-Token entstanden sind. Löschen des Tokens löscht sie mit.
ALTER TABLE sessions
    ADD COLUMN IF NOT EXISTS mcp_token_id integer REFERENCES mcp_tokens (id) ON DELETE CASCADE;
```

Das Datenmodell sorgt für drei Absicherungen:

| Absicherung | Umsetzung |
|---|---|
| **Widerruf beendet Sessions** | `DELETE FROM mcp_tokens WHERE id = ?`. `ON DELETE CASCADE` löscht alle Sessions des Tokens mit, der MCP ist sofort draußen. |
| **Ablauf beendet Sessions** | `SessionService.isExpired` prüft zusätzlich das Ablaufdatum des Tokens. `SessionAdapter.findById` holt es per `LEFT JOIN mcp_tokens` mit. Weil Sessions sich bei jeder Nutzung verlängern, würden sie sonst ewig leben. |
| **Token-Sessions dürfen keine Tokens verwalten** | Die Verwaltungs-Endpunkte antworten `403`, wenn die aktuelle Session ein `mcp_token_id` hat. Ein erbeutetes Token kann sich so keine Ersatz-Tokens anlegen. |

## Login: `POST /login/mcp`

Öffentlich wie `/login` (`permitAll`).

```json
POST /login/mcp
{ "token": "sita_mcp_…" }
```

| Fall | Antwort |
|---|---|
| Token gültig: Hash bekannt, `expires_at` in der Zukunft, Besitzer `active` | `200` mit derselben Antwort wie `/login`: `{ "successful": true, "sessionId": "<uuid>", "setPassword": false }`. Die neue Session trägt `mcp_token_id`, und `last_used_at` des Tokens wird gesetzt. |
| Token unbekannt, abgelaufen oder Besitzer inaktiv | `403` ohne Body, wie `/login` bei falschem Passwort |

## Was der MCP danach nutzt

Alles bestehende Endpunkte mit `authentication: <sessionId>`:

| Methode | Pfad | Zweck |
|---|---|---|
| GET | `/loggedIn` | Prüft, ob die Session noch gilt (siehe unten) |
| GET | `/api/wiki/types/{id}` | Typ eines Knotens |
| GET | `/api/wiki/topics/{id}` | Thema |
| GET | `/api/wiki/entries/{id}` | Eintrag (HTML) |
| GET | `/api/wiki/buttons/main-panel/{id}` | Kinder eines Themas |
| GET | `/api/wiki/buttons/left-panel/{id}` | Eltern-Thema und Geschwister |
| GET | `/api/poll/get/{id}` | Umfrage |
| GET | `/api/poll/closed/{id}` | Ist die Umfrage geschlossen? |

**403 ist zweideutig:** Bei einer ungültigen oder abgelaufenen Session antwortet das Backend mit `403`, genau wie bei fehlenden Rechten. Spring Security behandelt anonyme Requests so. Der MCP fragt deshalb nach jedem `403` per `GET /loggedIn`, ob die Session noch gilt:

- Gilt sie nicht mehr, meldet er sich per `/login/mcp` neu an und wiederholt die Anfrage einmal.
- Scheitert der Login (Token widerrufen oder abgelaufen), meldet er dem Nutzer, dass er ein neues Token braucht.

Dafür muss im Backend nichts geändert werden.

## Verwaltung (für das Frontend)

Unter `/api/**`, also nur mit Session nutzbar. Mit einer Token-Session antworten alle drei Endpunkte `403`. Jeder Nutzer sieht und verwaltet nur seine eigenen Tokens.

| Methode | Pfad | Body | Antwort |
|---|---|---|---|
| GET | `/api/mcp-tokens` | – | `200 [{ id, name, createdAt, lastUsedAt, expiresAt }]`, nie der Klartext |
| POST | `/api/mcp-tokens` | `{ "name": "Laptop" }` | `201 { id, name, createdAt, expiresAt, token }`, `token` nur hier, `expiresAt` = jetzt + 1 Jahr. `400` bei leerem oder zu langem Namen (max. 100). `409` bei mehr als 10 Tokens. |
| DELETE | `/api/mcp-tokens/{id}` | – | `204`; löscht Token und Sessions. `404` bei fremder oder unbekannter id |

Weitere Regeln:

- **Nutzer löschen:** Seine Tokens verschwinden mit (`ON DELETE CASCADE`), und damit auch die Token-Sessions.
- **Nutzer deaktivieren:** Neue Logins per Token scheitern. Bestehende Sessions laufen wie heute bei Passwort-Logins weiter.
- **Admin setzt das Passwort zurück:** Alle Tokens des Nutzers werden gelöscht. Ändert der Nutzer sein Passwort selbst, bleiben sie bestehen.

## Verhalten des MCP

- **stdio:** Das Token kommt aus `SITA_TOKEN`. Beim ersten Aufruf meldet sich der MCP an und nutzt die Session, bis sie ungültig wird.
- **HTTP:** Clients schicken das Token als `Authorization: Bearer <token>` an `/mcp` (Claude Code) oder in der URL `/mcp/<token>` (Claude Desktop und claude.ai, die keine eigenen Header setzen können).
  - Der MCP hält eine Session pro Token. Der erste Request löst den Login aus, alle weiteren nutzen die Session.
  - Clients, die wiederholt ungültige Tokens schicken, sperrt er pro IP (`MCP_AUTH_MAX_FAILURES`, `MCP_AUTH_BLOCK_WINDOW`).
- **Tokens im Log:** Der MCP loggt Tokens und Session-ids nie. Für seine Caches nutzt er nur den SHA-256 des Tokens.

## Später: OAuth

Remote-Connectors in Claude Desktop und claude.ai können keine eigenen Header setzen, sondern erwarten OAuth 2.1. Ein OAuth-Flow könnte dieselben `mcp_tokens` ausstellen. Der Tausch gegen eine Session bliebe gleich.
