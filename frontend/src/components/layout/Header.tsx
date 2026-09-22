import React from 'react';
import {
  ChevronDown,
  Search,
  Bell,
  Box,
  Globe2,
  DollarSign,
  ShieldCheck,
} from 'lucide-react';

export const Header: React.FC = () => {
  return (
    <div className="w-full bg-white select-none">
      {/* TIER 1: Enterprise Header Row */}
      <header className="flex h-[58px] w-full items-center justify-between border-b border-gray-200 px-6">
        {/* Left: 5 Enterprise Selectors */}
        <div className="flex items-center gap-3 overflow-x-auto no-scrollbar py-1">
          {/* Selector 1: Global Telecom Group */}
          <div className="flex min-w-[150px] flex-col justify-center rounded-lg border border-gray-200 bg-white px-3 py-1 text-left cursor-pointer hover:border-gray-300">
            <span className="text-[10px] text-gray-400 leading-tight">Global Telecom Group</span>
            <div className="flex items-center justify-between gap-2 mt-0.5">
              <span className="text-xs font-bold text-gray-900 leading-none truncate">
                Global Telecom Group
              </span>
              <ChevronDown className="h-3 w-3 text-gray-500 shrink-0" />
            </div>
          </div>

          {/* Selector 2: Legal Entity */}
          <div className="flex min-w-[145px] flex-col justify-center rounded-lg border border-gray-200 bg-white px-3 py-1 text-left cursor-pointer hover:border-gray-300">
            <span className="text-[10px] text-gray-400 leading-tight">Legal Entity</span>
            <div className="flex items-center justify-between gap-2 mt-0.5">
              <span className="text-xs font-bold text-gray-900 leading-none truncate">
                Zoiko Telecom Ltd
              </span>
              <ChevronDown className="h-3 w-3 text-gray-500 shrink-0" />
            </div>
          </div>

          {/* Selector 3: Tax Period */}
          <div className="hidden sm:flex min-w-[135px] flex-col justify-center rounded-lg border border-gray-200 bg-white px-3 py-1 text-left cursor-pointer hover:border-gray-300">
            <span className="text-[10px] text-gray-400 leading-tight">Tax Period</span>
            <div className="flex items-center justify-between gap-2 mt-0.5">
              <span className="text-xs font-bold text-gray-900 leading-none truncate">
                Apr 2026 - Jun 2026
              </span>
              <ChevronDown className="h-3 w-3 text-gray-500 shrink-0" />
            </div>
          </div>

          {/* Selector 4: Environment */}
          <div className="hidden md:flex min-w-[125px] flex-col justify-center rounded-lg border border-gray-200 bg-white px-3 py-1 text-left cursor-pointer hover:border-gray-300">
            <span className="text-[10px] text-gray-400 leading-tight">Environment</span>
            <div className="flex items-center justify-between gap-2 mt-0.5">
              <span className="text-xs font-bold text-gray-900 leading-none truncate">
                Production
              </span>
              <ChevronDown className="h-3 w-3 text-gray-500 shrink-0" />
            </div>
          </div>

          {/* Selector 5: Role / User */}
          <div className="hidden lg:flex min-w-[125px] flex-col justify-center rounded-lg border border-gray-200 bg-white px-3 py-1 text-left cursor-pointer hover:border-gray-300">
            <span className="text-[10px] text-gray-400 leading-tight">Role / User</span>
            <div className="flex items-center justify-between gap-2 mt-0.5">
              <span className="text-xs font-bold text-gray-900 leading-none truncate">
                Tax Manager
              </span>
              <ChevronDown className="h-3 w-3 text-gray-500 shrink-0" />
            </div>
          </div>
        </div>

        {/* Right: Search, Notifications, User profile */}
        <div className="flex items-center gap-4 shrink-0 pl-3">
          <button className="text-gray-700 hover:text-black cursor-pointer p-1">
            <Search className="h-4 w-4" />
          </button>

          <button className="relative text-gray-700 hover:text-black cursor-pointer p-1">
            <Bell className="h-4 w-4" />
            <span className="absolute top-1 right-1 h-1.5 w-1.5 rounded-full bg-red-500"></span>
          </button>

          <div className="flex items-center gap-2.5 pl-2 cursor-pointer">
            <img
              src="/avatar_hreynolds.png"
              alt="H. Reynolds"
              className="h-8 w-8 rounded-full object-cover border border-gray-200 shadow-2xs"
            />
            <div className="hidden xl:block text-left leading-tight">
              <div className="text-xs font-bold text-gray-900">H. Reynolds</div>
              <div className="text-[9px] font-semibold tracking-wider text-gray-400 uppercase">
                TAX DIRECTOR
              </div>
            </div>
          </div>
        </div>
      </header>

      {/* TIER 2: Secondary Context / Scope Filter Row */}
      <div className="flex flex-wrap items-center justify-between gap-3 px-6 py-3 bg-[#ffffff] border-b border-gray-100">
        <div className="flex flex-wrap items-center gap-3">
          {/* Card 1: Product / Service */}
          <div className="flex items-center gap-2 rounded-lg border border-gray-200 bg-white px-3 py-1.5 shadow-2xs hover:border-gray-300 cursor-pointer">
            <Box className="h-4 w-4 text-gray-500 shrink-0" />
            <div className="flex flex-col text-left">
              <span className="text-[9px] text-gray-400 leading-tight">Product / Service</span>
              <span className="text-xs font-bold text-gray-900 leading-tight">
                Mobile | Fixed | Digital Services
              </span>
            </div>
            <ChevronDown className="h-3 w-3 text-gray-400 ml-1 shrink-0" />
          </div>

          {/* Card 2: Jurisdiction Scope */}
          <div className="flex items-center gap-2 rounded-lg border border-gray-200 bg-white px-3 py-1.5 shadow-2xs hover:border-gray-300 cursor-pointer">
            <Globe2 className="h-4 w-4 text-gray-500 shrink-0" />
            <div className="flex flex-col text-left">
              <span className="text-[9px] text-gray-400 leading-tight">Jurisdiction Scope</span>
              <span className="text-xs font-bold text-gray-900 leading-tight">
                Global (120 jurisdictions)
              </span>
            </div>
            <ChevronDown className="h-3 w-3 text-gray-400 ml-1 shrink-0" />
          </div>

          {/* Card 3: Currency */}
          <div className="hidden sm:flex items-center gap-2 rounded-lg border border-gray-200 bg-white px-3 py-1.5 shadow-2xs hover:border-gray-300 cursor-pointer">
            <DollarSign className="h-4 w-4 text-gray-500 shrink-0" />
            <div className="flex flex-col text-left">
              <span className="text-[9px] text-gray-400 leading-tight">Currency</span>
              <span className="text-xs font-bold text-gray-900 leading-tight">
                USD (US Dollar)
              </span>
            </div>
            <ChevronDown className="h-3 w-3 text-gray-400 ml-1 shrink-0" />
          </div>

          {/* Card 4: Certified Ruleset */}
          <div className="hidden md:flex items-center gap-2 rounded-lg border border-gray-200 bg-white px-3 py-1.5 shadow-2xs hover:border-gray-300 cursor-pointer">
            <ShieldCheck className="h-4 w-4 text-gray-500 shrink-0" />
            <div className="flex flex-col text-left">
              <span className="text-[9px] text-gray-400 leading-tight">Certified Ruleset</span>
              <span className="text-xs font-bold text-gray-900 leading-tight">
                EU + OECD + Local (v1.4.2)
              </span>
            </div>
            <ChevronDown className="h-3 w-3 text-gray-400 ml-1 shrink-0" />
          </div>
        </div>

        {/* Right Status Badge */}
        <div className="flex items-center gap-1.5 rounded-full border border-gray-200 bg-white px-3 py-1 text-[11px] font-medium text-gray-700 shadow-2xs">
          <span className="h-2 w-2 rounded-full bg-[#10b981]"></span>
          <span className="font-semibold text-[#065f46]">Scope & trust active</span>
        </div>
      </div>
    </div>
  );
};
