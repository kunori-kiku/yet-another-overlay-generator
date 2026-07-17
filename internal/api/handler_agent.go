package api

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetryprotocol"
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
		// Refund the throttle slot for the two burned-VALID-token conflicts: the revoked-id
		// re-enroll guard (S4) and the duplicate-WG-key dedupe (plan-6) both run POST-consume,
		// so a real token was already spent — these are legitimate-operator conflicts, not
		// token-guesses. (Exactly the two sentinels the hand-rolled branches refunded on.)
		if errors.Is(err, controller.ErrNodeRevoked) || errors.Is(err, controller.ErrDuplicateWGKey) {
			h.enrollLimiter.succeed(ipKey)
		}
		// Token + WG-key sentinels map through the central table (errmap.go): ErrTokenInvalid/
		// ErrTokenConsumed → enrollment_token_invalid (401), ErrNodeRevoked → enroll_node_revoked
		// (409), ErrInvalidWGKey → req_field_invalid{wg_public_key} (400), ErrDuplicateWGKey →
		// duplicate_wg_key (409). Anything else here is a malformed request (400).
		if ae := mapControllerErr(err); ae != nil {
			writeAPIError(w, ae)
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
// TouchLastSeen-s the node so the registry reflects the check-in. Routed through the
// op() adapter (routes_controller.go), which runs the method guard + structural
// identity() check before this body — so the node/tenant arrive already resolved.
func (h *ControllerHandler) HandleConfig(ctx context.Context, tenant controller.TenantID, node string, _ http.ResponseWriter, _ *http.Request) (any, *apierr.Error) {
	// One ATOMIC snapshot of what this node is served: its current promoted bundle plus,
	// when the keystone is ON, the SERVED (last-promoted) signed trust-list — read under a
	// single store lock so a concurrent PromoteStaged can never hand the node a torn
	// (old-bundle, new-manifest) pair that would spuriously fail its bundle-digest binding.
	sc, err := h.store.GetServedConfig(ctx, tenant, node)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			return nil, apierr.New(apierr.CodeConfigNotFound).Wrap(err)
		}
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}

	// Best-effort check-in stamp; a failed touch must not deny the node its config.
	_ = h.store.TouchLastSeen(ctx, tenant, node, time.Now())

	// Read the caller's registry record so the response can carry its
	// RekeyRequested flag (the agent reacts to it by regenerating + re-registering
	// its WireGuard key). A failed read must not deny the node its config — the flag
	// then defaults to false and the agent re-learns it on a later fetch.
	rekeyRequested := false
	if n, err := h.store.GetNode(ctx, tenant, node); err == nil {
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
			return nil, apierr.New(apierr.CodeKeystoneNoSignedManifest)
		}
		files["trustlist.json"] = base64.StdEncoding.EncodeToString(sc.TrustList.TrustListJSON)
		files["trustlist.sig"] = base64.StdEncoding.EncodeToString(sc.TrustList.SignatureJSON)
	}

	return configResponseJSON{
		Generation:     sc.Bundle.Generation,
		Files:          files,
		RekeyRequested: rekeyRequested,
	}, nil
}

// HandlePoll long-polls for a generation strictly greater than ?after=N. It blocks
// on Store.WaitForGeneration under a ~55s server deadline derived from the request
// context. On advance it returns {generation}; on the deadline it returns 204 so the
// agent re-polls. The node identity comes from the bearer token (TouchLastSeen the
// caller).
//
// Routed through opRaw() (NOT op()): opRaw applies the SAME structural method-guard +
// identity() preamble as op but lets the handler write its OWN response, which HandlePoll
// requires — the deadline branch emits a bodyless 204 that op's writeJSON(200, result)
// contract cannot express (a 200-null would break the agent's re-poll loop). So this stays
// a raw writer, but the auth preamble is now structural rather than hand-rolled.
func (h *ControllerHandler) HandlePoll(ctx context.Context, tenant controller.TenantID, node string, w http.ResponseWriter, r *http.Request) *apierr.Error {
	after, err := parseAfter(r.URL.Query().Get("after"))
	if err != nil {
		return codedErr(apierr.CodeReqInvalidBody, err)
	}

	// Best-effort check-in stamp on each poll.
	_ = h.store.TouchLastSeen(ctx, tenant, node, time.Now())

	deadline := h.pollDeadline
	if deadline <= 0 {
		deadline = defaultPollDeadline
	}
	pollCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	gen, err := h.store.WaitForGeneration(pollCtx, tenant, after)
	if err != nil {
		// Deadline/cancellation → no advance within the window → 204, re-poll.
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			w.WriteHeader(http.StatusNoContent)
			return nil
		}
		return codedErr(apierr.CodeInternalStorage, err)
	}
	writeJSON(w, http.StatusOK, pollResponseJSON{Generation: gen})
	return nil
}

const (
	// maxReportedConditions bounds the conditions a node may send in one /report or /telemetry call
	// (~8x the live condition types). The agent's classify() already caps its output; the server must
	// not trust the client, so an over-count is rejected at the boundary rather than allocated.
	maxReportedConditions = 32
	// maxTelemetryMetrics / maxTelemetryMetricsBytes bound the /telemetry metrics map. node.Telemetry
	// is served verbatim in every ListNodes response, so an unbounded map would bloat the operator
	// /nodes payload as well as the per-node record.
	maxTelemetryMetrics      = telemetryprotocol.MaxMetrics
	maxTelemetryMetricsBytes = telemetryprotocol.MaxMetricsBytes // total across metric KEYS + values
	// maxConditionBytes bounds the total size of ALL of a single condition's attacker-controlled
	// string fields (Type+Status+Reason+Message+Since). Generous — a legit condition is a short
	// enum/timestamp set plus a <=160-rune Message (~640 bytes worst-case multibyte) — but it stops a
	// compromised node stuffing Reason/Since to bypass the Message cap and bloat every /nodes response.
	maxConditionBytes = 2048
)

// validateConditions bounds a reported conditions slice at the HTTP boundary: an over-count, a curated
// Message longer than the model cap, or any single condition whose total field bytes exceed the cap is
// rejected (the agent enforces these, but the server must not trust the client). Returns nil when
// within bounds.
func validateConditions(conds []runtimecontract.Condition) *apierr.Error {
	if len(conds) > maxReportedConditions {
		return apierr.New(apierr.CodeReqFieldInvalid).With("field", "conditions")
	}
	for i := range conds {
		c := conds[i]
		if len([]rune(c.Message)) > runtimecontract.ConditionMessageMax {
			return apierr.New(apierr.CodeReqFieldInvalid).With("field", "conditions")
		}
		if len(c.Type)+len(c.Status)+len(c.Reason)+len(c.Message)+len(c.Since) > maxConditionBytes {
			return apierr.New(apierr.CodeReqFieldInvalid).With("field", "conditions")
		}
	}
	return nil
}

// validateMetrics bounds the /telemetry metrics map: too many keys, or a total size (KEY bytes + value
// bytes — the keys are attacker-chosen) over the cap, is rejected. Returns nil when within bounds.
func validateMetrics(metrics map[string]json.RawMessage) *apierr.Error {
	if len(metrics) > maxTelemetryMetrics {
		return apierr.New(apierr.CodeReqFieldInvalid).With("field", "metrics")
	}
	total := 0
	for k, v := range metrics {
		total += len(k) + len(v)
		if total > maxTelemetryMetricsBytes {
			return apierr.New(apierr.CodeReqFieldInvalid).With("field", "metrics")
		}
	}
	return nil
}

// HandleReport records an agent's apply outcome for ITSELF: SetAppliedGeneration +
// TouchLastSeen. The node is the bearer token's node; the report body carries only
// the applied generation, checksum, health, version, and bounded conditions. This is
// operational Fleet state, not a durable security/operator audit event: a failing
// apply can retry every few seconds, and appending the same content-free "report"
// action each time would flood/rotate the hash-chained audit and add synchronous disk
// work while conveying none of the report's useful fields. Routed through op()
// (routes_controller.go): the adapter runs the method guard + structural identity()
// check before this body.
func (h *ControllerHandler) HandleReport(ctx context.Context, tenant controller.TenantID, node string, w http.ResponseWriter, r *http.Request) (any, *apierr.Error) {
	var req reportRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		return nil, codedErr(apierr.CodeReqInvalidBody, err)
	}
	if ae := validateConditions(req.Conditions); ae != nil {
		return nil, ae
	}

	now := time.Now()
	// Server-stamp the conditions' ObservedAt with the controller clock (inside the store): a node
	// clock cannot be trusted for ordering/ageing, so req.Conditions carry only the advisory Since.
	if err := h.store.SetAppliedGeneration(ctx, tenant, node, req.AppliedGeneration, req.Checksum, req.Health, req.AgentVersion, req.Conditions, now); err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			return nil, apierr.New(apierr.CodeNodeNotFound).Wrap(err)
		}
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}
	_ = h.store.TouchLastSeen(ctx, tenant, node, now)
	return map[string]string{"status": "ok"}, nil
}

type telemetryRequestMetadata struct {
	bootID    string
	sequence  uint64
	sampledAt time.Time
	interval  time.Duration
}

// telemetryMetadata reads protocol-v2 delivery metadata from headers, leaving the JSON body exactly
// compatible with strict legacy controllers. An absent protocol header is a legacy heartbeat. Old
// controllers ignore all of these headers, so a new agent can roll out before its controller.
func telemetryMetadata(r *http.Request) (*telemetryRequestMetadata, *apierr.Error) {
	protocol := r.Header.Get(telemetryprotocol.HeaderProtocol)
	if protocol == "" {
		return nil, nil
	}
	if protocol != telemetryprotocol.Version {
		return nil, apierr.New(apierr.CodeReqFieldInvalid).With("field", "telemetry_protocol")
	}
	bootID := r.Header.Get(telemetryprotocol.HeaderBootID)
	decodedBootID, err := hex.DecodeString(bootID)
	if err != nil || len(decodedBootID) != telemetryprotocol.BootIDBytes {
		return nil, apierr.New(apierr.CodeReqFieldInvalid).With("field", "boot_id")
	}
	sequence, err := strconv.ParseUint(r.Header.Get(telemetryprotocol.HeaderSequence), 10, 64)
	if err != nil || sequence == 0 {
		return nil, apierr.New(apierr.CodeReqFieldInvalid).With("field", "sequence")
	}
	sampledAt, err := time.Parse(time.RFC3339Nano, r.Header.Get(telemetryprotocol.HeaderSampledAt))
	if err != nil || sampledAt.IsZero() {
		return nil, apierr.New(apierr.CodeReqFieldInvalid).With("field", "sampled_at")
	}
	return &telemetryRequestMetadata{
		bootID:    bootID,
		sequence:  sequence,
		sampledAt: sampledAt.UTC(),
		interval:  telemetryInterval(r),
	}, nil
}

// telemetryInterval is advisory cadence. Missing, malformed, non-positive, oversized, or
// time.Duration-overflowing values are ignored; cadence metadata must never reject a heartbeat.
func telemetryInterval(r *http.Request) time.Duration {
	raw := r.Header.Get(telemetryprotocol.HeaderIntervalMillis)
	if raw == "" || len(raw) > telemetryprotocol.MaxIntervalHeaderBytes {
		return 0
	}
	millis, err := strconv.ParseInt(raw, 10, 64)
	const maxDurationMillis = int64(1<<63-1) / int64(time.Millisecond)
	if err != nil || millis <= 0 || millis > maxDurationMillis {
		return 0
	}
	return time.Duration(millis) * time.Millisecond
}

func writeTelemetryReceiptHeaders(w http.ResponseWriter, metadata *telemetryRequestMetadata, receipt controller.TelemetryReceipt) {
	w.Header().Set(telemetryprotocol.HeaderProtocol, telemetryprotocol.Version)
	w.Header().Set(telemetryprotocol.HeaderBootID, metadata.bootID)
	w.Header().Set(telemetryprotocol.HeaderAckSequence, strconv.FormatUint(receipt.AcknowledgedSequence, 10))
	w.Header().Set(telemetryprotocol.HeaderReceivedAt, receipt.ReceivedAt.UTC().Format(time.RFC3339Nano))
	w.Header().Set(telemetryprotocol.HeaderCapabilities, telemetryprotocol.CapabilityProbeSamplesV1)
	if receipt.Duplicate {
		w.Header().Set(telemetryprotocol.HeaderDuplicate, "true")
	}
}

// HandleTelemetry records a LIVE health heartbeat from the CALLER (the node from the bearer token,
// never the request body) — beta9-smoke-hardening plan-1. It carries NO applied_generation/checksum
// and writes the node's last_seen, conditions, volatile current metrics, and bounded cadence history
// via RecordTelemetry: telemetry is high-frequency observability kept strictly separate from deploy
// custody, so a heartbeat can never advance or regress the applied generation. Both routine
// endpoints are intentionally outside the durable audit chain because their useful high-frequency
// state is represented in Fleet. Conditions are server-stamped with the controller clock inside the
// store (a node clock cannot be trusted for ageing). The validated metrics map (the
// framework's extension slot — e.g. resource and probe results) feeds bounded history in full; only
// its cataloged live-visible subset feeds node.telemetry. It is not durable deploy state.
// Returns {status:"ok"}. Routed through op() (routes_controller.go): the adapter runs the method
// guard + structural identity() check before this body.
func (h *ControllerHandler) HandleTelemetry(ctx context.Context, tenant controller.TenantID, node string, w http.ResponseWriter, r *http.Request) (any, *apierr.Error) {
	metadata, ae := telemetryMetadata(r)
	if ae != nil {
		return nil, ae
	}
	var req telemetryRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		return nil, codedErr(apierr.CodeReqInvalidBody, err)
	}
	if ae := validateConditions(req.Conditions); ae != nil {
		return nil, ae
	}
	if ae := validateMetrics(req.Metrics); ae != nil {
		return nil, ae
	}

	receivedAt := time.Now().UTC()
	if metadata == nil {
		if err := h.store.RecordTelemetry(ctx, tenant, node, req.Conditions, req.Metrics, req.AgentVersion, receivedAt); err != nil {
			if errors.Is(err, controller.ErrNotFound) {
				return nil, apierr.New(apierr.CodeNodeNotFound).Wrap(err)
			}
			return nil, codedErr(apierr.CodeInternalStorage, err)
		}
		return map[string]string{"status": "ok"}, nil
	}

	receipt, err := h.store.RecordTelemetrySequenced(ctx, tenant, node, req.Conditions, req.Metrics, req.AgentVersion, metadata.bootID, metadata.sequence, metadata.sampledAt, metadata.interval, receivedAt)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			return nil, apierr.New(apierr.CodeNodeNotFound).Wrap(err)
		}
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}
	writeTelemetryReceiptHeaders(w, metadata, receipt)
	return map[string]string{"status": "ok"}, nil
}

// HandleRekey re-registers the CALLER's rotated WireGuard PUBLIC key (the node from
// the bearer token, never the request body). It stamps the new public key onto the
// node record and clears RekeyRequested, all via GetNode/UpsertNode so every other
// field is preserved. It is the agent's response to a rekey_requested=true /config:
// the controller never sees a private key (zero-knowledge custody). An empty
// wg_public_key is a 400. Returns {ok:true}. Routed through op() (routes_controller.go):
// the adapter runs the method guard + structural identity() check before this body.
func (h *ControllerHandler) HandleRekey(ctx context.Context, tenant controller.TenantID, node string, w http.ResponseWriter, r *http.Request) (any, *apierr.Error) {
	var req rekeyRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		return nil, codedErr(apierr.CodeReqInvalidBody, err)
	}
	if req.WGPublicKey == "" {
		return nil, apierr.New(apierr.CodeReqFieldRequired).With("field", "wg_public_key")
	}

	// controller.Rekey swaps the key + clears the flag under the per-tenant op lock
	// and enforces the SAME identity invariant as enroll (plan-6 review: the rekey
	// write path must not be able to create a duplicate the enroll dedupe forbids).
	if err := controller.Rekey(ctx, h.store, tenant, node, req.WGPublicKey, time.Now()); err != nil {
		// Context-specific: an unknown node is a 404 here (kept out of the central table).
		if errors.Is(err, controller.ErrNotFound) {
			return nil, apierr.New(apierr.CodeNodeNotFound).Wrap(err)
		}
		// Context-free WG-key sentinels (ErrInvalidWGKey → req_field_invalid{wg_public_key};
		// ErrDuplicateWGKey → duplicate_wg_key) map through the central table (errmap.go).
		if ae := mapControllerErr(err); ae != nil {
			return nil, ae
		}
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}
	return rekeyResponseJSON{OK: true}, nil
}
