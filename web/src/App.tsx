import { useMemo, useState } from 'react';
import { ExchangeList } from './components/ExchangeList';
import { ExchangeDetail } from './components/ExchangeDetail';
import { Toolbar } from './components/Toolbar';
import { useLiveExchanges } from './useLiveExchanges';
import { clearExchanges } from './api';
import type { Exchange } from './types';

export function App() {
  const { exchanges, status, clear, refresh } = useLiveExchanges();
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [filter, setFilter] = useState('');
  const [methodFilter, setMethodFilter] = useState<string>('');

  const filtered = useMemo(() => {
    const q = filter.trim().toLowerCase();
    return exchanges.filter((ex) => {
      if (methodFilter && ex.method !== methodFilter) return false;
      if (!q) return true;
      return (
        ex.host.toLowerCase().includes(q) ||
        ex.path.toLowerCase().includes(q) ||
        String(ex.status).includes(q)
      );
    });
  }, [exchanges, filter, methodFilter]);

  const selected: Exchange | undefined = exchanges.find((e) => e.id === selectedId);

  const methods = useMemo(() => {
    const s = new Set<string>();
    exchanges.forEach((e) => s.add(e.method));
    return [...s].sort();
  }, [exchanges]);

  return (
    <div className="app">
      <Toolbar
        status={status}
        count={filtered.length}
        total={exchanges.length}
        filter={filter}
        onFilterChange={setFilter}
        methodFilter={methodFilter}
        onMethodFilterChange={setMethodFilter}
        methods={methods}
        onClear={async () => {
          await clearExchanges();
          clear();
          setSelectedId(null);
          await refresh();
        }}
      />
      <div className="split">
        <ExchangeList
          exchanges={filtered}
          selectedId={selectedId}
          onSelect={setSelectedId}
        />
        <ExchangeDetail exchange={selected} />
      </div>
    </div>
  );
}
