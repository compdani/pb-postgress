# Passkeys

Enable **Passkeys** on an auth collection for WebAuthn sign-in with no email or username (discoverable credentials, user verification required):

- `request-passkey-registration` / `confirm-passkey-registration` (signed in) register a passkey; `request-passkey` / `auth-with-passkey` sign in;
- credentials are stored in `_passkeys`, and each user can list and delete their own;
- set the relying party ID and allowed origins in the collection options (default: the Application URL).

