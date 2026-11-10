package opencode

// PinnedOpenCodeVersion is the OpenCode release whose V2 OpenAPI contract is
// vendored under contract/openapi-v2.2.7.json. CI installs this exact version
// for the live E2E suite.
const PinnedOpenCodeVersion = "2.2.7"

// PinnedOpenAPISHA256 is the SHA-256 of contract/openapi-v2.2.7.json. The
// contract test fails when the vendored file changes without this pin being
// updated, making an API refresh a deliberate reviewable step.
const PinnedOpenAPISHA256 = "cd58a5f018895015583ef149d537c46051432f379062700f2ab727ee12e0e2de"
