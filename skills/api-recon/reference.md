# api-recon reference manual

Grep recipes, a `config.json` template, and troubleshooting guidance. Run every grep against the `js/` directory. For a single-line bundle, `js-beautify` or `sed 's/}/}\n/g'` may help, although raw grep with bounded context is usually sufficient.

## Script notes

Every file under `scripts/` is a **reference template** and must be adapted to the target before execution.

| Script | Common target-specific changes |
|---|---|
| `harvest_static.py` | Endpoint regex, webpack/Vite manifest handling, micro-frontend `publicPath`, retries, and concurrency |
| `runtime_harvest.js` | Neutralized fields and success values, stub matching and body shape, route sources, WebSocket recording, `waitUntil`, `routeTimeout`, and `proxy` |
| `preload.js` | `loginPathRe`, L1 stubs, `neutralize.fields`, `apiPattern`, L3 enablement, `recordDetail`, `observe.*`, and `neutralizeVueRouter` |
| `spider_mpa.py` | Destructive-link exclusions, cookie, depth/max limits, and same-origin filtering |
| `extract_route_map.py` | `routeMap` / `routeLink` regex and key-naming conventions |
| `build_perm_tree.py` | `userRouteAuth` parsing, `ROOTS`/`PREFIX_PARENT` hierarchy heuristics, and outer stub fields |
| `config.json` | The shared entry point for all target-specific settings above |

Put adapted files in the task workspace, for example `recon/`, and document every difference from the templates in the final report.

---

## A. Reverse the three gates

### A1. Render gate — How does the client decide the user is logged in?

```bash
grep -rhoaE '.{0,40}(isLogin|isAuthenticated|loggedIn|hasLogin|requireAuth)\b.{0,80}' js | head
grep -rhoaE 'function (getUser|getToken|getAuth)[0-9]?\([^)]*\)\{.{0,200}' js | head
grep -rhoaE '(localStorage|sessionStorage)\.getItem\("[^"]+"\)' js | sort -u
grep -rhoaE '(Cookies?|cookie)\.(get|load)\("[^"]+"\)' js | sort -u
grep -rhoaE '\batob\(|JSON\.parse\(|jwt|decode' js | head
```

Trace a chain such as `isLogin = f(getUser())` to `getUser = decode(storage.read(KEY))`. Determine the **storage key**, **container** (Cookie or localStorage), and **encoding**.

| Encoding | Configuration value used for the mock |
|---|---|
| Plain string, `"1"`, or token | `"value": "anything-truthy"` |
| `JSON.parse(x)` | `"value": "json:{\"id\":1,\"username\":\"admin\"}"` |
| `JSON.parse(atob(x))` | `"value": "b64json:{\"id\":1,\"username\":\"admin\"}"` |
| JWT | An unsigned/`alg:none` JWT, or one signed with a key embedded in the bundle |
| SM2/AES/RSA or similar encryption | Look for embedded keys. Forge only when the render gate merely needs a decodable blob; otherwise use static fallback. |

Record the result in `cookies` or `localStorage`.

### A2. Interceptor gate — What causes navigation to `/login`?

```bash
grep -rhoaE '.{0,60}(interceptors\.response|axios|request\.use).{0,120}' js | head
grep -rhoaE '.{0,40}(response_code|errcode|errno|\bcode\b|\bret\b|\bstatus\b)\s*[=!]==?\s*[\-0-9]{1,4}.{0,60}' js | head -20
grep -rhoaE '.{0,40}(未登录|请重新登录|登录已过期|unauthorized|登录失效|授权|token.{0,10}invalid).{0,40}' js | head
grep -rhoaE '.{0,30}(location\.href|router\.(push|replace)|navigate)\([^)]*login[^)]*\)' js | head
```

Determine the response **field name**, the **success value** (commonly `0` or `200`), and the failure values that trigger a redirect. Validate using a junk session only:

```bash
curl -sk -X POST -H 'Cookie: <fakekey>=junk' https://target/api/<protected> -d '{}' -H 'Content-Type: application/json'
```

Record the result in `neutralize.fields` and `neutralize.success`.

### A3. Content gate — Where do menus and permissions come from?

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl|resource|nav)[^"]*"' js | sort -u
grep -rhoaE '.{0,30}(menus|permissions|menuList|routeList|authList|role_permissions)\b.{0,120}' js | head
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|hasPermission|checkAuth' js | head
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head
```

Many enterprise applications use two permission layers:

| Source | Typical payload | Consumer |
|---|---|---|
| `.../role_permissions` | `{ permissions: string[], role_type }` | Route guards and control-level ACLs |
| `.../permissions/all` | `tree[{ code, position, children }]` | Sidebar menu rendering |
| Bundle `userRouteAuth` | `{ CODE: { url, name? } }` | Permission code to frontend path |
| Bundle `routeMap` | `{ KEY: { name, link } }` | Alias resolution such as webpack `o.DASHBOARD` |

Read the consumer to learn how `getResultTree(tree, permissions)` filters entries and which field `v-if` or `hasAuth(code)` checks.

For a small site, manually construct a permissive payload and add it to `stubs`. For a large site whose sidebar or modules remain blank, follow section I to restore the complete permission tree.

---

## B. `config.json` template

```json
{
  "baseUrl": "https://target/",
  "runtimeMode": "both",
  "chromium": "/usr/bin/chromium",

  "cookies": [
    { "name": "auth", "value": "b64json:{\"id\":1,\"username\":\"admin\",\"role\":\"admin\",\"func\":{},\"permissions\":[\"*\"]}" }
  ],
  "localStorage": { "token": "faketoken", "isLogin": "1" },

  "neutralize": {
    "fields": ["response_code", "code", "errno", "ret", "status"],
    "success": 0,
    "flags": { "success": true, "message": "ok" }
  },
  "forward": true,
  "loginUrlPattern": "/login",
  "apiPattern": "/api/|/rest/|/graphql",

  "mockTier": "L1+L2",
  "recordDetail": true,
  "observe": {
    "storageReads": false,
    "cookieReads": false,
    "xhrHeaders": true
  },
  "neutralizeVueRouter": true,
  "stubs": [
    {
      "match": "permissions/all|/menu|role_permissions",
      "body": {
        "response_code": 0, "code": 0,
        "data": {
          "permissions": ["*"],
          "menus": [
            { "name": "dashboard", "path": "/dashboard", "show": true, "children": [] },
            { "name": "alert", "path": "/alert", "show": true, "children": [] }
          ]
        }
      }
    }
  ],

  "explore": {
    "clickTabs": true,
    "clickTables": true,
    "pushStateFallback": true,
    "maxMenuItems": 50
  },

  "routes": ["/dashboard", "/alert", "/asset", "/device", "/report", "/config", "/system"],
  "waitMs": 1500, "perRouteMs": 900, "headless": true,
  "waitUntil": "domcontentloaded",
  "routeTimeout": 12000,
  "proxy": "",

  "captureResponses": true, "recordWs": true, "respMax": 600
}
```

Field notes:

- `runtimeMode`: `depth` (Puppeteer), `coverage` (browser MCP), or `both`.
- `cookies[].value` prefixes: `b64json:` means base64-encoded JSON; `json:` means raw JSON; no prefix means a literal.
- `forward: true` forwards the real request and rewrites the code field; `false` uses fully offline stubs.
- `mockTier` selects preload layers in coverage mode, such as `L1+L2` or `L1+L2+L3`.
- `routes` comes from `routes.txt`; after a forged menu is mounted, the harness also adds `<a href>` values.
- `captureResponses` and `recordWs` apply only in depth mode.
- For large SPAs, use `waitUntil: domcontentloaded` to avoid a `networkidle2` hang.
- `routeTimeout` is the per-route `page.goto` timeout in milliseconds.
- `proxy` becomes Puppeteer's `--proxy-server`; `HTTP_PROXY` and `HTTPS_PROXY` are also supported.

### B1. Dual-stub template (`role_permissions` and `permissions/all`)

```json
"stubs": [
  {
    "match": "role_permissions",
    "body": {
      "response_code": 0,
      "data": {
        "permissions": ["MONITOR", "MONITOR_ALERT", "THREAT", "ASSETS_RISK"],
        "role_type": "SUPER_ADMIN"
      }
    }
  },
  {
    "match": "permissions/all",
    "body": {
      "response_code": 0,
      "data": [
        {
          "code": "MONITOR",
          "position": 1,
          "children": [
            { "code": "MONITOR_ALERT", "position": 1, "children": [] }
          ]
        }
      ]
    }
  }
]
```

The outer field names (`response_code`, `code`, and `data`) must match the A2 interceptor gate. `permissions` must cover every leaf code in the tree.

---

## C. Coverage mode: preload configuration

Edit the `CONFIG` object at the top of `scripts/preload.js`, or replace it before CDP injection:

```javascript
const CONFIG = {
  loginPathRe: /\/(login|signin)(\/|$|\?)/i,
  mockTier: 'L1+L2',
  forward: true,
  recordDetail: true,
  extractUrlsFromResponse: true,
  neutralizeVueRouter: true,
  observe: { storageReads: false, cookieReads: false, xhrHeaders: true },
  neutralize: { fields: ['response_code', 'code'], success: 0 },
  stubs: [ /* same entries as config.json stubs */ ],
  apiPattern: /\/(api|apis|v\d+|dev|internal|graphql)\//i,
};
```

Verify `window.__API_RECON_PRELOAD__ === true` and that the pathname remains stable.

Export the recorded results:

```javascript
JSON.stringify({
  apis: [...window.__API_RECON_LOG__],
  detail: window.__API_RECON_DETAIL__,
  routes: [...(window.__API_RECON_ROUTES__ || [])],
  observe: window.__API_RECON_OBSERVE__,
}, null, 2)
```

---

## D. Preload and runtime hook coverage

| Hook capability | Recon value | Coverage |
|---|---|---|
| Hook fetch and `XHR.open` | Record request URLs and methods | Supported through `recordDetail` and `__API_RECON_LOG__` |
| Hook `XHR.setRequestHeader` | Discover Authorization and similar headers | Supported through `observe.xhrHeaders` |
| Hook localStorage and cookie reads | Confirm session-key names | Optional through `observe.storageReads/cookieReads` |
| Obtain Vue routes | Complete `frontendRoutes` | `__API_RECON_ROUTES__` for loaded routes |
| Neutralize Vue guards and login redirects | Mount modules so they emit APIs | `neutralizeVueRouter` plus native-navigation neutralization |
| Obtain React routes | Complete routes | Static analysis and clicks; no dedicated hook |
| Block page navigation to login | Keep the page available for analysis | Only blocks the login path so business navigation still works |
| Hook CryptoJS, SM, or similar encryption | Recover plaintext API bodies before encryption | Must be implemented manually; document the hook in the configuration |
| Anti-debugging bypass | Needed when runtime instrumentation is blocked | Must be implemented manually; static analysis remains available |

---

## E. Endpoint regex when static output is too small

Broaden `extract_endpoints` in `harvest_static.py`, or use:

```bash
grep -rhoaE '"/[a-z][A-Za-z0-9_/\-]{3,}"' js | sort -u
grep -rhoaE '/api/[a-zA-Z0-9_./-]+' js | sort -u
```

---

## F. Troubleshooting

| Symptom | Likely cause and response |
|---|---|
| Very few static APIs | Endpoint dialect mismatch; broaden the section E regex |
| Chunk count is much smaller than the manifest | Some chunks are CSS-only or undeployed; ensure 404 retries completed |
| Runtime still shows login | Recheck A1 storage key, container, encoding, and domain |
| Shell loads but modules are blank | Content gate failed; forge the A3 menu and verify route paths |
| Every route has only bootstrap/locale traffic | Permission codes are incomplete; restore the section I tree and check both stubs |
| Sidebar exists but child pages are blank | Tree lacks intermediate nodes or codes disagree with `userRouteAuth` |
| Every API redirects to login | Recheck `neutralize`; nested code fields may require recursive walking |
| No WebSocket frames | Subscription may require interaction; increase `perRouteMs` |
| Empty response bodies | Real response bodies exist only with `forward: true` |
| Chromium missing | Install Chromium or set `config.chromium` / `CHROMIUM` |
| Many mocks but still redirected | Hook ran too late or lacks a `location.href` setter; inject preload at document start |
| Lists are empty | Expected with L3 empty arrays; continue through tabs, settings, and details |
| Redux actions mistaken for routes | Exclude internal paths containing get/set/change/clear/toggle/upload |
| Vue still redirects | Inject preload at document start, or manually remove guards when `neutralizeVueRouter` is false |
| Response contains URLs absent from the log | Enable `extractUrlsFromResponse` or inspect `__API_RECON_DETAIL__` |
| Authorization header name unknown | Enable `observe.xhrHeaders` or inspect DevTools request headers |
| Runtime is extremely slow or times out | Use `waitUntil: domcontentloaded`, reduce `routeTimeout`, and avoid `networkidle2` |
| Proxy connection fails | Verify `proxy` and environment variables; Puppeteer and curl must use the same proxy port |

---

## G. Hardened targets

Runtime may stop at the login shell when the server progressively validates sessions, for example with an unforgeable signed cookie or server-rendered menu that cannot be stubbed.

- Static analysis remains sufficient for endpoint enumeration because module paths are still present in code.
- If the authorization permits use of a real session, run the same harness with `forward: true` and no neutralization to capture real methods, parameters, and responses.

---

## H. Per-task checklist

1. Confirm the authorized scope.
2. Read and adapt `scripts/harvest_static.py`, run it, and review `api_static.txt` and `routes.txt`.
3. Complete Phase 1b: expand around path anchors and follow the binding layer into `param_candidates.json` using section J.
4. Reverse A1, A2, and A3 and write a target-specific `config.json`.
5. Read and adapt `runtime_harvest.js` and `preload.js` before executing either.
6. For `runtimeMode=depth`, run `npm install`, then run the adapted harvester.
7. For `runtimeMode=coverage/both`, inject the adapted preload at document start, then perform browser MCP enumeration and the parameter trigger matrix.
8. If modules do not render, restore the section I permission tree, patch the stubs, and rerun.
9. Compare multiple parameter samples and infer from errors into `params_merged.json`.
10. Merge into `site_map.json` and `api_merged.txt`, accurately documenting coverage, gaps, and template changes.

---

## I. Permission-tree restoration (expanded Phase 4)

Use this when a simple forged `menus: [{ path, show: true }]` does not mount child modules.

### I1. Locate the authentication module

```bash
grep -l 'userRouteAuth' js/*.js
grep -l 'routeMap\|routeLink' js/*.js
grep -rhoaE 'getResultTree|role_permissions|permissions/all' js | head
```

Record the permission API paths, response-field names, and consuming chunk filenames.

### I2. Extract `routeMap`

```bash
python3 scripts/extract_route_map.py recon/js recon/
# Produces recon/route_map.json
```

If it reports `[!] no routeMap pattern found`, broaden the regex in `extract_route_map.py` or use:

```bash
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head -20
```

### I3. Build the permission tree and stubs

```bash
python3 scripts/build_perm_tree.py recon/js recon/ --config recon/config.json
```

The script:

1. Parses `userRouteAuth={MONITOR:{url:...},...}`, including aliases such as `He=o.DASHBOARD`.
2. Uses `route_map.json` to resolve aliases to real paths.
3. Infers parents from code prefixes, for example `MONITOR_ALERT` to `MONITOR`.
4. Writes `permissions_tree.json`, `permissions_all_stub.json`, and `role_permissions_stub.json`.
5. With `--config`, updates `config.json` stubs and extends `routes`.

Adapt these constants at the top of the script:

- `DEFAULT_ROOTS`: top-level module codes.
- `DEFAULT_PREFIX_PARENT`: `PREFIX_` to parent mappings.
- `DEFAULT_EXTRA_PARENT`: orphan nodes whose parent does not follow a prefix rule.

### I4. Validate stub consistency

```bash
# Permission count should be approximately the userRouteAuth entry count.
wc -l recon/perm_codes_all.txt
# Routes should cover every route_map link.
python3 -c "import json; m=json.load(open('recon/route_map.json')); r=set(json.load(open('recon/config.json'))['routes']); print('missing', [v['link'] for v in m.values() if v['link'] not in r])"
```

### I5. Rerun and compare runtime results

```bash
node recon/runtime_harvest.js recon/config.json
# Compare runtime_api.json counts before and after forging, and check whether
# module APIs appear for routes such as /attack and /asset.
```

| Before successful forging | After successful forging |
|---|---|
| Every route has the same three to five bootstrap calls | Different routes trigger different module APIs |
| Only `/api/locale/language` appears | `/api/web/...` module endpoints appear |
| `routes.txt` has only a few routes | Configuration receives 80–110 or more route-map links |

### I6. If restoration still fails

- In coverage mode, click the sidebar and tabs because permission checks may run only after interaction.
- Compare real authorized API nesting with stub nesting.
- Grep `hasPermission`, `checkRole`, `func.`, and similar control-level guards; extend `role_permissions.permissions`.
- Use static fallback: module paths remain in `api_static.txt`; runtime adds methods and bodies. Preserve untriggered candidates in `param_candidates.json`.

---

## J. Parameter reconstruction (Phases 1b, 5b, and 5c)

This is a method, not a universal script. Find paths with regex; find parameters through anchor context, UI bindings, multi-sample comparison, and error inference.

### J1. Expand from a path anchor to the payload object

```bash
# Anchor on a path discovered in Phase 1.
grep -n '"/api/user/list"' js/*.js
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' js | head
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' js | head
grep -rhoaE '(get|post|put|delete|patch)\([^,]+,\s*\{' js | head
```

### J2. Wrappers and transport forms

```bash
# Axios or a shared request wrapper
grep -rhoaE '(axios|request)\.(get|post|put|delete|patch)\(' js | head
grep -rhoaE 'interceptors\.(request|response)' js | head

# GraphQL
grep -rhoaE '(query|mutation)\s+\w+|gql`|graphql\(' js | head
grep -rhoaE '\$[a-zA-Z_]+\s*:\s*(Int|String|Boolean|\[)' js | head

# FormData and multipart
grep -rhoaE 'FormData|\.append\(' js | head

# Path parameters
grep -rhoaE 'path:\s*"/[^"]*:[^"]+"' js | head
grep -rhoaE 'useParams|route\.params|\$route\.params' js | head
```

### J3. Validation gate: required fields, format, and enumeration

```bash
grep -rhoaE '(required|message|pattern|enum|validator)\s*:' js | head
grep -rhoaE 'yup\.|zod\.|async-validator|Form\.Item|a-form-item|el-form-item' js | head
grep -rhoaE 'rules\s*:\s*\[|name:\s*["\'][a-zA-Z_]+["\']' js | head
grep -rhoaE 'label.*value|options\s*:\s*\[' js | head
```

### J4. Binding layer: form to API

```bash
grep -rhoaE 'onFinish|handleSubmit|getFieldsValue|validateFields' js | head
grep -rhoaE '(pick|omit|transform|dayjs|moment)\(' js | head
```

At runtime, follow DevTools Network request initiators upward from `fetch` or `send` to the payload builder.

### J5. Encrypted parameters

```bash
grep -rhoaE 'encrypt|decrypt|sign|CryptoJS|sm2|sm3|sm4|RSA|AES' js | head
```

Do not guess fields from ciphertext. Hook the encryption function's **input** and record the plaintext payload before encryption. Document the result in `config.json` or `param_candidates.json`.

### J6. Parameter trigger matrix (required in Phase 3)

Record each action type for each module and compare body/query samples:

| Action | Evidence to compare |
|---|---|
| Initial list | Default pagination values |
| Search | Keyword and filters |
| Advanced filtering | Optional fields |
| Create/edit | Complete entity |
| Bulk/export | `ids[]` and `exportType` |
| Sort/page | `sortField` and `order` |

Produce `param_samples.json` entries such as `[{ "path", "method", "action": "search", "body", "query", "headers" }]`.

### J7. Confidence rules

| Confidence | Condition |
|---|---|
| **High** | Static callsite and at least two consistent runtime samples |
| **Medium** | Static evidence only, or one runtime sample only |
| **Low** | Inferred from a response/error without a second validation |
| **Not triggered** | Field is statically known but the UI or permission path was not reached |

### J8. Quick paths by scenario

| Scenario | Order |
|---|---|
| REST list page | J1 payload object, four J6 comparisons, then J3 validation rules |
| Create/edit form | J3 form names, J4 submit chain, runtime submission, then an intentionally omitted field to observe a 400 |
| GraphQL | J2 variable declarations, then capture `variables` for each runtime operation |
| Encrypted body | J5 input hook; pre-encryption fields are the real parameters |

### J9. Mapping to api-recon phases

| api-recon phase | Parameter-recon work |
|---|---|
| Phase 1 static | J1 anchor expansion |
| Phase 2 A2 interceptor | Global injected fields such as `tenantId` and signatures |
| Phase 3 runtime | J6 trigger matrix and `param_samples.json` |
| Phase 4 permission tree | Sufficient permissions are needed to trigger all module forms and fields |
| Phase 5 merge | `params_merged.json` plus confidence; never infer required status from one sample |

### J10. Troubleshooting

| Symptom | Response |
|---|---|
| Field is static but never appears at runtime | Mark it `not triggered`; restore permissions, open advanced filters, and exercise linked select options |
| Same path has different body shapes | Expected; record separate entries by `action` instead of forcing one schema |
| Response is stubbed but parameters are needed | Inspect the real outbound body and headers; do not infer from the stub response |
| A 400 names a nested field | Account for outer `data`, `bizData`, or `variables` wrappers |
| GraphQL shows only the operation name | Expand the `variables` JSON and locate `$var: Type` declarations statically |

---
