# Postman collection — Flash Cashback

API tests you can run manually or automated.

## Files
- `FlashCashback.postman_collection.json` — all endpoints, grouped into Ops,
  Campaign, Payments, Cashback, plus negative/edge cases.
- `FlashCashback.Local.postman_environment.json` — points at
  `http://localhost:8080` with `user_id=demo-user`.

## Use it in the Postman app
1. Start the backend from the repo root: `docker compose up --build`.
2. Import both JSON files (Import → Files).
3. Select the **Flash Cashback Local** environment (top-right).
4. Run requests, or open the collection → **Run** to execute all with assertions.

Every write auto-generates a fresh `idempotency_key`. To see idempotency in
action, open **Payments → "Payment — idempotent retry (send twice)"** and hit
Send twice: the second response returns `"replayed": true` with the same
`payment_id`, and no extra cashback is awarded.

## Run it headless with Newman (CI-friendly)
```bash
npx newman run FlashCashback.postman_collection.json \
  -e FlashCashback.Local.postman_environment.json
```

## Collection variables
| Variable | Default | Purpose |
|----------|---------|---------|
| `base_url` | `http://localhost:8080` | API root |
| `user_id` | `demo-user` | Sent as `X-User-Id` |
| `payment_amount` | `50000` | Amount for "Make payment" |
| `redeem_amount` | `1000` | Amount for "Redeem cashback" |

> Tip: bump `payment_amount` and fire "Make payment" repeatedly for one user to
> watch the **50,000/day cap** kick in (status flips to `partial_daily_cap` then
> `daily_cap_reached`).
