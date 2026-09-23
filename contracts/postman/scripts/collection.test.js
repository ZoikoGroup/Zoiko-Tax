// Runs after every request in the collection.
//
// These encode controls from the ADRs rather than checking one endpoint, which
// is why they live at the collection level: a control that is only asserted
// where somebody remembered to assert it is a control with holes in it.

const contentType = pm.response.headers.get('Content-Type') || '';
const path = '/' + pm.request.url.getPath().replace(/^\/+/, '');

// Endpoints the contract does not carry yet. The collection includes requests
// for them so the intent is recorded and so W2 lane K has something to run
// against; until they exist they 404, and eighteen red rows would hide the
// rows that matter.
let pending = [];
try {
  pending = JSON.parse(pm.collectionVariables.get('pendingPaths') || '[]');
} catch (e) {
  pending = [];
}
const notImplemented = pm.response.code === 404 && pending.indexOf(path) !== -1;

if (notImplemented) {
  pm.test('not implemented yet (W2 lane K) — assertions skipped', function () {
    pm.expect(true).to.be.true;
  });
} else {
  let body = null;
  try { body = pm.response.json(); } catch (e) { body = null; }

  // ADR-0010 §2.9 · ADR-0001 §4.2 — a fiscal amount is never a JSON number.
  // JSON.parse turns a bare number into an IEEE 754 double before any of our
  // code sees it, so the defence has to be that the wire format never carries
  // one. Key matching is a heuristic; the contract's own lint checks the
  // schemas, and this checks what the server actually sent.
  const FISCAL_KEY = /(amount|total|tax|rate|net|gross|price|fee|charge|base|subtotal|balance|credit|debit)/i;

  const findNumericFiscal = function (node, prefix, out) {
    if (node === null || typeof node !== 'object') { return out; }
    if (Array.isArray(node)) {
      node.forEach(function (v, i) { findNumericFiscal(v, prefix + '[' + i + ']', out); });
      return out;
    }
    Object.keys(node).forEach(function (k) {
      const v = node[k];
      const p = prefix ? prefix + '.' + k : k;
      if (typeof v === 'number' && FISCAL_KEY.test(k)) { out.push(p); }
      else { findNumericFiscal(v, p, out); }
    });
    return out;
  };

  if (body) {
    pm.test('ADR-0010 §2.9 — no fiscal amount is a JSON number', function () {
      const found = findNumericFiscal(body, '', []);
      pm.expect(found, 'numeric fiscal fields: ' + found.join(', ')).to.have.lengthOf(0);
    });
  }

  // ADR-0011 §2.1 P2 — every timestamp is RFC 3339 UTC with exactly six
  // fractional digits and a literal Z. A response that emitted an offset or a
  // variable precision would digest differently from the record it describes.
  if (body) {
    pm.test('ADR-0011 P2 — timestamps carry exactly six fractional digits', function () {
      const wrong = [];
      const check = function (node, prefix) {
        if (node === null || typeof node !== 'object') { return; }
        if (Array.isArray(node)) { node.forEach(function (v, i) { check(v, prefix + '[' + i + ']'); }); return; }
        Object.keys(node).forEach(function (k) {
          const v = node[k];
          const p = prefix ? prefix + '.' + k : k;
          if (typeof v === 'string' && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}/.test(v)) {
            if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{6}Z$/.test(v)) { wrong.push(p + '=' + v); }
          } else { check(v, p); }
        });
      };
      check(body, '');
      pm.expect(wrong, 'not in canonical form: ' + wrong.join(', ')).to.have.lengthOf(0);
    });
  }

  if (pm.response.code >= 400) {
    pm.test('ADR-0016 §2.5 — errors are RFC 9457 Problem Details', function () {
      pm.expect(contentType).to.include('application/problem+json');
    });
    pm.test('ADR-0016 §2.5 — Problem carries type, title, status and a reason code', function () {
      const p = pm.response.json();
      pm.expect(p).to.have.property('type');
      pm.expect(p).to.have.property('title');
      pm.expect(p).to.have.property('status');
      pm.expect(p).to.have.property('ztx_reason_code');
    });
    pm.test('ADR-0016 §2.3 — a Problem states whether retrying is safe', function () {
      // A client that has to infer retryability from the status code will guess
      // wrong, and the guess that matters is the one that files a return twice.
      pm.expect(pm.response.json()).to.have.property('ztx_retryable');
    });
    pm.test('ADR-0016 §2.2 — an UNCERTAIN_* state is never an error response', function () {
      pm.expect(pm.response.text()).to.not.match(/UNCERTAIN_/);
    });
    pm.test('ADR-0016 §2.6 — error bodies carry no fiscal amount', function () {
      const found = findNumericFiscal(pm.response.json(), '', []);
      pm.expect(found).to.have.lengthOf(0);
    });
  }
}
