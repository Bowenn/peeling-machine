import { useEffect, useRef, useState } from 'react';
import type { Exchange } from './types';
import { listExchanges } from './api';

export type Status = 'connecting' | 'live' | 'error';

// Subscribes to /api/stream and also backfills with /api/exchanges on mount.
// Exchanges are kept newest-first in state.
export function useLiveExchanges(): {
  exchanges: Exchange[];
  status: Status;
  clear: () => void;
  refresh: () => Promise<void>;
} {
  const [exchanges, setExchanges] = useState<Exchange[]>([]);
  const [status, setStatus] = useState<Status>('connecting');
  const esRef = useRef<EventSource | null>(null);

  const refresh = async () => {
    const initial = await listExchanges(500);
    setExchanges(initial);
  };

  useEffect(() => {
    let cancelled = false;

    (async () => {
      try {
        const initial = await listExchanges(500);
        if (!cancelled) setExchanges(initial);
      } catch {
        // non-fatal; SSE may still work
      }

      if (cancelled) return;

      const es = new EventSource('/api/stream');
      esRef.current = es;

      es.addEventListener('open', () => setStatus('live'));
      es.addEventListener('error', () => setStatus('error'));
      es.addEventListener('exchange', (ev: MessageEvent<string>) => {
        try {
          const ex: Exchange = JSON.parse(ev.data);
          setExchanges((prev) => {
            // De-dupe in case backfill + stream overlap.
            if (prev.some((e) => e.id === ex.id)) return prev;
            return [ex, ...prev].slice(0, 1000);
          });
        } catch {
          /* ignore bad frame */
        }
      });
    })();

    return () => {
      cancelled = true;
      esRef.current?.close();
      esRef.current = null;
    };
  }, []);

  const clear = () => setExchanges([]);
  return { exchanges, status, clear, refresh };
}
