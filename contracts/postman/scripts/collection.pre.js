// Runs before every request in the collection.
//
// It exists for two things the collection cannot express as static values: a
// timestamp in the canonical form, and an email address that is different on
// every run.

// Canonical timestamps: RFC 3339 UTC with exactly six fractional digits
// (ADR-0011 §2.1 P2). Postman's {{$isoTimestamp}} gives seconds only, which is
// why these are computed rather than used directly — a timestamp at second
// precision is a different document from one at microsecond precision, and the
// two digest differently.
const canonical = function (d) {
  return d.toISOString().replace(/\.(\d{3})Z$/, '.$1000Z');
};
const now = new Date();
pm.collectionVariables.set('decisionTime', canonical(now));
pm.collectionVariables.set('eventTime', canonical(new Date(now.getTime() - 2000)));

// A fresh address per run, so `POST /v1/admin/users` does not fail with
// ALREADY_EXISTS the second time somebody runs the collection. It is set once
// and reused, so the create and the requests that act on the created user agree.
if (!pm.collectionVariables.get('newUserEmail')) {
  pm.collectionVariables.set('newUserEmail', 'postman+' + now.getTime() + '@acme.example');
}
