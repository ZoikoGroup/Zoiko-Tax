// Canonical domain types matching ZoikoTax specifications:
// ZTAX-DOM-001 (Domain Model), ZTAX-CLS-001 (Classification), ZTAX-OBL-001 (Obligations),
// ZTAX-FIN-001 (Subledger & Reconciliation), ZTAX-EVID-001 (Evidence Ledger), ZTAX-AIGOV-001 (AI Governance)

export type AuthorityClass =
  | 'FACT'
  | 'AUTHORITATIVE_DECISION'
  | 'OPERATIONAL_STATUS'
  | 'ANALYTIC'
  | 'AI_PROPOSAL'
  | 'EVIDENCE';

export type UserPersona =
  | 'tax_director'
  | 'compliance_manager'
  | 'telecom_ops_lead'
  | 'financial_controller'
  | 'content_engineer';

export interface Tenant {
  id: string; // UUIDv7
  name: string;
  type: 'MNO' | 'MVNO' | 'MVNE' | 'CARRIER' | 'VOIP_SIP' | 'IOT_AGGREGATOR';
  code: string;
  country: string;
  currency: string;
  activeLegalEntities: number;
  monthlyVolume: string;
  status: 'ACTIVE' | 'ONBOARDING' | 'MAINTENANCE';
  cellRegion: 'us-east-1' | 'eu-west-1' | 'ap-southeast-1';
}

export interface LegalEntity {
  id: string;
  tenantId: string;
  legalName: string;
  entityType: 'CORPORATION' | 'PARTNERSHIP' | 'LLC';
  jurisdictionId: string;
  fccFilerId?: string;
  vatNumber?: string;
  usfExemptionStatus: 'DIRECT_CONTRIBUTOR' | 'WHOLESALE_EXEMPT' | 'DE_MINIMIS';
}

export interface Money {
  amount: string; // Decimal string (no floats!) e.g. "1250.75"
  currency: string; // ISO 4217
}

export type TelecomServiceType =
  | 'WIRELESS_5G_POSTPAID'
  | 'WIRELESS_5G_PREPAID'
  | 'VOIP_INTERCONNECTED'
  | 'SIP_TRUNKING'
  | 'DEDICATED_INTERNET_ACCESS'
  | 'IOT_TELEMETRY'
  | 'SMS_A2P_MESSAGING'
  | 'CLOUD_PBX_SEATS'
  | 'ROAMING_DATA_GLOBAL';

export interface CanonicalServiceComponent {
  componentCode: string;
  name: string;
  category: 'VOICE' | 'BROADBAND_DATA' | 'MESSAGING' | 'IOT' | 'EQUIPMENT' | 'MANAGED_SERVICE';
  fcc499Category: string; // e.g. "Line 414.1 - Interstate End User"
  isRegulatoryFeeApplicable: boolean;
}

export interface CatalogProductSKU {
  id: string;
  sku: string;
  commercialName: string;
  serviceType: TelecomServiceType;
  canonicalComponents: CanonicalServiceComponent[];
  taxabilityCategory: string; // e.g. "TAXABLE_TELECOM_SERVICE"
  regulatoryRevenueCategory: string; // e.g. "INTERSTATE_TELECOMMUNICATIONS"
  confidenceScore: number; // 0.00 to 1.00
  mappingStatus: 'CERTIFIED' | 'AI_SUGGESTED' | 'NEEDS_REVIEW';
  lastUpdated: string;
}

export type FiscalDocumentType =
  | 'QUOTE'
  | 'COMMIT'
  | 'INVOICE'
  | 'CREDIT_NOTE'
  | 'ADJUSTMENT'
  | 'REFUND';

export type FiscalDocumentStatus =
  | 'DRAFT'
  | 'QUOTED'
  | 'COMMITTED'
  | 'SUPERSEDED'
  | 'REVERSED'
  | 'DISPUTED';

export interface TaxLineItem {
  id: string;
  jurisdictionName: string;
  jurisdictionLevel: 'FEDERAL' | 'STATE' | 'COUNTY' | 'CITY' | 'SPECIAL_DISTRICT';
  taxType: 'SALES_TAX' | 'TELECOM_EXCISE' | 'USF_FEDERAL' | 'USF_STATE' | 'PUC_SURCHARGE' | 'E911_FEE' | 'TRS_FEE' | 'VAT' | 'FRANCHISE_FEE';
  taxCategory: 'TAX' | 'REGULATORY_FEE';
  basis: string; // Decimal string
  rate: string;  // Decimal percentage e.g. "0.065000"
  taxAmount: string; // Decimal string e.g. "8.12"
  roundingApplied: string; // e.g. "HALF_EVEN"
  lawReference: string; // e.g. "Cal. Rev. & Tax. Code § 41020"
}

export interface FiscalTransaction {
  id: string; // UUIDv7
  idempotencyKey: string;
  tenantId: string;
  legalEntityId: string;
  documentType: FiscalDocumentType;
  status: FiscalDocumentStatus;
  customerAccountRef: string;
  customerType: 'CONSUMER' | 'BUSINESS' | 'WHOLESALE_RESELLER' | 'GOVERNMENT';
  originJurisdiction: string;
  destinationJurisdiction: string;
  eventTimestamp: string;
  decisionTimestamp: string;
  currency: string;
  netAmount: string;
  totalTaxAmount: string;
  totalRegulatoryFees: string;
  grossAmount: string;
  lineItems: {
    lineNumber: number;
    sku: string;
    productName: string;
    serviceType: TelecomServiceType;
    unitPrice: string;
    quantity: string;
    netAmount: string;
    taxes: TaxLineItem[];
  }[];
  evidenceManifestHash: string;
  engineVersion: string;
  ruleBundleHash: string;
  predecessorDecisionId?: string;
  supersededByDecisionId?: string;
  isLegalHold: boolean;
}

export interface ResponsibilityRoleAssignment {
  transactionScope: string;
  productFamily: string;
  supplier: string;
  carrier: string;
  resellerMvno: string;
  endCustomer: string;
  statutoryRemittanceRole: 'SUPPLIER' | 'CARRIER' | 'MVNO_RESELLER' | 'CUSTOMER';
  legalBasisCitation: string;
}

export interface NexusThresholdStatus {
  jurisdictionCode: string;
  jurisdictionName: string;
  economicThresholdAmount: string;
  accumulatedSales: string;
  percentThresholdReached: number;
  transactionCountThreshold: number;
  accumulatedTransactions: number;
  isNexusEstablished: boolean;
  registrationStatus: 'REGISTERED' | 'ACTION_REQUIRED' | 'MONITORING';
  daysToFilingDue: number;
}

export interface FilingObligation {
  id: string;
  authorityName: string;
  formNumber: string; // e.g. "FCC Form 499-Q", "CDTFA-401-E", "HMRC VAT 100"
  jurisdiction: string;
  period: string; // e.g. "2026-Q3", "2026-09"
  dueDate: string;
  obligationKind: 'TRANSACTION_CHARGE' | 'PERIODIC_CONTRIBUTION' | 'INFORMATION_RETURN';
  filingStatus: 'PENDING_PREPARATION' | 'READY_FOR_REVIEW' | 'FILED' | 'ACCEPTED' | 'OVERDUE';
  accruedAmount: string;
  assignedController: string;
}

export interface TCSLEntry {
  id: string;
  entryNumber: string;
  postingDate: string;
  sourceDocumentRef: string;
  accountCode: string;
  accountName: string;
  debit: string;
  credit: string;
  currency: string;
  jurisdictionCode: string;
  reconciliationStatus: 'MATCHED' | 'UNRECONCILED' | 'VARIANCE_FLAGGED';
  erpBatchId?: string;
}

export interface CountryRegulatoryPack {
  packId: string;
  countryCode: string;
  countryName: string;
  version: string;
  lifecycleState: 'CERTIFIED' | 'RELEASED' | 'IN_TESTING' | 'DEPRECATED';
  ruleCount: number;
  goldenVectorsCount: number;
  goldenVectorPassRate: number; // 100%
  sha256Digest: string;
  effectiveFrom: string;
  lastCertifiedAt: string;
  regulatoryBodies: string[];
}

export interface AIGovernanceLog {
  id: string;
  timestamp: string;
  modelIdentifier: string;
  modelRiskTier: 'TIER_1_LOW' | 'TIER_2_MODERATE' | 'TIER_3_HIGH';
  taskType: 'SKU_ONTOLOGY_DISCOVERY' | 'SHADOW_ASSURANCE_AUDIT' | 'EXPLANATION_GENERATION';
  inputSnippet: string;
  aiProposal: string;
  deterministicDAGDecision: string;
  varianceStatus: 'PERFECT_MATCH' | 'ADVISORY_DRIFT' | 'FLAGGED_ANOMALY';
  humanReviewerStatus: 'APPROVED' | 'PENDING' | 'REJECTED';
  boundaryViolationAttempted: boolean; // Must always be FALSE!
}
