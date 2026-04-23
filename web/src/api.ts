import type { Exchange } from './types';

// When bundled and served by Go, the app is same-origin as the API (:9090).
// In Vite dev, Vite proxies /api → :9090 (see vite.config.ts). Either way
// relative URLs work — no base URL configuration needed.

export async function listExchanges(limit = 500): Promise<Exchange[]> {
  const r = await fetch(`/api/exchanges?limit=${limit}`);
  if (!r.ok) throw new Error(`list exchanges: ${r.status}`);
  const j: { exchanges: Exchange[] } = await r.json();
  return j.exchanges ?? [];
}

export async function getExchange(id: number): Promise<Exchange> {
  const r = await fetch(`/api/exchanges/${id}`);
  if (!r.ok) throw new Error(`get exchange ${id}: ${r.status}`);
  return r.json();
}

export async function clearExchanges(): Promise<void> {
  const r = await fetch('/api/exchanges', { method: 'DELETE' });
  if (!r.ok && r.status !== 204) throw new Error(`clear: ${r.status}`);
}

export function caDownloadUrl(): string {
  return '/api/ca';
}
