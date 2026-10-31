# Umsetzungsplan: MCP-Tokens in sita-backend und sita-frontend

Plan zur Umsetzung von [api-tokens.md](api-tokens.md). Er beruht auf einer Analyse von `sita-backend@main` (Spring Boot 3.4, Kotlin, offene PRs #190 Spring Boot 4 und #192 wiki-service) und `sita-frontend@main` (Angular 20, Material, Standalone-Komponenten). Umgesetzt ist davon noch nichts.

## Kurzfassung

| Teil | Umfang | Abhängigkeit |
|---|---|---|
| Backend | Migration, Token-Service, Auth-Filter, Controller, Tests | am besten nach #192 |
| Frontend | Profil-Unterseite „MCP-Tokens“, Service, Menü-Button | Backend-Endpunkte |
| sita-mcp | `latest`-Tag beim Release, erstes Release, Repo veröffentlichen | Backend für einen echten Test |
| Deployment | zunächst nichts: der MCP läuft beim Nutzer per stdio | – |

## Entscheidungen (mit Vorschlag)

| # | Frage | Vorschlag | Begründung |
|---|---|---|---|
| 1 | Wer darf Tokens anlegen? | Jeder eingeloggte Nutzer | Ein Token kann nur lesen, was der Nutzer ohnehin lesen darf. Ein eigenes Recht `MCP_TOKEN_CREATE` bräuchte Migration, Enum-Eintrag (`PermissionStartupValidator`) und Admin-UI, ohne Sicherheitsgewinn. |
| 2 | Ablaufzeiten | 30 / 90 / 365 Tage, Standard 90, **kein „nie“** | Vergessene Tokens laufen von selbst aus. Ein neues Token ist schnell erzeugt. |
| 3 | Widerruf bei Passwortänderung | Ja beim Admin-Reset (`changePassword(…, reset = true)`), nein bei eigener Änderung | Ein Admin-Reset heißt oft „Account kompromittiert“. Ändert der Nutzer sein Passwort selbst, will er nicht alle MCP-Clients neu einrichten müssen. |
| 4 | Basis-Branch, Migrationsnummer | Auf `main` nach dem Merge von #192, als `V017__create_mcp_tokens.sql` | #192 belegt V016. Wird das Token-Feature zuerst fertig, bekommt es V016, und #192 muss umnummerieren. |
| 5 | Spring Boot 4 (#190) | Endpunkt-Liste per Regex auf `servletPath`, keine `AntPathRequestMatcher` | Funktioniert unverändert in Spring Security 6 und 7. Der Filter selbst nutzt nur `OncePerRequestFilter`. |
| 6 | Wie kommen Nutzer an das Image? | `promote.yml` taggt zusätzlich `latest`. Klären, ob `registry.simpletactics.de` ohne Login lesbar ist | Die Snippets im Frontend brauchen einen festen Tag. Ohne öffentlichen Pull muss jeder Nutzer vorher `docker login`. Alternativ das Image zusätzlich auf GHCR veröffentlichen, öffentlich wie das Repo. |
| 7 | HTTP-Modus deployen? | Erstmal nicht, nur stdio | Spart Stack, Reverse Proxy und Rate-Limit-Fragen. Der HTTP-Modus ist fertig und kann später in den Swarm. |

## Backend (sita-backend)

### Dateien

Paketstruktur wie bisher. Die ArchUnit-Regeln für Controller, Services und Ports bleiben eingehalten.

```
src/main/resources/db/migration/V017__create_mcp_tokens.sql
src/main/kotlin/de/simpletactics/
├── adapter/persistence/entity/McpTokenEntity.kt
├── adapter/persistence/McpTokenAdapter.kt            JdbcTemplate, analog SessionAdapter
├── services/port/McpTokenPort.kt
├── services/McpTokenService.kt
├── model/McpToken.kt                                 id, userId, username, name, scopes, Zeitstempel
├── security/McpTokenAuthenticationFilter.kt
├── security/McpTokenAuthentication.kt                AbstractAuthenticationToken
├── security/McpTokenAllowlist.kt
├── adapter/controller/McpTokenController.kt
└── adapter/dto/McpTokenDtos.kt                       McpTokenDto, CreateMcpTokenDto, CreatedMcpTokenDto, McpTokenInfoDto
```

Geändert werden:

- `security/SecurityConfig.kt`: neuen Filter vor `SessionAuthenticationFilter` registrieren.
- `services/UserService.kt`: in `changePassword(…, reset = true)` alle Tokens des Nutzers widerrufen.

### Migration

Wie in [api-tokens.md](api-tokens.md#datenmodell-vorschlag-flyway-migration-in-sita-backend). Der Fremdschlüssel auf `users(user_id)` hat `ON DELETE CASCADE`, damit `deleteUserByUserId` weiter funktioniert.

### Service

```kotlin
@Service
class McpTokenService(private val port: McpTokenPort, private val userService: UserService) {
    fun create(username: String, name: String, expiresInDays: Int): CreatedMcpToken  // Limit 10, erlaubt: 30/90/365
    fun list(username: String): List<McpToken>                                       // nur nicht widerrufene
    fun revoke(username: String, id: Int): Boolean                                   // false → 404
    fun revokeAllForUser(userId: Int)
    fun authenticate(rawToken: String): McpToken?                                    // null → 401
}
```

- **Erzeugen:** `SecureRandom` liefert 32 Bytes, daraus `"sita_mcp_" + Base64.getUrlEncoder().withoutPadding().encodeToString(bytes)`.
- **Hash:** `MessageDigest.getInstance("SHA-256")`, als Hex-String. Nachgeschlagen wird per `WHERE token_hash = ?`. Einen Vergleich in konstanter Zeit braucht es nicht, weil per Hash gesucht wird.
- **`authenticate`** lehnt ab, wenn das Token unbekannt ist, `revoked_at` gesetzt ist, `expires_at` vorbei ist oder `userService.isUserActive(username)` false liefert. `last_used_at` schreibt es nur, wenn der letzte Wert älter als eine Minute ist.

### Filter

```kotlin
class McpTokenAuthenticationFilter(
    private val tokenService: McpTokenService,
    private val globalPermissionService: GlobalPermissionService,
) : OncePerRequestFilter() {

    override fun doFilterInternal(request: HttpServletRequest, response: HttpServletResponse, chain: FilterChain) {
        val raw = request.getHeader(HttpHeaders.AUTHORIZATION)
            ?.removePrefix("Bearer ")
            ?.takeIf { it.startsWith(TOKEN_PREFIX) }
        if (raw == null) {
            chain.doFilter(request, response) // Session-Login wie bisher
            return
        }

        val token = tokenService.authenticate(raw)
        if (token == null) {
            response.setHeader(HttpHeaders.WWW_AUTHENTICATE, "Bearer")
            response.status = HttpServletResponse.SC_UNAUTHORIZED
            return
        }
        if (!McpTokenAllowlist.permits(request.method, request.servletPath)) {
            response.status = HttpServletResponse.SC_FORBIDDEN
            return
        }

        val authorities = globalPermissionService.getPermissionsForUser(token.username)
            .filter { it == Permission.WIKI_READ }
            .map { SimpleGrantedAuthority(it.name) }
        // Principal muss der Benutzername als String sein: WikiPermissionService liest `principal as? String`.
        SecurityContextHolder.getContext().authentication = McpTokenAuthentication(token.username, token.id, authorities)
        chain.doFilter(request, response)
    }
}
```

Fallstricke:

- **`response.status` statt `sendError`.** `sendError` leitet intern auf `/error` um. Diesen Pfad gibt die SecurityConfig nicht frei, er würde für anonyme Requests wieder als 403 enden.
- **Warum die Allowlist zwingend ist:** `WikiPermissionService.hasWikiPermission` lädt die Rollenrechte selbst aus der Datenbank. Ohne Allowlist könnte ein Token alles, was der Nutzer darf. Mehrere Endpunkte verlangen sogar nur eine Anmeldung und gar kein bestimmtes Recht: `/api/user/change/password`, `/api/updateProfile`, `/api/game/*`, `/api/poll/submitPollVotes`.

```kotlin
object McpTokenAllowlist {
    private val paths = listOf(
        "/api/tokens/current",
        "/api/wiki/types/\\d+",
        "/api/wiki/topics/\\d+",
        "/api/wiki/entries/\\d+",
        "/api/wiki/buttons/main-panel/\\d+",
        "/api/wiki/buttons/left-panel/\\d+",
        "/api/poll/get/\\d+",
        "/api/poll/closed/\\d+",
    ).map(::Regex)

    fun permits(method: String, path: String) = method == "GET" && paths.any { it.matches(path) }
}
```

### Controller

| Methode | Pfad | Hinweis |
|---|---|---|
| GET | `/api/tokens/current` | Liest `McpTokenAuthentication` aus dem SecurityContext. Mit Session-Login antwortet er 404, er ist nur für Tokens gedacht |
| GET | `/api/tokens` | Eigene Tokens |
| POST | `/api/tokens` | `201` mit Klartext-Token, `400` bei ungültigem Namen oder Ablauf, `409` über dem Limit |
| DELETE | `/api/tokens/{id}` | `204`, `404` bei fremder oder unbekannter id |

Die Verwaltungs-Endpunkte sind per Token nicht erreichbar, weil sie nicht in der Allowlist stehen. Ein Test hält das fest.

### Tests

Kotest mit `FunSpecIT` und MockMvc, wie `LoginControllerTestIT`. Testdaten kommen per `SQLScripts`.

- **Filter:** gültiges Token liefert 200; unbekanntes, abgelaufenes, widerrufenes Token und deaktivierter Nutzer liefern 401 mit `WWW-Authenticate`. Ein Endpunkt außerhalb der Allowlist (z. B. `PUT /api/wiki/entries/1/update`, `POST /api/tokens`, `PUT /api/user/change/password`) liefert 403.
- **Wiki-Rechte:** Ein Nutzer ohne lokale `WIKI_READ` auf Knoten X bekommt mit Token 403 auf X. Ein Admin-Token bekommt trotz globalem `WIKI_EDIT` beim Bearbeiten 403.
- **Service:** Das Format stimmt (Präfix, Länge), gespeichert ist nur der Hash, das Limit von 10 greift, `last_used_at` wird gedrosselt, Revoke wirkt.
- **Admin-Reset** widerruft die Tokens des Nutzers.
- **Session-Login** funktioniert unverändert (bestehende Tests).

## Frontend (sita-frontend)

### Dateien

```
src/app/modules/enum/profile-type.enum.ts                    + MCP_TOKENS = 'mcp-tokens'
src/app/modules/pages/mcp-token.module.ts                    McpToken, CreateMcpToken
src/app/modules/pages/response/created-mcp-token-response.module.ts
src/app/services/mcpTokenService.ts                          list / create / revoke, Stil wie passwordService
src/app/pages/profile/profile-routing.module.ts              + { path: 'mcp-tokens', component: McpTokensComponent }
src/app/pages/profile/mcp-tokens/mcp-tokens.component.{ts,html,scss,spec.ts}
src/app/elements/menuliste/menulist-right-main/menu-list-profile/menu-list-profile.component.html
                                                             + Button „MCP-Tokens“ im Fall 'none'
```

### Seite „MCP-Tokens“

- **Erklärtext** in zwei Sätzen: wofür das Token ist, dass es nur lesen kann und dass man es jederzeit widerrufen kann.
- **Liste** (Material-Tabelle): Name, erstellt, zuletzt genutzt („nie“), läuft ab. Je Zeile ein Button „Widerrufen“, der das vorhandene `DeleteModalComponent` öffnet, z. B. mit dem Text „Token ‚Laptop‘ widerrufen? MCP-Clients mit diesem Token verlieren sofort den Zugriff.“
- **Neues Token:** Name (Pflicht, max. 100 Zeichen) und Ablauf als `mat-select` mit 30, 90 (Standard) oder 365 Tagen. Ist das Limit erreicht, wird der Button deaktiviert und erklärt warum.
- **Nach dem Erstellen:** Ein Hinweiskasten zeigt das Token einmalig an, mit Kopieren-Button (`@angular/cdk/clipboard` ist über Material schon da). Dazu die Warnung „Wird nur jetzt angezeigt“ und darunter Snippets mit eingesetztem Token zum Kopieren:
  - Claude Code: `claude mcp add sita -e SITA_TOKEN=<token> -- docker run -i --rm -e SITA_TOKEN registry.simpletactics.de/sita-mcp:latest`
  - Claude Desktop: der JSON-Block aus dem README.
- **Datenschutz:** Das Token bleibt nur im Komponenten-State. Es landet nicht im Router-State, nicht im `localStorage` und nicht in Logs. Beim Verlassen der Seite ist es weg.
- **Fehler:** Der globale `errorInterceptor` springt bei jedem 403 auf die Zugriff-verweigert-Seite. Die Fehler dieser Seite (400, 409) zeigt die Seite selbst inline an.

### Tests

`mcp-tokens.component.spec.ts` mit `HttpTestingController`:

- Die Liste wird geladen.
- Erstellen zeigt das Token einmal an, ein neues Laden nicht mehr.
- Widerrufen ruft `DELETE` auf und entfernt die Zeile.
- Ein 409 zeigt eine Meldung und keine Weiterleitung.

## sita-mcp

- `promote.yml`: zusätzlich `crane tag "$image:$rc_version" latest`.
- Repo auf GitHub anlegen, öffentlich, und pushen. Prüfen, ob die Org-Secrets für öffentliche Repos freigegeben sind.
- Erstes Release über `prepare-release`, `cut-rc` und `promote`.
- Optional: eine eigene Secret-Scanning-Regel für `sita_mcp_[A-Za-z0-9_-]{43}`. Das braucht GitHub Advanced Security, für öffentliche Repos ist es kostenlos.

## Reihenfolge

1. #192 mergen (wiki-service), damit die Migrationsnummer feststeht.
2. **Backend-PR** (Feature-Branch, Conventional Commits wie in den neuen Workflows) → Review → Deploy.
3. **Frontend-PR** → Review → Deploy.
4. **sita-mcp:** `latest`-Tag ergänzen, veröffentlichen, erstes Release.
5. **Ende-zu-Ende-Test:** Token im Profil erzeugen, in Claude Code eintragen, `wiki_search` und `wiki_get_page` aufrufen, Token widerrufen. Danach muss der nächste Aufruf mit „Token ungültig“ scheitern.
6. Später: HTTP-Modus im Swarm, Schreib-Tools (Scope `wiki:write`), OAuth für Remote-Connectors.
