import React, { useState } from 'react';
import { Copy, Check } from 'lucide-react';

export interface JsonViewerProps {
  data: unknown;
  title?: string;
  maxHeight?: string;
}

export const JsonViewer: React.FC<JsonViewerProps> = ({
  data,
  title = 'JSON Manifest',
  maxHeight = 'max-h-80',
}) => {
  const [copied, setCopied] = useState(false);
  const jsonString = JSON.stringify(data, null, 2);

  const handleCopy = () => {
    navigator.clipboard.writeText(jsonString);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  return (
    <div className="rounded-lg border theme-subtle bg-[#faf8fc] dark:bg-[#100030] font-mono text-xs">
      <div className="flex items-center justify-between border-b theme-border px-3 py-2 bg-[#f3eef7]/80 dark:bg-[#150c33]/80">
        <span className="text-[11px] font-semibold theme-text-primary uppercase tracking-wider">
          {title}
        </span>
        <button
          onClick={handleCopy}
          className="inline-flex items-center gap-1 rounded bg-[#e8e1f0] dark:bg-[#1f1352] px-2 py-1 text-[10px] theme-text-primary hover:bg-[#dd7134] hover:text-white transition-colors cursor-pointer"
        >
          {copied ? <Check className="h-3 w-3 text-[#26735b] dark:text-[#34d399]" /> : <Copy className="h-3 w-3" />}
          {copied ? 'Copied' : 'Copy'}
        </button>
      </div>
      <pre
        className={`overflow-auto p-3 theme-text-secondary ${maxHeight} text-[11px] leading-relaxed selection:bg-[#bf6735]/30`}
      >
        {jsonString}
      </pre>
    </div>
  );
};
