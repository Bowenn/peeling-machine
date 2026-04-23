// Mirrors internal/capture.Exchange — see docs/api.md.
export type HeaderPair = [string, string];

export interface Exchange {
  id: number;
  started_at: string;
  duration_ms: number;
  scheme: 'http' | 'https';
  method: string;
  host: string;
  path: string;
  req_headers: HeaderPair[];
  req_body?: string;
  req_truncated?: boolean;
  status: number;
  resp_headers?: HeaderPair[];
  resp_body?: string;
  resp_truncated?: boolean;
  error?: string;
}
