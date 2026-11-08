# Pair your phone

A paired browser lets you use tmx and answer owner approvals from your phone.
It uses your owner PIN, not a passkey. Pair only a browser you control.

## Before you start

Install and start tmx using the [installation guide](INSTALL.md). At the host
computer, open **Settings → Safety → Owner PIN** and set a PIN of 6–12 digits
if you have not already done so.

Your installation must already have a trusted HTTPS address configured, and
your phone must be able to reach it. Use the exact configured address, including
its port if one is required. Pairing does not set up HTTPS, a VPN, router access
or firewall rules. A local-only installation is not reachable from a phone just
because it is running.

## Pair with your PIN

1. Open your installation's trusted HTTPS address in your phone's browser.
2. On **Pair this device**, enter a recognizable device name and tap
   **Pair this device**. Keep that page open.
3. Enter your owner PIN and tap **Pair with my PIN**.
4. Wait for **Device paired**, then tap **Continue**.

The request expires in two minutes. If it expires, start again in the same
browser. Incorrect PIN attempts can cause a temporary wait; follow the message
on screen. The phone receives its own browser session, so use the same browser
after pairing. Clearing its cookies or using a different browser requires pairing
again. An ordinary device session expires after 90 days.

## Approve the code at the host instead

If you prefer, leave the phone's pairing page open and read its displayed code.
On the host computer, open **Settings → Owner devices**, enter that code and
choose **Approve**. Complete the host's authentication prompt. The phone then
shows **Device paired**; tap **Continue**. Approve only the code visible on your
own phone. An already-paired remote browser cannot approve new device codes.

The QR invites in **Chats** connect chat contacts; they are a separate flow from
pairing an owner browser.

## Approvals and removing access

When an action needs owner confirmation, the phone asks for your owner PIN.
Some actions require a fresh confirmation even during an existing presence
session. Host safety switches and grace settings remain host-only.

To remove a browser's access, open **Settings → Owner devices**, find the device
and choose **Revoke**, then complete the requested owner confirmation. Revocation
ends that device's session and active connections and survives a daemon restart.

If you forgot the PIN, reset it from the host's local **Owner PIN** controls.
Do not share PINs, pairing codes or session links in chats, logs or issue reports.
If the page says **Restricted recovery**, it is not an ordinary paired session:
follow its displayed recovery instructions. Restoring normal owner access
requires fresh host authentication.