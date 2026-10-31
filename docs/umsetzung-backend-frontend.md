# Umsetzungsplan: MCP-Tokens in sita-backend und sita-frontend

Plan zur Umsetzung von [api-tokens.md](api-tokens.md). Er beruht auf einer Analyse von `sita-backend@main` (Spring Boot 3.4, Kotlin, offene PRs #190 Spring Boot 4 und #192 wiki-service) und `sita-frontend@main` (Angular 20, Material, Standalone-Komponenten). Umgesetzt ist davon noch nichts.

**Grundsatz:** im Backend so wenig wie möglich ändern. Der MCP tauscht sein Token gegen eine normale Session und verhält sich danach wie das Frontend. Session-Filter, `SecurityConfig` (bis auf eine Zeile), Rechteprüfung und alle Wiki-Endpunkte bleiben unverändert.

## Kurzfassung

| Teil | Umfang | Abhängigkeit |
|---|---|---|
| Backend | 1 Migration, Token-Port/-Adapter/-Service, Login-Endpunkt, Verwaltungs-Controller, kleine Erweiterung der Session-Klassen | am besten nach #192 |
| Frontend | Profil-Unterseite „MCP-Tokens“, Service, Menü-Button | Backend-Endpunkte |
| sita-mcp | fertig umgestellt; noch `latest`-Tag, erstes Release, Repo veröffentlichen | Backend für einen echten Test |
| Deployment | zunächst nichts: der MCP läuft beim Nutzer per stdio | – |

## Entscheidungen

| # | Frage | Entscheidung bzw. Vorschlag |
|---|---|---|
| 1 | Wie meldet sich der MCP an? | **Entschieden:** Token gegen Session tauschen (`POST /login/mcp`), danach wie das Frontend |
| 2 | Absicherungen | **Entschieden:** Widerruf und Ablauf beenden Sessions; Token-Sessions dürfen keine Tokens verwalten |
| 3 | Wer darf Tokens anlegen? | **Entschieden:** jeder eingeloggte Nutzer, kein eigenes Recht |
| 4 | Ablaufzeiten | **Entschieden:** immer 1 Jahr, nicht wählbar |
| 5 | Widerruf bei Passwort-Reset | **Entschieden:** Setzt ein Admin das Passwort zurück, werden alle MCP-Tokens des Nutzers gelöscht (und damit ihre Sessions). Ändert der Nutzer es selbst, bleiben sie bestehen |
| 6 | Basis-Branch, Migrationsnummer | Vorschlag: auf `main` nach dem Merge von #192, als `V017__create_mcp_tokens.sql` |
| 7 | Wie kommen Nutzer an das Image? | Vorschlag: `promote.yml` taggt zusätzlich `latest`. Klären, ob `registry.simpletactics.de` ohne Login lesbar ist, sonst zusätzlich öffentlich auf GHCR veröffentlichen |
| 8 | HTTP-Modus deployen? | Vorschlag: erstmal nicht, nur stdio |

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
  - Claude Code: `claude mcp add sita -e SITA_TOKEN=<token> -- docker run -i --rm -e SITA_TOKEN registry.simpletactics.de/sita-mcp:latest`
  - Claude Desktop: der JSON-Block aus dem README.
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
- `promote.yml`: zusätzlich `crane tag "$image:$rc_version" latest`.
- Repo auf GitHub anlegen, öffentlich, und pushen. Prüfen, ob die Org-Secrets für öffentliche Repos freigegeben sind.
- Erstes Release über `prepare-release`, `cut-rc` und `promote`.

## Reihenfolge

1. #192 mergen (wiki-service), damit die Migrationsnummer feststeht.
2. **Backend-PR** (Feature-Branch, Conventional Commits wie in den neuen Workflows) → Review → Deploy.
3. **Frontend-PR** → Review → Deploy.
4. **sita-mcp:** `latest`-Tag ergänzen, veröffentlichen, erstes Release.
5. **Ende-zu-Ende-Test:** Token im Profil erzeugen, in Claude Code eintragen, `wiki_search` und `wiki_get_page` aufrufen, Token widerrufen. Danach muss der nächste Aufruf mit „Token ungültig“ scheitern.
6. Später: HTTP-Modus im Swarm, Schreib-Tools, OAuth für Remote-Connectors.
