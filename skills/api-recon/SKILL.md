---
name: api-recon
description: Use this skill to inventory a website's API surface.
---

# API Reconnaissance

Within an **explicitly authorized scope**, discover as completely as possible the site's **backend APIs** (paths, methods, parameters, and response bodies), **frontend routes**, and **UI trigger points** such as tabs, dialogs, and table actions.

---

## Boundaries and prohibitions (required reading for the agent)

This skill performs **API and parameter-surface reconnaissance only**. It is not a vulnerability-discovery or exploitation phase.

### Task boundary

| Area | Allowed | Prohibited |
|---|---|---|
| **Target activity** | Enumerate paths, methods, parameters, routes, and UI trigger points | SQL injection, XSS, authorization bypass, brute force, vulnerability fuzzing, request tampering, or destructive operations |
| **Authentication** | Use hooks and stubs/mocks to bypass a **client-side** login gate | Ask for or guess credentials; submit a real login form |
| **Runtime analysis** | Hook requests without credentials and use mock responses to expose the post-login SPA shell | Workflows that require a real backend session before analysis can continue |

### Credential-free dynamic analysis (Phase 3 default)

1. Use `preload.js` or `runtime_harvest.js` to intercept and stub login, permission, menu, and other bootstrap APIs.
2. Return structurally valid, successful mock bodies for business-query APIs; data may be empty.
3. Keep the frontend rendering its post-login pages even when the backend is absent or returns 401, causing more XHR, fetch, and WebSocket activity.
4. **Empty data, blank tables, and placeholder UI are expected.** Do not switch to real authentication or vulnerability testing because of them.

In short: use mocks to mount frontend routes and components and **record outbound requests only**. The backend response content is secondary; the objective is to learn **which additional requests the frontend emits**.

### Hard workflow prohibitions

| Prohibited action | Required alternative |
|---|---|
| Grep, curl, or read a main `index-*.js` entry bundle for API paths before Phase 1 is complete | Run `OUTDIR/harvest_static.py` |
| Write replacement scripts such as `extract_apis.py` instead of using the harvester | Modify `OUTDIR/harvest_static.py` and rerun it |
| Repeat the same failed grep or command two or more times | Change strategy: inspect tool logs, modify the harvester, or consult the reference |
| Skip Gate A or B and run the original scripts directly | Copy them to `OUTDIR` and adapt them to the target |
| Use real usernames, passwords, OTP, OAuth, or similar authentication | Use stubs/mocks as described above |
| Skip stubbing and test authorization or injection in order to obtain real data | Record outbound traffic only; exploitation is outside the recon boundary |
| Delete or export sensitive data, perform bulk writes, or take another irreversible action | Avoid these actions during coverage clicking as well |
| Claim all pages and APIs were found without runtime analysis and dynamic enumeration | Meet the completion definition or clearly document limitations |
| Claim all parameters were found without the trigger matrix and comparison | Complete the Phase 3b matrix and Phase 5 comparison |
| Infer required versus optional fields from one runtime sample | Compare multiple samples or use validation rules and error responses |

---

## Two-layer model and runtime modes

| Layer | Output | Limitation |
|---|---|---|
| **Static** (JavaScript bundles) | Endpoint paths, draft routes, and candidate payload fields | No reliable HTTP methods; parameters require Phase 1b; runtime-built URLs may be missed |
| **Runtime** (live browser session) | Methods, bodies, responses, dynamic URLs, WebSockets, and SSE; multiple samples improve parameter coverage | A page must render before it sends requests; one sample cannot establish required versus optional fields |

| Runtime mode | Engine | Best use |
|---|---|---|
| **depth** | `runtime_harvest.js` (Puppeteer) | API inventory, methods, parameters, response bodies, WS/SSE, and reproducible batch runs |
| **coverage** | Browser plus `preload.js` | Deeper UI coverage through tabs, dialogs, and table actions |
| **both** | Depth followed by coverage | Most complete and most time-consuming |

**Parameter method:** use the harvester or regular expressions for paths. Determine parameters through **context expansion around path anchors, UI binding traces, multi-sample comparisons, and error-response inference**. See section J of [reference.md](reference.md) for grep recipes.

---

## Completion definition

Do not claim reconnaissance is complete until every applicable item is satisfied:

- [ ] **Static:** Phase 1 produced `api_static.txt`, `routes.txt`, and `js/`.
- [ ] **Runtime:** At least depth or coverage was completed. Coverage/both also requires an active hook and the dynamic-enumeration loop.
- [ ] **Post-login shell:** Visiting a business path does not return to `/login` (account for hash routes).
- [ ] **Parameters:** Coverage/both completed the parameter trigger matrix and `param_samples.json`; Phase 5 produced `params_merged.json`.
- [ ] **Depth where modules are blank:** Phase 4 restored the permission tree and Phase 3 was rerun until module-level APIs appear, rather than only locale/bootstrap requests.
- [ ] **Delivery:** All Phase 5 outputs exist, and `insert_assets` records discovered service and endpoint assets.

---

## Scripts and adaptation gates

Files under `scripts/` are reference templates only. **Do not run an unchanged template and treat its output as final.**

Rule: read it, adapt it to the target, copy it into `OUTDIR` such as `recon/`, and record changes in `CHANGES.md`. If the template does not fit, rewrite it using the same method rather than preserving an unsuitable implementation.

| Gate | When | Reference script copied to `OUTDIR` | Common adaptations |
|---|---|---|---|
| **A (static)** | After Phase 0 and before the first harvester/spider run | `harvest_static.py` / `spider_mpa.py` | Default regexes work for most sites. Change endpoint regexes, webpack/Vite `publicPath`, MPA exclusions, or cookies only when manifests or dialects require it. |
| **B (runtime)** | After Phase 2 and before depth/coverage | `runtime_harvest.js` / `preload.js` plus `config.json` | Cookie/localStorage keys, success values, stubs, login regex, API prefixes, and hash/history routing |

### Mandatory SPA order

The order cannot be rearranged. Phase numbers take priority over exploratory convenience.

| Step | Required | Prohibited |
|---|---|---|
| Immediately after Phase 0 | The next Bash command is `python3 OUTDIR/harvest_static.py <URL> OUTDIR` | Curl, grep, or read the main `index-*.js` entry, which is often larger than 500 KB |
| Gate A | Copy the script, make only necessary adaptations, and run it immediately | Manually extract APIs before deciding whether to run the harvester |
| Before Phase 1 is complete | Validate outputs with `wc -l`; modify and rerun the harvester after a 404 | Write an extraction replacement or repeatedly grep URLs that were not downloaded |
| From Phase 1b onward | Grep only `OUTDIR/js/*.js` | Substitute the main bundle for the harvester |

- Correct: copy `harvest_static.py`, optionally adjust its regex, and run it immediately.
- Incorrect: curl the main bundle, grep repeatedly, write a temporary extractor, and run the harvester last.
- For an MPA, the first Bash command after Phase 0 is `python3 OUTDIR/spider_mpa.py ...`.

---

## Tool and output constraints

| Constraint | Requirement |
|---|---|
| Large files | Do not read or grep `index-*.js` files larger than 100 KB into model context; process them with an `OUTDIR` script |
| Grep output | Always cap with `| head -20` or `-m 5`; retain path summaries in the conversation, not bundle fragments |
| Validation | Use `wc -l` and `ls | wc -l`; do not read entire directories |
| Preliminary regex | Optional, at most once, and only against HTML or a chunk no larger than 50 KB; use the harvester for formal static discovery |
| Reference | Recipes, templates, and troubleshooting are in [reference.md](reference.md); do not duplicate the entire reference inline |

---

## Execution roadmap

```text
Phase 0 classify and create OUTDIR
  -> Gate A -> Phase 1 harvest (run immediately)
  -> Phase 1b reverse parameters
  -> Phase 2 analyze the three authentication gates and write config.json
  -> Gate B -> Phase 3 runtime analysis and parameter matrix
  -> Phase 4 restore the permission tree when necessary, then rerun Phase 3
  -> Phase 5 merge, report, and call insert_assets for every discovered service
     and endpoint asset; never omit an already discovered asset
```

Complete the checklist in order; do not enter a later phase while an earlier required phase remains incomplete.

1. [ ] **Phase 0:** Identify SPA or MPA and create `OUTDIR`.
2. [ ] **Gate A and Phase 1:** Copy the script, immediately harvest, and validate with `wc -l`.
3. [ ] **Phase 1b:** Expand around path anchors and follow binding layers into `param_candidates.json`.
4. [ ] **Phase 2:** Analyze the three authentication gates and write `config.json`.
5. [ ] **Gate B:** Adapt the runtime scripts.
6. [ ] **Phase 3:** Run depth, coverage, or both; confirm the post-login shell; complete the parameter trigger matrix and `param_samples.json`.
7. [ ] **Phase 4 when needed:** Restore the permission tree, patch stubs, and rerun Phase 3.
8. [ ] **Phase 5:** Merge outputs, report, and call `insert_assets`.

---

## Phase 0 — Classification

Fetch the entry HTML and create `OUTDIR`. Do not modify the skill's own `scripts/` directory.

- **SPA:** An empty shell, a root element such as `<div id=app>`, and JavaScript chunks. Continue through Phases 1–5.
- **MPA:** Server-rendered HTML with `<form>` and no endpoint bundle. After Gate A run:

```bash
python3 recon/spider_mpa.py <BASE_URL> <OUTDIR> [--cookie "session=..."] [--max 300] [--depth 5] [--exclude "logout|delete|destroy"]
```

Outputs are `forms.txt`, `links.txt`, and `api_inline.txt`. If an apparent SPA produces approximately zero forms, continue with Phase 1.

---

## Phase 1 — Static discovery

Follow [Scripts and adaptation gates](#scripts-and-adaptation-gates) and [Tool and output constraints](#tool-and-output-constraints).

```bash
python3 recon/harvest_static.py <BASE_URL> <OUTDIR>
```

The harvester parses HTML scripts and webpack/Vite manifests, downloads lazy chunks, and produces `js/`, `api_static.txt`, `routes.txt`, and `chunkmap.txt`.

```bash
wc -l OUTDIR/api_static.txt OUTDIR/routes.txt
ls OUTDIR/js | wc -l
```

- Compare the downloaded chunk count with the manifest. After a 404, modify and rerun the harvester rather than curling chunks manually.
- If `api_static.txt` is unexpectedly small, broaden the endpoint regex in the `OUTDIR` copy and rerun it. See the reference.

### Phase 1b — Parameter reconstruction

Paths come from Phase 1. Parameter fields require separate analysis. Apply the grep-output limits above.

Completion criterion: for important endpoints, identify field names, transport location, inferred types, required/optional status, sample values, and confidence.

#### 1b.0 — Transport form

| Form | Parameter location | Static evidence to prioritize |
|---|---|---|
| REST JSON | Body and query | `(params|data|body)\s*:\s*\{` near the path anchor |
| GraphQL | `variables` | GraphQL templates and declarations such as `$page: Int` |
| Traditional form | URL-encoded body | `<form>` and `FormData` |
| File upload | Multipart | `FormData.append` |
| Path parameter | `/user/:id` | Route tables and `useParams` / `$route.params` |
| Encrypted/signed payload | Wrapped in `sign` or `data` | Hook the encryption function's input; see reference section D |

For each endpoint, record `transport: query|json|form|graphql|encrypted`.

#### 1b.1 — Expand around path anchors

Use a known path as the anchor and inspect the nearby payload-building object:

```bash
grep -n '"/api/user/list"' OUTDIR/js/*.js | head -20
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' OUTDIR/js/*.js | head -20
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' OUTDIR/js/*.js | head -20
```

| Wrapper | Parameter evidence |
|---|---|
| Axios instance | `data` / `params` |
| Shared request helper | Fields injected by interceptors |
| OpenAPI client | Generated method signatures |
| React Query / SWR | The hook's second argument |
| Vue composable | Composable arguments |

Also inspect surviving `yup`, `zod`, validation rules, `Form.Item name=`, and embedded Swagger metadata.

Produce `param_candidates.json` entries shaped like `{ path, fields[], source: "static-callsite", confidence }`.

#### 1b.2 — Follow the binding layer

```text
Form field -> onFinish/handleSubmit -> transform -> API payload
```

| Binding source | Method |
|---|---|
| Form submission | Follow submit to transform to API call |
| Table search | Follow `getFieldsValue()` into `params` |
| Router | Inspect `:id` and `?tab=` |
| Interceptor | Record global `tenantId`, pagination, and signature fields |
| Select options | Connect option values to API enumeration values |

In DevTools, follow the call stack upward from `fetch` or `XHR.send` to the payload builder.

#### 1b.3 — Three payload questions (distinct from the Phase 2 auth gates)

| Question | Required answer |
|---|---|
| **Construction** | Where the payload is built and how it is transformed |
| **Validation** | Required fields, patterns, and enumerations |
| **Transport** | Path, query, body, multipart, and headers |

While examining the Phase 2 interceptor gate, also record globally injected fields such as Authorization, `X-Tenant-Id`, and signatures.

#### 1b.4 — Connection to Phase 3

Static and binding analysis produces candidate fields. Establish **required, optional, and conditional dependencies** only through the Phase 3 parameter matrix, multi-sample comparison, and Phase 5 error inference.

---

## Phase 2 — Three authentication gates

Grep only inside `OUTDIR/js/`, cap output with `head`, and record results in `config.json`. See the reference for recipes.

| Gate | Question | Keywords |
|---|---|---|
| **Render gate** | How does the client decide the user is logged in? | `isLogin`, `getToken`, Cookie/localStorage |
| **Interceptor gate** | What causes navigation to `/login`? | `response_code`, `errno`, axios interceptor |
| **Content gate** | Where do menus and permissions come from? | `menu`, `permission`, `role`, `acl`, `routes` |

Do not treat a localStorage key name as a credential. Confirm it from a chunk or request chain.

**Gate B output:** put the conclusions in `config.json`, then adapt `OUTDIR/runtime_harvest.js` and `preload.js`.

### Phase 2b — API observation (optional)

Use the `OUTDIR` copy of `preload.js` to confirm session keys, Authorization headers, and nested API URLs:

| Setting | Output |
|---|---|
| `recordDetail: true` | `__API_RECON_DETAIL__` |
| `observe.xhrHeaders: true` | Header observations |
| `extractUrlsFromResponse: true` | Child APIs found in responses |
| `observe.storageReads/cookieReads: true` | Evidence to update the configuration |
| `neutralizeVueRouter: true` | `__API_RECON_ROUTES__` |

After each coverage pass, export `__API_RECON_LOG__`, `__API_RECON_DETAIL__`, `__API_RECON_ROUTES__`, and `__API_RECON_OBSERVE__`.

---

## Phase 3 — Runtime discovery

Gate B must be complete. Follow the [boundaries](#boundaries-and-prohibitions-required-reading-for-the-agent) and the credential-free mocking strategy.

Set `runtimeMode` in `config.json` to `"depth"`, `"coverage"`, or `"both"`. See the reference for a template.

### Shared hook and stub layers

| Layer | Scope | Purpose |
|---|---|---|
| L1 exact | Auth, permission, and bootstrap stubs | Pass initial authentication |
| L2 negative-code correction | All JSON responses | Rewrite logged-out codes to success |
| L3 fallback | Unmatched `/api`-like requests | Return an empty success body so the UI can mount |

- **depth:** Fake authentication, forward and rewrite business codes, apply stubs, traverse hash/history routes, and produce `runtime_api.json`.
- **coverage:** Inject `preload.js` at **document start** through CDP `addScriptToEvaluateOnNewDocument` or a userscript.

Verify `window.__API_RECON_PRELOAD__` exists and business paths do not return to `/login`.

```bash
cd recon && npm install
node runtime_harvest.js config.json
```

### Phase 3b — Dynamic coverage enumeration (required)

1. Click every main-navigation and sidebar entry and wait one to three seconds for network activity.
2. Click tabs such as `role=tab` and `.ant-tabs-tab`.
3. Open view, edit, and details actions on the first table row.
4. Exercise export, filter, and create controls, while avoiding irreversible deletion.
5. Merge APIs and routes after entering each module.
6. For SPAs only, use controlled `pushState` navigation for uncovered `routes.txt` paths. Do not do this for MPAs.

Complete the **parameter trigger matrix** for every module and compare multiple request samples:

| Action | Common additional parameters |
|---|---|
| Initial list | Pagination and default filters |
| Search | Keyword and filters |
| Advanced filters | Additional optional fields |
| Create/edit | Full entity |
| Bulk/export/sort | `ids[]`, `exportType`, and `sortField` |

Even when responses are stubbed, outbound bodies and headers remain real. Treat the request as the evidence. Produce `scan_raw.json`, `param_samples.json`, and `api_detail.json`.

- **Vue:** Use `neutralizeVueRouter: true` with document-start preload.
- **React:** Combine `routes.txt`, sidebar clicks, and `pushState`.
- **both:** Run depth first, then coverage.

---

## Phase 4 — Permission-tree restoration

Trigger this phase when module pages are blank or every route produces only bootstrap traffic such as locale requests. The content gate has not been passed.

| Observation | Meaning |
|---|---|
| Post-login shell loads | Render and interceptor gates passed |
| Sidebar entries are missing or pages are blank | Stub shape or permission codes are incomplete |
| Every route has the same small API set | A permission condition such as `v-if` failed |
| `routes.txt` contains far fewer routes than the bundle | Recover additional routes from the auth module |

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl)[^"]*"' OUTDIR/js/*.js | sort -u | head -30
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|menuList|authList' OUTDIR/js/*.js | head -20
```

A common chain is flat `role_permissions` codes plus a `permissions/all` tree, filtered through `getResultTree` and mapped to paths by `userRouteAuth[CODE].url`.

```bash
python3 recon/extract_route_map.py recon/js recon/
python3 recon/build_perm_tree.py recon/js recon/ --config recon/config.json
```

Intermediate outputs include `route_map.json`, `userRouteAuth.json`, `permissions_tree.json`, `*_stub.json`, and `perm_codes_all.txt`.

Validate that the outer `response_code` matches the interceptor gate, flat codes align with the tree, and `routes` covers every route-map link.

After updating `config.json`, **rerun Phase 3**. For large SPAs, adjust `waitUntil`, `routeTimeout`, and `perRouteMs` as described in reference sections A3 and I.

---

## Phase 5 — Merge and report

### Output table

| File | Phase | Content |
|---|---|---|
| `js/`, `api_static.txt`, `routes.txt`, `chunkmap.txt` | 1 | Static bundles and paths |
| `param_candidates.json` | 1b | Candidate static parameter fields |
| `config.json` | 2 | Authentication gates and runtime configuration |
| `runtime_api.json` | 3a | Detailed depth capture including WS/SSE |
| `param_samples.json`, `scan_raw.json`, `api_detail.json` | 3b | Multiple samples, click log, and request details |
| `route_map.json` and related files | 4 | Permission-tree intermediates when Phase 4 ran |
| `params_merged.json` | 5 | Merged parameter fields and confidence |
| `api_merged.txt` | 5 | `METHOD /path [params] [static|runtime|both]` |
| `site_map.json` | 5 | Routes, APIs, parameters, features, and limitations |
| `insert_assets` | 5 | Record all service and endpoint assets |

### Phase 5b — Merge parameters

Compare samples in `param_samples.json`. There is intentionally no universal merge script. Apply the high/medium/low/not-triggered confidence rules from reference section J7.

### Phase 5c — Infer from errors

Within the authorized scope, an incomplete request may be sent to observe a 400 response. This is parameter reconnaissance, not vulnerability testing. Useful evidence includes `field 'x' is required` and invalid-enum errors. Account for wrappers such as `data`, `variables`, and pre-encryption `bizData`.

The report must state the runtime mode, static and runtime API counts, parameter confidence, uncovered modules, and a summary of `CHANGES.md` differences from the templates.

Suggested `site_map.json` shape:

```json
{
  "site": "https://example.com",
  "runtimeMode": "both",
  "appType": "vue-spa",
  "routeGuardStrategy": ["nav-neutralize", "L1-auth", "L2-patch", "forward"],
  "apisFromStatic": [],
  "apisFromRuntime": [],
  "apis": [],
  "params": [{ "method": "POST", "path": "/api/user/list", "transport": "json", "fields": [] }],
  "frontendRoutes": [],
  "routesVerifiedByClick": [],
  "featuresTriggered": [],
  "limitations": ""
}
```

See [reference.md](reference.md) for additional fields and grep recipes.

---

## General notes

- **Framework independent:** webpack, Vite, and Angular lazy-loading follow the same method.
- **Transports:** REST/JSON, GraphQL, WebSocket, and SSE are supported; gRPC-web is outside scope.
- **SSR:** Client fetches can be recorded, but RSC and Server Actions cannot be completely enumerated.
- **Blind spots:** JSVMP, WASM, and strict HMAC/mTLS validation require static fallback and an explicit limitation.
- **Parameter blind spots:** Conditional fields, hidden parameters, and WASM payload construction should be marked `not triggered` or `unreachable`.
- **Static discovery is the safety net:** when runtime access is blocked, static analysis can still enumerate endpoint paths.

---

## Additional resources

- Grep recipes, a `config.json` template, troubleshooting, hook details, parameter-reconstruction section J, and the site-map template: [reference.md](reference.md)
- Reference-script locations: the [Scripts and adaptation gates](#scripts-and-adaptation-gates) table
