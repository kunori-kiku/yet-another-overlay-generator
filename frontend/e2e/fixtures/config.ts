// Single source of the E2E harness constants. The Go side (cmd/e2eserver) has NO
// hard-coded test credentials — globalSetup passes every value below to the controller
// boot as flags, so this file is authoritative. Changing a value here changes the boot.

// TENANT is the controller tenant id the boot serves under.
export const TENANT = 'e2e'

// OPERATOR_USER / OPERATOR_PASS are the seeded operator login account (controller.SeedOperator).
// The password clears the controller's min-length floor (8 chars).
export const OPERATOR_USER = 'e2e-operator'
export const OPERATOR_PASS = 'e2e-operator-pass'

// ENROLL_NODE is the node id the controller pre-mints a single-use enrollment token for and
// the id cmd/e2eagent enrolls as. It matches the seed topology's first node.
export const ENROLL_NODE = 'node-1'
