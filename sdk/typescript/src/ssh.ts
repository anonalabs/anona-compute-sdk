/**
 * SSH helpers: short-lived user certificates and an `ssh_config` snippet.
 *
 * Gateway contract (plan 05, section 3.7):
 * `POST /v1/sandboxes/{id}/ssh-certs` with `{publicKey}` returns
 * `{certificate, user, host, proxyCommand, expiresAt}`; the certificate is
 * valid for 10 minutes on that sandbox only (principal `sbx:<id>`) and logs
 * in as `user`.
 */
import { type ConnectionConfig, InvalidArgumentError } from "e2b";

import { postJson } from "./http.js";

export const DEFAULT_USER = "user";
export const DEFAULT_PROXY_COMMAND = "anona-compute ssh --proxy %h";
// The key types the gateway signs.
const KEY_TYPES = ["ssh-ed25519", "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521", "ssh-rsa"];

export interface SshCertificate {
  cert: string;
  user: string;
  host: string;
  proxyCommand: string;
  expiresAt?: Date;
}

export interface SshConfigOpts {
  identityFile?: string;
  certificateFile?: string;
  alias?: string;
  user?: string;
  proxyCommand?: string;
}

/** `sandbox.ssh`: certificates and client config for one sandbox. */
export class Ssh {
  constructor(
    private readonly sandboxId: string,
    private readonly conn: ConnectionConfig,
  ) {}

  /** Ask the gateway to sign an OpenSSH public key (the key text, not a path). */
  async certificate(publicKey: string): Promise<SshCertificate> {
    const key = publicKey.trim();
    if (key.includes("PRIVATE KEY")) throw new InvalidArgumentError("refusing to send a private key");
    if (!KEY_TYPES.some((t) => key.startsWith(t + " "))) {
      throw new InvalidArgumentError(`unsupported public key; expected one of ${KEY_TYPES.join(", ")}`);
    }
    const body = await postJson<{
      certificate: string;
      user?: string;
      host?: string;
      proxyCommand?: string;
      expiresAt?: string;
    }>(this.conn, `/v1/sandboxes/${encodeURIComponent(this.sandboxId)}/ssh-certs`, {
      publicKey: key,
    });
    if (!body) throw new InvalidArgumentError("gateway returned an empty certificate response");
    return {
      cert: body.certificate,
      user: body.user || DEFAULT_USER,
      host: body.host || this.sandboxId,
      proxyCommand: body.proxyCommand || DEFAULT_PROXY_COMMAND,
      expiresAt: body.expiresAt ? new Date(body.expiresAt) : undefined,
    };
  }

  /** Return an `ssh_config` block for this sandbox. Makes no network call. */
  config(opts: SshConfigOpts = {}): string {
    const identity = opts.identityFile ?? "~/.ssh/anona_ed25519";
    const cert = opts.certificateFile ?? `${identity}-cert.pub`;
    return [
      `Host ${opts.alias ?? `anona-${this.sandboxId}`}`,
      `  HostName ${this.sandboxId}`,
      `  User ${opts.user ?? DEFAULT_USER}`,
      `  ProxyCommand ${opts.proxyCommand ?? DEFAULT_PROXY_COMMAND}`,
      `  IdentityFile ${identity}`,
      `  CertificateFile ${cert}`,
      "  IdentitiesOnly yes",
      "  StrictHostKeyChecking accept-new",
      "",
    ].join("\n");
  }
}
