# Security

Culp Industries takes the security of St.Cloud\~OS seriously.

## Reporting security issues

**Please do not report security vulnerabilities through public issues.**

Report them privately to the security contact configured for your deployment
(the `SUPPORT_EMAIL` address shown on the St.Cloud\~OS Support page).

Please include as much of the following as you can:

* type of issue,
* affected component and path,
* configuration required to reproduce the issue,
* step-by-step instructions to reproduce,
* proof-of-concept (if available),
* impact of the issue.

## Handling of secrets

Never commit credentials. Stripe keys, webhook secrets, SMTP passwords,
database credentials and the Founder bootstrap password are provided through
deployment environment variables / secret storage only. See `.env.example`.
