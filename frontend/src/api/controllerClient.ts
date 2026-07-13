// HTTP client for the controller panel — a barrel over the per-domain modules under ./controller/.
// Each function targets an operator-facing route exposed by internal/api/handler_controller.go (the
// operator namespace, kept separate from the agent namespace /api/v1/agent/):
//   <baseURL><pathPrefix>/api/v1/operator/<route>
// Auth is uniformly Authorization: Bearer <operatorToken>. The backend responds with snake_case
// JSON, which each domain module maps at the boundary into the camelCase controller types (see
// ../types/controller).
//
// Error convention: any non-2xx throws a ControllerError, which preserves the backend's coded error
// envelope on .body ({ error: { code, message, params } }, or a non-JSON body wrapped as
// { error: "<text>" }). The store localizes it via tError at the catch site per the current language
// (it never surfaces the raw "<status> <JSON>" to the operator).
//
// This module re-exports the public client surface unchanged so existing import sites keep working;
// the implementation lives in the per-domain modules (auth / fleet / deploy / keystone / settings /
// release / telemetry) over the shared ./controller/transport plumbing.

export * from './controller/auth';
export * from './controller/fleet';
export * from './controller/deploy';
export * from './controller/keystone';
export * from './controller/settings';
export * from './controller/release';
export * from './controller/telemetry';

// From the shared transport, re-export ONLY the originally-public surface. The fetch/CSRF/error
// plumbing (request / postJSON / errorFromResponse / controllerErrorFromText) stays internal to the
// api/controller/ modules and is intentionally NOT part of the public client API.
export { ControllerError, controllerErrorCode, ctlURL } from './controller/transport';
export type { ControllerConfig } from './controller/transport';
