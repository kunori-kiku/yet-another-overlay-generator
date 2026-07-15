package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseRefVerifierIgnoresCheckoutSyntheticTag(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is unavailable")
	}

	script, err := filepath.Abs("verify-release-ref.sh")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	checkout := filepath.Join(root, "checkout")
	runReleaseRefGit(t, root, "init", "--bare", remote)
	runReleaseRefGit(t, root, "init", seed)
	runReleaseRefGit(t, seed, "config", "user.name", "Release Test")
	runReleaseRefGit(t, seed, "config", "user.email", "release-test@example.invalid")

	tracked := filepath.Join(seed, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runReleaseRefGit(t, seed, "add", "tracked.txt")
	runReleaseRefGit(t, seed, "commit", "-m", "first")
	runReleaseRefGit(t, seed, "branch", "-M", "main")
	runReleaseRefGit(t, seed, "remote", "add", "origin", remote)
	runReleaseRefGit(t, seed, "push", "-u", "origin", "main")

	const tag = "v9.9.9-rc.7"
	runReleaseRefGit(t, seed, "tag", "-a", tag, "-m", "fixture")
	runReleaseRefGit(t, seed, "push", "origin", "refs/tags/"+tag)
	approvedCommit := runReleaseRefGit(t, seed, "rev-parse", "HEAD")

	runReleaseRefGit(t, root, "clone", "--no-checkout", remote, checkout)
	// Reproduce actions/checkout@v4's tag-event refspec: the annotated tag was
	// fetched, then its local ref was force-updated to the peeled github.sha.
	runReleaseRefGit(t, checkout, "update-ref", "refs/tags/"+tag, approvedCommit)
	if got := runReleaseRefGit(t, checkout, "cat-file", "-t", "refs/tags/"+tag); got != "commit" {
		t.Fatalf("synthetic checkout tag type = %q, want commit", got)
	}
	if output, err := runReleaseRefVerifier(checkout, script, tag, approvedCommit); err != nil {
		t.Fatalf("annotated remote tag rejected after synthetic checkout: %v\n%s", err, output)
	}
	if got := runReleaseRefGit(t, checkout, "cat-file", "-t", "refs/tags/"+tag); got != "commit" {
		t.Fatalf("verifier rewrote checkout tag type to %q, want commit", got)
	}
	validationRef := "refs/yaog-release-tags/" + tag
	if got := runReleaseRefGit(t, checkout, "cat-file", "-t", validationRef); got != "tag" {
		t.Fatalf("validation ref type = %q, want tag", got)
	}

	// A lightweight remote tag must fail even if a stale annotated validation
	// object remains in the checkout from the successful verification above.
	runReleaseRefGit(t, seed, "tag", "-d", tag)
	runReleaseRefGit(t, seed, "tag", tag, approvedCommit)
	runReleaseRefGit(t, seed, "push", "--force", "origin", "refs/tags/"+tag)
	if output, err := runReleaseRefVerifier(checkout, script, tag, approvedCommit); err == nil {
		t.Fatalf("lightweight remote tag unexpectedly passed:\n%s", output)
	}

	// A new annotated object that moves the tag to another commit must also fail.
	if err := os.WriteFile(tracked, []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runReleaseRefGit(t, seed, "add", "tracked.txt")
	runReleaseRefGit(t, seed, "commit", "-m", "second")
	runReleaseRefGit(t, seed, "tag", "-d", tag)
	runReleaseRefGit(t, seed, "tag", "-a", tag, "-m", "moved")
	runReleaseRefGit(t, seed, "push", "--force", "origin", "refs/tags/"+tag)
	if output, err := runReleaseRefVerifier(checkout, script, tag, approvedCommit); err == nil {
		t.Fatalf("moved annotated remote tag unexpectedly passed:\n%s", output)
	}

	runReleaseRefGit(t, seed, "push", "origin", ":refs/tags/"+tag)
	if output, err := runReleaseRefVerifier(checkout, script, tag, approvedCommit); err == nil {
		t.Fatalf("absent remote tag unexpectedly passed:\n%s", output)
	}
}

func runReleaseRefVerifier(dir, script, tag, commit string) ([]byte, error) {
	command := exec.Command("bash", script, tag, commit)
	command.Dir = dir
	return command.CombinedOutput()
}

func runReleaseRefGit(t *testing.T, dir string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
