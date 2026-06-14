# Audit findings — extensible-i18n-and-structural-hardening
<!-- source: background audit task waaymn4es (Expanded whole-repo audit: 10 angles, 166 agents, 4.5M tokens, 24.8min, adversarially verified) -->
<!-- generated: 2026-06-14 -->

**156 findings: 106 CONFIRMED, 18 PLAUSIBLE, 32 REFUTED.** Severity (confirmed): {'minor': 7, 'major': 99}.

Angles: sec-authz, sec-custody, sec-injection, mode-correctness, robust-backend, robust-frontend, i18n-userfacing, struct-backend, struct-frontend, struct-crosscutting. mode-correctness produced 0 new confirmed/plausible (the PR#68 compile-gate verified complete) — the remaining mode work is the known PR#68-review items in M4.

## Security (CONFIRMED) — 3

### C1. [major] sec-injection — `internal/renderer/deploy.go:134`
**SSHKeyPath is not escaped for PowerShell double-quote context**

- **Why:** At line 134-135, when generating PowerShell deploy scripts, SSHKeyPath is inserted directly into double-quoted ssh/scp command arguments without PowerShell-specific escaping. A malicious SSHKeyPath like 'C:\path\with"quote.pem' would break out of the double quotes and allow PowerShell command injection. The bash variant uses Go's %q quoting (line 131-132) which is safe, but the PowerShell variant must use powerShellArgQuote() to escape backticks, double-quotes, and dollar signs. This is especially critical since these options are injected into `& ssh` and `& scp` calls in the generated PowerShell script.
- **Verdict:** Confirmed at /home/kunorikiku/source/yet-another-overlay-generator/internal/renderer/deploy.go:133-135. In buildSSHOpts the PowerShell ("dquote") branch interpolates SSHKeyPath via `fmt.Sprintf(`-i "%s"`, node.SSHKeyPath)` (lines 134-135) — bare double quotes with NO PowerShell escaping. The bash branch directly above (lines 131-132) uses `fmt.Sprintf("-i %q", ...)`, where Go's %q yields a properly-escaped bash double-quoted token, so bash is safe; the PowerShell variant is not.  Trace: SSHKeyPa…

### C2. [major] sec-injection — `internal/validator/schema.go:302`
**SSHKeyPath field is not validated for unsafe characters**

- **Why:** Unlike ssh_host, ssh_alias, and ssh_user which are validated against sshFieldCharset (lines 290-301), SSHKeyPath is never validated. It accepts arbitrary paths without any charset restrictions. A path containing PowerShell metacharacters (backticks, dollar signs, double quotes) or bash metacharacters can lead to command injection in both bash and PowerShell deploy scripts. The field should be validated to reject or safely handle metacharacters, or at minimum documented as untrusted input that must be sanitized by the renderer.
- **Verdict:** CONFIRMED — real command-injection via unvalidated ssh_key_path into operator-side deploy scripts.  VALIDATOR GAP (internal/validator/schema.go): The per-node SSH charset check (lines 290-301) validates ssh_host, ssh_alias, and ssh_user against sshFieldCharset (`^[A-Za-z0-9._:@-]+$`, line 21), but the node validation block ends at line 302 with NO check for SSHKeyPath. The field is defined at internal/model/topology.go:103 (`ssh_key_path`) and is never charset-restricted anywhere. Compiler.Compi…

### C3. [minor] sec-injection — `internal/renderer/deploy.go:131`
**SSH key path escaping is inconsistent between bash and PowerShell renderers**

- **Why:** The bash renderer uses Go's %q quoting for SSHKeyPath (line 131-132), which properly escapes the value. However, the PowerShell renderer (line 134-135) uses plain double-quote wrapping without powerShellArgQuote() application. This inconsistency is a maintenance risk and has already led to the major vulnerability at line 134. To maintain defense-in-depth, both branches should apply context-appropriate escaping functions consistently.
- **Verdict:** CONFIRMED as a real defect, though the finding understates it. Verified against internal/renderer/deploy.go:128-137, escape.go:44-78, validator/schema.go:288-301, and frontend NodeEditor.tsx:474.  Facts confirmed by reading + an end-to-end reproduction test (RenderDeployScripts with a hostile ssh_key_path): 1. The escaping IS inconsistent: bash branch (deploy.go:131-132) uses fmt.Sprintf("-i %q", node.SSHKeyPath); PowerShell branch (deploy.go:134-135) uses fmt.Sprintf(`-i "%s"`, node.SSHKeyPath)…

## Robustness (CONFIRMED) — 2

### C1. [major] robust-frontend — `frontend/src/stores/controllerStore.ts:944`
**pendingShrink confirmation modal not cleared on deploy error**

- **Why:** When deploy() throws an error after line 892 (during stage/promote/signature), the catch block at lines 944-950 sets error and loading=false but does NOT clear pendingShrink. This leaves the shrink confirmation dialog visible even though the deploy failed, creating confusing UX where the modal appears stuck. User must manually cancel the dialog to proceed.
- **Verdict:** Verified against controllerStore.ts and DeployBar.tsx. Trace: (1) The shrink-confirm modal renders SOLELY on `pendingShrink` being truthy and is a full-screen z-50 overlay (DeployBar.tsx:300-301, `fixed inset-0 z-50 ... bg-black/50`). (2) First deploy() detects shrink, sets pendingShrink (controllerStore.ts:874-885), returns at line 886 → modal appears. (3) User confirms → deploy({confirmedShrink:true}) (DeployBar.tsx:344). (4) On the confirmed call, `confirming` is truthy (line 838-841), so the…

### C2. [minor] robust-frontend — `frontend/src/stores/topologyStore.ts:576`
**JSON parsing error on failed response not handled gracefully**

- **Why:** When res.ok is false at line 574, the code calls await res.json() at line 575. If the response body is not valid JSON (e.g., HTML error page), the JSON.parse will throw a SyntaxError. While this is caught by the outer catch block (line 598), the error message will be 'Compile request failed' rather than the actual error, reducing debuggability. Similar pattern exists in validate/export/downloadDeployScript.
- **Verdict:** Verified against frontend/src/stores/topologyStore.ts. The cited pattern is real and repeats across all four API actions: - compile (line 574-576): `if (!res.ok) { const errData = await res.json(); throw new Error(errData.error || 'Compile failed'); }` - validate (line 536-538): same shape, fallback '校验失败' - exportArtifacts (line 615-617): same shape, fallback '导出失败' - downloadDeployScript (line 651-653): same shape, fallback 'Failed to generate deploy script'  In each, on a non-2xx response the…

## Structural — backend (CONFIRMED) — 2

### C1. [major] struct-backend — `frontend/src/i18n.ts:10`
**Frontend i18n uses inline txt(lang, zh, en) calls with no parameter support or error-code enums**

- **Why:** The i18n module (lines 10-148) provides a bare txt(lang, zh, en) function that returns a single string per language, with no support for: (1) parameterized strings (e.g., 'Loaded {count} nodes' with count substitution), (2) pluralization, (3) error codes (so the API can send an error code and the UI looks up its localized message). Backend errors come as strings (already flagged above), and the frontend has no mechanism to map an error-code enum to translated messages. STRINGS constant is purely static text. Recommendation: Redesign i18n as a key-based system with params: t('nodes.loadFailed', {count: 5}) → returns parameterized message. Backend then sends error codes instead of strings, and…
- **Verdict:** Verified against frontend/src/i18n.ts and its ~280 call sites. The finding's structural description is accurate AND has concrete material consequences:  (1) txt(lang, zh, en) at i18n.ts:10-12 is exactly as described — returns one of two literal strings, no params, no plural, no key/code lookup. STRINGS (i18n.ts:16-148) is purely static [zh,en] tuples.  (2) The lack of an error-code -> localized-message mechanism is not a taste preference; it forces real defects. Backend errors (Chinese-only, per…

### C2. [minor] struct-backend — `internal/allocator/ip.go:1`
**IP allocator error messages are inconsistently formatted (mix of complete and truncated text)**

- **Why:** Looking at grep results, error messages in allocator/ip.go are malformed: 'CIDR %s  Domain %s ' (incomplete), 'CIDR %s  IP : %w' (incomplete), ' CIDR: %s' (leading space). These appear to be either truncated output from the grep or actual formatting issues in the source. This suggests the error messages may be broken or incomplete, making them confusing to users. Recommendation: Audit the allocator error messages for completeness and consistency.
- **Verdict:** CONFIRMED. The finding's hedged diagnosis ("grep truncation OR real formatting issue") resolves definitively to a real source defect. Hexdump of internal/allocator/ip.go:68 proves the format string is literally `" %s  Domain %s "` (bytes: 22 20 25 73 20 20 44 6f 6d 61 69 6e 20 25 73 20 22) with ZERO multi-byte UTF-8 sequences — the Chinese characters that should form a coherent sentence are simply absent, leaving stray double-spaces, the bare English word "Domain", and the verbs `%s`. Same corru…

## Structural — frontend (CONFIRMED) — 3

### C1. [major] struct-frontend — `frontend/src/i18n.ts:10`
**i18n module uses inline positional parameters (txt(lang, zh, en)) with no key catalog, preventing addition of new languages and backend error localization.**

- **Why:** Current design: `export function txt(lang: UILanguage, zh: string, en: string): string { return lang === 'zh' ? zh : en; }` with hardcoded `UILanguage = 'zh' | 'en'`. This requires: (1) modifying the function signature and every call site to add a 3rd language, (2) creating dual-string tuples everywhere (no reuse across features), (3) impossible to localize backend error codes which are Chinese-only (lines 538, 576 in topologyStore.ts fall back to English strings while backend returns Chinese). To add Japanese or Russian, you'd need to touch 150+ call sites and the type itself. Backend errors from Go handlers (handler.go) are hardcoded Chinese strings ("仅支持 POST 请求", "创建临时目录失败", "导出产物失败") —…
- **Verdict:** Verified against code. frontend/src/i18n.ts:1,10-12 has exactly `export type UILanguage = 'zh' | 'en'` and `export function txt(lang, zh, en): string { return lang === 'zh' ? zh : en; }` — positional dual-string, no key catalog, no param/interpolation support (no %s/{0} placeholders anywhere in the file). STRINGS (lines 16-148) is just a [zh, en] tuple table consumed via the same positional txt(). Concrete extension cost confirmed: adding a 3rd language (e.g. Japanese) requires changing the txt…

### C2. [major] struct-frontend — `frontend/src/i18n.ts:1`
**No parameter interpolation mechanism in i18n; all text strings are static literals, preventing dynamic content localization (e.g., error messages with variable values like node names, counts).**

- **Why:** Cannot localize strings like: "${rekeyingCount} node(s) still rotating keys" (DeployBar.tsx line 99). All 130+ STRINGS entries are static [zh, en] tuples with no placeholder support. Any dynamic message requires inline txt() call with conditional string concatenation in components. This violates DRY and makes third-language addition harder — you'd have to parameterize 150+ existing call sites.
- **Verdict:** Verified against code. i18n.ts:10-12 defines txt(lang, zh, en) which accepts only two pre-formed static strings — there is no placeholder/parameter/interpolation mechanism. All 77 STRINGS entries (lines 16-148) are static [zh, en] `as const` tuples. DeployBar.tsx line 99-100 reproduces exactly: `${rekeyingCount} node(s) still rotating keys` is interpolated directly into a template literal passed inline to txt() because it cannot be expressed in STRINGS. The same dynamic-content workaround recurs…

### C3. [major] struct-frontend — `frontend/src/stores/topologyStore.ts:530`
**Backend error messages are Chinese-only and unreachable by frontend i18n. topologyStore.ts hardcodes fallback English strings (line 538: 'Compile failed', 576: '校验失败') while backend returns Chinese from Go handlers.**

- **Why:** Lines 538, 576, 617, 669 catch errors as plain strings: `throw new Error(errData.error || '校验失败')`. The backend (handler.go lines 78–91) emits Chinese: "仅支持 POST 请求", "创建临时目录失败", "导出产物失败". Frontend has no way to (1) map error codes to keys, (2) localize backend messages, or (3) interpolate parameters. If backend adds a new validation error, the frontend has zero path to localize it. Error strings are entangled in logic rather than separated into a catalog.
- **Verdict:** Verified against code. Frontend topologyStore.ts catches API errors as plain strings with hardcoded, inconsistently-bilingual fallbacks: line 538 `errData.error || '校验失败'` (validate), 576 `|| 'Compile failed'` (compile), 617 `|| '导出失败'` (export), 653 `|| 'Failed to generate deploy script'`, 669 download-fallback. (Note: the finding's WHY swaps 538/576 — 538 is the Chinese `'校验失败'` and 576 is the English `'Compile failed'` — but both lines and the described pattern are exactly real.)  Backend con…

## Structural — cross-cutting (CONFIRMED) — 4

### C1. [major] struct-crosscutting — `internal/api/server.go:116`
**Chinese-only error message in error recovery path; no error-code envelope for frontend branching**

- **Why:** The fallback error "服务器内部错误" (server internal error) is Chinese-only. Backend throws plain `Error(status, message)` with no machine-readable error code — frontend cannot localize or branch on error types. The recoverPanics middleware (line 111-120) catches all unhandled panics and returns this opaque string. Frontend error handlers display raw error.message verbatim (controllerStore.ts:392), expecting English or localized strings.
- **Verdict:** Verified end-to-end against the code. (1) server.go:116 — recoverPanics middleware (lines 106-122) catches every unhandled panic and emits writeError(tracked, http.StatusInternalServerError, "服务器内部错误"), a hardcoded Chinese-only string, exactly as cited. (2) No error-code envelope: writeError (handler.go:578-580) marshals the apiError struct (handler.go:42-45) which has ONLY `Error string` (json:"error") plus an optional `Details any` — there is no machine-readable code/type field anywhere. All 2…

### C2. [major] struct-crosscutting — `internal/api/handler.go:206`
**Chinese-only error messages in export/deploy-script paths**

- **Why:** Lines 206, 212, 218, 267 return Chinese error strings: "创建临时目录失败" (failed to create temp dir), "导出产物失败" (export failed), "打包 ZIP 失败" (ZIP packaging failed), "渲染 Babel 配置失败" (Babel render failed). These are user-facing errors that will appear in the API response. Frontend (controllerStore.ts, DeployBar.tsx) displays error.message directly without localization.
- **Verdict:** All four cited lines are present and accurate in internal/api/handler.go: line 206 "创建临时目录失败", line 212 "导出产物失败: %v", line 218 "打包 ZIP 失败: %v" (in HandleExport), and line 267 "渲染 Babel 配置失败: %v" (in HandleDeployScript). The cited English glosses are correct. These are user-facing: writeError (handler.go:578-580) serializes the message into apiError{Error: message} as JSON. The local-mode frontend store reads errData.error and throws it as the message — exportArtifacts (topologyStore.ts:615-617,…

### C3. [major] struct-crosscutting — `frontend/src/i18n.ts:10`
**i18n module has no error-code dictionary; no mapping between backend error codes and localized strings**

- **Why:** txt() function (line 10) takes (lang, zh, en) inline — every string pair is hardcoded at call sites. There is no centralized error-code -> {zh, en} map. Error messages from the backend (writeError body) arrive as English OR Chinese strings with no code attached — frontend cannot translate them. When backend returns "failed to generate WireGuard keys: <specific error>", the frontend receives the entire error.Error string and displays it verbatim (controllerStore.ts:392,409,420,etc.). A user running zh locale sees English technical errors.
- **Verdict:** Verified against code. (1) i18n.ts:10 — txt(lang, zh, en) is inline; STRINGS (lines 16-148) is a flat dictionary of hardcoded [zh,en] *UI label* pairs only. There is NO error-code -> {zh,en} map anywhere. (2) Backend has no error code to map against: internal/api/handler.go:42-45 apiError = {Error string, Details any} — a bare message string, no code field; writeError (handler.go:578) just wraps that string. (3) The transport drops the status+raw-string verbatim into the frontend: controllerClie…

### C4. [minor] struct-crosscutting — `internal/api/handler.go:78`
**Mixed-language HTTP error messages in air-gap API routes**

- **Why:** Handler.HandleValidate (line 78) returns Chinese "仅支持 GET 请求" while other routes return English "only GET is supported" (handler_controller.go:66,553). Commit 4fa6ccd partially migrated key-gen errors to English but left HTTP method guards untouched. Inconsistent across the three air-gap routes (validate, compile, export, deploy-script).
- **Verdict:** Verified against code. internal/api/handler.go emits Chinese HTTP method-guard errors on all 5 air-gap routes: HandleHealth L78 ("仅支持 GET 请求"), HandleValidate L91, HandleCompile L124, HandleExport L172, HandleDeployScript L233 (all "仅支持 ... 请求"). By contrast internal/api/handler_controller.go uniformly returns English ("only GET/POST is supported") on every controller route (L501,553,624,666,711,780,810,845,884,946,982,1030,1055,1146,1211,1308,1366,1400). All go through writeError (handler.go:57…

---

## Security (PLAUSIBLE) — 2

### P1. [minor] sec-custody — `frontend/src/stores/controllerStore.ts:156`
**The pendingShrink.snapshot field retains a full Topology copy (including nodes with public IPs, SSH targets, and edge data) while awaiting user confirmation. If the confirmation dialog is abandoned or the tab is navigated away before confir…**

- **Why:** The confirmShrink snapshot at line 157-162 is stored in memory (not persisted) for the typed-confirmation UX, but it holds sensitive controller-mode fleet metadata (line 839-881 deploys the snapshot unchanged, binding it to what the operator saw). If the user cancels the dialog, the snapshot remains in controllerStore state until the next user action or logout. It's memory-only (not persisted), so not a localStorage leak, but it's a longer-lived copy than necessary. Mitigation: the snapshot is only set on shrink-detections (emptying or dropping 50%+ of nodes), which are rare.
- **Verdict:** Verified against frontend/src/stores/controllerStore.ts and the consuming components. The factual claims hold:  1. pendingShrink.snapshot IS a full Topology copy retaining sensitive fleet metadata. Declared `snapshot: Topology` (line 161); set to `cleanTopo` (line 881), which is the stripped topology — private keys are removed (stripPrivateKeys, line 848), but nodes (public IPs, SSH targets) and edges are retained. On confirmed deploy the snapshot is uploaded unchanged (lines 838-840: `cleanTopo…

### P2. [blocker] sec-injection — `internal/controller/filestore.go:72`
**bundlePath does not validate the 'kind' parameter for path traversal**

- **Why:** At line 120-126, the bundlePath() function constructs paths using `nc + "." + kind + ".json"`. While nodeID is properly sanitized via sanitizeComponent() (line 121), the 'kind' parameter is NOT sanitized. If 'kind' contains path separators or dot-dot sequences (e.g., '../../../etc/passwd'), it could escape the bundles/ directory. The function is called from PutBundle() and GetBundle() with hard-coded 'kind' values ('staged' or 'current'), but should defensively sanitize 'kind' as a path component to prevent future misuse or confusion.
- **Verdict:** The factual observation is correct — bundlePath() at internal/controller/filestore.go:120-126 sanitizes nodeID (line 121 via sanitizeComponent) but does NOT sanitize the kind parameter before building `nc+"."+kind+".json"`. However, the [blocker] path-traversal claim has NO concrete trigger and is not exploitable:  1. bundlePath is an UNEXPORTED method (func (fs *FileStore) bundlePath), reachable only from within the controller package. 2. It has exactly four call sites, all in filestore.go: lin…

## Robustness (PLAUSIBLE) — 2

### P1. [minor] robust-backend — `internal/controller/filestore.go:196`
**writeJSONAtomic swallows os.Remove error on temp file cleanup**

- **Why:** Line 196: `_ = os.Remove(tmp)` silently discards the error from removing the temp file if the atomic rename (line 195) fails. While the primary error (from os.Rename) is correctly returned, a failure to clean up the .tmp file leaves orphaned temp files on disk, which could accumulate over time or indicate a deeper filesystem issue (permissions, disk space). The comment does not justify the swallow; best practice is to log or wrap the cleanup error.
- **Verdict:** Factually accurate: at /home/kunorikiku/source/yet-another-overlay-generator/internal/controller/filestore.go:196, `_ = os.Remove(tmp)` does discard the temp-file cleanup error after a failed `os.Rename` (line 195), while the primary rename error is correctly returned (line 197). However, this is real-but-minor and borders on a taste preference, not a material bug or design problem:  1. This is the standard idiomatic Go atomic write-via-rename pattern: on rename failure, attempt best-effort clea…

### P2. [minor] robust-frontend — `frontend/src/stores/controllerStore.ts:899`
**Canvas marked as server-held after updateTopology even if later steps fail**

- **Why:** At line 899, after updateTopology succeeds, setCanvasFromServer(true) is called. However, if stage/promote/signature later fails at lines 900-938, the canvas is already marked as server-held. This means on logout, the server-held canvas is flushed even though the deploy wasn't fully completed. Per the comment on lines 893-898, this is intentional ('even if stage/promote later fails — it IS on the server now') but is worth noting as design decision that prioritizes security (clearing unfinished deploys) over data preservation.
- **Verdict:** The trace is accurate. In controllerStore.ts:892 updateTopology() succeeds, then :899 calls setCanvasFromServer(true). The subsequent stage/signature/promote steps (:900-938) can throw — e.g. the signing-prerequisite Error at :914-916, signManifest, postTrustlistSignature, or promote — and the catch block (:944-950) only sets error/loading/signing, never resetting canvasFromServer. So the flag stays true. Per clearServerCanvasAtGate (:257) and topologyStore partialize (:685-695), that means the…

## Structural — backend (PLAUSIBLE) — 7

### P1. [major] struct-backend — `internal/api/handler_controller.go:1`
**Backend error strings are Chinese-only with no error codes or localization support**

- **Why:** Throughout handler_controller.go, errors are returned as bare Chinese strings (e.g., writeError(..., "仅支持 POST 请求"), "failed to load configuration") with no error-code enum system. This creates a maintenance burden: operator-facing error messages are hardcoded in the HTTP layer, making them impossible to translate at the i18n level or to classify programmatically. The frontend cannot build a lookup table from error codes to localized messages. Example: line 113-114 returns opaque error strings for different auth failures (missing token vs. invalid token) that an API caller cannot distinguish. Recommendation: Introduce an ErrorCode enum (e.g., ErrMissingToken, ErrInvalidToken) and include {co…
- **Verdict:** The underlying phenomenon is real, but the finding is mis-cited to a file that does not exhibit it. I read internal/api/handler_controller.go IN FULL (all 1565 lines) and grepped for CJK: there are ZERO Chinese characters in it. Every controller error string is English — e.g. handler_controller.go:501 `writeError(w, http.StatusMethodNotAllowed, "only POST is supported")`, :568 "failed to load configuration", :514 "node id is reserved". So the finding's core assertion ("Throughout handler_control…

### P2. [major] struct-backend — `internal/render/render.go:86`
**Error messages in render.GenerateKeys mix Chinese and English inconsistently**

- **Why:** Lines 86, 90, 110, 120, 127 show mixed-language error messages. Line 86: 'AgentHeld 托管模式下缺少 WireGuard 公钥' (Chinese), line 90: 'WireGuard 私钥解析失败' (Chinese), but line 110/120 use English 'failed to parse WireGuard private key' / 'node %s has a pinned WireGuard public key but no matching private key' (English, very verbose). This inconsistency propagates to the API response body, confusing users. Recommendation: Establish a single language baseline (English for structured errors, with i18n tags for user-facing messages) and audit the entire codebase for consistency.
- **Verdict:** The finding's literal claims are all accurate and verified against the code at internal/render/render.go. Within the single function GenerateKeys, error messages mix languages: the AgentHeld branch is Chinese (line 86 "节点 %s 在 AgentHeld 托管模式下缺少 WireGuard 公钥..."; line 90 "节点 %s 的 WireGuard 私钥解析失败: %w"), while the AirGap branch case (a) and case (b) are English (line 110 "node %s: failed to parse WireGuard private key: %w"; line 120 "node %s has a pinned WireGuard public key but no matching privat…

### P3. [major] struct-backend — `internal/controller/store.go:304`
**Store interface is large and mixed-concern; bundles, operators, settings, and audit are conflated in a single interface**

- **Why:** The Store interface (lines 318-516) has 45+ methods spanning disparate concerns: registry (UpsertNode, GetNode), topology management (PutTopology, GetTopology), bundles (StageBundle, PromoteStaged), enrollment tokens, login challenges, operators, sessions, audit, keystone, and settings. This mega-interface couples all these concerns and makes it hard to: (1) implement a subset (e.g., a read-only Store), (2) test one concern independently, (3) swap implementations for specific operations (e.g., use a specialized audit backend while keeping the rest file-based). Recommendation: Segment into focused sub-interfaces: RegistryStore, TopologyStore, BundleStore, AuditStore, SessionStore, etc., and c…
- **Verdict:** The interface exists and is large (internal/controller/store.go:318-516), spanning the disparate concerns the finding names (registry, topology, bundles/generation, enrollment tokens, login challenges, node API tokens, audit, keystone, operators/sessions, settings). But several claims are inflated or unsupported by the code:  (1) METHOD COUNT IS OVERSTATED. The interface has 39 methods (verified: 39 `Xxx(ctx ...)` signatures in the block; both impls expose exactly 39), not "45+".  (2) THE CITED…

### P4. [major] struct-backend — `internal/controller/store.go:34`
**Sentinel errors have no codes; handlers must string-match error messages to branch logic**

- **Why:** Lines 34-58 define sentinel errors like ErrTokenInvalid, ErrDuplicateWGKey, etc., but they have no error codes. Handlers branch on errors.Is() checks (e.g., handler_controller.go line 528 checks errors.Is(err, controller.ErrTokenInvalid)), which is correct. However, when an error is surfaced to the client, it is returned as a bare string message (line 529: writeError(..., err.Error())), which loses the semantic information. A client cannot programmatically distinguish ErrTokenInvalid from ErrTokenConsumed without parsing error strings. Recommendation: Introduce an ErrorCode field in each sentinel (e.g., const ErrTokenInvalid = &CodedError{Code: "token_invalid", Message: "enrollment token is…
- **Verdict:** The cited facts check out, but the structural-cost claim is overstated, landing this at real-but-minor rather than a concrete design problem.  VERIFIED FACTS (all true): - internal/controller/store.go:34-58 define sentinel errors (ErrNotFound, ErrNoStagedBundle, ErrTokenInvalid, ErrTokenConsumed, ErrChallengeInvalid, ErrDuplicateWGKey) as plain errors.New() with no machine-readable code field. - handler_controller.go:528-538 branches with errors.Is() (correct) but surfaces the error as a bare st…

### P5. [major] struct-backend — `internal/controller/compile.go:1`
**CompileAndStage driver uses file I/O and temp directories as an internal data-passing mechanism**

- **Why:** Lines 1-250 reveal that CompileAndStage: (1) calls render.GenerateKeys on a mutable topo (lines 132-133 comment), (2) invokes the air-gap compiler.Compile unchanged, (3) EXPORTS to a temp directory via artifacts.Export (line 207-208 comment), (4) READS the temp directory back into file maps, (5) stages those file maps as bundles. The temp directory is a data bus: it exists solely to reuse the air-gap export logic, which writes files the controller then reads back into memory. This is fragile: if artifacts.Export changes its output format, compile.go breaks silently. It also has performance/concurrency implications: concurrent CompileAndStage calls may contend on the filesystem. Recommendatio…
- **Verdict:** The mechanism is described accurately: CompileAndStage (internal/controller/compile.go:208-276) does drive GenerateKeys (line 208) → compiler.Compile (line 212) → render.All (line 216) → artifacts.Export to a temp dir (lines 232-239) → readBundleDir back into file maps (lines 253-258) → StageBundle (lines 267-274). The temp dir is genuinely used as an internal data bus to reuse the air-gap export logic — this is even stated outright in the file header (lines 11-16). So a real, if mild, structura…

### P6. [minor] struct-backend — `internal/api/handler_controller.go:59`
**Handler layer directly calls JSON unmarshal and validation; should delegate to a service layer**

- **Why:** Handler methods (e.g., HandleUpdateTopology, line 709-774) perform inline JSON parsing, custody checking, model unmarshaling, and store operations. This mixes HTTP concerns with business logic. While the custody gate at lines 730-746 is critical, it lives in the HTTP layer and could be hard to reuse (e.g., if a gRPC surface is added later, the same gate must be re-implemented or extracted). Recommendation: Extract a controller.ValidateTopologyForUpdate(body []byte) error function that performs custody validation and returns structured errors; have the handler call it. This enables reuse and isolation of business rules from transport.
- **Verdict:** The finding's factual core is verifiable: in /home/kunorikiku/source/yet-another-overlay-generator/internal/api/handler_controller.go, HandleUpdateTopology (lines 709-774) does inline the custody gate — JSON unmarshal into model.Topology (line 731), the private-key rejection loop (lines 740-746), and canonicalization (line 753) — all in the HTTP handler. The cited claim that this gate "lives in the HTTP layer" is accurate, and a hypothetical second transport (e.g. gRPC) would indeed have to re-i…

### P7. [minor] struct-backend — `internal/api/handler_controller.go:499`
**HandleEnroll reserves operator identity but validation is ad-hoc (line 513-516), not centralized**

- **Why:** Line 513-516 checks req.NodeID == h.operatorName to prevent a node from enrolling as the operator. This same check is repeated at HandleEnrollmentToken (line 1075-1078). If the set of reserved identities grows (e.g., system nodes), this pattern will fragment. Recommendation: Extract a helper func h.isReservedNodeID(string) bool that centralizes all reserved-identity checks; use it in both Enroll and token mint.
- **Verdict:** Verified against the code: the reserved-identity check `if req.NodeID == h.operatorName { writeError(w, http.StatusForbidden, "node id is reserved") }` is genuinely duplicated at exactly two sites — internal/api/handler_controller.go:513-516 (HandleEnroll) and :1075-1078 (HandleEnrollmentToken) — with identical condition and identical error string. So the factual claim (ad-hoc, repeated check) is accurate.  However, this is a structural finding and the cited maintenance cost is speculative rathe…

## Structural — frontend (PLAUSIBLE) — 2

### P1. [major] struct-frontend — `frontend/src/stores/controllerStore.ts:82`
**controllerStore is a god-store: 177 fields including auth state, fleet nodes/audit, deploy results, UI flags (signing/enrolling/loginCeremony), settings, hydration logic, shrink guards, keystone enrollment, and TOTP/passkey management—mixin…**

- **Why:** Lines 85–237 define ControllerState with auth (sessionToken, operatorToken, loggedIn, csrfToken), TOTP/passkey enrollment (totpRequired, totpEnabled, passkeyRegistered), fleet data (nodes, audit, lastDeploy), UI signals (loading, error, signing, enrolling, loginCeremony), settings, guards (pendingShrink, lastStrippedKeys), keystone fields (operatorCredentialId/Alg/RpId/PublicKeyPEM), and full methods for deploy/login/refresh/revoke/etc. This violates separation of concerns: auth should be separate from fleet management; UI signals (signing, enrolling) should not live in domain state. Changes to one concern cascade across all consumers (243 call sites across components).
- **Verdict:** The structural concern is real but the finding's quantitative evidence is materially wrong and its severity is overstated. FACTS verified against frontend/src/stores/controllerStore.ts:  1. Concern-mixing is REAL and correctly enumerated. The ControllerState interface (lines 85-237) genuinely co-locates: auth/session (sessionToken, operatorToken, loggedIn, csrfToken, operatorName, sessionExpiresAt — lines 88-113), 2FA/passkey enrollment (totpRequired, totpEnabled, passkeyRegistered — 119-125), k…

### P2. [major] struct-frontend — `frontend/src/stores/controllerStore.ts:317`
**Bidirectional store coupling: controllerStore reads useTopologyStore.getState() in lines 256, 394, 558, 685–686; topologyStore reads useControllerStore.getState() in lines 20, 394, 558. This creates runtime-only circular dependency that bre…**

- **Why:** Lines 20 & 47 in topologyStore.ts use lazy getState() to avoid module-init cycles, but the coupling is still bidirectional: controllerStore.hydrateFromServer (line 534) calls useTopologyStore.getState().loadTopology(); topologyStore.importProject (line 394) reads useControllerStore.getState().mode. Any refactor of either store's initialization risks deadlock. This is fragile and hard to test in isolation.
- **Verdict:** Traced both files in full. The CORE severity-claim is REFUTED; only the bare existence of bidirectional coupling holds, and the finding's line citations are wrong.  LINE CITATIONS (mostly wrong, file numbers transposed): controllerStore reads useTopologyStore.getState() at lines 256, 534, 847, 899 — NOT "256, 394, 558, 685-686". topologyStore reads useControllerStore.getState() at lines 394, 558, 686 — NOT "20, 394, 558" (line 20 is the static import, not a getState call). The author swapped whi…

## Structural — cross-cutting (PLAUSIBLE) — 3

### P1. [major] struct-crosscutting — `internal/api/handler_login.go:152`
**TOTP-required 401 uses writeJSON with structured body (totpRequiredJSON) but generic error path uses writeError (string-only); asymmetric contract**

- **Why:** Line 152-155 writeJSON(w, 401, totpRequiredJSON{Error:..., TOTPRequired:true}) while wrong-password path (line 97/104) writeError(w, 401, "invalid..."). Frontend login() distinguishes them by parsing JSON (line 316-331) but this contract is implicit — no OpenAPI spec, no generated types, no enforcement that the two branches return conforming shapes. Same issue for passkey_required (handler_passkey.go line 120).
- **Verdict:** The finding's factual claims are accurate but its severity (major) and framing ("asymmetric contract") are overstated; there is no concrete bug trigger.  Verified against the code: - internal/api/handler_login.go:152-155 — the TOTP-needed 401 does use writeJSON(w, 401, totpRequiredJSON{Error:..., TOTPRequired:true}). Confirmed. - internal/api/handler_login.go:97 and :104 — the unknown-user / wrong-password 401 use writeError(w, 401, "invalid username or password"). Confirmed. - internal/api/hand…

### P2. [major] struct-crosscutting — `internal/api/handler_controller.go:790`
**Compile/stage/promote errors are returned as plain strings with no structured context for the frontend to display actionable diagnostics**

- **Why:** Line 790 shows writeError(w, statusForBodyErr(err), err.Error()) for topology validation. The err.Error() is a plain string like "node X has invalid CIDR". The frontend receives it (controllerStore.ts:946) and displays it as-is. No error code, no affected-node-id, no remediation hint. Comparison: browser devtools show localized, contextual errors; this API returns raw compiler output.
- **Verdict:** The CORE defect is real, but the finding misattributes it and overstates parts.  VERIFIED REAL: - Compile/stage/promote errors are returned as plain strings with no structured context. internal/api/handler.go:578-580 writeError() emits apiError{Error: message} only; the wire struct (handler.go:42-45) has a Details any `json:"details,omitempty"` slot but it is NEVER populated anywhere in internal/api (grep shows the struct def is its only reference). There is no error code and no affected-node-id…

### P3. [minor] struct-crosscutting — `internal/api/handler.go:325`
**decodeJSON returns internal error strings that leak to the frontend; not suitable for display to users**

- **Why:** decodeJSON (line 325-335) returns fmt.Errorf("读取请求体失败...", "JSON 解析失败..."). These internal diagnostic strings are returned via writeError and displayed in the browser (line 506 Handler.HandleEnroll). A frontend user who sends malformed JSON sees "JSON 解析失败" (Chinese) instead of a polished, localized message. The error chain is thrown verbatim instead of being normalized to a user-facing string.
- **Verdict:** The underlying defect class is REAL and reproducible (raw/non-localized error strings reach the browser verbatim), but the finding's specifics are materially garbled, so it cannot be CONFIRMED as written.  What's real: - writeError (handler.go:578) serializes its message into the JSON envelope {"error": message} (apiError, handler.go:42-45) sent to the client. - The frontend surfaces these verbatim: controllerClient.ts login() (line 311-332) throws Error(`${res.status} ${text}`), comment at 289-…

---

## i18n migration scope — 94 enumerated non-localized user-facing strings

Grouped by file. FRONTEND files migrate under M1 (txt→t(key)); BACKEND files migrate under M3 (Chinese Errorf/writeError → typed coded errors). Lines are representative anchors from the audit, not exhaustive per file — the migration must sweep each file fully.

### BACKEND (70 findings across 10 files)

- **`internal/api/auth_controller.go`** (3) — lines 162, 167, 172
  - e.g. English-only error message 'missing or invalid CSRF token' returned to user
- **`internal/api/handler.go`** (12) — lines 78, 91, 124, 172, 206, 206, 212, 218, 233, 267, 274, 540
  - e.g. Chinese-only error message '仅支持 GET 请求' returned to user on invalid HTTP method for /health
- **`internal/api/handler_bootstrap.go`** (7) — lines 77, 85, 119, 130, 141, 146, 169
  - e.g. English-only error message 'failed to read settings' returned to user
- **`internal/api/handler_login.go`** (8) — lines 66, 87, 97, 143, 176, 181, 224, 238
  - e.g. English-only error message 'only POST is supported' returned to user
- **`internal/api/handler_passkey.go`** (19) — lines 130, 184, 200, 215, 224, 228, 232, 245, 261, 282, 295, 301, 321, 330, 342, 349, 377, 391, 397
  - e.g. English-only error message 'failed to issue passkey challenge' returned to user
- **`internal/api/handler_totp.go`** (11) — lines 44, 49, 58, 73, 90, 103, 109, 116, 129, 147, 154
  - e.g. English-only error message 'missing authenticated identity' returned to user from TOTP endpoint
- **`internal/api/server.go`** (1) — lines 116
  - e.g. Chinese-only error message in panic recovery handler
- **`internal/compiler/compiler.go`** (3) — lines 82, 88, 125
  - e.g. Chinese-only error message 'schema : %v' with schema validation errors
- **`internal/render/render.go`** (2) — lines 86, 90
  - e.g. Chinese-only error message about missing WireGuard public key in AgentHeld mode
- **`internal/validator/nat.go`** (4) — lines 53, 72, 76, 103
  - e.g. Chinese-only validation warning about unreachable target node in NAT scenario

### FRONTEND (24 findings across 5 files)

- **`frontend/src/components/deploy/EnrollmentFlow.tsx`** (1) — lines 69
  - e.g. Hardcoded English-only fallback error message 'Failed to mint enrollment token'
- **`frontend/src/components/deploy/PasskeySettings.tsx`** (2) — lines 38, 47
  - e.g. Hardcoded English-only fallback error message 'Failed to register passkey'
- **`frontend/src/components/deploy/TwoFactorSettings.tsx`** (3) — lines 50, 66, 88
  - e.g. Hardcoded English-only fallback error message 'Failed to start 2FA enrollment'
- **`frontend/src/stores/controllerStore.ts`** (9) — lines 392, 409, 420, 437, 467, 470, 486, 704, 810
  - e.g. Hardcoded English-only fallback error message 'Failed to refresh controller state'
- **`frontend/src/stores/topologyStore.ts`** (9) — lines 538, 544, 561, 576, 600, 617, 635, 653, 668
  - e.g. Hardcoded Chinese error message '校验失败' in fallback—should use txt() or i18n helper

---

## Known additional scope (not from this audit; carried in)

### Full i18n call-site migration (M1)
The audit enumerated *hardcoded* (non-`txt`) strings above. Independently, there are **406 `txt(lang,
zh, en)` call sites** and **77 positional `[zh,en]` `STRINGS` tuples** across `frontend/src/**` that
must migrate to the new `t(key, params?)` API. M1's scope = (all 406 `txt` sites) + (the audit's
frontend hardcoded strings) → keyed catalog; then delete `txt`/`STRINGS`.

### Mode-boundary parity + in-flight race (M4) — from the PR#68 independent review (`wvhxdb7jq`)
The #68 review produced 6 PLAUSIBLE items; the audit's `mode-correctness` angle found 0 NEW confirmed
issues (the compile-gate verified complete), so these remain the M4 worklist:
1. **In-flight compile mode-flip race** — `topologyStore.ts` `compile()` line ~558 guard rejects fresh
   invocations, but an in-flight compile that resolves *after* a switch to controller mode still writes
   `data.topology.nodes` (reconstructed private keys) to the store + localStorage (success branch
   ~587-597). Fix: re-check `mode === 'local'` in the success branch before persisting, or capture a
   generation token and abort on mode change.
2. **`exportArtifacts()` lacks the controller-mode guard** (`topologyStore.ts` ~607) — parity with
   `compile()`. Page-gated today (unreachable in controller mode); defense-in-depth.
3. **`downloadDeployScript()` lacks the controller-mode guard** (`topologyStore.ts` ~643) — same.

### Backend untranslated strings carried from #68 context (fold into M3)
- `internal/render/render.go:86,90,127` (AgentHeld/AirGap key-gen errors still Chinese).
- `cmd/compiler/main.go:57` (`编译失败: %v`) + other `cmd/compiler/main.go` Chinese error returns.

---

## Controller-mode boundary audit (2026-06-14 addendum)
<!-- source: workflow wf_3e846c65-4b2 (Opus 4.8 re-run; 43 agents; 8 disjoint surfaces → per-finding
     adversarial verify → completeness critic). 25 confirmed + 6 critic-additional, deduped into 7
     themes. SUPERSEDES the first run wf_a4d0486b-7a3 (Explore/Haiku-defaulted — its confirm/reject
     judgments are untrusted; only its candidate locations were reused). -->

User-reported defect class: **LOCAL-mode functionality leaking into the newly-introduced CONTROLLER
mode** (and the reverse). Exemplar: the Topbar "Flush" button. plan-4 (shipped) only guarded the
air-gap *key* paths (`compile`/`exportArtifacts`/`downloadDeployScript` refuse in controller mode +
`partialize` key-leak); this **UX / persistence / mode-transition** class is untouched by any shipped
plan. Deduped into 7 themes → owned by new plans 10/11/12 (decisions D11–D14 in the outline).

### T1. [security] Mode-switch boundary parity — `SettingsPage` switch leaks fleet secrets → **plan-10**
- **What:** `LoginPage`'s controller→local switch flushes a server-held canvas (`LoginPage.tsx:315-325`,
  `serverHeld ? flushWorkspace() : purgeModeBoundaryState()`); **`SettingsPage`'s switch does not**
  (`SettingsPage.tsx:44-56` calls only `purgeModeBoundaryState`). So the server-held design (fleet
  public IPs + SSH targets) survives the switch AND `canvasFromServer` is no longer controller-true, so
  `partialize` (`topologyStore.ts:739`) re-enables persistence → confidential fleet data lands in
  `localStorage`, readable while logged out. The reverse (local→controller) does **no** local-artifact
  purge and **skips re-hydration when already logged in** (`checkSession`/`controllerStore.ts:621-625`,
  `wasLoggedIn` short-circuit), so stale local state (compileResult/history/validateResult) masquerades
  as the server design.
- **Source findings:** confirmed #14, #17, #15; critic A2; A3 (translucency, cosmetic).
- **Fix:** factor ONE shared switch helper (`serverHeld ? flushWorkspace : purgeModeBoundaryState`,
  `clearModeNotices`, `setMode`) used by BOTH `LoginPage` and `SettingsPage` so they cannot diverge
  again; fork the dialog copy on `canvasFromServer`; on local→controller purge local-only artifacts and
  force `hydrateFromServer` when not server-held; restore `uiStore.translucency` to the local default on
  controller→local (A3).

### T2. [data-loss / missing-feature] No controller-mode Save primitive → **plan-10**
- **What:** Canvas edits in controller mode never reach the server except via the heavyweight `deploy()`
  (`controllerStore.ts:827`, which `updateTopology`→`stage`→`promote`s to the LIVE fleet behind the
  shrink-confirm). The clean primitive — `updateTopology` (persist authoritative copy + version history,
  NO fleet touch, `controllerClient.ts`) — is reachable only *inside* deploy. WIP is lost on
  refresh/relogin (`partialize` blanks the server-held mirror; rehydrate restores the pre-edit server
  copy) with no Save and no dirty indicator.
- **Source findings:** confirmed #5, #6, #10, #12, #19, #8 (import non-durable); critic A4 (conflicts).
- **Fix (decisions D12/D13):** add `controllerStore.saveDesign()` = `stripPrivateKeys` →
  `updateTopology` (persist-only) → `setCanvasFromServer(true)` → refresh `lastSyncedAt`. Track a dirty
  flag (compare current vs last-synced snapshot via the comparator `hydrateFromServer` uses,
  `controllerStore.ts:539`). **Client-side conflict warn (D13):** capture the server base snapshot/hash
  at hydrate; before save re-GET and compare; if the server changed, warn + offer re-sync-with-backup
  (reuse `exportProject` like the hydrate path) instead of blind overwrite. NO backend version field.
  Harden `clearServerCanvasAtGate` (`controllerStore.ts:254`): when the mirror is dirty, back up / confirm
  before flushing rather than silently discarding.

### T3. [confusing] Topbar I/O cluster (import/export/flush) is mode-blind → **plan-11**
- **What:** `Topbar.tsx:50` gates import/export/flush on `onDesign` only, no mode check (unlike the
  `mode==='local'` idiom in `CanvasToolbar.tsx:52` / `UserMenu.tsx:60`). Flush carries a false
  "cannot be undone" promise (server keeps the copy; re-login re-hydrates); Export writes fleet IPs/SSH
  to disk unwarned; Import flips `canvasFromServer=false` (`importProject`→`loadTopology(topo)` default,
  `topologyStore.ts:428`) so the work is silently dropped at next hydrate.
- **Source findings:** confirmed #1, #2, #3, #4, #7, #11, #13, #21.
- **Fix:** gate the cluster to `mode==='local'` at the Topbar (do NOT refuse `flushWorkspace`/`exportProject`
  in the store — both are security primitives used by the gate-flush + pre-hydration-backup paths; gate
  the BUTTON). Surface the plan-10 **Save** button on the Design surface for controller mode in its place.

### T4. [confusing] Local-only constructs surfaced in controller mode → **plan-11**
- **What:** AuditView "Compile History" / config-diff (`SecurityPage`) live in controller mode though
  compile history never populates; DeployPage renders the local `CompilePreview` in both modes;
  NodeEditor's "Pin private key (persist)" toggle + pinned-key panel (`NodeEditor.tsx:223-251`) are live
  and meaningless on a zero-knowledge server-held node (and write `fixed_private_key=true` into the
  server-bound design); `clearHistory` is a dead no-op. **Validate is NOT a leak** — per D11 `/validate`
  stays usable (auth'd) as a controller preflight; keep the Validate button.
- **Source findings:** confirmed #9, #22, #23; critic A1. (#18 reclassified: keep.)
- **Fix:** mode-gate each local-only construct to `mode==='local'`.

### T5. [confusing] Controller-only constructs surfaced in local mode (reverse direction) → **plan-11**
- **What:** Connection + Bootstrap settings sections render in local mode (#16); cached controller fleet
  node data persists into local mode (Overview controller section + deep-linked `/fleet`,
  `/fleet/nodes/:id`) (#20); EnrollmentFlow/NodeRegistry deep-linkable in local mode via `/fleet` (A5).
- **Source findings:** confirmed #16, #20; critic A5.
- **Fix:** render-gate `FleetPage` (covers NodeRegistry + EnrollmentFlow) and the Overview controller
  section on `mode==='controller'`; gate the Connection/Bootstrap settings sections; clear the persisted
  fleet cache on the local boundary.

### T6. [security] Backend has zero mode-awareness → **plan-12**
- **What:** `/api/compile`, `/api/export`, `/api/deploy-script`, `/api/validate` are mounted
  **unauthenticated** on the controller's operator/panel port (`internal/api/server.go`,
  `handler_controller.go`) — an unauthenticated compute / key-gen oracle + DoS surface in a controller
  deployment.
- **Source findings:** confirmed #25.
- **Fix (decision D11):** mount the air-gap endpoints behind the existing operator-auth middleware in a
  controller deployment (keeps `/validate` usable for the panel's preflight; closes the unauth hole).

### T7. [data-loss, narrow] `deploy()` confirmed-shrink mislabels a divergent canvas → **plan-10**
- **What:** on the confirmed-shrink success path `deploy()` sets `setCanvasFromServer(true)` on the LIVE
  canvas (`controllerStore.ts:899`), but the uploaded design was the earlier `snapshot`; any edits made
  after the warning are now marked server-held and get flushed by `partialize`/gate with no backup.
- **Source findings:** critic A6.
- **Fix:** on confirmed-shrink success `loadTopology(confirming.snapshot, true)` so the canvas matches
  what was uploaded (or detect divergence + back up). Folded into plan-10 (custody-flag owner).

---

## Milestone mapping (which findings each plan owns)

| Plan | Owns |
|---|---|
| **plan-0 (security)** | C1/C2/C3 sec-injection (PowerShell SSHKeyPath escape + validator gap), the 3 sec-authz (all i18n-class — fold the *security-critical installer* Chinese strings into M3 but FIX the injection here), sec-* PLAUSIBLE (custody, injection) |
| **plan-1 (i18n core + migration)** | The i18n structural finding (`i18n.ts` keyed-catalog redesign) + all 406 `txt` sites + frontend hardcoded strings (EnrollmentFlow, PasskeySettings, TwoFactorSettings, controllerStore, topologyStore) |
| **plan-2 (error envelope)** | The struct-crosscutting error-contract findings ({error:string}→{code,params,message}) |
| **plan-3 (backend string migration)** | All BACKEND i18n-enumerated files + render.go:86/90/127 + cmd/compiler + the **corrupted `allocator/ip.go:68` format string** (real bug) + the security-critical Chinese installer strings (handler.go:510/543) |
| **plan-4 (mode-boundary)** | The 3 PR#68-review mode items above |
| **plan-6 (robustness)** | robust-frontend C1 (pendingShrink not cleared on deploy error), C2 (JSON-parse on error body), robust-* PLAUSIBLE |
| **plan-7 (struct-backend)** | struct-backend confirmed + the 7 struct-backend PLAUSIBLE (triage) |
| **plan-8 (struct-frontend)** | struct-frontend confirmed (component decomposition, store coupling) + PLAUSIBLE |
| **plan-9 (cross-cutting + docs + closure)** | struct-crosscutting confirmed (test-coverage gaps, namespace cleanliness, doc drift) + PLAUSIBLE + docs/migration/closure |
| **plan-10 (controller-mode boundary CORE)** | T1 (mode-switch security parity — shared helper) + T2 (controller-mode Save primitive + dirty + client-side conflict warn + gate-flush hardening) + T7 (deploy confirmed-shrink flag) + A3 (translucency boundary) |
| **plan-11 (controller-mode boundary UI gating)** | T3 (Topbar I/O cluster gating + Save button surfacing) + T4 (local-only constructs hidden in controller) + T5 (controller-only constructs hidden in local + fleet-cache clear) |
| **plan-12 (backend mode-awareness)** | T6 (air-gap endpoints behind operator-auth in a controller deployment) |
