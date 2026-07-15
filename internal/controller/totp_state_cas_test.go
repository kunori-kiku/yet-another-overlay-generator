package controller

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCompareAndSetTOTPStatePreservesLoginCredentialAndRejectsStaleState(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			store := impl.factory(t)
			const tenant = TenantID("totp-state-cas")
			now := time.Unix(1_700_000_000, 0).UTC()
			if err := store.PutOperator(ctx, tenant, Operator{
				Username: "admin", PasswordHash: "password-v1", CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				t.Fatalf("PutOperator: %v", err)
			}

			// Simulate a passkey registration landing while TOTP confirmation is in flight.
			login := &LoginCredential{
				Alg: "webauthn-eddsa", CredentialID: "login-a", PublicKeyPEM: "pem-a",
				RPID: "rp.example", Origin: "https://rp.example",
			}
			if err := store.CompareAndSetLoginCredential(ctx, tenant, "admin", nil, login, now.Add(time.Minute)); err != nil {
				t.Fatalf("CompareAndSetLoginCredential: %v", err)
			}
			enabled := TOTPState{Secret: "totp-a", LastUsedStep: 100}
			if err := store.CompareAndSetTOTPState(ctx, tenant, "admin", TOTPState{}, enabled, now.Add(2*time.Minute)); err != nil {
				t.Fatalf("enable TOTP CAS: %v", err)
			}

			got, err := store.GetOperator(ctx, tenant, "admin")
			if err != nil {
				t.Fatalf("GetOperator: %v", err)
			}
			if got.LoginCredential == nil || *got.LoginCredential != *login {
				t.Fatalf("TOTP CAS clobbered concurrent login credential: %+v", got.LoginCredential)
			}
			if got.TOTPSecret != enabled.Secret || got.TOTPLastUsedStep != enabled.LastUsedStep {
				t.Fatalf("stored TOTP state = {%q,%d}, want %+v", got.TOTPSecret, got.TOTPLastUsedStep, enabled)
			}

			// A login consuming a newer TOTP step invalidates the disable snapshot. The stale disable
			// must neither clear TOTP nor touch the login credential.
			if advanced, err := store.AdvanceTOTPStep(ctx, tenant, "admin", 101); err != nil || !advanced {
				t.Fatalf("AdvanceTOTPStep: advanced=%v err=%v", advanced, err)
			}
			if err := store.CompareAndSetTOTPState(ctx, tenant, "admin", enabled, TOTPState{}, now.Add(3*time.Minute)); !errors.Is(err, ErrTOTPStateChanged) {
				t.Fatalf("stale disable = %v, want ErrTOTPStateChanged", err)
			}
			got, _ = store.GetOperator(ctx, tenant, "admin")
			if got.TOTPSecret != enabled.Secret || got.TOTPLastUsedStep != 101 {
				t.Fatalf("stale disable changed TOTP state: %+v", got)
			}
			if got.LoginCredential == nil || *got.LoginCredential != *login {
				t.Fatalf("stale disable changed login credential: %+v", got.LoginCredential)
			}
		})
	}
}
