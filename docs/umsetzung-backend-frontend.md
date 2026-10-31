# Umsetzungsplan: MCP-Tokens in sita-backend und sita-frontend

Plan zur Umsetzung von [api-tokens.md](api-tokens.md). Er beruht auf einer Analyse von `sita-backend@main` (Spring Boot 3.4, Kotlin, offene PRs #190 Spring Boot 4 und #192 wiki-service) und `sita-frontend@main` (Angular 20, Material, Standalone-Komponenten). Umgesetzt ist davon noch nichts.

**Grundsatz:** im Backend so wenig wie möglich ändern. Der MCP tauscht sein Token gegen eine normale Session und verhält sich danach wie das Frontend. Session-Filter, `SecurityConfig` (bis auf eine Zeile), Rechteprüfung und alle Wiki-Endpunkte bleiben unverändert.

## Kurzfassung

| Teil | Umfang | Abhängigkeit |
|---|---|---|
| Backend | 1 Migration, Token-Port/-Adapter/-Service, Login-Endpunkt, Verwaltungs-Controller, kleine Erweiterung der Session-Klassen | am besten nach #192 |
| Frontend | Profil-Unterseite „MCP-Tokens“, Service, Menü-Button | Backend-Endpunkte |
| sita-mcp | fertig umgestellt und auf GitHub; noch das erste Release | Backend für einen echten Test |
| Deployment | neuer Service `mcp` im Stack `sita` (deployment-repo) und eine Route `/mcp` im Reverse Proxy | erstes Release von sita-mcp |

## Entscheidungen

| # | Frage | Entscheidung bzw. Vorschlag |
|---|---|---|
| 1 | Wie meldet sich der MCP an? | **Entschieden:** Token gegen Session tauschen (`POST /login/mcp`), danach wie das Frontend |
| 2 | Absicherungen | **Entschieden:** Widerruf und Ablauf beenden Sessions; Token-Sessions dürfen keine Tokens verwalten |
| 3 | Wer darf Tokens anlegen? | **Entschieden:** jeder eingeloggte Nutzer, kein eigenes Recht |
| 4 | Ablaufzeiten | **Entschieden:** immer 1 Jahr, nicht wählbar |
| 5 | Widerruf bei Passwort-Reset | **Entschieden:** Setzt ein Admin das Passwort zurück, werden alle MCP-Tokens des Nutzers gelöscht (und damit ihre Sessions). Ändert der Nutzer es selbst, bleiben sie bestehen |
| 6 | Basis-Branch, Migrationsnummer | Keine Entscheidung nötig, nur beim Umsetzen beachten: auf `main`, nächste freie Flyway-Nummer. Nach #192 ist das `V017__create_mcp_tokens.sql`, sonst `V016` (dann muss #192 umnummerieren) |
| 7 | Wie kommen Nutzer an das Image? | **Entschieden:** gar nicht. Die Registry ist nicht öffentlich, der MCP läuft zentral (siehe 8). Ein `latest`-Tag ist nicht nötig |
| 8 | Wo läuft der MCP? | **Entschieden:** zentral per HTTP im Swarm. Nutzer tragen nur URL und Token ein und brauchen kein Docker |

## Backend (sita-backend)

### Neue Dateien

Paketstruktur wie bisher. Die ArchUnit-Regeln für Controller, Services und Ports bleiben eingehalten.

```
src/main/resources/db/migration/V017__create_mcp_tokens.sql   Tabelle + sessions.mcp_token_id (siehe api-tokens.md)
src/main/kotlin/de/simpletactics/
├── adapter/persistence/entity/McpTokenEntity.kt
├── adapter/persistence/McpTokenAdapter.kt          JdbcTemplate, analog SessionAdapter
├── services/port/McpTokenPort.kt
├── services/McpTokenService.kt
├── model/McpToken.kt
├── model/McpLogin.kt                               data class McpLogin(val token: String)
├── adapter/controller/McpTokenController.kt        /api/mcp-tokens
└── adapter/dto/McpTokenDtos.kt                     McpTokenDto, CreateMcpTokenDto, CreatedMcpTokenDto
```

### Geänderte Dateien

| Datei | Änderung |
|---|---|
| `adapter/controller/LoginController.kt` | neuer Endpunkt `POST /login/mcp`, Antwort `LoginDto` wie `/login` |
| `security/SecurityConfig.kt` | `"/login/mcp"` in die `permitAll`-Liste |
| `adapter/persistence/entity/SessionEntity.kt`, `model/Session.kt` | `mcpTokenId: Int?`; im Entity zusätzlich `mcpTokenExpiresAt: Instant?` |
| `adapter/persistence/SessionAdapter.kt` | `save` schreibt `mcp_token_id`; `findById` per `LEFT JOIN mcp_tokens t ON t.id = s.mcp_token_id` und liest `t.expires_at` mit |
| `services/SessionService.kt` | `generateAndSaveSessionId(username, mcpTokenId: Int? = null)`; durch den Default-Wert ändert sich an den bestehenden Aufrufen nichts. `isExpired` prüft zusätzlich `mcpTokenExpiresAt` |
| `services/UserService.kt` | in `changePassword(…, reset = true)` alle Tokens des Nutzers löschen |

### Login-Endpunkt

```kotlin
@PostMapping("/login/mcp")
fun loginWithMcpToken(@RequestBody login: McpLogin): ResponseEntity<LoginDto> {
    val token = mcpTokenService.findValidToken(login.token)       // Hash, Ablauf, Besitzer aktiv
        ?: return ResponseEntity.status(HttpStatus.FORBIDDEN).build()
    val session = sessionService.generateAndSaveSessionId(token.username, mcpTokenId = token.id)
    mcpTokenService.markUsed(token.id)
    return ResponseEntity.ok(LoginDto(true, session.sessionId, false))
}
```

### Service

```kotlin
@Service
class McpTokenService(private val port: McpTokenPort, private val userService: UserService) {
    fun create(username: String, name: String): CreatedMcpToken                      // Limit 10, Ablauf: jetzt + 1 Jahr
    fun list(username: String): List<McpToken>
    fun delete(username: String, id: Int): Boolean                                   // false → 404
    fun deleteAllForUser(userId: Int)
    fun findValidToken(rawToken: String): McpToken?
    fun markUsed(id: Int)
}
```

- **Erzeugen:** `SecureRandom` liefert 32 Bytes, daraus `"sita_mcp_" + Base64.getUrlEncoder().withoutPadding().encodeToString(bytes)`.
- **Hash:** `MessageDigest.getInstance("SHA-256")`, als Hex-String. Nachgeschlagen wird per `WHERE token_hash = ?`.
- **`findValidToken`** liefert `null`, wenn das Präfix fehlt, der Hash unbekannt ist, `expires_at` vorbei ist oder `userService.isUserActive(username)` false liefert.

### Verwaltungs-Controller

```kotlin
@RestController
@RequestMapping("/api/mcp-tokens")
class McpTokenController(private val tokenService: McpTokenService, private val sessionService: SessionService) {
    // GET, POST, DELETE /{id} wie in api-tokens.md

    // Token-Sessions dürfen keine Tokens verwalten.
    private fun currentSessionOrForbidden(): Session? {
        val sessionId = SecurityContextHolder.getContext().authentication?.credentials as? UUID ?: return null
        return sessionService.getSessionBySessionId(sessionId)?.takeIf { it.mcpTokenId == null }
    }
}
```

`SessionController` liest die Session-id auf dieselbe Weise aus.

### Tests

Kotest mit `FunSpecIT` und MockMvc, wie `LoginControllerTestIT`. Testdaten kommen per `SQLScripts`.

- **`/login/mcp`:** gültiges Token liefert 200 und eine Session mit `mcp_token_id`. Unbekanntes, abgelaufenes Token und inaktiver Nutzer liefern 403 ohne Session.
- **Widerruf:** `DELETE /api/mcp-tokens/{id}` löscht auch die Sessions des Tokens. Der nächste Request mit dieser Session liefert 403, `/loggedIn` liefert false.
- **Ablauf:** Eine Session, deren Token abgelaufen ist, gilt als abgelaufen, auch wenn ihr eigenes `expires_at` in der Zukunft liegt.
- **Verwaltung mit Token-Session:** `GET`, `POST` und `DELETE /api/mcp-tokens` liefern 403.
- **Verwaltung mit Login-Session:** anlegen (Klartext nur in der Antwort, in der DB nur der Hash), auflisten, löschen. Das Limit von 10 ergibt 409, ein ungültiger Name ergibt 400, eine fremde id ergibt 404.
- **Admin-Reset** löscht die Tokens des Nutzers.
- **Bestehende Tests** für Login und Session bleiben grün.

## Frontend (sita-frontend)

### Dateien

```
src/app/modules/enum/profile-type.enum.ts                    + MCP_TOKENS = 'mcp-tokens'
src/app/modules/pages/mcp-token.module.ts                    McpToken, CreateMcpToken
src/app/modules/pages/response/created-mcp-token-response.module.ts
src/app/services/mcpTokenService.ts                          list / create / delete, Stil wie passwordService
src/app/pages/profile/profile-routing.module.ts              + { path: 'mcp-tokens', component: McpTokensComponent }
src/app/pages/profile/mcp-tokens/mcp-tokens.component.{ts,html,scss,spec.ts}
src/app/elements/menuliste/menulist-right-main/menu-list-profile/menu-list-profile.component.html
                                                             + Button „MCP-Tokens“ im Fall 'none'
```

### Seite „MCP-Tokens“

- **Erklärtext** in zwei Sätzen: wofür das Token ist, dass man es jederzeit widerrufen kann und dass man es wie ein Passwort behandeln soll.
- **Liste** (Material-Tabelle): Name, erstellt, zuletzt genutzt („nie“), läuft ab. Je Zeile ein Button „Widerrufen“, der das vorhandene `DeleteModalComponent` öffnet, z. B. mit dem Text „Token ‚Laptop‘ widerrufen? MCP-Clients mit diesem Token verlieren sofort den Zugriff.“
- **Neues Token:** nur ein Name (Pflicht, max. 100 Zeichen). Der Hinweis „Läuft nach einem Jahr ab“ steht daneben. Ist das Limit erreicht, wird der Button deaktiviert und erklärt warum.
- **Nach dem Erstellen:** Ein Hinweiskasten zeigt das Token einmalig an, mit Kopieren-Button (`@angular/cdk/clipboard`). Dazu die Warnung „Wird nur jetzt angezeigt“ und darunter Snippets mit eingesetztem Token zum Kopieren:
  - Claude Code: `claude mcp add --transport http sita https://simpletactics.de/mcp --header "Authorization: Bearer <token>"`
  - Claude Desktop und claude.ai (unter *Einstellungen → Connectors → Eigenen Connector hinzufügen*): `https://simpletactics.de/mcp/<token>`. Dazu der Hinweis, dass diese URL selbst geheim ist.
- **Datenschutz:** Das Token bleibt nur im Komponenten-State. Es landet nicht im Router-State, nicht im `localStorage` und nicht in Logs.
- **Fehler:** Der globale `errorInterceptor` springt bei jedem 403 auf die Zugriff-verweigert-Seite. Die Fehler dieser Seite (400, 409) zeigt die Seite selbst inline an.

### Tests

`mcp-tokens.component.spec.ts` mit `HttpTestingController`:

- Die Liste wird geladen.
- Erstellen zeigt das Token einmal an, ein neues Laden nicht mehr.
- Widerrufen ruft `DELETE` auf und entfernt die Zeile.
- Ein 409 zeigt eine Meldung und keine Weiterleitung.

## sita-mcp

- Der Client ist bereits umgestellt: Login per `/login/mcp`, Session-Cache, nach `403` eine Prüfung per `/loggedIn` und bei Bedarf ein neuer Login.
- Repo auf GitHub anlegen, öffentlich, und pushen. Prüfen, ob die Org-Secrets für öffentliche Repos freigegeben sind.
- Erstes Release über `prepare-release`, `cut-rc` und `promote`.

## Reihenfolge

1. #192 mergen (wiki-service), damit die Migrationsnummer feststeht.
2. **Backend-PR** (Feature-Branch, Conventional Commits wie in den neuen Workflows) → Review → Deploy.
3. **Frontend-PR** → Review → Deploy.
4. **sita-mcp:** erstes Release.
5. **Deployment:** Service im Stack `sita` und Route im Reverse Proxy (siehe unten).
6. **Ende-zu-Ende-Test:** Token im Profil erzeugen, mit der URL in Claude Code eintragen, `wiki_search` und `wiki_get_page` aufrufen, Token widerrufen. Danach muss der nächste Aufruf mit „Token ungültig“ scheitern.
7. Später: Schreib-Tools, OAuth für Remote-Connectors (dann ohne Token in der URL).

## Deployment (deployment-repo und Reverse Proxy)

Neuer Service im bestehenden Stack `sita/prod/docker-compose.yaml` (und analog `sita/dev`). Im selben Stack erreicht er das Backend direkt unter dem Service-Namen, ohne Umweg über den Proxy:

```yaml
  mcp:
    image: registry.simpletactics.de/sita-mcp:<version>
    deploy:
      resources:
        limits:
          cpus: '0.25'
          memory: 64M
        reservations:
          cpus: '0.10'
          memory: 32M
      replicas: 1          # Limiter und Session-Cache leben im Speicher der Instanz
      update_config:
        parallelism: 1
        order: start-first
      restart_policy:
        condition: on-failure
    environment:
      - TZ=Europe/Berlin
      - MCP_TRANSPORT=http
      - SITA_BACKEND_URL=http://backend:8080
      - MCP_CLIENT_IP_HEADER=X-Forwarded-For
    healthcheck:
      test: ["CMD", "/sita-mcp", "healthcheck"]
      interval: 30s
      timeout: 5s
      retries: 3
    networks:
      - main
```

Secrets braucht der Service keine: Die Tokens bringen die Nutzer mit.

**Reverse Proxy:** Die Konfiguration liegt in keinem Repo. Nötig ist:

- Eine Route `https://simpletactics.de/mcp` **und** `/mcp/…` auf den Service `mcp`, Port 8080. Anders als bei `/backend/` darf das Präfix nicht abgeschnitten werden.
- **Kein Access-Log für `/mcp/…`:** Bei Claude Desktop und claude.ai steht das Token in der URL. Bei nginx reicht ein eigener `location /mcp/ { access_log off; … }`. Alternativ wird der Pfad maskiert geloggt. Der MCP selbst loggt keine URLs und entfernt das Token aus der URL, bevor er die Anfrage verarbeitet.
- `X-Forwarded-For` setzen, bei nginx: `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;`. Der MCP wertet den letzten Eintrag aus, also den, den der Proxy selbst anhängt. Davorgesetzte gefälschte Einträge bleiben wirkungslos.
- Antworten nicht puffern, weil Streamable HTTP Server-Sent Events nutzen kann. Bei nginx: `proxy_buffering off;` und ein großzügiges `proxy_read_timeout`.
- Nur über HTTPS erreichbar machen, weil die Tokens im Header mitgehen.

**Hinweis zum Limiter:** Remote-Connectors in Claude Desktop und claude.ai verbinden sich von den Servern von Anthropic aus, nicht vom Rechner des Nutzers. Viele Nutzer teilen sich dann wenige IPs. Wer dort wiederholt falsche Tokens benutzt, kann für 15 Minuten auch andere claude.ai-Nutzer sperren. Für den Start ist das vertretbar, Tokens zu erraten ist ohnehin aussichtslos. Wird es zum Problem, `MCP_AUTH_MAX_FAILURES` hochsetzen.
