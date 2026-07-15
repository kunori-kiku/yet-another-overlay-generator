package api

// handler_webauthn_enrollment.go supplies the one-use server challenge used to prove user
// verification while a NEW browser WebAuthn credential is enrolled. Registration itself still
// uses navigator.credentials.create(); immediately afterwards the candidate credential signs this
// challenge with navigator.credentials.get(userVerification:"required"). The corresponding finish
// handlers verify that assertion against the candidate public key before persisting either a login
// passkey or the keystone operator passkey.

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/trustlist"
)

// Ten minutes comfortably covers create()+get() on slower authenticators. The record is one-use,
// authenticated, and replace-bounded to one live challenge per actor+purpose.
const webAuthnEnrollmentChallengeTTL = 10 * time.Minute

const (
	webAuthnEnrollmentLogin    = "login"
	webAuthnEnrollmentKeystone = "keystone"
)

type webAuthnEnrollmentBeginRequestJSON struct {
	Purpose string `json:"purpose"`
}

type webAuthnEnrollmentBeginResponseJSON struct {
	Challenge string `json:"challenge"`
}

// webAuthnEnrollmentSubject purpose-scopes a stored challenge as well as actor-scoping it. A login
// enrollment proof cannot therefore be replayed into a keystone pin (or vice versa), even for the
// same authenticated operator.
func webAuthnEnrollmentSubject(actor, purpose string) string {
	return "webauthn-enrollment:" + purpose + ":" + actor
}

// HandleWebAuthnEnrollmentBegin issues the server nonce a newly-created candidate credential must
// assert over before either enrollment endpoint will store it. The route is operator-authenticated;
// login-purpose requests additionally require a real operator account (not break-glass auth).
func (h *ControllerHandler) HandleWebAuthnEnrollmentBegin(ctx context.Context, tenant controller.TenantID, actor string, w http.ResponseWriter, r *http.Request) (any, *apierr.Error) {
	var req webAuthnEnrollmentBeginRequestJSON
	if err := decodeJSON(w, r, &req); err != nil {
		return nil, codedErr(apierr.CodeReqInvalidBody, err)
	}
	if req.Purpose != webAuthnEnrollmentLogin && req.Purpose != webAuthnEnrollmentKeystone {
		return nil, apierr.New(apierr.CodeReqFieldInvalid).With("field", "purpose")
	}
	// A break-glass bearer has an operator identity but no login account. Reject a login-purpose
	// begin here instead of issuing a challenge that /passkey/register can never consume.
	if req.Purpose == webAuthnEnrollmentLogin {
		if _, aerr := h.currentOperatorAccount(ctx, tenant, actor); aerr != nil {
			return nil, aerr
		}
	}

	now := time.Now().UTC()
	challenge, record := controller.NewAssertionChallenge(
		webAuthnEnrollmentSubject(actor, req.Purpose),
		webAuthnEnrollmentChallengeTTL,
		now,
	)
	if err := h.store.ReplaceAssertionChallengeForSubject(ctx, tenant, record, now); err != nil {
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}
	return webAuthnEnrollmentBeginResponseJSON{Challenge: challenge}, nil
}

// verifyWebAuthnEnrollmentProof atomically consumes the purpose/actor-scoped server challenge and
// verifies a UV assertion from the exact candidate credential. It is the ONLY server-side UV gate:
// ordinary login/signing/membership assertions intentionally use the generic UP+signature verifier.
func (h *ControllerHandler) verifyWebAuthnEnrollmentProof(
	ctx context.Context,
	tenant controller.TenantID,
	actor, purpose string,
	proof *trustlist.SignedTrustList,
	pin trustlist.PinnedCredential,
) *apierr.Error {
	if proof == nil {
		return apierr.New(apierr.CodeReqFieldRequired).With("field", "enrollment_proof")
	}
	challengeText, err := trustlist.AssertionChallenge(*proof)
	if err != nil {
		return apierr.New(apierr.CodeWebAuthnEnrollmentVerifyFailed).Wrap(err)
	}
	challenge, err := base64.RawURLEncoding.DecodeString(challengeText)
	if err != nil {
		return apierr.New(apierr.CodeWebAuthnEnrollmentVerifyFailed).Wrap(err)
	}
	if len(challenge) != controller.AssertionChallengeBytes {
		return apierr.New(apierr.CodeWebAuthnEnrollmentVerifyFailed).Wrap(errors.New("enrollment challenge has an invalid length"))
	}

	// Verify first so a malformed/UP-only assertion does not destroy an otherwise reusable challenge.
	// Replay safety still comes from the atomic consume below: concurrent valid submissions can both
	// finish public-key work, but exactly one can burn the server-issued nonce.
	if err := trustlist.VerifyUserVerifiedAssertion(*proof, pin, challenge); err != nil {
		return apierr.New(apierr.CodeWebAuthnEnrollmentVerifyFailed).Wrap(err)
	}
	err = h.store.ConsumeAssertionChallenge(
		ctx,
		tenant,
		controller.HashToken(challengeText),
		webAuthnEnrollmentSubject(actor, purpose),
		time.Now().UTC(),
	)
	if err != nil {
		if errors.Is(err, controller.ErrChallengeInvalid) {
			return apierr.New(apierr.CodeWebAuthnEnrollmentVerifyFailed).Wrap(err)
		}
		return codedErr(apierr.CodeInternalStorage, err)
	}
	return nil
}
