/**
 * Live view helpers: viewer tokens, embed URLs and revoking viewers.
 *
 * Gateway contract (plan 05, section 3.4):
 * - `POST /v1/sandboxes/{id}/viewer-tokens` with
 *   `{scope, channels, ttlSeconds}` returns `{token, expiresAt, embedUrl}`.
 * - `POST /v1/sandboxes/{id}/viewer-tokens:revoke-all` returns 200 `{epoch}`.
 */
import { type ConnectionConfig, InvalidArgumentError } from "e2b";

import { postJson } from "./http.js";

export const SCOPES = ["view", "control"] as const;
export const CHANNELS = ["dom", "pty", "desktop", "thumb"] as const;
export const MIN_TTL = 30;
export const MAX_TTL = 600;

export type ViewerScope = (typeof SCOPES)[number];
export type ViewerChannel = (typeof CHANNELS)[number];

export interface ViewerTokenOpts {
  scope?: ViewerScope;
  channels?: ViewerChannel[];
  /** Lifetime in seconds, 30 to 600. Default 600. */
  ttl?: number;
}

export interface ViewerTokenClaims {
  iss: string;
  aud: string;
  sub: string;
  tid: string;
  sbx: string;
  ch: ViewerChannel[];
  scp: ViewerScope;
  ep: number;
  iat: number;
  nbf: number;
  exp: number;
  jti: string;
}

/** A signed viewer token. `toString`/`toJSON` never include the token itself. */
export class ViewerToken {
  readonly expiresAt: Date;
  readonly #token: string;
  readonly #embedUrl: string;

  constructor(token: string, expiresAt: Date, embedUrl: string) {
    this.#token = token;
    this.expiresAt = expiresAt;
    this.#embedUrl = embedUrl;
  }

  get token(): string {
    return this.#token;
  }

  get embedUrl(): string {
    return this.#embedUrl;
  }

  /** Decode the JWS payload without checking the signature (inspection only). */
  claims(): ViewerTokenClaims {
    const parts = this.#token.split(".");
    if (parts.length !== 3 || !parts[1]) throw new Error("viewer token is not a compact JWS");
    return JSON.parse(Buffer.from(parts[1], "base64url").toString("utf8")) as ViewerTokenClaims;
  }

  toString(): string {
    return `ViewerToken(expiresAt=${this.expiresAt.toISOString()})`;
  }

  toJSON(): { expiresAt: string } {
    return { expiresAt: this.expiresAt.toISOString() };
  }
}

function validate(scope: string, channels: string[], ttl: number): void {
  if (!(SCOPES as readonly string[]).includes(scope)) {
    throw new InvalidArgumentError(`scope must be one of ${SCOPES.join(", ")}, got ${scope}`);
  }
  if (channels.length === 0) throw new InvalidArgumentError("channels must not be empty");
  const unknown = channels.filter((c) => !(CHANNELS as readonly string[]).includes(c));
  if (unknown.length) {
    throw new InvalidArgumentError(`unknown channels ${unknown.join(", ")}; allowed ${CHANNELS.join(", ")}`);
  }
  if (!Number.isInteger(ttl) || ttl < MIN_TTL || ttl > MAX_TTL) {
    throw new InvalidArgumentError(`ttl must be an integer between ${MIN_TTL} and ${MAX_TTL} seconds`);
  }
}

/** `sandbox.live`: viewer tokens for the live view of one sandbox. */
export class Live {
  constructor(
    private readonly sandboxId: string,
    private readonly conn: ConnectionConfig,
  ) {}

  private path(suffix = ""): string {
    return `/v1/sandboxes/${encodeURIComponent(this.sandboxId)}/viewer-tokens${suffix}`;
  }

  /** Mint a viewer token (default: view-only DOM channel, 600 s). */
  async token(opts: ViewerTokenOpts = {}): Promise<ViewerToken> {
    const scope = opts.scope ?? "view";
    const channels = opts.channels ?? ["dom"];
    const ttl = opts.ttl ?? MAX_TTL;
    validate(scope, channels, ttl);
    const body = await postJson<{ token: string; expiresAt: string; embedUrl: string }>(
      this.conn,
      this.path(),
      { scope, channels, ttlSeconds: ttl },
    );
    if (!body) throw new InvalidArgumentError("gateway returned an empty viewer token response");
    return new ViewerToken(body.token, new Date(body.expiresAt), body.embedUrl);
  }

  /** Mint a token for one channel and return its embed URL. */
  async url(opts: { mode?: ViewerChannel; scope?: ViewerScope; ttl?: number } = {}): Promise<string> {
    const tok = await this.token({ scope: opts.scope, channels: [opts.mode ?? "dom"], ttl: opts.ttl });
    return tok.embedUrl;
  }

  /**
   * Embed URL for the desktop channel. Control scope by default: the desktop
   * channel carries RFB input, which a view token may not send.
   */
  async desktopUrl(opts: { control?: boolean; ttl?: number } = {}): Promise<string> {
    return this.url({ mode: "desktop", scope: opts.control === false ? "view" : "control", ttl: opts.ttl });
  }

  /** Close every open viewer of this sandbox and invalidate issued tokens. */
  async revokeAll(): Promise<void> {
    await postJson(this.conn, this.path(":revoke-all"));
  }
}
