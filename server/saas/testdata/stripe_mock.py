#!/usr/bin/env python3
"""Minimal Stripe API mock for local CulpOS end-to-end testing.

Implements the endpoints CulpOS calls and test helpers that emit properly
signed webhook events to the application. Never use in production.

  POST /__pay?session=cs_...       complete checkout -> checkout.session.completed + invoice.paid
  POST /__fail?sub=sub_...         invoice.payment_failed (status past_due)
  POST /__delete?sub=sub_...       customer.subscription.deleted
  POST /__replay                   re-send the last event (duplicate delivery)
  GET  /checkout/cs_...            test checkout page ("Pay $333.88 / month" submits /__pay)
  GET  /portal/cus_...             test customer portal page (link back to CulpOS)
"""
import hashlib, hmac, json, os, sys, time, urllib.parse, urllib.request, itertools
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

APP = os.environ.get("APP_WEBHOOK_URL", "http://localhost:18080/stripe/webhook")
SECRET = os.environ.get("STRIPE_WEBHOOK_SECRET", "whsec_testsecret")
PRICE = os.environ.get("STRIPE_PRICE_ID", "price_culpos")
AMOUNT = int(os.environ.get("STRIPE_PRICE_AMOUNT", "33388"))
PUBLIC = os.environ.get("MOCK_PUBLIC_URL", "http://localhost:18181")
seq = itertools.count(1)
customers, sessions, subs = {}, {}, {}
last_event = {}

def nid(p): return f"{p}_{int(time.time())}{next(seq):04d}"

def send(etype, obj):
    global last_event
    ev = {"id": nid("evt"), "type": etype, "created": int(time.time()), "data": {"object": obj}}
    last_event = ev
    return deliver(ev)

def deliver(ev):
    body = json.dumps(ev).encode()
    ts = str(int(time.time()))
    sig = hmac.new(SECRET.encode(), (ts + ".").encode() + body, hashlib.sha256).hexdigest()
    req = urllib.request.Request(APP, data=body, headers={"Stripe-Signature": f"t={ts},v1={sig}", "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req) as r: return r.status
    except urllib.error.HTTPError as e: return e.code

def sub_obj(s): return s

class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def out(self, code, obj):
        b = json.dumps(obj).encode()
        self.send_response(code); self.send_header("Content-Type", "application/json"); self.send_header("Content-Length", str(len(b))); self.end_headers(); self.wfile.write(b)
    def html(self, code, body):
        b = ("<!doctype html><meta charset=utf-8><meta name=viewport content='width=device-width,initial-scale=1'>"
             "<title>Test Checkout</title><body style='font-family:system-ui;max-width:420px;margin:60px auto;padding:0 16px'>"
             + body).encode()
        self.send_response(code); self.send_header("Content-Type", "text/html; charset=utf-8"); self.send_header("Content-Length", str(len(b))); self.end_headers(); self.wfile.write(b)
    def form(self):
        n = int(self.headers.get("Content-Length", 0) or 0)
        return {k: v[0] for k, v in urllib.parse.parse_qs(self.rfile.read(n).decode()).items()}
    def do_GET(self):
        p = urllib.parse.urlparse(self.path)
        if p.path.startswith("/v1/subscriptions/"):
            sid = p.path.rsplit("/", 1)[1]
            return self.out(200, subs[sid]) if sid in subs else self.out(404, {"error": {"type": "invalid_request_error", "message": "No such subscription"}})
        if p.path.startswith("/checkout/"):
            sid = p.path.rsplit("/", 1)[1]
            if sid not in sessions: return self.html(404, "<h1>Checkout session not found</h1>")
            price = "$%d.%02d" % divmod(AMOUNT, 100)
            return self.html(200, f"<p>TEST MODE</p><h1>Subscribe to CulpOS</h1><p id=amount>{price} per month</p>"
                                  f"<form method=post action='/__pay?session={sid}&redirect=1'><button id=pay type=submit>Pay {price} / month</button></form>")
        if p.path.startswith("/portal/"):
            cid = p.path.rsplit("/", 1)[1]
            return self.html(200, f"<p>TEST MODE</p><h1>Billing portal</h1><p>Customer {cid}</p><a id=back href='{urllib.parse.parse_qs(p.query).get('return', [''])[0]}'>Return to CulpOS</a>")
        if p.path == "/__state": return self.out(200, {"customers": customers, "sessions": sessions, "subs": subs})
        self.out(404, {"error": {"message": "not found"}})
    def do_POST(self):
        p = urllib.parse.urlparse(self.path); q = {k: v[0] for k, v in urllib.parse.parse_qs(p.query).items()}
        f = self.form()
        if p.path == "/v1/customers":
            cid = nid("cus"); customers[cid] = {"id": cid, "email": f.get("email"), "metadata": {"company_id": f.get("metadata[company_id]")}}
            return self.out(200, customers[cid])
        if p.path == "/v1/checkout/sessions":
            sid = nid("cs_test"); sessions[sid] = {"id": sid, "customer": f["customer"], "price": f["line_items[0][price]"], "client_reference_id": f.get("client_reference_id"), "company_id": f.get("metadata[company_id]"), "success_url": f["success_url"]}
            return self.out(200, {"id": sid, "url": f"{PUBLIC}/checkout/{sid}"})
        if p.path == "/v1/billing_portal/sessions":
            return self.out(200, {"url": f"{PUBLIC}/portal/{f['customer']}?return=" + urllib.parse.quote(f.get("return_url", ""))})
        if p.path.startswith("/v1/subscriptions/"):
            sid = p.path.rsplit("/", 1)[1]; s = subs[sid]
            if "cancel_at_period_end" in f: s["cancel_at_period_end"] = f["cancel_at_period_end"] == "true"
            send("customer.subscription.updated", s)
            return self.out(200, s)
        if p.path == "/__pay":
            cs = sessions[q["session"]]; now = int(time.time())
            sid = nid("sub")
            subs[sid] = {"id": sid, "object": "subscription", "customer": cs["customer"], "status": "active", "current_period_start": now, "current_period_end": now + 30*86400,
                         "cancel_at_period_end": False, "canceled_at": None, "metadata": {"company_id": cs["company_id"]},
                         "items": {"data": [{"price": {"id": cs["price"]}}]}, "default_payment_method": {"type": "card", "card": {"brand": "visa", "last4": "4242"}}}
            r1 = send("checkout.session.completed", {"id": cs["id"], "mode": "subscription", "status": "complete", "payment_status": "paid", "client_reference_id": cs["client_reference_id"],
                                                     "customer": cs["customer"], "subscription": sid, "metadata": {"company_id": cs["company_id"]}})
            r2 = send("invoice.paid", {"id": nid("in"), "customer": cs["customer"], "subscription": sid, "amount_paid": 33388, "currency": "usd", "created": now})
            if q.get("redirect"):
                self.send_response(303); self.send_header("Location", cs["success_url"]); self.end_headers(); return
            return self.out(200, {"subscription": sid, "webhooks": [r1, r2], "success_url": cs["success_url"]})
        if p.path == "/__fail":
            s = subs[q["sub"]]; s["status"] = "past_due"
            r = send("invoice.payment_failed", {"id": nid("in"), "customer": s["customer"], "subscription": s["id"], "amount_due": 33388, "currency": "usd", "created": int(time.time())})
            return self.out(200, {"webhook": r})
        if p.path == "/__recover":
            s = subs[q["sub"]]; s["status"] = "active"
            r = send("invoice.paid", {"id": nid("in"), "customer": s["customer"], "subscription": s["id"], "amount_paid": 33388, "currency": "usd", "created": int(time.time())})
            return self.out(200, {"webhook": r})
        if p.path == "/__delete":
            s = subs[q["sub"]]; s["status"] = "canceled"; s["canceled_at"] = int(time.time()); s["current_period_end"] = int(time.time()) - 1
            return self.out(200, {"webhook": send("customer.subscription.deleted", s)})
        if p.path == "/__replay":
            return self.out(200, {"webhook": deliver(last_event)})
        if p.path == "/__badsig":
            body = json.dumps({"id": "evt_forged", "type": "checkout.session.completed", "data": {"object": {}}}).encode()
            req = urllib.request.Request(APP, data=body, headers={"Stripe-Signature": "t=%d,v1=%s" % (time.time(), "00"*32)})
            try:
                with urllib.request.urlopen(req) as r: return self.out(200, {"status": r.status})
            except urllib.error.HTTPError as e: return self.out(200, {"status": e.code})
        self.out(404, {"error": {"message": "not found"}})

if __name__ == "__main__":
    ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1]) if len(sys.argv) > 1 else 18181), H).serve_forever()
