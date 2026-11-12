/**
 * Anona Compute extras on top of the E2B SDK.
 *
 * The stock `e2b` package already works with `E2B_DOMAIN=compute.anonalabs.com`
 * and `E2B_API_KEY`. This package adds `configure()`, a `Sandbox` subclass that
 * defaults to the Anona domain, live view tokens, SSH certificates and idle pause.
 */
export {
  ENV_MAP,
  DEFAULT_API_URL,
  DEFAULT_DOMAIN,
  DOMAIN_ENV,
  anonaDefaults,
  applyEnv,
  configure,
  type ConfigureOpts,
} from "./config.js";
export { IdlePause, MAX_IDLE_PAUSE_MINUTES } from "./idle.js";
export {
  CHANNELS,
  Live,
  MAX_TTL,
  MIN_TTL,
  SCOPES,
  ViewerToken,
  type ViewerChannel,
  type ViewerScope,
  type ViewerTokenClaims,
  type ViewerTokenOpts,
} from "./live.js";
export { Sandbox } from "./sandbox.js";
export { SIZES, SIZE_METADATA_KEY, parseSize, withSize, type SandboxSize } from "./size.js";
export { Ssh, type SshCertificate, type SshConfigOpts } from "./ssh.js";

/** @deprecated E2B deprecates `E2B_API_URL`; set `E2B_DOMAIN` instead (decision X-5). */
export const API_URL_ENV = "E2B_API_URL";
