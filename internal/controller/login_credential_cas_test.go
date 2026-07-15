package controller

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCompareAndSetLoginCredentialPreservesAccountFieldsAndRejectsStaleState(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)
			const tenant = TenantID("login-cas")
			now := time.Unix(1_700_000_000, 0).UTC()
			op := Operator{
				Username: "admin", PasswordHash: "password-v1", TOTPSecret: "totp-v1",
				CreatedAt: now, UpdatedAt: now,
			}
			if err := s.PutOperator(ctx, tenant, op); err != nil {
				t.Fatalf("PutOperator: %v", err)
			}

			a := &LoginCredential{Alg: "webauthn-eddsa", CredentialID: "a", PublicKeyPEM: "pem-a", RPID: "rp", Origin: "https://rp"}
			if err := s.CompareAndSetLoginCredential(ctx, tenant, "admin", nil, a, now.Add(time.Minute)); err != nil {
				t.Fatalf("nil -> A CAS: %v", err)
			}

			// Simulate an unrelated account update landing during a browser ceremony. The field-scoped
			// CAS below must preserve it rather than writing back a stale whole Operator snapshot.
			current, err := s.GetOperator(ctx, tenant, "admin")
			if err != nil {
				t.Fatalf("GetOperator: %v", err)
			}
			current.PasswordHash = "password-v2"
			current.TOTPSecret = "totp-v2"
			if err := s.PutOperator(ctx, tenant, current); err != nil {
				t.Fatalf("concurrent PutOperator: %v", err)
			}
			b := &LoginCredential{Alg: "webauthn-es256", CredentialID: "b", PublicKeyPEM: "pem-b", RPID: "rp", Origin: "https://rp"}
			if err := s.CompareAndSetLoginCredential(ctx, tenant, "admin", a, b, now.Add(2*time.Minute)); err != nil {
				t.Fatalf("A -> B CAS: %v", err)
			}
			got, err := s.GetOperator(ctx, tenant, "admin")
			if err != nil {
				t.Fatalf("GetOperator after CAS: %v", err)
			}
			if got.PasswordHash != "password-v2" || got.TOTPSecret != "totp-v2" {
				t.Fatalf("CAS clobbered concurrent account fields: %+v", got)
			}
			if got.LoginCredential == nil || *got.LoginCredential != *b {
				t.Fatalf("stored credential = %+v, want B", got.LoginCredential)
			}

			if err := s.CompareAndSetLoginCredential(ctx, tenant, "admin", a, nil, now.Add(3*time.Minute)); !errors.Is(err, ErrLoginCredentialChanged) {
				t.Fatalf("stale A -> nil CAS = %v, want ErrLoginCredentialChanged", err)
			}
			got, _ = s.GetOperator(ctx, tenant, "admin")
			if got.LoginCredential == nil || *got.LoginCredential != *b {
				t.Fatalf("stale disable cleared/replaced B: %+v", got.LoginCredential)
			}
		})
	}
}
