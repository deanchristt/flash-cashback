// Thin API client for the Flash Cashback service.
//
// Authentication is out of scope, so the caller's identity is passed via the
// X-User-Id header (the backend would derive this from a verified token in
// production). Every write carries a client-generated idempotency key so retries
// are safe.

// RFC-4122-ish v4 UUID without pulling in a dependency. Fine for idempotency
// keys and demo identities.
export function uuid() {
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0;
    const v = c === 'x' ? r : (r & 0x3) | 0x8;
    return v.toString(16);
  });
}

// Format integer IDR into "Rp 1.234.567".
export function formatIDR(amount) {
  const n = Number(amount) || 0;
  return 'Rp ' + n.toString().replace(/\B(?=(\d{3})+(?!\d))/g, '.');
}

async function request(baseUrl, path, { method = 'GET', userId, body } = {}) {
  const headers = { 'Content-Type': 'application/json' };
  if (userId) headers['X-User-Id'] = userId;

  const res = await fetch(baseUrl.replace(/\/$/, '') + path, {
    method,
    headers,
    body: body ? JSON.stringify(body) : undefined,
  });

  const text = await res.text();
  let data = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    data = { raw: text };
  }

  if (!res.ok) {
    const message = data?.error?.message || `HTTP ${res.status}`;
    const err = new Error(message);
    err.status = res.status;
    err.code = data?.error?.code;
    throw err;
  }
  return data;
}

export const api = {
  getCampaign: (baseUrl) => request(baseUrl, '/v1/campaign'),

  getBalance: (baseUrl, userId) =>
    request(baseUrl, '/v1/cashback/balance', { userId }),

  getHistory: (baseUrl, userId) =>
    request(baseUrl, '/v1/cashback/history?limit=50', { userId }),

  makePayment: (baseUrl, userId, amount) =>
    request(baseUrl, '/v1/payments', {
      method: 'POST',
      userId,
      body: { amount, idempotency_key: uuid() },
    }),

  redeem: (baseUrl, userId, amount) =>
    request(baseUrl, '/v1/cashback/redeem', {
      method: 'POST',
      userId,
      body: { amount, idempotency_key: uuid() },
    }),
};
