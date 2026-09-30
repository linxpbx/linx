---
title: A phone won't register
audience: admin
section: running
keywords: [register, registration failed, desk phone offline, not registered, 401, 403, tls, srtp, can't connect phone, phone app]
screens: []
---
# A phone won't register

"Register" means the phone signs in to Linx. When it can't, check in this order.

1. *The address.* The server is `sip.` in front of your domain, port 5061. The phone must be on your home or office network.
2. *TLS.* The phone's transport must be TLS, not UDP or TCP.
3. *The username and password*, exactly as Linx showed them. If in doubt, open the phone in Extensions and choose **New password**, then enter it on the phone.
4. *Encrypted audio (SRTP)* set to required on the phone.
5. *The phone's clock.* A phone with the wrong date can't check Linx's certificate. Set its time server (NTP).
6. *Older phones* may not trust Linx's certificate or may lack TLS 1.2. Update their firmware.

Still stuck? System → **Status** shows the phone system's **Recent log**, and [linx doctor](linx-doctor) checks the phone ports.

Details of the settings: [Desk phones and phone apps](desk-phones-and-phone-apps).
