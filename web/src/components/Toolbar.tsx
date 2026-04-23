import { useState } from 'react';
import type { Status } from '../useLiveExchanges';
import { CaDownloadModal } from './CaDownloadModal';

interface Props {
  status: Status;
  count: number;
  total: number;
  filter: string;
  onFilterChange: (v: string) => void;
  methodFilter: string;
  onMethodFilterChange: (v: string) => void;
  methods: string[];
  onClear: () => void;
}

export function Toolbar({
  status,
  count,
  total,
  filter,
  onFilterChange,
  methodFilter,
  onMethodFilterChange,
  methods,
  onClear,
}: Props) {
  const [caModalOpen, setCaModalOpen] = useState(false);
  return (
    <header className="toolbar">
      <div className="brand">
        <span className="brand-dot" />
        <span>Peeling Machine</span>
      </div>

      <input
        className="filter"
        type="search"
        placeholder="Filter host / path / status…"
        value={filter}
        onChange={(e) => onFilterChange(e.target.value)}
      />

      <select
        className="method-select"
        value={methodFilter}
        onChange={(e) => onMethodFilterChange(e.target.value)}
        aria-label="Filter by method"
      >
        <option value="">All methods</option>
        {methods.map((m) => (
          <option key={m} value={m}>
            {m}
          </option>
        ))}
      </select>

      <span className="count">
        {count} / {total}
      </span>

      <span className={`status status-${status}`} title={`SSE: ${status}`}>
        <span className="status-dot" />
        {status}
      </span>

      <div className="spacer" />

      <button className="btn" onClick={() => setCaModalOpen(true)}>
        Download CA
      </button>
      <button className="btn btn-danger" onClick={onClear}>
        Clear
      </button>

      <CaDownloadModal open={caModalOpen} onClose={() => setCaModalOpen(false)} />
    </header>
  );
}
