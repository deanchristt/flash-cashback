# Flash Cashback — Mobile (Expo)

React Native (Expo) client for the Flash Cashback service. It shows the user's
cashback balance and the campaign budget, lets them make a payment (which earns
cashback), redeem their balance, and view their cashback history.

## Run

```bash
npm install
npm start
```

Then:
- press **w** to open in a web browser (fastest for a demo), or
- scan the QR code with the **Expo Go** app on your phone.

## Connecting to the backend

The API base URL and the user identity (`X-User-Id`) are editable in the app's
**Connection** card, so you can demo without rebuilding.

- **Web / iOS simulator:** `http://localhost:8080` works.
- **Android emulator:** `http://10.0.2.2:8080` (default).
- **Physical device:** use your computer's LAN IP, e.g. `http://192.168.1.10:8080`
  (the phone's `localhost` is the phone itself). Make sure the backend is
  reachable on your network.

Start the backend first from the repo root: `docker compose up --build`.

## Notes

- Every write sends a client-generated idempotency key, so retries are safe.
- Authentication is out of scope; identity is passed via `X-User-Id`. In
  production this header would be set by an auth gateway from a verified token.
