package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// HandleEnroll runs the node-enrollment ceremony. It requires NO bearer token (it
// is the route a node calls before it has one); the single-use enrollment token is
// the authentication. The tenant is the configured one (single-tenant v1) — never
// taken from the request body. On success it returns the node's per-node API token
// ONCE.
func (h *ControllerHandler) HandleEnroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	// Per-IP brute-force gate BEFORE parsing the body: /enroll has no bearer token, so an
	// attacker can spray enrollment-token guesses here. Reserve a slot atomically; if this IP
	// is locked out, reject with 429 + Retry-After without touching the body. A successful
	// enroll refunds the slot below, so only failures count toward the lockout.
	now := time.Now().UTC()
	ipKey := "ip:" + h.clientIP(r)
	allowed, _, retry := h.enrollLimiter.registerAttempt(now, ipKey)
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		writeAPIError(w, apierr.New(apierr.CodeAuthRateLimited))
		return
	}
	var req enrollRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}
	// Reserve the operator identity: a node must never enroll AS the operator. Doing
	// so would mint a node whose identity collides with the operator name stamped on
	// operator routes. The operator is authenticated by its own out-of-band token,
	// never through this node-enrollment path.
	if h.isReservedNodeID(req.NodeID) {
		writeAPIError(w, apierr.New(apierr.CodeNodeIDReserved))
		return
	}

	result, err := controller.Enroll(r.Context(), h.store, h.tenant, controller.EnrollRequest{
		Token:       req.Token,
		NodeID:      req.NodeID,
		WGPublicKey: req.WGPublicKey,
	}, time.Now())
	if err != nil {
		// Token errors are an authorization failure (bad/expired/consumed token);
		// everything else here is a malformed request. We map token errors to 401 and
		// the rest to 400 so a caller can distinguish "your token is no good" from a
		// malformed request.
		if errors.Is(err, controller.ErrTokenInvalid) || errors.Is(err, controller.ErrTokenConsumed) {
			writeAPIError(w, apierr.New(apierr.CodeEnrollmentTokenInvalid).Wrap(err))
			return
		}
		// Revoked node-id re-enroll attempt (S4): a VALID token was burned (the guard runs
		// post-consume), so refund the throttle slot (not a token guess) and surface a 409 —
		// the operator must delete the node before its id can be reused.
		if errors.Is(err, controller.ErrNodeRevoked) {
			h.enrollLimiter.succeed(ipKey)
			writeAPIError(w, apierr.New(apierr.CodeEnrollNodeRevoked).Wrap(err))
			return
		}
		// Malformed WireGuard public key: rejected up front (before the token is burned), so it is a
		// plain bad request — not a token-guess and not a duplicate conflict.
		if errors.Is(err, controller.ErrInvalidWGKey) {
			writeAPIError(w, apierr.New(apierr.CodeReqFieldInvalid).With("field", "wg_public_key").Wrap(err))
			return
		}
		// Duplicate WG pubkey under another node-id (plan-6): a conflict the operator
		// must resolve (revoke the other node or reuse its id), not a malformed request.
		if errors.Is(err, controller.ErrDuplicateWGKey) {
			// This path is reached only AFTER a VALID enrollment token was burned (the dedupe
			// check runs post-consume), so it is a legitimate-operator conflict, not a
			// token-guess — refund the throttle slot rather than counting it toward lockout.
			h.enrollLimiter.succeed(ipKey)
			writeAPIError(w, apierr.New(apierr.CodeDuplicateWGKey).Wrap(err))
			return
		}
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}

	// Success: refund the reserved slot so a legitimate node (and an operator bulk-enrolling
	// several nodes behind one NAT) never accumulates toward the per-IP lockout.
	h.enrollLimiter.succeed(ipKey)
	writeJSON(w, http.StatusOK, enrollResponseJSON{
		ApiToken: result.APIToken,
		NodeID:   result.NodeID,
	})
}

// HandleConfig returns the CALLER's current bundle (the node taken from the bearer
// token, never the request). 404 before the first promote for that node. It also
// TouchLastSeen-s the node so the registry reflects the check-in.
func (h *ControllerHandler) HandleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "GET"))
		return
	}
	tenant, node, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}

	// One ATOMIC snapshot of what this node is served: its current promoted bundle plus,
	// when the keystone is ON, the SERVED (last-promoted) signed trust-list — read under a
	// single store lock so a concurrent PromoteStaged can never hand the node a torn
	// (old-bundle, new-manifest) pair that would spuriously fail its bundle-digest binding.
	sc, err := h.store.GetServedConfig(r.Context(), tenant, node)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			writeAPIError(w, apierr.New(apierr.CodeConfigNotFound).Wrap(err))
			return
		}
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}

	// Best-effort check-in stamp; a failed touch must not deny the node its config.
	_ = h.store.TouchLastSeen(r.Context(), tenant, node, time.Now())

	// Read the caller's registry record so the response can carry its
	// RekeyRequested flag (the agent reacts to it by regenerating + re-registering
	// its WireGuard key). A failed read must not deny the node its config — the flag
	// then defaults to false and the agent re-learns it on a later fetch.
	rekeyRequested := false
	if n, err := h.store.GetNode(r.Context(), tenant, node); err == nil {
		rekeyRequested = n.RekeyRequested
	}

	files := make(map[string]string, len(sc.Bundle.Files)+2)
	for path, content := range sc.Bundle.Files {
		files[path] = base64.StdEncoding.EncodeToString(content)
	}

	// KEYSTONE: when an operator credential is pinned, APPEND the off-host-signed
	// membership manifest (trustlist.json) and its detached signature (trustlist.sig)
	// to the SERVED file map — NOT to the bundle's checksums.sha256. The manifest binds
	// each node's checksums.sha256 digest, so it cannot live inside that very checksum
	// set; the agent verifies it against its pinned credential and asserts this node's
	// member.BundleSHA256 matches hex(sha256(checksums.sha256)). A promote cannot occur
	// without a valid signed manifest (PromoteStaged gate), so a promoted bundle always
	// has one to serve; we still fail closed (HasTrustList false) if it is somehow absent.
	if sc.KeystoneOn {
		if !sc.HasTrustList {
			writeAPIError(w, apierr.New(apierr.CodeKeystoneNoSignedManifest))
			return
		}
		files["trustlist.json"] = base64.StdEncoding.EncodeToString(sc.TrustList.TrustListJSON)
		files["trustlist.sig"] = base64.StdEncoding.EncodeToString(sc.TrustList.SignatureJSON)
	}

	writeJSON(w, http.StatusOK, configResponseJSON{
		Generation:     sc.Bundle.Generation,
		Files:          files,
		RekeyRequested: rekeyRequested,
	})
}

// HandlePoll long-polls for a generation strictly greater than ?after=N. It blocks
// on Store.WaitForGeneration under a ~55s server deadline derived from the request
// context. On advance it returns {generation}; on the deadline it returns 204 so the
// agent re-polls. The node identity comes from the bearer token (TouchLastSeen the
// caller).
func (h *ControllerHandler) HandlePoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "GET"))
		return
	}
	tenant, node, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	after, err := parseAfter(r.URL.Query().Get("after"))
	if err != nil {
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}

	// Best-effort check-in stamp on each poll.
	_ = h.store.TouchLastSeen(r.Context(), tenant, node, time.Now())

	deadline := h.pollDeadline
	if deadline <= 0 {
		deadline = defaultPollDeadline
	}
	ctx, cancel := context.WithTimeout(r.Context(), deadline)
	defer cancel()

	gen, err := h.store.WaitForGeneration(ctx, tenant, after)
	if err != nil {
		// Deadline/cancellation → no advance within the window → 204, re-poll.
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	writeJSON(w, http.StatusOK, pollResponseJSON{Generation: gen})
}

const (
	// maxReportedConditions bounds the conditions a node may send in one /report or /telemetry call
	// (~8x the live condition types). The agent's classify() already caps its output; the server must
	// not trust the client, so an over-count is rejected at the boundary rather than allocated.
	maxReportedConditions = 32
	// maxTelemetryMetrics / maxTelemetryMetricsBytes bound the /telemetry metrics map. node.Telemetry
	// is served verbatim in every ListNodes response, so an unbounded map would bloat the operator
	// /nodes payload as well as the per-node record.
	maxTelemetryMetrics      = 32
	maxTelemetryMetricsBytes = 64 << 10 // 64 KiB total across all metric values
)

// validateConditions bounds a reported conditions slice at the HTTP boundary: an over-count, or a
// curated Message longer than the model cap, is rejected (the agent enforces the cap, but the server
// must not trust the client). Returns nil when within bounds.
func validateConditions(conds []model.Condition) *apierr.Error {
	if len(conds) > maxReportedConditions {
		return apierr.New(apierr.CodeReqFieldInvalid).With("field", "conditions")
	}
	for i := range conds {
		if len([]rune(conds[i].Message)) > model.ConditionMessageMax {
			return apierr.New(apierr.CodeReqFieldInvalid).With("field", "conditions")
		}
	}
	return nil
}

// validateMetrics bounds the /telemetry metrics map: too many keys, or a total raw-value size over the
// cap, is rejected. Returns nil when within bounds.
func validateMetrics(metrics map[string]json.RawMessage) *apierr.Error {
	if len(metrics) > maxTelemetryMetrics {
		return apierr.New(apierr.CodeReqFieldInvalid).With("field", "metrics")
	}
	total := 0
	for _, v := range metrics {
		total += len(v)
		if total > maxTelemetryMetricsBytes {
			return apierr.New(apierr.CodeReqFieldInvalid).With("field", "metrics")
		}
	}
	return nil
}

// HandleReport records an agent's apply outcome for ITSELF: SetAppliedGeneration +
// TouchLastSeen + an audit entry. The node is the bearer token's node; the report
// body carries only the applied generation, checksum, and a health string.
func (h *ControllerHandler) HandleReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	tenant, node, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	var req reportRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}
	if ae := validateConditions(req.Conditions); ae != nil {
		writeAPIError(w, ae)
		return
	}

	now := time.Now()
	// Server-stamp the conditions' ObservedAt with the controller clock (inside the store): a node
	// clock cannot be trusted for ordering/ageing, so req.Conditions carry only the advisory Since.
	if err := h.store.SetAppliedGeneration(r.Context(), tenant, node, req.AppliedGeneration, req.Checksum, req.Health, req.AgentVersion, req.Conditions, now); err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			writeAPIError(w, apierr.New(apierr.CodeNodeNotFound).Wrap(err))
			return
		}
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	_ = h.store.TouchLastSeen(r.Context(), tenant, node, now)
	if _, err := h.store.AppendAudit(r.Context(), tenant, controller.AuditEntry{
		Timestamp: now,
		Actor:     "agent:" + node,
		Action:    "report",
		NodeID:    node,
	}); err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// HandleTelemetry records a LIVE health heartbeat from the CALLER (the node from the bearer token,
// never the request body) — beta9-smoke-hardening plan-1. Unlike HandleReport it carries NO
// applied_generation/checksum and writes ONLY the node's conditions + last_seen via RecordTelemetry:
// telemetry is high-frequency observability kept strictly separate from deploy custody, so a heartbeat
// can never advance or regress the applied generation. It is INTENTIONALLY NOT audited — a 30s
// heartbeat would flood the hash-chained audit log (HandleReport's append); do not "fix" the
// asymmetry by adding an audit entry here. Conditions are server-stamped with the controller clock
// inside the store (a node clock cannot be trusted for ageing). The metrics map (the framework's
// extension slot — e.g. wireguard_peers) is persisted wholesale and served under node.telemetry.
// Returns {status:"ok"}.
func (h *ControllerHandler) HandleTelemetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	tenant, node, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	var req telemetryRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}
	if ae := validateConditions(req.Conditions); ae != nil {
		writeAPIError(w, ae)
		return
	}
	if ae := validateMetrics(req.Metrics); ae != nil {
		writeAPIError(w, ae)
		return
	}
	if err := h.store.RecordTelemetry(r.Context(), tenant, node, req.Conditions, req.Metrics, req.AgentVersion, time.Now()); err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			writeAPIError(w, apierr.New(apierr.CodeNodeNotFound).Wrap(err))
			return
		}
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// HandleRekey re-registers the CALLER's rotated WireGuard PUBLIC key (the node from
// the bearer token, never the request body). It stamps the new public key onto the
// node record and clears RekeyRequested, all via GetNode/UpsertNode so every other
// field is preserved. It is the agent's response to a rekey_requested=true /config:
// the controller never sees a private key (zero-knowledge custody). An empty
// wg_public_key is a 400. Returns {ok:true}.
func (h *ControllerHandler) HandleRekey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))
		return
	}
	tenant, node, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	var req rekeyRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}
	if req.WGPublicKey == "" {
		writeAPIError(w, apierr.New(apierr.CodeReqFieldRequired).With("field", "wg_public_key"))
		return
	}

	// controller.Rekey swaps the key + clears the flag under the per-tenant op lock
	// and enforces the SAME identity invariant as enroll (plan-6 review: the rekey
	// write path must not be able to create a duplicate the enroll dedupe forbids).
	if err := controller.Rekey(r.Context(), h.store, tenant, node, req.WGPublicKey, time.Now()); err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			writeAPIError(w, apierr.New(apierr.CodeNodeNotFound).Wrap(err))
			return
		}
		if errors.Is(err, controller.ErrInvalidWGKey) {
			writeAPIError(w, apierr.New(apierr.CodeReqFieldInvalid).With("field", "wg_public_key").Wrap(err))
			return
		}
		if errors.Is(err, controller.ErrDuplicateWGKey) {
			writeAPIError(w, apierr.New(apierr.CodeDuplicateWGKey).Wrap(err))
			return
		}
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	writeJSON(w, http.StatusOK, rekeyResponseJSON{OK: true})
}
