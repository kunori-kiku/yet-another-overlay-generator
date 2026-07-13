package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/trustlist"
)

// stagedManifest returns the tenant's STAGED membership manifest (the to-be-signed
// canonical bytes CompileAndStage stored) and its epoch. The manifest binds, per member,
// the node's bundle digest (BundleSHA256 = hex(sha256(checksums.sha256))), so the
// off-host signature covers what RUNS — install.sh + every config — not merely the
// member list. The manifest is built at STAGE time (not projected from the live
// registry), so GET /trustlist and POST /trustlist-signature both operate over the exact
// bytes a node will be served. ErrNotFound surfaces when nothing has been staged yet.
func (h *ControllerHandler) stagedManifest(ctx context.Context, t controller.TenantID) (canonical []byte, epoch int64, err error) {
	stored, err := h.store.GetCurrentSignedTrustList(ctx, t)
	if err != nil {
		return nil, 0, err
	}
	return stored.TrustListJSON, stored.Epoch, nil
}

// pinFromParts builds the trustlist.PinnedCredential the verifier checks against from a
// credential's raw fields, parsing the PEM by algorithm and carrying through the WebAuthn
// RPID/Origin binding values. It is shared by the keystone membership credential
// (pinFromCredential) and the per-operator passkey LOGIN credential
// (pinFromLoginCredential, handler_passkey.go) — the same WebAuthn verification, two
// callers.
func pinFromParts(alg, credentialID, publicKeyPEM, rpid, origin string) (trustlist.PinnedCredential, error) {
	pin := trustlist.PinnedCredential{
		Alg:          trustlist.Alg(alg),
		CredentialID: credentialID,
		RPID:         rpid,
		Origin:       origin,
	}
	switch trustlist.Alg(alg) {
	case trustlist.AlgEd25519, trustlist.AlgWebAuthnEdDSA:
		pub, err := trustlist.ParseEd25519PinPEM([]byte(publicKeyPEM))
		if err != nil {
			return trustlist.PinnedCredential{}, err
		}
		pin.Ed25519Pub = pub
	case trustlist.AlgWebAuthnES256:
		pub, err := trustlist.ParseES256Pin([]byte(publicKeyPEM))
		if err != nil {
			return trustlist.PinnedCredential{}, err
		}
		pin.ES256Pub = pub
	default:
		return trustlist.PinnedCredential{}, errors.New("unsupported operator credential algorithm")
	}
	return pin, nil
}

// pinFromCredential builds the trustlist.PinnedCredential the verifier checks against
// from a stored keystone OperatorCredential.
func pinFromCredential(c controller.OperatorCredential) (trustlist.PinnedCredential, error) {
	return pinFromParts(c.Alg, c.CredentialID, c.PublicKeyPEM, c.RPID, c.Origin)
}

// HandleOperatorCredential is the keystone-credential resource (operator-only): GET reports the
// SERVER-authoritative keystone status; POST pins or rotates the off-host operator signing
// credential. The op() wrapper enforces operator auth for both methods.
func (h *ControllerHandler) HandleOperatorCredential(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleOperatorCredentialStatus(w, r)
	case http.MethodPost:
		h.handleOperatorCredentialPin(w, r)
	default:
		writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "GET, POST"))
	}
}

// handleOperatorCredentialStatus (GET) returns the keystone status from SERVER truth: whether a
// credential is pinned, its PUBLIC identifiers + fingerprint, and whether a rotation has left the
// served fleet needing a fresh signed deploy. It returns ONLY non-secret public material so the
// panel can stop deriving "enrolled" from a browser-local cache — a cleared browser would
// otherwise falsely read "Not enrolled" and invite a fleet-stranding re-pin.
func (h *ControllerHandler) handleOperatorCredentialStatus(w http.ResponseWriter, r *http.Request) {
	tenant, _, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	cred, err := h.store.GetOperatorCredential(r.Context(), tenant)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			writeJSON(w, http.StatusOK, operatorCredentialStatusJSON{Pinned: false})
			return
		}
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	// A stored credential that no longer parses is an internal fault (it was validated at pin time),
	// not a client error.
	fp, err := controller.KeystoneFingerprint(cred)
	if err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	redeploy, err := controller.KeystoneRedeployRequired(r.Context(), h.store, tenant, cred)
	if err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	writeJSON(w, http.StatusOK, operatorCredentialStatusJSON{
		Pinned:       true,
		Alg:          cred.Alg,
		CredentialID: cred.CredentialID,
		RPID:         cred.RPID,
		Origin:       cred.Origin,
		Fingerprint:  fp,
		// Non-secret public PEM (audit-only): lets a cleared/fresh browser recover the WebAuthn
		// signing descriptor and re-prompt the authenticator without re-pinning. See the
		// operatorCredentialStatusJSON doc for why this is safe to serve.
		PublicKeyPEM:     cred.PublicKeyPEM,
		RedeployRequired: redeploy,
	})
}

// handleOperatorCredentialPin (POST) pins or rotates the off-host operator signing credential,
// turning KEYSTONE ON for the tenant. The public_key_pem MUST parse for the declared alg (a
// malformed pin is rejected here, not at verify time). Rotation discipline: re-pinning a
// DIFFERENT credential strands every enrolled node until each is re-provisioned out of band AND a
// fresh deploy is signed under the new key, so a change is a deliberate, acknowledged, audited
// operation — never a silent overwrite:
//
//   - first pin (no prior credential): trust-on-first-use, audited "pin-operator-credential".
//   - idempotent re-pin (same key + binding): refreshed in place, no fleet impact, no audit.
//   - changed credential WITHOUT rotate:true: refused (CodeKeystoneRotationRequiresAck), no mutation.
//   - changed credential WITH rotate:true: stored, audited "rotate-operator-credential", and the
//     result reports redeploy_required when the served fleet is still signed under the old key.
//
// A keystone, once pinned, is intentionally NEVER turned OFF through any API/CLI surface (there is
// no DELETE here, no Store unset, no command). This is deliberate: pinning is a one-way trust
// commitment. The only way to clear it is to delete operator_credential.json out of band on the
// controller host — which is UNSUPPORTED and strands the fleet. Because a keystone-OFF promote does
// not advance the served trust-list, an out-of-band un-pin followed by a keystone-OFF deploy and a
// re-pin would leave /config serving a fresh bundle paired with the stale last-keystone-ON manifest;
// every node then fails closed on its digest binding until the operator signs + promotes a fresh
// deploy under the re-enabled keystone. That is recoverable (re-sign + promote), never a forgery.
func (h *ControllerHandler) handleOperatorCredentialPin(w http.ResponseWriter, r *http.Request) {
	tenant, operator, ok := identity(r.Context())
	if !ok {
		writeAPIError(w, apierr.New(apierr.CodeInternalIdentityMissing))
		return
	}
	var req operatorCredentialRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		writeCodedOr(w, apierr.CodeReqInvalidBody, err)
		return
	}
	// Validate the PEM parses for the declared algorithm before pinning it.
	switch trustlist.Alg(req.Alg) {
	case trustlist.AlgEd25519, trustlist.AlgWebAuthnEdDSA:
		if _, err := trustlist.ParseEd25519PinPEM([]byte(req.PublicKeyPEM)); err != nil {
			writeAPIError(w, apierr.New(apierr.CodeReqFieldInvalid).With("field", "public_key_pem").Wrap(err))
			return
		}
	case trustlist.AlgWebAuthnES256:
		if _, err := trustlist.ParseES256Pin([]byte(req.PublicKeyPEM)); err != nil {
			writeAPIError(w, apierr.New(apierr.CodeReqFieldInvalid).With("field", "public_key_pem").Wrap(err))
			return
		}
	default:
		writeAPIError(w, apierr.New(apierr.CodeReqUnsupportedAlg).With("alg", req.Alg))
		return
	}

	newCred := controller.OperatorCredential{
		Alg:          req.Alg,
		CredentialID: req.CredentialID,
		PublicKeyPEM: req.PublicKeyPEM,
		RPID:         req.RPID,
		Origin:       req.Origin,
	}

	// RPID/Origin are baked UNQUOTED into the root-executed bootstrap script's OP_FLAGS
	// accumulator (the unquoted ${OP_FLAGS} is intentional word-splitting — see
	// validateOperatorCredentialBinding). Reject whitespace + shell metacharacters at pin
	// time so that expansion is safe by construction; this mirrors the validate-at-pin
	// discipline already applied to the mimic catalog (validateMimicCatalog).
	if err := validateOperatorCredentialBinding(newCred); err != nil {
		writeAPIError(w, err)
		return
	}

	prior, perr := h.store.GetOperatorCredential(r.Context(), tenant)
	switch {
	case errors.Is(perr, controller.ErrNotFound):
		// First pin: keystone turns ON (trust-on-first-use).
		if err := h.store.SetOperatorCredential(r.Context(), tenant, newCred); err != nil {
			writeCodedOr(w, apierr.CodeInternalStorage, err)
			return
		}
		if !h.auditKeystone(r.Context(), w, tenant, operator, "pin-operator-credential") {
			return
		}
		writeJSON(w, http.StatusOK, operatorCredentialPinResultJSON{OK: true})
		return
	case perr != nil:
		writeCodedOr(w, apierr.CodeInternalStorage, perr)
		return
	}

	same, err := controller.SameKeystoneCredential(prior, newCred)
	if err != nil {
		// The PRIOR stored credential won't parse — an internal fault (it was validated when pinned).
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	if same {
		// Idempotent re-pin of the same key + binding: refresh in place, no fleet impact, no audit churn.
		if err := h.store.SetOperatorCredential(r.Context(), tenant, newCred); err != nil {
			writeCodedOr(w, apierr.CodeInternalStorage, err)
			return
		}
		writeJSON(w, http.StatusOK, operatorCredentialPinResultJSON{OK: true, Unchanged: true})
		return
	}

	// A genuinely different credential — a rotation. Refuse without an explicit acknowledgement.
	if !req.Rotate {
		writeAPIError(w, apierr.New(apierr.CodeKeystoneRotationRequiresAck))
		return
	}
	if err := h.store.SetOperatorCredential(r.Context(), tenant, newCred); err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	if !h.auditKeystone(r.Context(), w, tenant, operator, "rotate-operator-credential") {
		return
	}
	redeploy, err := controller.KeystoneRedeployRequired(r.Context(), h.store, tenant, newCred)
	if err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return
	}
	writeJSON(w, http.StatusOK, operatorCredentialPinResultJSON{OK: true, Rotated: true, RedeployRequired: redeploy})
}

// auditKeystone appends a keystone credential audit entry (a trust transition that must be
// attributable in the hash-chained log). Unlike the best-effort stage-path audits, a failure to
// record a keystone pin/rotate IS surfaced as an error: the credential change already committed,
// so an un-auditable trust transition must not be reported as a clean success. Returns false (and
// writes the error) on failure so the caller stops.
func (h *ControllerHandler) auditKeystone(ctx context.Context, w http.ResponseWriter, tenant controller.TenantID, operator, action string) bool {
	if _, err := h.store.AppendAudit(ctx, tenant, controller.AuditEntry{
		Timestamp: time.Now(),
		Actor:     "operator:" + operator,
		Action:    action,
	}); err != nil {
		writeCodedOr(w, apierr.CodeInternalStorage, err)
		return false
	}
	return true
}

// HandleTrustList returns the STAGED membership manifest's canonical bytes (base64) plus
// its epoch (operator-only). These are EXACTLY the bytes that get signed and verified —
// the panel signs challenge = SHA256(decoded bytes). Each member carries its bundle
// digest, so the off-host signature covers what RUNS (install.sh + every config), not
// only the member list. 404 when nothing has been staged yet (stage a deploy first).
func (h *ControllerHandler) HandleTrustList(ctx context.Context, tenant controller.TenantID, _ string, _ http.ResponseWriter, _ *http.Request) (any, *apierr.Error) {
	canonical, epoch, err := h.stagedManifest(ctx, tenant)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			return nil, apierr.New(apierr.CodeNoStagedManifest)
		}
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}
	return trustListResponseJSON{
		TrustListJSON: base64.StdEncoding.EncodeToString(canonical),
		Epoch:         epoch,
	}, nil
}

// HandleTrustListSignature accepts the operator's off-host signature over the staged
// membership manifest (operator-only). It (a) re-derives the staged manifest canonical
// bytes server-side from the store; (b) rejects a submitted trustlist_json that does not
// byte-equal them (409 substitution guard — the operator must sign exactly what was
// staged); (c) builds the pinned credential from the stored OperatorCredential and
// verifies the signature with trustlist.Verify (400 on any verification failure); (d)
// stores the signature onto the staged manifest record (keeping its canonical bytes +
// epoch), records a "sign-trustlist" audit entry, and returns 200. A 412 is returned
// when no operator credential is pinned; a 404 when nothing has been staged.
func (h *ControllerHandler) HandleTrustListSignature(ctx context.Context, tenant controller.TenantID, actor string, w http.ResponseWriter, r *http.Request) (any, *apierr.Error) {
	var req trustListSignatureRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		return nil, codedErr(apierr.CodeReqInvalidBody, err)
	}

	// A signature is meaningless without a pinned credential to verify it against.
	cred, err := h.store.GetOperatorCredential(ctx, tenant)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			return nil, apierr.New(apierr.CodeNoPinnedCredential)
		}
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}

	// (a) Re-derive the staged manifest canonical bytes from the store.
	canonical, epoch, err := h.stagedManifest(ctx, tenant)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			return nil, apierr.New(apierr.CodeNoStagedManifest)
		}
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}

	// (b) Substitution guard: the operator must have signed EXACTLY these bytes.
	submitted, err := base64.StdEncoding.DecodeString(req.TrustListJSON)
	if err != nil {
		return nil, apierr.New(apierr.CodeReqFieldInvalid).With("field", "trustlist_json")
	}
	if !bytes.Equal(submitted, canonical) {
		return nil, apierr.New(apierr.CodeStagedManifestMismatch)
	}

	// Parse the staged manifest so trustlist.Verify checks the signature over its exact
	// canonical bytes (Verify re-canonicalizes the parsed value internally).
	var manifest trustlist.TrustList
	if err := json.Unmarshal(canonical, &manifest); err != nil {
		return nil, apierr.New(apierr.CodeInternalStorage).Wrap(err)
	}

	// (c) Verify the off-host signature against the PINNED credential.
	pin, err := pinFromCredential(cred)
	if err != nil {
		return nil, apierr.New(apierr.CodeInternalStorage).Wrap(err)
	}
	if err := trustlist.Verify(manifest, req.Signed, pin); err != nil {
		return nil, apierr.New(apierr.CodeManifestSignatureInvalid).Wrap(err)
	}

	// (d) Store the signature onto the staged manifest record (canonical bytes + epoch
	// unchanged) and audit it.
	signedJSON, err := json.Marshal(req.Signed)
	if err != nil {
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}
	if err := h.store.PutSignedTrustList(ctx, tenant, controller.StoredTrustList{
		TrustListJSON: canonical,
		SignatureJSON: signedJSON,
		Epoch:         epoch,
	}); err != nil {
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}
	if _, err := h.store.AppendAudit(ctx, tenant, controller.AuditEntry{
		Timestamp: time.Now(),
		Actor:     "operator:" + actor,
		Action:    "sign-trustlist",
	}); err != nil {
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}
	return map[string]any{"ok": true, "epoch": epoch}, nil
}
