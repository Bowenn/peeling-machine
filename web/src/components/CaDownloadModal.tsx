import { useEffect, useState } from 'react';
import { caDownloadUrl } from '../api';

interface Props {
  open: boolean;
  onClose: () => void;
}

interface UninstallLink {
  platform: string;
  href: string;
}

const UNINSTALL_LINKS: UninstallLink[] = [
  {
    platform: 'macOS (Keychain Access)',
    href: 'https://support.apple.com/guide/keychain-access/remove-a-certificate-kyca3004/mac',
  },
  {
    platform: 'Windows (certmgr)',
    href: 'https://learn.microsoft.com/en-us/windows-hardware/drivers/install/trusted-root-certification-authorities-certificate-store',
  },
  {
    platform: 'Linux (ca-certificates)',
    href: 'https://ubuntu.com/server/docs/security-trust-store',
  },
  {
    platform: 'iOS (Profiles & Device Management)',
    href: 'https://support.apple.com/guide/iphone/install-or-remove-configuration-profiles-iph6c493b19/ios',
  },
  {
    platform: 'Android (user credentials)',
    href: 'https://support.google.com/pixelphone/answer/2844832',
  },
];

export function CaDownloadModal({ open, onClose }: Props) {
  const [ackDecrypt, setAckDecrypt] = useState(false);
  const [ackOwn, setAckOwn] = useState(false);

  // Reset checkboxes every time the modal opens — acknowledgements do not
  // persist across sessions; every download is a fresh consent event.
  useEffect(() => {
    if (open) {
      setAckDecrypt(false);
      setAckOwn(false);
    }
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [open, onClose]);

  if (!open) return null;

  const ready = ackDecrypt && ackOwn;

  function doDownload() {
    if (!ready) return;
    const a = document.createElement('a');
    a.href = caDownloadUrl();
    a.download = 'peeling-machine-ca.pem';
    document.body.appendChild(a);
    a.click();
    a.remove();
    onClose();
  }

  return (
    <div className="modal-backdrop" onClick={onClose} role="presentation">
      <div
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="ca-modal-title"
        onClick={(e) => e.stopPropagation()}
      >
        <h2 id="ca-modal-title" className="modal-title">
          <span className="modal-warn-icon" aria-hidden>!</span>
          Install Peeling Machine Root CA
        </h2>

        <p className="modal-lead">
          Installing this certificate grants Peeling Machine the ability to
          <strong> decrypt TLS traffic</strong> from any app or browser on the
          installing device that trusts it. Treat it like a key to your device.
        </p>

        <label className="modal-check">
          <input
            type="checkbox"
            checked={ackDecrypt}
            onChange={(e) => setAckDecrypt(e.target.checked)}
          />
          <span>
            I understand installing this CA lets Peeling Machine decrypt all
            TLS traffic from this machine.
          </span>
        </label>

        <label className="modal-check">
          <input
            type="checkbox"
            checked={ackOwn}
            onChange={(e) => setAckOwn(e.target.checked)}
          />
          <span>
            I will <strong>not</strong> install this CA on devices I do not
            own, and I will uninstall it when finished.
          </span>
        </label>

        <div className="modal-uninstall">
          <h3>How to uninstall</h3>
          <ul>
            {UNINSTALL_LINKS.map((l) => (
              <li key={l.platform}>
                <a href={l.href} target="_blank" rel="noreferrer noopener">
                  {l.platform}
                </a>
              </li>
            ))}
          </ul>
        </div>

        <div className="modal-actions">
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button
            className="btn btn-primary"
            disabled={!ready}
            onClick={doDownload}
          >
            Download CA
          </button>
        </div>
      </div>
    </div>
  );
}
