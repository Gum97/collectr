// Submission path.
//
// Target from docs/02-estimation.md: p99 under 500ms at 100 RPS. This request is
// the opposite of the redirect in every way -- it writes to six tables in one
// transaction, takes an advisory lock on the audit chain, and encrypts a field --
// so it is where the design's cost actually lives.
//
//   docker compose --profile load run --rm k6 run /scripts/submit.js
import http from 'k6/http';
import { check } from 'k6';
import { Counter } from 'k6/metrics';

const duplicates = new Counter('submit_duplicates');
const rejected = new Counter('submit_rejected');

const BASE = __ENV.BASE_URL || 'http://caddy';
const HOST = __ENV.HOST_HEADER || 'localhost';
const FORM = __ENV.FORM || 'fm_test';
const VERSION = __ENV.FORM_VERSION;
const DOCUMENT = __ENV.CONSENT_DOC;
const HASH = __ENV.CONSENT_HASH;
const RATE = Number(__ENV.RATE || 100);

export const options = {
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
  scenarios: {
    submit: {
      executor: 'constant-arrival-rate',
      rate: RATE, timeUnit: '1s', duration: '60s',
      preAllocatedVUs: 50, maxVUs: 300,
    },
  },
  thresholds: {
    'http_req_duration': ['p(99)<500', 'p(95)<250'],
    'http_req_failed': ['rate<0.01'],
  },
  discardResponseBodies: false,
};

export default function () {
  // A distinct phone number per iteration: reusing one would collapse every
  // submission onto a single data subject and measure a lock, not the path.
  const n = `09${String(Math.floor(Math.random() * 1e8)).padStart(8, '0')}`;
  const takesBranch = Math.random() < 0.5;

  const answers = {
    f_name: `Load ${n}`,
    f_phone: n,
    f_used: takesBranch ? 'o_yes' : 'o_no',
  };
  if (takesBranch) {
    answers.f_rating = 1 + Math.floor(Math.random() * 5);
    answers.f_health = 'tinh trang ' + n;
  }

  const res = http.post(
    `${BASE}/api/pub/forms/${FORM}/submissions`,
    JSON.stringify({
      form_version_id: VERSION,
      answers,
      consents: [
        { purpose: 'service', granted: true },
        { purpose: 'marketing', granted: Math.random() < 0.4 },
      ],
      consent_proof: { document_id: DOCUMENT, rendered_hash: 'sha256:' + HASH },
    }),
    {
      headers: {
        Host: HOST,
        'Content-Type': 'application/json',
        // Unique per request: the endpoint requires one, and reusing it would
        // measure the duplicate-rejection path instead of the write path.
        'Idempotency-Key': `load-${__VU}-${__ITER}-${Date.now()}`,
      },
      tags: { name: 'submit' },
    },
  );

  if (res.status === 409) duplicates.add(1);
  if (res.status === 422) rejected.add(1);

  check(res, {
    'created': (r) => r.status === 201,
    'returns a receipt': (r) => r.status !== 201 || !!r.json('receipt_token'),
  });
}
