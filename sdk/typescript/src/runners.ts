/**
 * Anona Runners: read GitHub Actions runner installations and jobs.
 *
 * Beta: Anona Runners may change.
 *
 * Gateway contract (Anona routes, not E2B): `GET /v1/runners/installations`,
 * `POST /v1/runners/installations/link`, `GET /v1/runners/jobs` and
 * `GET /v1/runners/jobs/{id}`. A 401 rejects with `AuthenticationError`;
 * `403 runners_not_eligible` rejects with the same type carrying the API's message.
 */
import { ConnectionConfig } from "e2b";

import { anonaDefaults } from "./config.js";
import { getJson, postJson } from "./http.js";

export interface Installation {
  id: number;
  accountLogin: string;
  accountType: "Organization" | "User" | string;
  suspended: boolean;
  linkedAt: string;
}

export interface InstallationList {
  eligible: boolean;
  reason: string;
  allowedSizes: string[];
  installations: Installation[];
}

export type RunnerJobStatus =
  | "queued"
  | "dispatching"
  | "dispatched"
  | "running"
  | "completed"
  | "refused"
  | "failed"
  | "cancelled";

export interface RunnerJob {
  id: string;
  repo: string;
  workflow: string;
  jobName: string;
  label: string;
  size: string;
  status: RunnerJobStatus | string;
  reason: string;
  sandboxId?: string | null;
  queuedAt?: string | null;
  startedAt?: string | null;
  finishedAt?: string | null;
  conclusion?: string | null;
  githubUrl: string;
}

export interface ListJobsOpts {
  repo?: string;
  status?: string;
  /** Default 20, max 500. */
  limit?: number;
  /** Cursor from a previous page's `nextCursor`: list older jobs. */
  before?: string;
}

export interface RunnerJobPage {
  jobs: RunnerJob[];
  /** Cursor for the next older page; empty on the last page. */
  nextCursor: string;
}

function config(): ConnectionConfig {
  return new ConnectionConfig({ ...anonaDefaults() });
}

export const runners = {
  async listInstallations(): Promise<InstallationList> {
    const body = await getJson<Partial<InstallationList>>(config(), "/v1/runners/installations");
    return {
      eligible: body?.eligible ?? false,
      reason: body?.reason ?? "",
      allowedSizes: body?.allowedSizes ?? [],
      installations: body?.installations ?? [],
    };
  },

  /** The GitHub URL to open to install the Anona Runners app. */
  async linkUrl(): Promise<string> {
    const body = await postJson<{ url?: string }>(config(), "/v1/runners/installations/link");
    return body?.url ?? "";
  },

  async listJobs(opts: ListJobsOpts = {}): Promise<RunnerJob[]> {
    return (await runners.listJobsPage(opts)).jobs;
  },

  /** One page of jobs plus the cursor for the next older page. */
  async listJobsPage(opts: ListJobsOpts = {}): Promise<RunnerJobPage> {
    const q = new URLSearchParams();
    if (opts.repo) q.set("repo", opts.repo);
    if (opts.status) q.set("status", opts.status);
    if (opts.before) q.set("before", opts.before);
    q.set("limit", String(opts.limit ?? 20));
    const body = await getJson<{ jobs?: RunnerJob[]; nextCursor?: string }>(config(), `/v1/runners/jobs?${q}`);
    return { jobs: body?.jobs ?? [], nextCursor: body?.nextCursor ?? "" };
  },

  async getJob(id: string): Promise<RunnerJob> {
    const body = await getJson<RunnerJob>(config(), `/v1/runners/jobs/${encodeURIComponent(id)}`);
    return body as RunnerJob;
  },
};
