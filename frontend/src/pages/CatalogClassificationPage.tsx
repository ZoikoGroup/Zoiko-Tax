import React, { useState, useEffect } from 'react';
import { apiService } from '../services/apiService';
import { CatalogProductSKU } from '../types/domain';
import { DataTable, Column } from '../components/common/DataTable';
import { StatusBadge } from '../components/common/StatusBadge';
import { Modal } from '../components/common/Modal';
import {
  Layers,
  Sparkles,
  CheckCircle,
  Shield,
} from 'lucide-react';

export const CatalogClassificationPage: React.FC = () => {
  const [skus, setSkus] = useState<CatalogProductSKU[]>([]);
  const [selectedSku, setSelectedSku] = useState<CatalogProductSKU | null>(null);

  useEffect(() => {
    async function load() {
      const data = await apiService.getCatalogSKUs();
      setSkus(data);
    }
    load();
  }, []);

  const columns: Column<CatalogProductSKU>[] = [
    {
      key: 'sku',
      header: 'Product SKU',
      sortable: true,
      render: row => (
        <div>
          <div className="font-mono font-bold theme-text-primary text-xs">{row.sku}</div>
          <div className="text-[10px] theme-text-muted font-sans">{row.serviceType}</div>
        </div>
      ),
    },
    {
      key: 'commercialName',
      header: 'Commercial Name',
      sortable: true,
      render: row => (
        <div className="font-semibold theme-text-primary">{row.commercialName}</div>
      ),
    },
    {
      key: 'canonicalComponents',
      header: 'Decomposed Components',
      render: row => (
        <div className="flex flex-wrap gap-1">
          {row.canonicalComponents.map((c, i) => (
            <span
              key={i}
              className="rounded theme-subtle px-1.5 py-0.5 font-mono text-[10px] theme-text-secondary border"
            >
              {c.category}
            </span>
          ))}
        </div>
      ),
    },
    {
      key: 'taxabilityCategory',
      header: 'Taxability Class (ZTAX-CLS-001)',
      render: row => (
        <span className="font-mono text-[11px] text-[#dd7134] font-semibold">
          {row.taxabilityCategory}
        </span>
      ),
    },
    {
      key: 'regulatoryRevenueCategory',
      header: 'FCC 499-A Regulatory Class',
      render: row => (
        <span className="font-mono text-[11px] text-[#5b2a86] dark:text-[#c084fc] font-semibold">
          {row.regulatoryRevenueCategory}
        </span>
      ),
    },
    {
      key: 'mappingStatus',
      header: 'Status & Confidence',
      sortable: true,
      align: 'right',
      render: row => (
        <div className="flex flex-col items-end gap-1">
          <StatusBadge status={row.mappingStatus} />
          <span className="font-mono text-[10px] theme-text-muted">
            {(row.confidenceScore * 100).toFixed(0)}% confidence
          </span>
        </div>
      ),
    },
  ];

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="rounded-2xl border theme-card p-6 shadow-xl">
        <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4">
          <div>
            <div className="flex items-center gap-2">
              <span className="rounded bg-[#5b2a86]/15 px-2 py-0.5 font-mono text-[10px] font-semibold text-[#5b2a86] dark:text-[#c084fc] border border-[#5b2a86]/30">
                SPECIFICATION: ZTAX-CLS-001
              </span>
              <span className="text-xs theme-text-muted">•</span>
              <span className="text-xs theme-text-secondary font-medium">Dual Classification Workbench</span>
            </div>
            <h1 className="mt-2 text-2xl font-bold tracking-tight theme-text-primary">
              Telecom Product Ontology & SKU Mapping
            </h1>
            <p className="mt-1 text-xs theme-text-muted max-w-2xl leading-relaxed">
              Decompose commercial carrier offerings into canonical telecom service components.
              Taxability (sales & excise tax) and Regulatory Revenue (FCC USF & State PUC) are two
              independent authoritative decisions with separate provenance.
            </p>
          </div>

          <div className="flex items-center gap-2">
            <span className="rounded-lg bg-[#5b2a86]/15 px-3 py-1.5 font-mono text-xs font-semibold text-[#5b2a86] dark:text-[#d8b4fe] border border-[#5b2a86]/30 flex items-center gap-1.5">
              <Layers className="h-4 w-4" />
              Dual Pipeline Enforced
            </span>
          </div>
        </div>
      </div>

      {/* Constitutional Doctrine Highlights */}
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        <div className="rounded-xl border theme-card p-4">
          <div className="flex items-center gap-2 text-xs font-bold theme-text-primary">
            <CheckCircle className="h-4 w-4 text-[#dd7134]" />
            Identity First, Law Second
          </div>
          <p className="mt-1 text-[11px] theme-text-muted">
            Commercial plans like "Unlimited 5G" decompose into underlying voice, data, and messaging
            components prior to rate evaluation.
          </p>
        </div>

        <div className="rounded-xl border theme-card p-4">
          <div className="flex items-center gap-2 text-xs font-bold theme-text-primary">
            <Shield className="h-4 w-4 text-[#5b2a86] dark:text-[#c084fc]" />
            Mandatory Dual Classification
          </div>
          <p className="mt-1 text-[11px] theme-text-muted">
            Taxability and regulatory fee liability can never be inferred from one another. Each maintains
            an independent rule DAG and evidence vector.
          </p>
        </div>

        <div className="rounded-xl border theme-card p-4">
          <div className="flex items-center gap-2 text-xs font-bold theme-text-primary">
            <Sparkles className="h-4 w-4 text-[#9a5b12] dark:text-[#fbbf24]" />
            AI Shadow Assurance
          </div>
          <p className="mt-1 text-[11px] theme-text-muted">
            AI assists with product catalog parsing and bundle extraction, but authoritative rate applications
            require certified human sign-off.
          </p>
        </div>
      </div>

      {/* SKU Table */}
      <DataTable
        data={skus}
        columns={columns}
        searchPlaceholder="Search by SKU, commercial title, or classification..."
        searchKey={r => `${r.sku} ${r.commercialName} ${r.taxabilityCategory}`}
        onRowClick={row => setSelectedSku(row)}
      />

      {/* Detail Modal for Selected SKU */}
      {selectedSku && (
        <Modal
          isOpen={!!selectedSku}
          onClose={() => setSelectedSku(null)}
          title={selectedSku.commercialName}
          subtitle={`SKU: ${selectedSku.sku} | Mapping Status: ${selectedSku.mappingStatus}`}
          maxWidth="2xl"
        >
          <div className="space-y-4">
            <div className="grid grid-cols-2 gap-3 text-xs">
              <div className="rounded-lg theme-subtle p-3 border">
                <span className="theme-text-muted uppercase text-[10px]">Service Type</span>
                <div className="font-mono theme-text-primary font-semibold mt-1">
                  {selectedSku.serviceType}
                </div>
              </div>
              <div className="rounded-lg theme-subtle p-3 border">
                <span className="theme-text-muted uppercase text-[10px]">AI Confidence Score</span>
                <div className="font-mono text-[#26735b] dark:text-[#34d399] font-semibold mt-1">
                  {(selectedSku.confidenceScore * 100).toFixed(1)}% Match
                </div>
              </div>
            </div>

            {/* Canonical Decomposition */}
            <div>
              <h4 className="text-xs font-bold uppercase tracking-wider theme-text-primary mb-2">
                Canonical Service Components ({selectedSku.canonicalComponents.length})
              </h4>
              <div className="space-y-2">
                {selectedSku.canonicalComponents.map((comp, i) => (
                  <div
                    key={i}
                    className="rounded-lg border theme-subtle p-3 text-xs"
                  >
                    <div className="flex items-center justify-between">
                      <span className="font-bold theme-text-primary">{comp.name}</span>
                      <span className="rounded bg-[#bf6735]/15 px-2 py-0.5 font-mono text-[10px] text-[#dd7134]">
                        {comp.category}
                      </span>
                    </div>
                    <div className="mt-1 font-mono text-[11px] text-[#5b2a86] dark:text-[#c084fc]">
                      FCC Category: {comp.fcc499Category}
                    </div>
                    <div className="mt-1 text-[10px] theme-text-muted">
                      Regulatory Fee Subject: {comp.isRegulatoryFeeApplicable ? 'YES (Line 414)' : 'NO (Exempt/Information)'}
                    </div>
                  </div>
                ))}
              </div>
            </div>

            {/* Dual Classification Footprint */}
            <div className="rounded-lg border theme-subtle p-3 text-xs space-y-2">
              <div className="text-[11px] font-bold theme-text-primary uppercase tracking-wider">
                Authoritative Decision Keys
              </div>
              <div className="flex justify-between border-b theme-border pb-1.5">
                <span className="theme-text-muted">Taxability Category:</span>
                <span className="font-mono text-[#dd7134] font-semibold">{selectedSku.taxabilityCategory}</span>
              </div>
              <div className="flex justify-between">
                <span className="theme-text-muted">Regulatory Revenue Category:</span>
                <span className="font-mono text-[#5b2a86] dark:text-[#c084fc] font-semibold">
                  {selectedSku.regulatoryRevenueCategory}
                </span>
              </div>
            </div>
          </div>
        </Modal>
      )}
    </div>
  );
};
