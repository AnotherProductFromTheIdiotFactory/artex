package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// assetInterceptCandidates extracts domain/IP/URL candidates from an asset input
// for interception. It classifies the URL host so URL-only services/endpoints can
// still match domain or IP rules.
func assetInterceptCandidates(item assetInputItem) (domains, ips, urls []string) {
	add := func(dst *[]string, s string) {
		if s = strings.TrimSpace(s); s != "" {
			*dst = append(*dst, s)
		}
	}
	add(&domains, item.Domain)
	for _, d := range item.BoundDomains {
		add(&domains, d)
	}
	add(&ips, item.IP)
	add(&ips, item.ServiceIP)
	add(&urls, item.URL)
	if item.URL != "" {
		if u, err := url.Parse(item.URL); err == nil {
			if h := u.Hostname(); h != "" {
				if net.ParseIP(h) != nil {
					add(&ips, h)
				} else {
					add(&domains, h)
				}
			}
		}
	}
	return domains, ips, urls
}

// assetInputLabel returns a short identifier for interception messages.
func assetInputLabel(item assetInputItem) string {
	typ := strings.TrimSpace(item.Type)
	var target string
	switch {
	case strings.TrimSpace(item.Domain) != "":
		target = strings.TrimSpace(item.Domain)
	case strings.TrimSpace(item.URL) != "":
		target = strings.TrimSpace(item.URL)
	case strings.TrimSpace(item.IP) != "":
		target = strings.TrimSpace(item.IP)
	case strings.TrimSpace(item.ServiceIP) != "":
		target = strings.TrimSpace(item.ServiceIP)
	default:
		target = "(unknown)"
	}
	if typ != "" {
		return fmt.Sprintf("[%s] %s", typ, target)
	}
	return target
}

// =====================================================================
// Unified asset insertion tools
// =====================================================================

// SetAssetStore wires the asset store and company store onto this ToolSet
// so the insert_assets, add_company_scope, and list_assets tools are active.
func (t *ToolSet) SetAssetStore(as *db.AssetStore, cs *db.CompanyStore) {
	t.as = as
	t.cs = cs
}

// assetInputItem is one element of the insert_assets "assets" array.
type assetInputItem struct {
	Type string `json:"type"` // root_domain|ip|subdomain|app|service|endpoint

	// ---- root_domain / subdomain ----
	Domain      string   `json:"domain"`
	ICP         string   `json:"icp"`
	RecordType  string   `json:"record_type"`
	RecordValue []string `json:"record_value"`

	// ---- ip ----
	IP           string           `json:"ip"`
	BoundDomains []string         `json:"bound_domains"`
	OpenPorts    []db.PortService `json:"open_ports"`

	// ---- app ----
	AppName     string `json:"app_name"`
	BundleID    string `json:"bundle_id"`
	Category    string `json:"category"`
	Description string `json:"description"`
	AppICP      string `json:"app_icp"`
	CompanyID   *int64 `json:"company_id"` // explicit company link (app only; others auto-attribute via scope)

	// ---- service (http) ----
	URL           string           `json:"url"`
	Technologies  []string         `json:"technologies"`
	StatusCode    *int             `json:"status_code"`
	ContentLength *int64           `json:"content_length"`
	PageTitle     string           `json:"page_title"`
	FaviconMMH3   string           `json:"favicon_mmh3"`
	Auth          []map[string]any `json:"auth"`
	ServiceName   string           `json:"service_name"`
	ServiceIP     string           `json:"service_ip"` // optional enrichment IP

	// ---- service (other) ----
	Port  int    `json:"port"`
	Proto string `json:"proto"`

	// ---- endpoint ----
	Method string           `json:"method"`
	Params []map[string]any `json:"params"`
}

// insertAssets is the unified insert_assets agent tool.
func (t *ToolSet) insertAssets() actool.CoreTool {
	return writeTool(
		"insert_assets",
		"Register newly discovered assets in a batch; different types may be mixed (see the type enum).\n"+
			"Required fields: root_domain -> domain; ip -> ip (IPv4/IPv6, never a hostname); subdomain -> domain; app -> app_name; HTTP service -> url; non-HTTP service -> service_name + port with at least one of ip/domain; endpoint -> url + method. See field descriptions for the rest.\n"+
			"auth, technologies, and params are appended and merged; they do not replace existing values.\n"+
			"Returns {results:[{index,id,type}], errors:[{index,error}]}",
		obj(map[string]any{
			// task_id is not model-visible; SetTaskID supplies the authoritative task.
			"assets": map[string]any{
				"type":        "array",
				"description": "Asset array; each item is one asset record",
				"items": obj(map[string]any{
					"type": map[string]any{
						"type":        "string",
						"enum":        []string{"root_domain", "ip", "subdomain", "app", "service", "endpoint"},
						"description": "Asset type",
					},
					// root_domain / subdomain
					"domain":      str("Root domain or subdomain; required for root_domain/subdomain"),
					"icp":         str("Optional ICP registration number"),
					"record_type": str("Optional DNS record type for a subdomain, such as A/AAAA/CNAME/MX"),
					"record_value": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Optional DNS values for a subdomain, such as [\"1.2.3.4\",\"2.3.4.5\"]",
					},
					// ip
					"ip": str("IPv4/IPv6 address, never a hostname (use type=subdomain and domain for hostnames); required for ip and optional for service/endpoint association"),
					"bound_domains": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Optional domains bound to this IP",
					},
					"open_ports": map[string]any{
						"type":        "array",
						"description": "Optional open-port list for an IP",
						"items": obj(map[string]any{
							"port":    intp("Port number"),
							"service": str("Optional service name, such as http/ssh/mysql"),
						}, "port"),
					},
					// app
					"app_name":    str("Application name; required for app"),
					"bundle_id":   str("Optional application bundle ID"),
					"category":    str("Optional application category"),
					"description": str("Optional application description"),
					"app_icp":     str("Optional application ICP registration"),
					"company_id":  intp("Optional owning-company ID for an app; apps cannot be auto-attributed by scope, so provide the ID returned by add_company_scope"),
					// service (http)
					"url":         str("Complete URL including scheme and port; required for an HTTP service; service_type becomes http automatically"),
					"status_code": intp("Optional HTTP response status, such as 200/301/403/404"),
					"content_length": map[string]any{
						"type":        "integer",
						"description": "Optional HTTP response-body size in bytes",
					},
					"page_title":   str("Optional page <title> content"),
					"favicon_mmh3": str("Optional favicon MMH3 hash"),
					"technologies": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Optional fingerprints/technologies, such as [\"Nginx\",\"Vue\",\"Bootstrap\"]",
					},
					"auth": map[string]any{
						"type":        "array",
						"description": "Optional discovered authentication records with fields such as type/username/password; appended, not replaced",
						"items":       map[string]any{"type": "object"},
					},
					// service (non-HTTP)
					"service_name": str("Service name, such as ssh/mysql/redis; required for a non-HTTP service"),
					"port":         intp("Port number; required for a non-HTTP service"),
					// endpoint
					"method": str("HTTP method, such as GET/POST/PUT/PATCH/DELETE; required for endpoint"),
					"params": map[string]any{
						"type":        "array",
						"description": "Optional request parameters with location (query/body/header/path), name, value, and type; appended, not replaced",
						"items":       map[string]any{"type": "object"},
					},
				}, "type"),
			},
		}, "assets"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("insert_assets is unavailable: AssetStore is not initialized"), nil
			}
			var a struct {
				Assets []assetInputItem `json:"assets"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("invalid input: " + err.Error()), nil
			}
			// The program assigns task_id through SetTaskID; model input cannot omit or
			// misassign ownership. Task-independent callers use t.taskID=0.
			taskID := t.taskID

			type result struct {
				Index int    `json:"index"`
				ID    int64  `json:"id"`
				Type  string `json:"type"`
			}
			type errEntry struct {
				Index int    `json:"index"`
				Error string `json:"error"`
			}

			var results []result
			var errs []errEntry

			// Load asset-gate rules once. A read failure skips gating without blocking
			// insertion. Block rules are global plus task-local; allow rules are task-local.
			blockRules, _ := t.as.ListAssetInterceptRules()
			var allowRules []db.AssetInterceptRule
			if t.taskID > 0 {
				if tb, ta, err := t.as.TaskInterceptRulesSplit(t.taskID); err == nil {
					blockRules = append(blockRules, tb...)
					allowRules = ta
				}
			}

			for i, item := range a.Assets {
				// Apply blocks before allows; rejected assets skip Upsert and all side effects.
				domains, ips, urls := assetInterceptCandidates(item)
				if d := db.EvaluateAssetGate(blockRules, allowRules, domains, ips, urls); !d.Allowed {
					errs = append(errs, errEntry{
						Index: i,
						Error: fmt.Sprintf("asset %s %s; insertion is prohibited", assetInputLabel(item), d.Reason),
					})
					continue
				}

				typ := strings.TrimSpace(item.Type)
				var id int64
				var err error

				switch typ {
				case "root_domain":
					id, err = t.as.UpsertRootDomain(db.UpsertRootDomainReq{
						Domain: item.Domain,
						ICP:    item.ICP,
						TaskID: taskID,
					})

				case "ip":
					id, err = t.as.UpsertIP(db.UpsertIPReq{
						IP:           item.IP,
						BoundDomains: item.BoundDomains,
						OpenPorts:    item.OpenPorts,
						TaskID:       taskID,
					})

				case "subdomain":
					id, err = t.as.UpsertSubdomain(db.UpsertSubdomainReq{
						Domain:      item.Domain,
						RecordType:  item.RecordType,
						RecordValue: item.RecordValue,
						ICP:         item.ICP,
						TaskID:      taskID,
					})

				case "app":
					id, err = t.as.UpsertApp(db.UpsertAppReq{
						Name:        item.AppName,
						BundleID:    item.BundleID,
						Category:    item.Category,
						Description: item.Description,
						ICP:         item.AppICP,
						CompanyID:   item.CompanyID,
						TaskID:      taskID,
					})

				case "service":
					// distinguish HTTP vs other by presence of url
					if item.URL != "" {
						// agent may send "ip" or "service_ip" for the enrichment IP; accept both
						svcIP := item.ServiceIP
						if svcIP == "" {
							svcIP = item.IP
						}
						id, err = t.as.UpsertHTTPService(db.UpsertHTTPServiceReq{
							URL:           item.URL,
							Technologies:  item.Technologies,
							StatusCode:    item.StatusCode,
							ContentLength: item.ContentLength,
							PageTitle:     item.PageTitle,
							FaviconMMH3:   item.FaviconMMH3,
							Auth:          item.Auth,
							IP:            svcIP,
							TaskID:        taskID,
						})
					} else {
						id, err = t.as.UpsertOtherService(db.UpsertOtherServiceReq{
							Domain:      item.Domain,
							IP:          item.IP,
							Port:        item.Port,
							ServiceName: item.ServiceName,
							Auth:        item.Auth,
							TaskID:      taskID,
						})
					}

				case "endpoint":
					id, err = t.as.UpsertEndpoint(db.UpsertEndpointReq{
						URL:    item.URL,
						Method: item.Method,
						Params: item.Params,
						IP:     item.ServiceIP,
						TaskID: taskID,
					})

				default:
					errs = append(errs, errEntry{Index: i, Error: "unknown type: " + typ})
					continue
				}

				if err != nil {
					errs = append(errs, errEntry{Index: i, Error: err.Error()})
					continue
				}
				results = append(results, result{Index: i, ID: id, Type: typ})
				t.writes.Assets++
				t.anchorOwner(id)
				if taskID > 0 {
					var sourceNodeID *int64
					if t.ownerNode > 0 {
						nodeID := t.ownerNode
						sourceNodeID = &nodeID
					}
					summary := "Agent registered through insert_assets"
					if t.ownerNode > 0 {
						summary = fmt.Sprintf("Worker intent #%d registered through insert_assets", t.ownerNode)
					}
					_ = t.as.SetTaskAssetSource(taskID, id, "agent", summary, sourceNodeID)
				}
				// Auto-scope only the explicit top-level worker item at conservative
				// granularity. Derived side-effect assets bypass this path, preventing scope
				// expansion. task_scope remains the authorization/filter boundary regardless
				// of whether coverage metrics use it as a denominator.
				{
					svcIP := item.ServiceIP
					if svcIP == "" {
						svcIP = item.IP
					}
					_ = t.as.AddAutoScope(taskID, typ, item.Domain, item.URL, svcIP)
				}
			}

			return jsonResult(map[string]any{
				"results": results,
				"errors":  errs,
			})
		},
	)
}

// addCompanyScope writes to company_scope table and triggers asset attribution.
func (t *ToolSet) addCompanyScope() actool.CoreTool {
	return writeTool(
		"add_company_scope",
		"Add domains, IPs, CIDRs, ICP registrations, or company keywords to a company's asset scope. Domains, networks, and ICP values automatically claim matching assets; keywords are scope hints for agents only.\n"+
			"Company names are unique: create the company if absent or reuse it and merge scope if present.\n"+
			"Provide one scope item per line; the system detects a root domain, URL, single IP, CIDR, ICP registration, or company keyword.\n"+
			"Always provide reason with ownership evidence such as WHOIS, certificate, or ASN data.\n"+
			"Guardrails reject bare TLDs and overly broad networks (IPv4 /16-/32, IPv6 /32-/128). Invalid lines are skipped and returned in errors.",
		obj(map[string]any{
			"company": str("Unique company name; create if absent or reuse if present"),
			"scope":   str("Asset scope, one item per line: domain / URL / IP / CIDR / ICP registration / company keyword"),
			"reason":  str("Required ownership basis, evidence, or source"),
			"logo":    str("Optional company logo URL; used only when creating the company"),
		}, "company", "scope"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.cs == nil {
				return actool.Errorf("add_company_scope is unavailable: CompanyStore is not initialized"), nil
			}
			var a struct {
				Company string `json:"company"`
				Scope   string `json:"scope"`
				Reason  string `json:"reason"`
				Logo    string `json:"logo"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if strings.TrimSpace(a.Company) == "" {
				return actool.Errorf("company must not be empty"), nil
			}
			companyID, _, err := t.cs.UpsertCompany(a.Company, a.Logo)
			if err != nil {
				return actool.Errorf("failed to create or retrieve company: " + err.Error()), nil
			}
			lines := splitLines(a.Scope)
			added, skipped, invalid, errMsgs := t.cs.AddScope(companyID, lines, a.Reason)
			out := map[string]any{
				"company_id": companyID,
				"added":      added,
				"skipped":    skipped,
				"invalid":    invalid,
			}
			if len(errMsgs) > 0 {
				out["errors"] = errMsgs
			}
			return jsonResult(out)
		},
	)
}

// addTaskScope lets the plan agent add test scope to THE CURRENT TASK — the coverage
// denominator and the task's authorization edge. Worker discoveries are auto-scoped
// (precise host) by insertAssets; this tool is for DELIBERATELY WIDENING: pull a whole
// root domain or whole company into scope, or add a specific subdomain / ip.
func (t *ToolSet) addTaskScope() actool.CoreTool {
	return writeTool(
		"add_task_scope",
		"Add test scope to **the current task**. This is the task's authorization boundary and the denominator for asset-test coverage.\n"+
			"Supported kind values: company (all company assets), root_domain (root plus all subdomains), subdomain (one exact subdomain), ip, cidr, icp, keyword.\n"+
			"Workers automatically add each encountered host at exact-host granularity. This tool **deliberately widens scope** to a root domain/company or adds a named subdomain/IP, so use only explicit authorization.\n"+
			"For company, value is an existing company name or ID; for root_domain/subdomain, a domain; for ip/cidr, an address/range; for icp/keyword, the registration or company keyword.\n"+
			"Always provide an auditable reason. Use entries for multiple items.",
		obj(map[string]any{
			"entries": map[string]any{"type": "array", "description": "Batch [{kind, value}], where kind is company/root_domain/subdomain/ip/cidr/icp/keyword", "items": map[string]any{"type": "object"}},
			"kind":    str("Single item: company / root_domain / subdomain / ip / cidr / icp / keyword"),
			"value":   str("Single item: company name or ID / domain / IP / CIDR / ICP / keyword"),
			"reason":  str("Required auditable basis for adding scope"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("add_task_scope is unavailable: AssetStore is not initialized"), nil
			}
			if t.taskID <= 0 {
				return actool.Errorf("add_task_scope requires task context; no task is active"), nil
			}
			type scopeEntry struct {
				Kind  string `json:"kind"`
				Value string `json:"value"`
			}
			var a struct {
				Entries    []scopeEntry `json:"entries"`
				scopeEntry              // Single-item mode.
				Reason     string       `json:"reason"`
			}
			_ = json.Unmarshal(in, &a)
			items := a.Entries
			if len(items) == 0 {
				items = []scopeEntry{a.scopeEntry}
			}
			var added []map[string]any
			errs := map[string]string{}
			for i, e := range items {
				ts, err := t.as.AddAgentScope(t.taskID, strings.TrimSpace(e.Kind), e.Value, a.Reason, "agent")
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				added = append(added, map[string]any{"kind": ts.Kind, "domain": ts.Domain, "net": ts.Net, "value": ts.Value, "company_id": ts.CompanyID})
			}
			out := map[string]any{"added": added}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		},
	)
}

// listUntestedAssets lets the plan agent pull the current + directly inherited
// scope's not-yet-tested assets on demand (filter by type, paginated).
func (t *ToolSet) listUntestedAssets() actool.CoreTool {
	return readTool(
		"list_untested_assets",
		"List assets in the current task and directly related task scopes that are not yet covered by a fact anchor. Related scope is read-only context for your decision; the tool does not decide whether to test.\n"+
			"Optionally filter by root_domain/subdomain/service/app/endpoint/ip.\n"+
			"Pagination starts at page 1 with page_size 10 by default. Returns {assets:[{id,type,label}], total, page, page_size}. Available only in task context.",
		obj(map[string]any{
			"type":      str("Optional asset-type filter: root_domain/subdomain/service/app/endpoint/ip"),
			"page":      intp("Page number starting at 1; default 1"),
			"page_size": intp("Items per page; default 10"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("list_untested_assets is unavailable: AssetStore is not initialized"), nil
			}
			if t.taskID <= 0 || t.ts == nil {
				return actool.Errorf("list_untested_assets requires task context"), nil
			}
			var a struct {
				Type     string `json:"type"`
				Page     int    `json:"page"`
				PageSize int    `json:"page_size"`
			}
			_ = json.Unmarshal(in, &a)
			if a.Page <= 0 {
				a.Page = 1
			}
			if a.PageSize <= 0 {
				a.PageSize = 10
			}
			offset := (a.Page - 1) * a.PageSize
			assets, total, err := t.as.ListUntestedAssetsWithSources(t.taskID, strings.TrimSpace(a.Type), a.PageSize, offset)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(map[string]any{
				"assets": assets, "total": total, "page": a.Page, "page_size": a.PageSize,
			})
		},
	)
}

// listAssets lets an agent query the asset table.
func (t *ToolSet) listAssets() actool.CoreTool {
	return readTool(
		"list_assets",
		"Query the asset store with a DSL expression or retrieve by id/ids, with pagination. Returns only assets in the current task and directly related task scopes.\n"+
			"DSL: field=value for fuzzy ILIKE; field==value exact; field!=value exclusion; numeric fields support > >= < <=; a bare word is full-text fuzzy search. Combine with AND/OR (AND has higher precedence) and parentheses. Supply asset type through the separate type parameter, not the DSL.\n"+
			"When id/ids is absent, dsl must be nonempty; unconditional full-store queries are forbidden.\n"+
			"Fields: domain (root/subdomain/service domain), root_domain, ip, url, page_title, icp, service_name, app_name, method (GET/POST, etc.), service_type (http|other), record_type (A/CNAME, etc.), technology (array; = fuzzy, == exact), and integer port/status_code/company_id.\n"+
			"Examples: status_code>=400 AND technology=shiro ; (port==80 OR port==443) AND technology=nginx",
		obj(map[string]any{
			"dsl":    str(`DSL query expression; see tool description for syntax and fields. Required when id/ids is absent.`),
			"type":   str("Asset-type filter: root_domain|ip|subdomain|app|service|endpoint; may combine with dsl, but type alone is insufficient"),
			"id":     intp("Optional direct lookup by one asset ID; mutually exclusive with dsl/type"),
			"ids":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Optional direct lookup by multiple asset IDs; mutually exclusive with dsl/type"},
			"limit":  intp("Optional result limit; default 10"),
			"offset": intp("Optional pagination offset; default 0"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("list_assets is unavailable: AssetStore is not initialized"), nil
			}
			var a struct {
				DSL    string  `json:"dsl"`
				Type   string  `json:"type"`
				ID     int64   `json:"id"`
				IDs    []int64 `json:"ids"`
				Limit  int     `json:"limit"`
				Offset int     `json:"offset"`
			}
			_ = json.Unmarshal(in, &a)
			if a.Limit <= 0 {
				a.Limit = 10
			}

			var assets []*db.Asset
			var err error
			switch {
			case a.ID > 0:
				assets, err = t.as.GetByIDsInScope(t.taskID, []int64{a.ID})
			case len(a.IDs) > 0:
				assets, err = t.as.GetByIDsInScope(t.taskID, a.IDs)
			case a.DSL != "":
				assets, err = t.as.QueryDSLInScope(a.DSL, a.Type, t.taskID, a.Limit, a.Offset)
			default:
				return actool.Errorf("dsl must not be empty when id/ids is absent; provide a condition because unconditional full-asset queries are forbidden"), nil
			}
			if err != nil {
				return actool.Errorf("DSL error: " + err.Error()), nil
			}
			return jsonResult(map[string]any{
				"count":  len(assets),
				"assets": assets,
			})
		},
	)
}

// listCompanies lets an agent enumerate companies with scope and asset count.
func (t *ToolSet) listCompanies() actool.CoreTool {
	return readTool(
		"list_companies",
		"List companies in the asset store with their scope and attributed asset count. Use it to discover companies and obtain company_id for linking an app in insert_assets or filtering list_assets. Optional search performs a case-insensitive fuzzy company-name filter; empty returns all.",
		obj(map[string]any{
			"search": str("Optional case-insensitive fuzzy company-name filter; empty returns all"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.cs == nil {
				return actool.Errorf("list_companies is unavailable: CompanyStore is not initialized"), nil
			}
			var a struct {
				Search string `json:"search"`
			}
			_ = json.Unmarshal(in, &a)
			cos, err := t.cs.ListCompanies()
			if err != nil {
				return actool.Errorf("failed to query companies: " + err.Error()), nil
			}
			q := strings.ToLower(strings.TrimSpace(a.Search))
			type companyOut struct {
				ID         int64    `json:"id"`
				Name       string   `json:"name"`
				AssetCount int      `json:"asset_count"`
				Scope      []string `json:"scope"`
			}
			out := make([]companyOut, 0, len(cos))
			for _, c := range cos {
				if q != "" && !strings.Contains(strings.ToLower(c.Name), q) {
					continue
				}
				scope := make([]string, 0, len(c.Scope))
				for _, r := range c.Scope {
					scope = append(scope, r.Raw)
				}
				out = append(out, companyOut{ID: c.ID, Name: c.Name, AssetCount: c.AssetCount, Scope: scope})
			}
			return jsonResult(map[string]any{"count": len(out), "companies": out})
		},
	)
}

// splitLines splits a multi-line string into non-empty trimmed lines.
func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// WorkerTools returns the tool set for a work agent.
func (t *ToolSet) WorkerTools() []actool.CoreTool {
	return []actool.CoreTool{
		// Keep list_findings so workers avoid duplicate reports.
		t.listFindings(),
		t.addFinding(), t.recordFact(),
		// Asset management; handlers guard nil stores. Company scope belongs to
		// planner/main/Auto, not a single-intent worker.
		t.insertAssets(), t.listAssets(),
		// Cross-work review lets workers reuse observations. search_all_worker_traces
		// finds steps without an intent_id; get_worker_trace inspects a known work.
		t.searchAllWorkerTraces(), t.getWorkerTrace(),
		// node_detail retrieves full details for a known intent/node ID.
		t.nodeDetail(),
		// Keep list_facts/list_companies/list_worker_traces for planner/main; workers
		// execute and persist one intent rather than perform cross-work planning.
	}
}

// MainAgentTools returns the human-interface tool set.
func (t *ToolSet) MainAgentTools() []actool.CoreTool {
	return []actool.CoreTool{
		t.graphOverview(), t.listFindings(), t.listFacts(), t.nodeDetail(),
		t.expandDigest(), // cold-digest §6.1
		t.getWorkerOutput(), t.getWorkerTrace(), t.searchAllWorkerTraces(), t.addHint(), t.addIntent(),
		// Humans can steer running work without interrupting or losing progress.
		t.steerWorkTool(),
		// Humans can add a final goal at runtime for planner reevaluation.
		t.setGoals(),
		// Humans can add/change allow/deny operational constraints.
		t.setConstraints(),
		// asset management (handlers guard nil store internally)
		t.insertAssets(), t.addCompanyScope(), t.listAssets(),
		t.addFinding(), t.recordFact(),
		t.addTaskScope(),
		// Query untested in-scope assets by type/page for planner judgment.
		t.listUntestedAssets(),
	}
}

// AllDomainTools returns the union of all domain tools across all agent types,
// deduped by name (mainagent order wins). Used by the server to build a registry
// for injecting domain tools into agents (Auto, custom) that don't own a per-task
// ToolSet. The caller provides real stores; tools are callable at taskID=0 scope.
func (t *ToolSet) AllDomainTools() []actool.CoreTool {
	seen := map[string]bool{}
	var out []actool.CoreTool
	all := append(append(t.MainAgentTools(), t.PlannerTools()...), t.WorkerTools()...)
	for _, tool := range all {
		if !seen[tool.Name()] {
			seen[tool.Name()] = true
			out = append(out, tool)
		}
	}
	return out
}
