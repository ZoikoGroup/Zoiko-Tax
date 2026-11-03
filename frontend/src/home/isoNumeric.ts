/**
 * ISO 3166-1 alpha-2 → numeric, for the countries the dashboard can report:
 * the research catalogue's profiles and every country a content pack covers.
 * The world map's geometry (Natural Earth, via world-atlas) is keyed by the
 * numeric code; the API by alpha-2. A country absent here is drawn as not
 * available, which is what the API says of a country it does not list.
 */
export const ISO_NUMERIC: Readonly<Record<string, string>> = {
  AE: '784', AR: '032', AT: '040', AU: '036', BB: '052', BD: '050', BE: '056', BG: '100', BH: '048', BR: '076',
  BS: '044', CA: '124', CH: '756', CL: '152', CO: '170', CY: '196', CZ: '203', DE: '276', DK: '208', DO: '214',
  EE: '233', EG: '818', ES: '724', FI: '246', FR: '250', GB: '826', GH: '288', GR: '300', GY: '328', HR: '191',
  HU: '348', ID: '360', IE: '372', IN: '356', IT: '380', JM: '388', JP: '392', KE: '404', KR: '410', KY: '136',
  LT: '440', LU: '442', LV: '428', MA: '504', MT: '470', MX: '484', MY: '458', NG: '566', NL: '528', NO: '578',
  NZ: '554', PE: '604', PH: '608', PK: '586', PL: '616', PR: '630', PT: '620', QA: '634', RO: '642', RW: '646',
  SA: '682', SE: '752', SG: '702', SI: '705', SK: '703', TH: '764', TR: '792', TT: '780', TZ: '834', UG: '800',
  US: '840', VN: '704', ZA: '710', ZM: '894',
}
