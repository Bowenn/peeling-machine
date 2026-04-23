import type { Exchange } from '../types';

interface Props {
  exchanges: Exchange[];
  selectedId: number | null;
  onSelect: (id: number) => void;
}

export function ExchangeList({ exchanges, selectedId, onSelect }: Props) {
  return (
    <div className="list">
      <div className="list-header">
        <span className="col-method">Method</span>
        <span className="col-status">Status</span>
        <span className="col-host">Host</span>
        <span className="col-path">Path</span>
        <span className="col-dur">ms</span>
      </div>
      <div className="list-body">
        {exchanges.length === 0 && (
          <div className="empty">
            Waiting for traffic… point a client at <code>http://localhost:8080</code>.
          </div>
        )}
        {exchanges.map((ex) => (
          <button
            key={ex.id}
            className={`row ${selectedId === ex.id ? 'selected' : ''} ${ex.error ? 'errored' : ''}`}
            onClick={() => onSelect(ex.id)}
          >
            <span className={`col-method method-${ex.method}`}>{ex.method}</span>
            <span className={`col-status ${statusClass(ex.status)}`}>
              {ex.error ? 'ERR' : ex.status || '—'}
            </span>
            <span className="col-host" title={ex.host}>
              {schemeIcon(ex.scheme)}
              {ex.host}
            </span>
            <span className="col-path" title={ex.path}>
              {ex.path}
            </span>
            <span className="col-dur">{ex.duration_ms}</span>
          </button>
        ))}
      </div>
    </div>
  );
}

function schemeIcon(s: string): string {
  return s === 'https' ? '🔒 ' : '';
}

function statusClass(s: number): string {
  if (!s) return 'status-none';
  if (s >= 500) return 'status-5xx';
  if (s >= 400) return 'status-4xx';
  if (s >= 300) return 'status-3xx';
  if (s >= 200) return 'status-2xx';
  return 'status-1xx';
}
