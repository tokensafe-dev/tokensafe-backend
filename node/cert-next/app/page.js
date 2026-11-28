'use client';

import { useState } from 'react';

export default function Page() {
  const [probes, setProbes] = useState(0);
  return (
    <main>
      <h1>OUMV Next bridge certification app</h1>
      <button data-agent-id="probe-btn" onClick={() => setProbes((value) => value + 1)}>
        Probe {probes}
      </button>
    </main>
  );
}
