package main

// shipped_defaults_table_test.go — THE TABLE. See shipped_defaults_test.go for the walker
// that derives the population and for how these values were measured.
//
// A row is `<file>::<name>` for a declared default and `<file>::<lhs>#<n>` for the inline
// `if X == "" { X = … }` shape, numbered in source order within the file so two copies of the
// same fallback are two rows rather than one — which is how ws.Plan's defended and undefended
// copies became visible.
//
// THE `census:` MARKER IS PROVENANCE, NOT DECORATION. UNPINNED means the W3.49 mutation run
// changed that value and the ENTIRE suite stayed green; PINNED means a named test went red.
// Rows with no marker were discovered by the walker but were outside the mutated population
// (mostly the wiring rows, where a constant is read into a local), and saying so is cheaper
// than implying a measurement that was not taken.

type recordedDefault struct {
	value string
	note  string
}

var recordedDefaults = map[string]recordedDefault{
	// ── AUTHORISATION. The role a caller who names none is given. These are the
	//    highest-stakes rows in the table and a literals-only census would not have them.
	"internal/guest/handler.go::in.Role#1":    {"GuestRoleViewer", "ROLE a guest invite grants when none is asked for — least privilege, and the only thing holding it there is this line"},
	"internal/member/mgmt_handler.go::role#1": {"authz.RoleMember", "ROLE a member is added with when none is given — not admin, and nothing else says so"},

	// ── BILLING. Two copies of one literal; the census found one defended and one not.
	"internal/workspace/store.go::ws.Plan#1": {`"free"`, "PLAN a workspace is CREATED on. census: PINNED (TestCreate_InsertsWorkspace)"},
	"internal/workspace/store.go::ws.Plan#2": {`"free"`, "PLAN an update with no plan falls back to. census: UNPINNED — the create copy is defended and this one is not"},

	// ── SECURITY BOUNDS.
	"internal/gatewayauth/gatewayauth.go::MinSecretLen":      {"16", "shortest gateway transit secret the auth boundary will defend. census: PINNED"},
	"internal/config/config.go::MinMemberSyncSecretLen":      {"16", "minimum strength of the token gating EVERY tenant's roster. census: PINNED"},
	"internal/config/config.go::IntegrationEncryptionKeyLen": {"32", "AES-256 key length for per-workspace provider tokens. census: PINNED (W3.43's coupling test)"},

	// ── REQUEST CAPS. The bound a request inherits when it names none.
	"internal/httpx/httpx.go::DefaultMaxBody":              {"1 << 20", "body cap EVERY JSON route inherits. census: UNPINNED"},
	"internal/httpx/httpx.go::GitHubWebhookMaxBody":        {"5 << 20", "body cap on the GitHub webhook. census: UNPINNED"},
	"internal/httpx/httpx.go::ImportMaxBody":               {"96 << 20", "body cap on the multipart CSV import — the largest thing this service will read. census: UNPINNED"},
	"internal/integrations/handler.go::maxIntegrationBody": {"1 << 20", "body cap on the provider-token route. census: UNPINNED"},
	"internal/importer/job_handler.go::jobMaxUploadBytes":  {"64 << 20", "upload cap on the async import job. census: UNPINNED"},

	// ── OPERATOR-FACING.
	"internal/guest/handler.go::inviteBaseURL#1":    {`"http://localhost:5173"`, "the prefix stitched into EVERY invite link when TRACK_INVITE_BASE_URL is unset — it is emitted to a human. census: UNPINNED"},
	"internal/importer/linear.go::defaultLinearURL": {`"https://api.linear.app/graphql"`, "the HOST this service talks to when none is configured. census: PINNED (TestLinearDefaultEndpoint_IsPinned)"},
	"internal/importer/linear.go::url#1":            {"defaultLinearURL", "wiring: the constant above is what an unconfigured client uses"},

	// ── DATABASE.
	"internal/db/db.go::DefaultStatementTimeoutMS":       {`"10000"`, "server-side statement timeout every connection carries, in ms. census: UNPINNED"},
	"internal/db/db.go::DefaultConnectTimeout":           {"5 * time.Second", "per-connection dial timeout. census: UNPINNED"},
	"internal/db/db.go::cfg.ConnConfig.ConnectTimeout#1": {"DefaultConnectTimeout", "wiring: the constant above is actually applied to the pool config"},

	// ── RESILIENCE. The breaker that decides whether this service reports itself healthy.
	"internal/dbresil/dbresil.go::DefaultFailureThreshold": {"3", "consecutive failures before the breaker opens. census: UNPINNED"},
	"internal/dbresil/dbresil.go::DefaultProbeInterval":    {"2 * time.Second", "how often a tripped breaker re-probes. census: UNPINNED"},
	"internal/dbresil/dbresil.go::DefaultPingTimeout":      {"2 * time.Second", "ping timeout the breaker judges health on. census: UNPINNED"},
	"internal/dbresil/dbresil.go::failureThreshold#1":      {"DefaultFailureThreshold", "wiring: the constant above is what a zero-valued option falls back to"},
	"internal/dbresil/dbresil.go::interval#1":              {"DefaultProbeInterval", "wiring"},
	"internal/dbresil/dbresil.go::pingTimeout#1":           {"DefaultPingTimeout", "wiring"},

	// ── BACKGROUND WORK AND OUTBOUND CALLS.
	"internal/importer/runner.go::defaultRunnerInterval":       {"2 * time.Second", "importer poll interval. census: UNPINNED"},
	"internal/importer/runner.go::interval#1":                  {"defaultRunnerInterval", "wiring"},
	"internal/importer/providerhttp.go::defaultMaxAttempts":    {"3", "retry budget against a provider API. census: UNPINNED"},
	"internal/importer/providerhttp.go::defaultRequestTimeout": {"20 * time.Second", "per-request timeout to a provider. census: UNPINNED"},
	"internal/importer/jira.go::defaultRetryAfter":             {"time.Second", "backoff used when Jira sends no Retry-After header. census: UNPINNED"},
	"internal/lensintegration/syncer.go::defaultSyncInterval":  {"15 * time.Minute", "how often Track pulls spend from Lens. census: UNPINNED"},
	"internal/lensintegration/syncer.go::interval#1":           {"defaultSyncInterval", "wiring"},

	// ── REPORTING WINDOWS. What period a caller who names none is answered about.
	"internal/analytics/engine.go::defaultWindowDays": {"30", "analytics window in days. census: UNPINNED"},
	"internal/analytics/engine.go::cycles#1":          {"5", "how many cycles a velocity read covers. census: PINNED (TestGetVelocity_TheCycleBoundsAreWired_RealPG)"},
	"internal/lensintegration/client.go::days#1":      {"30", "spend window queried from Lens (1 of 3 identical copies). census: UNPINNED"},
	"internal/lensintegration/client.go::days#2":      {"30", "spend window queried from Lens (2 of 3). census: UNPINNED"},
	"internal/lensintegration/client.go::days#3":      {"30", "spend window queried from Lens (3 of 3). census: UNPINNED"},
	"internal/mcp/server.go::in.Days#1":               {"7", "reporting window an MCP agent that names none gets. census: UNPINNED"},

	// ── PAGE SIZES AND CAPS.
	"internal/member/handler.go::defaultLimit": {"500", "roster page size when none is asked for. census: UNPINNED"},
	"internal/member/handler.go::maxLimit":     {"500", "hard cap on the roster read. census: UNPINNED"},
	"internal/mcp/server.go::in.Limit#1":       {"20", "MCP list_issues page size. census: UNPINNED (its CAP of 100 is pinned; the default beneath it is not)"},
	"internal/mcp/server.go::in.Limit#2":       {"10", "MCP search_issues page size. census: UNPINNED"},
	"internal/issue/store.go::limit#1":         {"10", "issue store page size (1 of 2). census: UNPINNED"},
	"internal/issue/store.go::limit#2":         {"25", "issue store page size (2 of 2). census: UNPINNED"},
	"internal/notification/store.go::limit#1":  {"50", "notification page size. census: PINNED (TestList_ReturnsUnreadFirst)"},
	"internal/scoring/store.go::limit#1":       {"50", "scoring page size. census: UNPINNED"},
	"internal/ai/engine.go::limit#1":           {"25", "how many issues the AI engine reads when no limit is given. census: UNPINNED"},

	// ── INPUTS TO THE AI PLANNER. These reach a model; they are not cosmetic.
	"internal/ai/handler.go::in.TeamSize#1":  {"5", "team size fed to the AI planner when the caller sends none. census: UNPINNED"},
	"internal/ai/handler.go::in.CycleDays#1": {"14", "cycle length fed to the AI planner when the caller sends none. census: UNPINNED"},

	// ── WHAT A NEWLY CREATED RECORD IS.
	"internal/issue/store.go::issue.Status#1":            {"model.StatusBacklog", "status a new issue gets (1 of 2 sites)"},
	"internal/issue/store.go::issue.Status#2":            {"model.StatusBacklog", "status a new issue gets (2 of 2 sites)"},
	"internal/cycle/store.go::c.Status#1":                {`"upcoming"`, "status a new cycle gets. census: PINNED"},
	"internal/milestone/store.go::m.Status#1":            {`"upcoming"`, "status a new milestone gets. census: PINNED"},
	"internal/project/store.go::p.Status#1":              {`"active"`, "status a new project gets. census: PINNED"},
	"internal/workflow/engine.go::status.Category#1":     {"CategoryUnstarted", "category a workflow status with none falls into"},
	"internal/template/store.go::t.DefaultStatus#1":      {`"backlog"`, "status a template with none falls back to. census: UNPINNED"},
	"internal/template/store.go::t.DefaultPriority#1":    {"3", "priority a template with none falls back to. census: UNPINNED"},
	"internal/workspace/store.go::DefaultTeamName":       {`"General"`, "name of the team every new workspace is seeded with. census: UNPINNED"},
	"internal/workspace/store.go::DefaultTeamIdentifier": {`"GEN"`, "identifier prefix every new workspace's issues inherit. census: UNPINNED"},
	"internal/featureboard/store.go::out#1":              {`"board"`, "bucket a feature-board post with none lands in. census: UNPINNED"},
	"internal/scoring/handler.go::method#1":              {"ScoringRICE", "scoring method used when the caller names none"},

	// ── EXPORT AND GROUPING. D17 changes the BYTES a download contains.
	"internal/analytics/handler.go::format#1":  {`"csv"`, "EXPORT FORMAT when none is asked for. census: PINNED (TestExport_ASuccessfulExportStillStreamsItsCSVDownload)"},
	"internal/analytics/handler.go::groupBy#1": {`"status"`, "grouping an analytics read falls back to (1 of 2). census: UNPINNED"},
	"internal/analytics/handler.go::gb#1":      {`"status"`, "grouping fallback (2 of 2, same file). census: UNPINNED"},

	// ── AUDIT AND OBSERVABILITY. Not decoration: these end up in a record of who did what.
	"internal/automation/engine.go::creator#1":       {`"automation"`, "the ACTOR recorded when an automation makes a change. census: UNPINNED"},
	"cmd/track/main.go::path#1":                      {`"unknown"`, "the metrics label an unrouted request is counted under. census: UNPINNED"},
	"internal/featureboard/store.go::p.AuthorName#1": {`"Anonymous"`, "display name on an authorless post. census: UNPINNED"},

	// ── COSMETIC. Recorded because the census is a POPULATION and a gap in it is where the
	//    next default hides — but flagged, because W4.44's rule holds: a cosmetic default
	//    that nothing pins is a true row and not a finding.
	"internal/template/store.go::t.Icon#1":        {`"📋"`, "cosmetic: template icon. census: UNPINNED"},
	"internal/label/store.go::l.Color#1":          {`"#94a3b8"`, "cosmetic: label colour. census: UNPINNED"},
	"internal/team/store.go::t.Color#1":           {`"#6366f1"`, "cosmetic: team colour. census: PINNED (TestCreate_InsertsTeam)"},
	"internal/workflow/engine.go::status.Color#1": {`"#94a3b8"`, "cosmetic: workflow status colour. census: UNPINNED"},
}
