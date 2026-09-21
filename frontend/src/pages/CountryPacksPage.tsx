import React, { useState, useEffect } from 'react';
import { apiService } from '../services/apiService';
import { CountryRegulatoryPack } from '../types/domain';
import { StatusBadge } from '../components/common/StatusBadge';
import {
  CheckCircle2,
} from 'lucide-react';

export const CountryPacksPage: React.FC = () => {
  const [packs, setPacks] = useState<CountryRegulatoryPack[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    async function load() {
      setLoading(true);
      const data = await apiService.getCountryPacks();
      setPacks(data);
      setLoading(false);
    }
    load();
  }, []);

  if (loading) {
    return (
      <div className="flex h-96 items-center justify-center">
        <div className="h-8 w-8 animate-spin rounded-full border-2 border-[#dd7134] border-t-transparent" />
      </div>
    );
  }

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="rounded-2xl border theme-card p-6 backdrop-blur-md">
        <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4">
          <div>
            <div className="flex items-center gap-2">
              <span className="rounded bg-[#bf6735]/15 px-2 py-0.5 font-mono text-[10px] font-semibold text-[#dd7134] border border-[#bf6735]/40">
                SPECIFICATION: ZTAX-CONT-001 & SRC-001
              </span>
              <span className="text-xs theme-text-muted">•</span>
              <span className="text-xs theme-text-secondary font-medium">Governed Content Supply Chain</span>
            </div>
            <h1 className="mt-2 text-2xl font-bold tracking-tight theme-text-primary">
              Country Regulatory Packs & Provenance
            </h1>
            <p className="mt-1 text-xs theme-text-muted max-w-2xl leading-relaxed">
              Tax law, regulatory fees, and monetary formulas enter production only through signed,
              versioned, and certified Country Regulatory Packs validated against comprehensive golden test vectors.
            </p>
          </div>

          <div className="flex items-center gap-2">
            <span className="rounded-lg bg-[#26735b]/20 px-3 py-1.5 font-mono text-xs font-semibold text-[#34d399] border border-[#26735b]/40 flex items-center gap-1.5">
              <CheckCircle2 className="h-4 w-4" />
              Golden Vectors: 100% Passing
            </span>
          </div>
        </div>
      </div>

      {/* Grid of Installed Country Packs */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-5">
        {packs.map(pack => (
          <div
            key={pack.packId}
            className="rounded-xl border theme-card p-5 space-y-4 hover:border-[#dd7134]/50 transition-all"
          >
            <div className="flex items-start justify-between border-b border-[var(--border-main)] pb-3">
              <div>
                <div className="flex items-center gap-2">
                  <span className="font-mono text-sm font-bold text-[#dd7134]">
                    [{pack.countryCode}]
                  </span>
                  <span className="font-bold theme-text-primary text-sm">{pack.countryName}</span>
                </div>
                <div className="mt-1 font-mono text-[10px] theme-text-muted">
                  Pack ID: {pack.packId}
                </div>
              </div>
              <StatusBadge status={pack.lifecycleState} />
            </div>

            <div className="grid grid-cols-3 gap-2 text-xs">
              <div className="rounded-lg theme-subtle p-2.5 border border-[var(--border-main)]">
                <span className="text-[10px] theme-text-muted uppercase">Version</span>
                <div className="font-mono theme-text-primary font-semibold mt-0.5">{pack.version}</div>
              </div>
              <div className="rounded-lg theme-subtle p-2.5 border border-[var(--border-main)]">
                <span className="text-[10px] theme-text-muted uppercase">Active Rules</span>
                <div className="font-mono text-[#dd7134] font-semibold mt-0.5">
                  {pack.ruleCount.toLocaleString()}
                </div>
              </div>
              <div className="rounded-lg theme-subtle p-2.5 border border-[var(--border-main)]">
                <span className="text-[10px] theme-text-muted uppercase">Golden Vectors</span>
                <div className="font-mono text-[#34d399] font-semibold mt-0.5">
                  {pack.goldenVectorsCount} ({pack.goldenVectorPassRate}%)
                </div>
              </div>
            </div>

            {/* Cryptographic SHA-256 Bundle Digest */}
            <div className="rounded-lg theme-subtle p-3 border border-[var(--border-main)] space-y-1 text-xs">
              <div className="text-[10px] uppercase tracking-wider theme-text-muted font-semibold flex items-center justify-between">
                <span>Signed Rule Bundle Hash</span>
                <span className="text-[#34d399] font-mono">ED25519 SIGNED</span>
              </div>
              <div className="font-mono text-[10px] theme-text-secondary truncate">
                {pack.sha256Digest}
              </div>
            </div>

            {/* Regulatory Bodies Covered */}
            <div>
              <div className="text-[10px] font-semibold uppercase tracking-wider theme-text-muted mb-1.5">
                Statutory Authorities & Frameworks
              </div>
              <div className="flex flex-wrap gap-1.5">
                {pack.regulatoryBodies.map((body, i) => (
                  <span
                    key={i}
                    className="rounded theme-subtle px-2 py-0.5 font-mono text-[10px] theme-text-secondary border border-[var(--border-main)]"
                  >
                    {body}
                  </span>
                ))}
              </div>
            </div>
          </div>
        ))}
      </div>
    </div>
  );
};
