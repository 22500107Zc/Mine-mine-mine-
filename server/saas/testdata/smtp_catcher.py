#!/usr/bin/env python3
"""Local SMTP catcher for end-to-end testing: writes each message to DIR.

  python3 smtp_catcher.py /tmp/mail [port]      (default port 2525)

Never use in production. Requires Python 3.11 or older (smtpd module).
"""
import asyncore, os, smtpd, sys, time

DIR = sys.argv[1]
PORT = int(sys.argv[2]) if len(sys.argv) > 2 else 2525


class Catcher(smtpd.SMTPServer):
    def process_message(self, peer, mailfrom, rcpttos, data, **kw):
        path = os.path.join(DIR, f"{time.time():.6f}-{rcpttos[0]}.eml")
        with open(path, "wb") as f:
            f.write(data if isinstance(data, bytes) else data.encode())


os.makedirs(DIR, exist_ok=True)
Catcher(("127.0.0.1", PORT), None, decode_data=False)
asyncore.loop()
