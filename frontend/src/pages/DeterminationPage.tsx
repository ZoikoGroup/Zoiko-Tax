import React, { useState } from 'react';
import { useApp } from '../context/AppContext';
import { apiService } from '../services/apiService';
import { QuoteRequestInput, QuoteResult } from '../services/taxEngine';
import { StatusBadge } from '../components/common/StatusBadge';
import { JsonViewer } from '../components/common/JsonViewer';
import {
  Calculator,
  Zap,
  CheckCircle2,
  Lock,
  Layers,
  FileCode2,
} from 'lucide-react';

export const DeterminationPage: React.FC = () => {
  const { activeTenant, addToast } = useApp();

  // Calculator inputs
  const [inputs, setInputs] = useState<QuoteRequestInput>({
    tenantId: activeTenant.id,
    legalEntityId: '018f4a2b-7c10-7002-8f11-c00200000011',
    customerType: 'BUSINESS',
    originState: 'CA',
    destinationState: 'CA',
    serviceType: 'WIRELESS_5G_POSTPAID',
    unitAmount: '150.00',
    quantity: 2,
  });

  const [quoteResult, setQuoteResult] = useState<QuoteResult | null>(null);
  const [committedTx, setCommittedTx] = useState<any>(null);
  const [calculating, setCalculating] = useState(false);
  const [committing, setCommitting] = useState(false);

  const handleCalculate = async () => {
    setCalculating(true);
    setCommittedTx(null);
    try {
      const res = await apiService.requestQuote(inputs);
      setQuoteResult(res);
      addToast('success', 'Quote Calculated', `Net $${res.netAmount} → Gross $${res.grossAmount}`);
    } finally {
      setCalculating(false);
    }
  };

  const handleCommit = async () => {
    if (!quoteResult) return;
    setCommitting(true);
    try {
      const idempKey = `IDEMP-${activeTenant.code}-${Date.now()}`;
      const tx = await apiService.commitTransaction(quoteResult, inputs, idempKey);
      setCommittedTx(tx);
      addToast(
        'success',
        'Transaction Committed & Sealed',
        `UUIDv7 ${tx.id} committed to immutable evidence ledger.`
      );
    } finally {
      setCommitting(false);
    }
  };

  return (
    <div className="space-y-6">
      {/* Page Header */}
      <div className="rounded-2xl border theme-card p-6 shadow-xl">
        <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4">
          <div>
            <div className="flex items-center gap-2">
              <span className="rounded bg-[#bf6735]/15 px-2 py-0.5 font-mono text-[10px] font-bold text-[#dd7134] border border-[#bf6735]/30">
                HOT PATH: C0 DETERMINATION
              </span>
              <span className="text-xs theme-text-muted">•</span>
              <span className="text-xs theme-text-secondary font-mono">POST /v1/quotes & :commit</span>
            </div>
            <h1 className="mt-2 text-2xl font-bold tracking-tight theme-text-primary">
              Real-Time Determination & Simulator
            </h1>
            <p className="mt-1 text-xs theme-text-muted max-w-2xl leading-relaxed">
              Test sub-15ms deterministic single-line calculation under ADR-0002 decimal arithmetic.
              Decomposes commercial telecom offerings into canonical service components with dual classification.
            </p>
          </div>

          <div className="flex items-center gap-2">
            <span className="rounded-lg bg-[#26735b]/15 px-3 py-1.5 font-mono text-xs font-semibold text-[#26735b] dark:text-[#34d399] border border-[#26735b]/30 flex items-center gap-1.5">
              <CheckCircle2 className="h-4 w-4" />
              p99 Latency: 11.4 ms
            </span>
          </div>
        </div>
      </div>

      {/* Main Grid */}
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-12">
        {/* Left Column: Input Parameters */}
        <div className="lg:col-span-5 space-y-4">
          <div className="rounded-xl border theme-card p-5 shadow-xl">
            <div className="flex items-center justify-between border-b theme-border pb-3">
              <h2 className="text-xs font-bold uppercase tracking-wider theme-text-primary flex items-center gap-2">
                <Calculator className="h-4 w-4 text-[#dd7134]" />
                Commercial Transaction Input
              </h2>
              <span className="text-[10px] font-mono theme-text-muted">Envelope v1</span>
            </div>

            <div className="mt-4 space-y-3.5">
              {/* Service Type */}
              <div>
                <label className="block text-[11px] font-medium theme-text-muted uppercase tracking-wider mb-1">
                  Telecom Service SKU
                </label>
                <select
                  value={inputs.serviceType}
                  onChange={e => setInputs({ ...inputs, serviceType: e.target.value })}
                  className="w-full rounded-lg border theme-input px-3 py-2 text-xs focus:border-[#dd7134] focus:outline-none"
                >
                  <option value="WIRELESS_5G_POSTPAID">Infinite 5G Unlimited (Voice + Data Bundle)</option>
                  <option value="SIP_TRUNKING">Elastic SIP Trunking (24 Ch Interconnected VoIP)</option>
                  <option value="IOT_TELEMETRY">Global M2M Connected Telemetry (100MB)</option>
                  <option value="SMS_A2P_MESSAGING">10DLC A2P SMS Messaging Tier (100k Pack)</option>
                </select>
              </div>

              {/* Customer Type */}
              <div>
                <label className="block text-[11px] font-medium theme-text-muted uppercase tracking-wider mb-1">
                  Customer Entity Classification
                </label>
                <select
                  value={inputs.customerType}
                  onChange={e => setInputs({ ...inputs, customerType: e.target.value as any })}
                  className="w-full rounded-lg border theme-input px-3 py-2 text-xs focus:border-[#dd7134] focus:outline-none"
                >
                  <option value="BUSINESS">B2B Enterprise Account</option>
                  <option value="CONSUMER">Retail Consumer</option>
                  <option value="WHOLESALE_RESELLER">Wholesale Reseller (USF Form 499 Exemption)</option>
                </select>
              </div>

              {/* Origin & Destination States */}
              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label className="block text-[11px] font-medium theme-text-muted uppercase tracking-wider mb-1">
                    Origin Jurisdiction
                  </label>
                  <select
                    value={inputs.originState}
                    onChange={e => setInputs({ ...inputs, originState: e.target.value })}
                    className="w-full rounded-lg border theme-input px-3 py-2 text-xs focus:border-[#dd7134] focus:outline-none"
                  >
                    <option value="CA">California (CA)</option>
                    <option value="TX">Texas (TX)</option>
                    <option value="NY">New York (NY)</option>
                    <option value="FL">Florida (FL)</option>
                  </select>
                </div>

                <div>
                  <label className="block text-[11px] font-medium theme-text-muted uppercase tracking-wider mb-1">
                    Destination Situs
                  </label>
                  <select
                    value={inputs.destinationState}
                    onChange={e => setInputs({ ...inputs, destinationState: e.target.value })}
                    className="w-full rounded-lg border theme-input px-3 py-2 text-xs focus:border-[#dd7134] focus:outline-none"
                  >
                    <option value="CA">California (CDTFA + CPUC)</option>
                    <option value="TX">Texas (Comptroller + TUSF)</option>
                    <option value="NY">New York (NYS + NYC MTA)</option>
                    <option value="FL">Florida (CST Communications)</option>
                  </select>
                </div>
              </div>

              {/* Unit Amount & Quantity */}
              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label className="block text-[11px] font-medium theme-text-muted uppercase tracking-wider mb-1">
                    Unit Price ($)
                  </label>
                  <input
                    type="text"
                    value={inputs.unitAmount}
                    onChange={e => setInputs({ ...inputs, unitAmount: e.target.value })}
                    className="w-full rounded-lg border theme-input px-3 py-2 font-mono text-xs focus:border-[#dd7134] focus:outline-none"
                    placeholder="150.00"
                  />
                </div>

                <div>
                  <label className="block text-[11px] font-medium theme-text-muted uppercase tracking-wider mb-1">
                    Quantity (Units/Lines)
                  </label>
                  <input
                    type="number"
                    min="1"
                    value={inputs.quantity}
                    onChange={e => setInputs({ ...inputs, quantity: parseInt(e.target.value, 10) || 1 })}
                    className="w-full rounded-lg border theme-input px-3 py-2 font-mono text-xs focus:border-[#dd7134] focus:outline-none"
                  />
                </div>
              </div>

              {/* Action Buttons */}
              <div className="pt-2 flex flex-col gap-2.5">
                <button
                  onClick={handleCalculate}
                  disabled={calculating}
                  className="flex w-full items-center justify-center gap-2 rounded-xl bg-gradient-to-r from-[#bf6735] to-[#dd7134] hover:from-[#dd7134] hover:to-[#f2792a] py-2.5 text-xs font-semibold text-white shadow-[0_4px_14px_0_rgba(221,113,52,0.35)] disabled:opacity-50 transition-all cursor-pointer"
                >
                  <Zap className="h-4 w-4" />
                  {calculating ? 'Evaluating Rule DAG...' : 'Calculate Quote (POST /v1/quotes)'}
                </button>

                {quoteResult && (
                  <button
                    onClick={handleCommit}
                    disabled={committing || !!committedTx}
                    className="flex w-full items-center justify-center gap-2 rounded-xl border border-[#26735b]/50 bg-[#26735b]/15 hover:bg-[#26735b]/25 text-[#26735b] dark:text-[#34d399] py-2.5 text-xs font-semibold disabled:opacity-50 transition-all cursor-pointer"
                  >
                    <Lock className="h-4 w-4" />
                    {committedTx
                      ? 'Committed & Sealed in Ledger'
                      : committing
                      ? 'Sealing Manifest...'
                      : 'Commit Transaction (POST /v1/transactions:commit)'}
                  </button>
                )}
              </div>
            </div>
          </div>

          {/* Dual Classification Context Card */}
          {quoteResult && (
            <div className="rounded-xl border theme-card p-4 shadow-xl">
              <div className="flex items-center gap-2 text-xs font-bold theme-text-primary">
                <Layers className="h-4 w-4 text-[#dd7134]" />
                ZTAX-CLS-001 Dual Classification Decomposition
              </div>
              <div className="mt-3 space-y-2 text-xs">
                <div className="flex justify-between border-b theme-border pb-1.5">
                  <span className="theme-text-muted">Taxability Category:</span>
                  <span className="font-mono text-[#dd7134] font-semibold">
                    {quoteResult.dualClassification.taxabilityClass}
                  </span>
                </div>
                <div className="flex justify-between border-b theme-border pb-1.5">
                  <span className="theme-text-muted">FCC Form 499 Category:</span>
                  <span className="font-mono text-[#5b2a86] dark:text-[#d8b4fe] font-semibold">
                    {quoteResult.dualClassification.regulatoryCategory}
                  </span>
                </div>
                <div className="flex justify-between">
                  <span className="theme-text-muted">Safe Harbor Voice Split:</span>
                  <span className="font-mono text-[#26735b] dark:text-[#34d399] font-bold">
                    {(parseFloat(quoteResult.dualClassification.telecomVoiceAllocationRatio) * 100).toFixed(1)}% Voice /{' '}
                    {(parseFloat(quoteResult.dualClassification.broadbandInformationServiceRatio) * 100).toFixed(1)}% Broadband
                  </span>
                </div>
              </div>
            </div>
          )}
        </div>

        {/* Right Column: Output Schedule */}
        <div className="lg:col-span-7 space-y-4">
          {quoteResult ? (
            <>
              {/* Financial Totals Header */}
              <div className="rounded-xl border theme-card p-5 shadow-xl">
                <div className="flex items-center justify-between border-b theme-border pb-3">
                  <div className="flex items-center gap-2">
                    <StatusBadge
                      status={committedTx ? 'COMMITTED' : 'QUOTE_PROVISIONAL'}
                    />
                    <span className="font-mono text-xs theme-text-muted">
                      Currency: USD | Rounding: HALF_EVEN
                    </span>
                  </div>
                  <span className="font-mono text-[10px] theme-text-muted">
                    ADR-0002 Decimal Context
                  </span>
                </div>

                <div className="mt-4 grid grid-cols-2 sm:grid-cols-4 gap-4 text-center">
                  <div className="rounded-lg theme-subtle p-3 border">
                    <div className="text-[10px] uppercase tracking-wider theme-text-muted font-semibold">
                      Net Amount
                    </div>
                    <div className="mt-1 font-mono text-lg font-bold theme-text-primary">
                      ${quoteResult.netAmount}
                    </div>
                  </div>

                  <div className="rounded-lg theme-subtle p-3 border">
                    <div className="text-[10px] uppercase tracking-wider text-[#dd7134] font-semibold">
                      Transaction Tax
                    </div>
                    <div className="mt-1 font-mono text-lg font-bold text-[#dd7134]">
                      ${quoteResult.totalTax}
                    </div>
                  </div>

                  <div className="rounded-lg theme-subtle p-3 border">
                    <div className="text-[10px] uppercase tracking-wider text-[#5b2a86] dark:text-[#d8b4fe] font-semibold">
                      Regulatory Fees
                    </div>
                    <div className="mt-1 font-mono text-lg font-bold text-[#5b2a86] dark:text-[#d8b4fe]">
                      ${quoteResult.totalRegulatoryFees}
                    </div>
                  </div>

                  <div className="rounded-lg bg-[#bf6735]/15 p-3 border border-[#bf6735]/40">
                    <div className="text-[10px] uppercase tracking-wider text-[#dd7134] font-bold">
                      Gross Total
                    </div>
                    <div className="mt-1 font-mono text-lg font-bold theme-text-primary">
                      ${quoteResult.grossAmount}
                    </div>
                  </div>
                </div>
              </div>

              {/* Line-Level Tax Breakdown Table */}
              <div className="rounded-xl border theme-card p-5 shadow-xl">
                <h3 className="text-xs font-bold uppercase tracking-wider theme-text-primary mb-3 flex items-center justify-between">
                  <span>Line-Item Tax & Surcharge Schedule</span>
                  <span className="text-[10px] theme-text-muted lowercase font-normal">
                    {quoteResult.taxLines.length} statutory line items
                  </span>
                </h3>

                <div className="overflow-x-auto">
                  <table className="w-full text-left text-xs">
                    <thead className="border-b theme-table-head text-[10px] uppercase">
                      <tr>
                        <th className="py-2 pr-3">Jurisdiction & Authority</th>
                        <th className="py-2 px-3">Classification</th>
                        <th className="py-2 px-3 text-right">Taxable Basis</th>
                        <th className="py-2 px-3 text-right">Rate</th>
                        <th className="py-2 pl-3 text-right">Amount</th>
                      </tr>
                    </thead>
                    <tbody className="divide-y theme-subtle theme-text-secondary">
                      {quoteResult.taxLines.map((line, idx) => (
                        <tr key={idx} className="theme-row-hover">
                          <td className="py-2.5 pr-3">
                            <div className="font-semibold theme-text-primary">{line.name}</div>
                            <div className="text-[10px] theme-text-muted font-mono">
                              {line.citation}
                            </div>
                          </td>
                          <td className="py-2.5 px-3">
                            <span
                              className={`rounded px-1.5 py-0.5 font-mono text-[9px] font-semibold ${
                                line.category === 'REGULATORY_FEE'
                                  ? 'bg-[#5b2a86]/15 text-[#5b2a86] dark:text-[#d8b4fe] border border-[#5b2a86]/30'
                                  : 'bg-[#bf6735]/15 text-[#dd7134] border border-[#bf6735]/30'
                              }`}
                            >
                              {line.category === 'REGULATORY_FEE' ? 'REGULATORY FEE' : 'TRANSACTION TAX'}
                            </span>
                          </td>
                          <td className="py-2.5 px-3 font-mono text-right theme-text-muted">
                            {line.basis}
                          </td>
                          <td className="py-2.5 px-3 font-mono text-right theme-text-primary">
                            {line.rateFormatted}
                          </td>
                          <td className="py-2.5 pl-3 font-mono font-bold text-right text-[#26735b] dark:text-[#34d399]">
                            ${line.amount}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </div>

              {/* Cryptographic Evidence Manifest Card */}
              <div className="rounded-xl border theme-card p-4 shadow-xl">
                <div className="flex items-center justify-between mb-2">
                  <div className="flex items-center gap-2 text-xs font-bold theme-text-primary">
                    <FileCode2 className="h-4 w-4 text-[#26735b] dark:text-[#34d399]" />
                    ZTAX-EVID-001 Evidence Manifest & Replay Digest
                  </div>
                  <span className="font-mono text-[10px] text-[#26735b] dark:text-[#34d399] font-bold">
                    BIT-FOR-BIT REPRODUCIBLE
                  </span>
                </div>

                <div className="mt-2 space-y-2 font-mono text-[11px]">
                  <div className="flex items-center justify-between rounded theme-subtle p-2 border">
                    <span className="theme-text-muted">Manifest Hash:</span>
                    <span className="text-[#26735b] dark:text-[#34d399] truncate max-w-sm">
                      {quoteResult.evidenceManifestHash}
                    </span>
                  </div>
                  <div className="flex items-center justify-between rounded theme-subtle p-2 border">
                    <span className="theme-text-muted">Rule Bundle Hash:</span>
                    <span className="text-[#dd7134] truncate max-w-sm">
                      {quoteResult.ruleBundleHash}
                    </span>
                  </div>
                </div>

                {committedTx && (
                  <div className="mt-4 pt-3 border-t theme-border">
                    <div className="text-xs font-semibold theme-text-primary mb-2">
                      Committed Record Envelope (UUIDv7):
                    </div>
                    <JsonViewer data={committedTx} title={`Committed Transaction [${committedTx.id}]`} maxHeight="max-h-56" />
                  </div>
                )}
              </div>
            </>
          ) : (
            <div className="flex h-96 flex-col items-center justify-center rounded-xl border border-dashed theme-subtle p-8 text-center">
              <Calculator className="h-12 w-12 text-[#bf6735]/40 mb-3" />
              <h3 className="text-sm font-semibold theme-text-primary">
                Simulator Ready for Determination
              </h3>
              <p className="mt-1 text-xs theme-text-muted max-w-md">
                Configure commercial telecom parameters on the left and click "Calculate Quote" to run
                the deterministic rule DAG and inspect dual classification line items.
              </p>
            </div>
          )}
        </div>
      </div>
    </div>
  );
};
