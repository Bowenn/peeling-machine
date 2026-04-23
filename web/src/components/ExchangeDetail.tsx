import { useState } from 'react';
import type { Exchange, HeaderPair } from '../types';
import { decodeBody, findHeader, prettyPrintIfJSON } from '../body';

interface Props {
  exchange: Exchange | undefined;
}

type Tab = 'request' | 'response';

export function ExchangeDetail({ exchange }: Props) {
  const [tab, setTab] = useState<Tab>('request');

  if (!exchange) {
    return (
      <div className="detail empty-detail">
        Select a request to inspect headers and body.
      </div>
    );
  }

  return (
    <div className="detail">
      <div className="detail-header">
        <div className="url-line">
          <span className={`pill method-${exchange.method}`}>{exchange.method}</span>
          <span className="scheme">{exchange.scheme}://</span>
          <span className="host">{exchange.host}</span>
          <span className="path">{exchange.path}</span>
        </div>
        <div className="meta-line">
          <span>#{exchange.id}</span>
          <span>{new Date(exchange.started_at).toLocaleTimeString()}</span>
          <span>{exchange.duration_ms} ms</span>
          {exchange.error ? (
            <span className="error">error: {exchange.error}</span>
          ) : (
            <span>status: {exchange.status}</span>
          )}
        </div>
      </div>

      <div className="tabs">
        <button
          className={`tab ${tab === 'request' ? 'active' : ''}`}
          onClick={() => setTab('request')}
        >
          Request
        </button>
        <button
          className={`tab ${tab === 'response' ? 'active' : ''}`}
          onClick={() => setTab('response')}
        >
          Response
        </button>
      </div>

      {tab === 'request' ? (
        <Side
          headers={exchange.req_headers}
          body={exchange.req_body}
          truncated={exchange.req_truncated}
        />
      ) : (
        <Side
          headers={exchange.resp_headers}
          body={exchange.resp_body}
          truncated={exchange.resp_truncated}
          errorMsg={exchange.error}
        />
      )}
    </div>
  );
}

function Side({
  headers,
  body,
  truncated,
  errorMsg,
}: {
  headers: HeaderPair[] | undefined;
  body: string | undefined;
  truncated: boolean | undefined;
  errorMsg?: string;
}) {
  const ct = findHeader(headers, 'content-type');
  const decoded = decodeBody(body);
  const display = decoded.isBinary ? decoded.text : prettyPrintIfJSON(decoded.text, ct);

  return (
    <div className="side">
      <section className="headers">
        <h3>Headers</h3>
        {(!headers || headers.length === 0) && <div className="dim">(none)</div>}
        <table>
          <tbody>
            {headers?.map(([k, v], i) => (
              <tr key={i}>
                <td className="hdr-name">{k}</td>
                <td className="hdr-val">{v}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>

      <section className="body">
        <h3>
          Body{' '}
          <span className="dim">
            ({decoded.byteLength} bytes
            {truncated ? ', truncated' : ''}
            {decoded.isBinary ? ', binary — hex preview' : ''})
          </span>
        </h3>
        {errorMsg && <div className="error-box">{errorMsg}</div>}
        {decoded.byteLength === 0 && !errorMsg ? (
          <div className="dim">(empty)</div>
        ) : (
          <pre className="body-pre">{display}</pre>
        )}
      </section>
    </div>
  );
}
