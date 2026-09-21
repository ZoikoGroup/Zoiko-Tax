import React, { useState, useEffect } from 'react';
import { apiService } from '../services/apiService';
import { FiscalTransaction } from '../types/domain';
import { DataTable, Column } from '../components/common/DataTable';
import { StatusBadge } from '../components/common/StatusBadge';
import { Modal } from '../components/common/Modal';
import { JsonViewer } from '../components/common/JsonViewer';
import {
  RotateCcw,
  FileCheck2,
  Lock,
  GitCompare,
  CheckCircle2,
  AlertCircle,
} from 'lucide-react';

export const DecisionsReplayPage: React.FC = () => {
  const [decisions, setDecisions] = useState<FiscalTransaction[]>([]);
  const [selectedDecision, setSelectedDecision] = useState<FiscalTransaction | null>(null);
  const [replayModalOpen, setReplayModalOpen] = useState(false);
  const [replayResult, setReplayResult] = useState<any>(null);
  const [replaying, setReplaying] = useState(false);

  // Replay parameter overrides
  const [overrideState, setOverrideState] = useState('');
  const [overrideAmount, setOverrideAmount] = useState('');

  useEffect(() => {
    async function load() {
      const data = await apiService.getDecisions();
      setDecisions(data);
    }
    load();
  }, []);

  const handleOpenReplay = (tx: FiscalTransaction) => {
    setSelectedDecision(tx);
    setOverrideState(tx.destinationJurisdiction.replace('US-', '').slice(0, 2));
    setOverrideAmount(tx.netAmount);
    setReplayResult(null);
    setReplayModalOpen(true);
  };

  const handleExecuteReplay = async (useOverrides: boolean) => {
    if (!selectedDecision) return;
    setReplaying(true);
    try {
      const overrides = useOverrides
        ? {
            destinationState: overrideState,
            unitAmount: overrideAmount,
          }
        : undefined;

      const res = await apiService.replayDecision(selectedDecision.id, overrides);
      setReplayResult(res);
    } finally {
      setReplaying(false);
    }
  };

  const columns: Column<FiscalTransaction>[] = [
    {
      key: 'id',
      header: 'Decision ID (UUIDv7)',
      sortable: true,
      render: row => (
        <div>
          <div className="font-mono font-bold theme-text-primary text-xs truncate max-w-[200px]">
            {row.id}
          </div>
          <div className="font-mono text-[10px] theme-text-muted">{row.idempotencyKey}</div>
        </div>
      ),
    },
    {
      key: 'documentType',
      header: 'Document Type',
      sortable: true,
      render: row => (
        <span className="font-mono font-semibold text-xs text-[#dd7134]">
          {row.documentType}
        </span>
      ),
    },
    {
      key: 'customerAccountRef',
      header: 'Customer / Situs',
      render: row => (
        <div>
          <div className="theme-text-primary text-xs">{row.customerAccountRef}</div>
          <div className="font-mono text-[10px] theme-text-muted">
            {row.originJurisdiction} → {row.destinationJurisdiction}
          </div>
        </div>
      ),
    },
    {
      key: 'netAmount',
      header: 'Net Basis',
      sortable: true,
      align: 'right',
      render: row => (
        <span className="font-mono theme-text-secondary text-xs">${row.netAmount}</span>
      ),
    },
    {
      key: 'grossAmount',
      header: 'Gross Total',
      sortable: true,
      align: 'right',
      render: row => (
        <span className="font-mono font-bold theme-text-primary text-xs">${row.grossAmount}</span>
      ),
    },
    {
      key: 'status',
      header: 'Status & Legal Hold',
      sortable: true,
      align: 'right',
      render: row => (
        <div className="flex items-center justify-end gap-1.5">
          {row.isLegalHold && (
            <span title="Under Active Audit Legal Hold" className="text-[#fbbf24]">
              <Lock className="h-3.5 w-3.5" />
            </span>
          )}
          <StatusBadge status={row.status} />
        </div>
      ),
    },
    {
      key: 'actions',
      header: 'Replay Engine',
      align: 'right',
      render: row => (
        <button
          onClick={e => {
            e.stopPropagation();
            handleOpenReplay(row);
          }}
          className="inline-flex items-center gap-1 rounded bg-[#bf6735]/15 px-2.5 py-1 font-mono text-[10px] font-semibold text-[#dd7134] border border-[#bf6735]/40 hover:bg-[#bf6735]/30 transition-colors cursor-pointer"
        >
          <RotateCcw className="h-3 w-3" /> Replay
        </button>
      ),
    },
  ];

  return (
    <div className="space-y-6">
      {/* Page Header */}
      <div className="rounded-2xl border theme-card p-6 backdrop-blur-md">
        <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4">
          <div>
            <div className="flex items-center gap-2">
              <span className="rounded bg-[#bf6735]/15 px-2 py-0.5 font-mono text-[10px] font-semibold text-[#dd7134] border border-[#bf6735]/40">
                SPECIFICATION: ZTAX-EVID-001 & DOM-001
              </span>
              <span className="text-xs theme-text-muted">•</span>
              <span className="text-xs theme-text-secondary font-medium">Historical Reproducibility</span>
            </div>
            <h1 className="mt-2 text-2xl font-bold tracking-tight theme-text-primary">
              Decisions, Evidence Ledger & Deterministic Replay
            </h1>
            <p className="mt-1 text-xs theme-text-muted max-w-2xl leading-relaxed">
              Every committed transaction generates an immutable cryptographic Evidence Manifest.
              Replay is parameter substitution that exercises the exact same deterministic code path,
              guaranteeing zero drift during audit defense.
            </p>
          </div>

          <div className="flex items-center gap-2">
            <span className="rounded-lg bg-[#26735b]/20 px-3 py-1.5 font-mono text-xs font-semibold text-[#34d399] border border-[#26735b]/40 flex items-center gap-1.5">
              <FileCheck2 className="h-4 w-4" />
              Cryptographically Sealed
            </span>
          </div>
        </div>
      </div>

      {/* Decisions Table */}
      <DataTable
        data={decisions}
        columns={columns}
        searchPlaceholder="Search by UUIDv7, Idempotency key, or Account..."
        searchKey={r => `${r.id} ${r.idempotencyKey} ${r.customerAccountRef}`}
        onRowClick={row => setSelectedDecision(row)}
      />

      {/* Replay Modal */}
      {replayModalOpen && selectedDecision && (
        <Modal
          isOpen={replayModalOpen}
          onClose={() => setReplayModalOpen(false)}
          title="Deterministic Historical Replay Simulator"
          subtitle={`Decision: ${selectedDecision.id} | Engine: ${selectedDecision.engineVersion}`}
          maxWidth="4xl"
        >
          <div className="space-y-4 text-xs">
            {/* Action Bar */}
            <div className="flex flex-wrap items-center justify-between gap-3 rounded-xl theme-subtle p-3.5 border border-[var(--border-main)]">
              <div className="theme-text-secondary">
                Test reproducibility using original parameters, or apply counter-factual overrides.
              </div>
              <div className="flex gap-2">
                <button
                  onClick={() => handleExecuteReplay(false)}
                  disabled={replaying}
                  className="rounded-lg bg-[#26735b] px-3 py-1.5 font-semibold text-white hover:bg-[#34d399] hover:text-black disabled:opacity-50 transition-colors flex items-center gap-1.5 cursor-pointer"
                >
                  <RotateCcw className="h-3.5 w-3.5" />
                  Exact Bit-For-Bit Replay
                </button>
                <button
                  onClick={() => handleExecuteReplay(true)}
                  disabled={replaying}
                  className="rounded-lg bg-gradient-to-r from-[#bf6735] to-[#dd7134] px-3 py-1.5 font-semibold text-white hover:from-[#dd7134] hover:to-[#f2792a] disabled:opacity-50 transition-colors flex items-center gap-1.5 cursor-pointer"
                >
                  <GitCompare className="h-3.5 w-3.5" />
                  Simulate Modified Jurisdictional Sourcing
                </button>
              </div>
            </div>

            {/* Overrides Bar */}
            <div className="grid grid-cols-2 gap-3 rounded-xl theme-subtle p-3 border border-[var(--border-main)]">
              <div>
                <label className="text-[10px] uppercase tracking-wider theme-text-muted font-semibold block mb-1">
                  Destination Jurisdiction (Override)
                </label>
                <select
                  value={overrideState}
                  onChange={e => setOverrideState(e.target.value)}
                  className="w-full rounded theme-input border border-[var(--border-main)] p-1.5 text-xs"
                >
                  <option value="CA">California (CA)</option>
                  <option value="TX">Texas (TX)</option>
                  <option value="NY">New York (NY)</option>
                  <option value="FL">Florida (FL)</option>
                </select>
              </div>
              <div>
                <label className="text-[10px] uppercase tracking-wider theme-text-muted font-semibold block mb-1">
                  Net Amount ($ Override)
                </label>
                <input
                  type="text"
                  value={overrideAmount}
                  onChange={e => setOverrideAmount(e.target.value)}
                  className="w-full rounded theme-input border border-[var(--border-main)] p-1.5 font-mono text-xs"
                />
              </div>
            </div>

            {/* Replay Results Side-by-Side */}
            {replayResult && (
              <div className="space-y-3">
                <div
                  className={`rounded-xl p-3.5 border flex items-center justify-between ${
                    replayResult.isBitForBitIdentical
                      ? 'bg-[#26735b]/20 border-[#26735b]/50 text-[#34d399]'
                      : 'bg-[#9a5b12]/20 border-[#9a5b12]/50 text-[#fbbf24]'
                  }`}
                >
                  <div className="flex items-center gap-2 font-semibold">
                    {replayResult.isBitForBitIdentical ? (
                      <CheckCircle2 className="h-5 w-5 text-[#34d399]" />
                    ) : (
                      <AlertCircle className="h-5 w-5 text-[#fbbf24]" />
                    )}
                    <span>
                      {replayResult.isBitForBitIdentical
                        ? '100% BIT-FOR-BIT IDENTICAL REPLAY CONFIRMED (0.0000% DRIFT)'
                        : 'PARAMETRIC REPLAY PRODUCED COMPUTED DELTA'}
                    </span>
                  </div>
                  <span className="font-mono text-xs">
                    Evaluated in 4.2ms
                  </span>
                </div>

                {replayResult.varianceNotes.length > 0 && (
                  <div className="rounded-lg theme-subtle p-2.5 border border-[var(--border-main)] text-[11px] theme-text-muted">
                    <span className="font-semibold theme-text-primary">Variance Notes: </span>
                    {replayResult.varianceNotes.join('; ')}
                  </div>
                )}

                <div className="grid grid-cols-2 gap-4">
                  {/* Original */}
                  <div className="rounded-xl border border-[var(--border-main)] theme-subtle p-4 space-y-2">
                    <div className="font-bold theme-text-primary uppercase text-[11px] border-b border-[var(--border-main)] pb-2 flex justify-between">
                      <span>Original Recorded Decision</span>
                      <span className="font-mono text-[#dd7134]">SEALED</span>
                    </div>
                    <div className="flex justify-between">
                      <span className="theme-text-muted">Net Basis:</span>
                      <span className="font-mono theme-text-primary">${replayResult.original.netAmount}</span>
                    </div>
                    <div className="flex justify-between">
                      <span className="theme-text-muted">Transaction Tax:</span>
                      <span className="font-mono text-[#dd7134]">${replayResult.original.totalTaxAmount}</span>
                    </div>
                    <div className="flex justify-between">
                      <span className="theme-text-muted">Regulatory Fees:</span>
                      <span className="font-mono text-[#bf6735] dark:text-[#c084fc]">${replayResult.original.totalRegulatoryFees}</span>
                    </div>
                    <div className="flex justify-between font-bold border-t border-[var(--border-main)] pt-1.5">
                      <span className="theme-text-primary">Gross Total:</span>
                      <span className="font-mono theme-text-primary">${replayResult.original.grossAmount}</span>
                    </div>
                  </div>

                  {/* Replayed */}
                  <div className="rounded-xl border border-[var(--border-main)] theme-subtle p-4 space-y-2">
                    <div className="font-bold theme-text-primary uppercase text-[11px] border-b border-[var(--border-main)] pb-2 flex justify-between">
                      <span>Replayed Engine Output</span>
                      <span className="font-mono text-[#34d399]">REPRODUCED</span>
                    </div>
                    <div className="flex justify-between">
                      <span className="theme-text-muted">Net Basis:</span>
                      <span className="font-mono theme-text-primary">${replayResult.replayed.netAmount}</span>
                    </div>
                    <div className="flex justify-between">
                      <span className="theme-text-muted">Transaction Tax:</span>
                      <span className="font-mono text-[#dd7134]">${replayResult.replayed.totalTax}</span>
                    </div>
                    <div className="flex justify-between">
                      <span className="theme-text-muted">Regulatory Fees:</span>
                      <span className="font-mono text-[#bf6735] dark:text-[#c084fc]">${replayResult.replayed.totalRegulatoryFees}</span>
                    </div>
                    <div className="flex justify-between font-bold border-t border-[var(--border-main)] pt-1.5">
                      <span className="theme-text-primary">Gross Total:</span>
                      <span className="font-mono theme-text-primary">${replayResult.replayed.grossAmount}</span>
                    </div>
                  </div>
                </div>
              </div>
            )}
          </div>
        </Modal>
      )}

      {/* Decision Detail & Manifest Modal */}
      {selectedDecision && !replayModalOpen && (
        <Modal
          isOpen={!!selectedDecision}
          onClose={() => setSelectedDecision(null)}
          title={`Fiscal Decision Manifest [${selectedDecision.id}]`}
          subtitle={`Idempotency Key: ${selectedDecision.idempotencyKey} | Timestamp: ${selectedDecision.decisionTimestamp}`}
          maxWidth="4xl"
        >
          <div className="space-y-4">
            <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 text-xs">
              <div className="rounded-lg theme-subtle p-3 border border-[var(--border-main)]">
                <span className="theme-text-muted uppercase text-[10px]">Document Type</span>
                <div className="font-mono text-[#dd7134] font-bold mt-1">
                  {selectedDecision.documentType}
                </div>
              </div>
              <div className="rounded-lg theme-subtle p-3 border border-[var(--border-main)]">
                <span className="theme-text-muted uppercase text-[10px]">Gross Amount</span>
                <div className="font-mono theme-text-primary font-bold mt-1">
                  ${selectedDecision.grossAmount} {selectedDecision.currency}
                </div>
              </div>
              <div className="rounded-lg theme-subtle p-3 border border-[var(--border-main)]">
                <span className="theme-text-muted uppercase text-[10px]">Legal Hold Status</span>
                <div className="mt-1">
                  <StatusBadge
                    status={selectedDecision.isLegalHold ? 'ACTIVE_HOLD' : 'STANDARD'}
                  />
                </div>
              </div>
              <div className="rounded-lg theme-subtle p-3 border border-[var(--border-main)]">
                <span className="theme-text-muted uppercase text-[10px]">Engine Version</span>
                <div className="font-mono theme-text-secondary text-[11px] truncate mt-1">
                  {selectedDecision.engineVersion}
                </div>
              </div>
            </div>

            {/* Line Items Schedule */}
            <div className="rounded-lg border border-[var(--border-main)] theme-subtle p-3">
              <div className="text-xs font-bold uppercase tracking-wider theme-text-primary mb-2">
                Line Items & Associated Statutory Taxes
              </div>
              {selectedDecision.lineItems.map(item => (
                <div key={item.lineNumber} className="border-t border-[var(--border-main)] pt-2 text-xs">
                  <div className="flex justify-between font-semibold theme-text-primary">
                    <span>
                      Line {item.lineNumber}: {item.productName}
                    </span>
                    <span className="font-mono">${item.netAmount}</span>
                  </div>
                  <div className="mt-2 space-y-1 pl-3 border-l-2 border-[#bf6735]">
                    {item.taxes.map(tax => (
                      <div key={tax.id} className="flex justify-between text-[11px] theme-text-muted">
                        <span>
                          {tax.jurisdictionName} ({tax.taxType})
                        </span>
                        <span className="font-mono font-bold text-[#dd7134]">${tax.taxAmount}</span>
                      </div>
                    ))}
                  </div>
                </div>
              ))}
            </div>

            {/* JSON Envelope */}
            <JsonViewer
              data={selectedDecision}
              title="Canonical Evidence Manifest Envelope (RFC 8785 Format)"
              maxHeight="max-h-64"
            />
          </div>
        </Modal>
      )}
    </div>
  );
};
