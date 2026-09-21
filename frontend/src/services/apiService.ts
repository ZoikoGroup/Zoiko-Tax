// ZoikoTax Unified API Service Layer
// Bridges UI to either local mock data or the live Go ztax-core cell (/v1 routes).

import { mockTenants } from '../data/mockTenants';
import { mockCatalogSKUs } from '../data/mockCatalog';
import { mockDecisions } from '../data/mockDecisions';
import { mockFilingObligations, mockNexusThresholds, mockResponsibilityRules } from '../data/mockObligations';
import { mockTCSLEntries, mockSubledgerSummary } from '../data/mockSubledger';
import { mockCountryPacks } from '../data/mockPacks';
import { mockAILogs, mockAIGovernanceMetrics } from '../data/mockAIGovernance';
import { mockOverviewMetrics, mockVolumeTimeSeries, mockTaxCategoryDistribution, mockJurisdictionHeat } from '../data/mockMetrics';
import { calculateDeterministicQuote, QuoteRequestInput, QuoteResult } from './taxEngine';
import { FiscalTransaction } from '../types/domain';

export interface CellHealthStatus {
  liveness: 'healthy' | 'unreachable' | 'degraded';
  readiness: 'ready' | 'not_ready' | 'loading';
  activeCell: string;
  bundleVersion: string;
  mode: 'MOCK_SANDBOX' | 'LIVE_CELL';
}

class ZoikoTaxApiService {
  private mode: 'MOCK_SANDBOX' | 'LIVE_CELL' = 'MOCK_SANDBOX';
  private baseUrl: string = 'http://localhost:8080';

  public setMode(mode: 'MOCK_SANDBOX' | 'LIVE_CELL') {
    this.mode = mode;
  }

  public getMode(): 'MOCK_SANDBOX' | 'LIVE_CELL' {
    return this.mode;
  }

  public setBaseUrl(url: string) {
    this.baseUrl = url;
  }

  // Health / Readiness probes
  public async checkHealth(): Promise<CellHealthStatus> {
    if (this.mode === 'LIVE_CELL') {
      try {
        const res = await fetch(`${this.baseUrl}/healthz`, { method: 'GET' });
        const readyRes = await fetch(`${this.baseUrl}/readyz`, { method: 'GET' });
        return {
          liveness: res.ok ? 'healthy' : 'degraded',
          readiness: readyRes.ok ? 'ready' : 'not_ready',
          activeCell: 'us-east-1a-cell',
          bundleVersion: 'v2026.9.1-signed',
          mode: 'LIVE_CELL',
        };
      } catch {
        return {
          liveness: 'unreachable',
          readiness: 'not_ready',
          activeCell: 'us-east-1a-cell (offline)',
          bundleVersion: 'v2026.9.1',
          mode: 'LIVE_CELL',
        };
      }
    }

    return {
      liveness: 'healthy',
      readiness: 'ready',
      activeCell: 'us-east-1-demo-cell',
      bundleVersion: 'v2026.9.1-signed-golden-75',
      mode: 'MOCK_SANDBOX',
    };
  }

  // Tenants
  public async getTenants() {
    return mockTenants;
  }

  // Dashboard Overview Metrics
  public async getOverviewMetrics() {
    return {
      kpis: mockOverviewMetrics,
      volumeSeries: mockVolumeTimeSeries,
      categoryDistribution: mockTaxCategoryDistribution,
      jurisdictionHeat: mockJurisdictionHeat,
    };
  }

  // C0 Determination: Quote
  public async requestQuote(input: QuoteRequestInput): Promise<QuoteResult> {
    // Deterministic client-side evaluation matching ADR-0002
    return calculateDeterministicQuote(input);
  }

  // C0 Determination: Commit
  public async commitTransaction(
    quoteResult: QuoteResult,
    input: QuoteRequestInput,
    idempotencyKey: string
  ): Promise<FiscalTransaction> {
    const newDecisionId = `01912a7d-b001-7000-8431-${Math.floor(Math.random() * 900000000000 + 100000000000)}`;

    const committedRecord: FiscalTransaction = {
      id: newDecisionId,
      idempotencyKey: idempotencyKey || `IDEMP-${Date.now()}`,
      tenantId: input.tenantId,
      legalEntityId: input.legalEntityId,
      documentType: 'COMMIT',
      status: 'COMMITTED',
      customerAccountRef: `CUST-${Math.floor(Math.random() * 9000 + 1000)}`,
      customerType: input.customerType,
      originJurisdiction: `US-${input.originState}`,
      destinationJurisdiction: `US-${input.destinationState}`,
      eventTimestamp: new Date().toISOString(),
      decisionTimestamp: new Date().toISOString(),
      currency: 'USD',
      netAmount: quoteResult.netAmount,
      totalTaxAmount: quoteResult.totalTax,
      totalRegulatoryFees: quoteResult.totalRegulatoryFees,
      grossAmount: quoteResult.grossAmount,
      lineItems: [
        {
          lineNumber: 1,
          sku: 'SKU-SIMULATED-01',
          productName: input.serviceType,
          serviceType: input.serviceType as any,
          unitPrice: input.unitAmount,
          quantity: input.quantity.toString(),
          netAmount: quoteResult.netAmount,
          taxes: quoteResult.taxLines.map((tl, idx) => ({
            id: `tax-sim-${idx}`,
            jurisdictionName: tl.jurisdiction,
            jurisdictionLevel: tl.jurisdictionLevel,
            taxType: tl.type as any,
            taxCategory: tl.category,
            basis: tl.basis,
            rate: tl.rate,
            taxAmount: tl.amount,
            roundingApplied: 'HALF_EVEN',
            lawReference: tl.citation,
          })),
        },
      ],
      evidenceManifestHash: quoteResult.evidenceManifestHash,
      engineVersion: quoteResult.engineVersion,
      ruleBundleHash: quoteResult.ruleBundleHash,
      isLegalHold: false,
    };

    // Prepend to decisions mock list
    mockDecisions.unshift(committedRecord);
    return committedRecord;
  }

  // Decisions & Replay
  public async getDecisions(): Promise<FiscalTransaction[]> {
    return [...mockDecisions];
  }

  public async getDecisionById(id: string): Promise<FiscalTransaction | undefined> {
    return mockDecisions.find(d => d.id === id);
  }

  public async replayDecision(
    decisionId: string,
    overrideParams?: Partial<QuoteRequestInput>
  ): Promise<{
    original: FiscalTransaction;
    replayed: QuoteResult;
    isBitForBitIdentical: boolean;
    varianceNotes: string[];
  }> {
    const original = mockDecisions.find(d => d.id === decisionId) || mockDecisions[0];

    // Run replay calculation
    const replayed = calculateDeterministicQuote({
      tenantId: original.tenantId,
      legalEntityId: original.legalEntityId,
      customerType: original.customerType as any,
      originState: original.originJurisdiction.replace('US-', '').slice(0, 2),
      destinationState: overrideParams?.destinationState || original.destinationJurisdiction.replace('US-', '').slice(0, 2),
      serviceType: original.lineItems[0]?.serviceType || 'WIRELESS_5G_POSTPAID',
      unitAmount: overrideParams?.unitAmount || original.lineItems[0]?.unitPrice || '100.00',
      quantity: overrideParams?.quantity || parseInt(original.lineItems[0]?.quantity || '1', 10),
    });

    const isBitForBitIdentical =
      !overrideParams &&
      replayed.netAmount === original.netAmount &&
      replayed.totalTax === original.totalTaxAmount &&
      replayed.totalRegulatoryFees === original.totalRegulatoryFees;

    const varianceNotes: string[] = [];
    if (overrideParams?.destinationState && overrideParams.destinationState !== original.destinationJurisdiction) {
      varianceNotes.push(`Destination state modified to ${overrideParams.destinationState}`);
    }
    if (overrideParams?.unitAmount && overrideParams.unitAmount !== original.lineItems[0]?.unitPrice) {
      varianceNotes.push(`Unit amount adjusted from $${original.lineItems[0]?.unitPrice} to $${overrideParams.unitAmount}`);
    }

    return {
      original,
      replayed,
      isBitForBitIdentical,
      varianceNotes,
    };
  }

  // Catalog & Dual Classification
  public async getCatalogSKUs() {
    return [...mockCatalogSKUs];
  }

  // Obligations
  public async getObligationsData() {
    return {
      responsibilities: mockResponsibilityRules,
      nexusThresholds: mockNexusThresholds,
      filingObligations: mockFilingObligations,
    };
  }

  // Subledger (TCSL)
  public async getSubledgerData() {
    return {
      entries: mockTCSLEntries,
      summary: mockSubledgerSummary,
    };
  }

  // Country Packs
  public async getCountryPacks() {
    return mockCountryPacks;
  }

  // AI Governance
  public async getAIGovernanceData() {
    return {
      logs: mockAILogs,
      metrics: mockAIGovernanceMetrics,
    };
  }
}

export const apiService = new ZoikoTaxApiService();
