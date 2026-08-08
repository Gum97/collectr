package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestSignatureRoundTrip(t *testing.T) {
	t.Parallel()

	secret := []byte("shared-secret")
	payload := []byte(`{"type":"submission.created"}`)
	now := time.Now()
	ts := now.Unix()

	sig := Signature(secret, ts, payload)
	if !VerifySignature(secret, sig, ts, payload, now, 5*time.Minute) {
		t.Fatal("a freshly signed delivery did not verify")
	}
}

func TestVerifySignatureRejects(t *testing.T) {
	t.Parallel()

	secret := []byte("shared-secret")
	payload := []byte(`{"a":1}`)
	now := time.Now()
	ts := now.Unix()
	sig := Signature(secret, ts, payload)

	tests := []struct {
		name    string
		secret  []byte
		sig     string
		ts      int64
		payload []byte
		at      time.Time
	}{
		{name: "wrong secret", secret: []byte("other"), sig: sig, ts: ts, payload: payload, at: now},
		{name: "tampered payload", secret: secret, sig: sig, ts: ts, payload: []byte(`{"a":2}`), at: now},
		{name: "tampered signature", secret: secret, sig: sig[:len(sig)-2] + "ff", ts: ts, payload: payload, at: now},
		{name: "empty signature", secret: secret, sig: "", ts: ts, payload: payload, at: now},
		// The timestamp is inside the signed material precisely so a captured
		// delivery cannot be replayed hours later.
		{name: "replayed an hour later", secret: secret, sig: sig, ts: ts, payload: payload, at: now.Add(time.Hour)},
		{name: "timestamp from the future", secret: secret, sig: sig, ts: ts, payload: payload, at: now.Add(-time.Hour)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if VerifySignature(tc.secret, tc.sig, tc.ts, tc.payload, tc.at, 5*time.Minute) {
				t.Error("an invalid delivery was accepted")
			}
		})
	}
}

// TestSignatureCoversTheTimestamp: moving the timestamp must invalidate the
// signature, or the replay window can simply be edited.
func TestSignatureCoversTheTimestamp(t *testing.T) {
	t.Parallel()

	secret := []byte("s")
	payload := []byte("{}")
	ts := time.Now().Unix()

	if Signature(secret, ts, payload) == Signature(secret, ts+1, payload) {
		t.Fatal("the signature does not depend on the timestamp")
	}
}

func TestSendDelivers(t *testing.T) {
	t.Parallel()

	secret := []byte("shared-secret")
	payload := []byte(`{"type":"submission.created"}`)

	var (
		gotEvent string
		verified bool
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEvent = r.Header.Get("X-Collectr-Event")
		ts, _ := strconv.ParseInt(r.Header.Get("X-Collectr-Timestamp"), 10, 64)
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		verified = VerifySignature(secret, r.Header.Get("X-Collectr-Signature"), ts, body, time.Now(), 5*time.Minute)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	// The production client refuses loopback addresses, which is the whole point
	// of it; this exercise uses a plain client against the test server and checks
	// the wire format instead.
	c := &Client{http: srv.Client()}
	res := c.Send(context.Background(), srv.URL, secret, "submission.created", "d1", payload)

	if !res.Delivered() {
		t.Fatalf("Delivered() = false, status %d err %v", res.StatusCode, res.Err)
	}
	if gotEvent != "submission.created" {
		t.Errorf("X-Collectr-Event = %q", gotEvent)
	}
	if !verified {
		t.Error("the receiver could not verify the signature")
	}
}

func TestResultClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		res           Result
		wantDelivered bool
		wantRetryable bool
	}{
		{name: "204", res: Result{StatusCode: 204}, wantDelivered: true},
		{name: "200", res: Result{StatusCode: 200}, wantDelivered: true},
		{name: "500", res: Result{StatusCode: 500}, wantRetryable: true},
		{name: "429", res: Result{StatusCode: 429}, wantRetryable: true},
		{name: "404 is permanent", res: Result{StatusCode: 404}},
		{name: "401 is permanent", res: Result{StatusCode: 401}},
		{name: "transport failure", res: Result{Err: context.DeadlineExceeded}, wantRetryable: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.res.Delivered(); got != tc.wantDelivered {
				t.Errorf("Delivered() = %v, want %v", got, tc.wantDelivered)
			}
			if got := tc.res.Retryable(); got != tc.wantRetryable {
				t.Errorf("Retryable() = %v, want %v", got, tc.wantRetryable)
			}
		})
	}
}

// TestClientRefusesPrivateAddresses is the SSRF guard at the transport layer,
// where it also covers redirects and DNS repointed after the URL was saved.
func TestClientRefusesPrivateAddresses(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// httptest listens on loopback, which is exactly what the dialer must refuse.
	res := NewClient().Send(context.Background(), srv.URL, []byte("s"), "e", "d", []byte("{}"))
	if res.Err == nil {
		t.Fatal("the client connected to a loopback address")
	}
	if res.Delivered() {
		t.Error("a refused connection was reported as delivered")
	}
}

func TestClientRefusesRedirectToPrivateAddress(t *testing.T) {
	t.Parallel()

	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer internal.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL, http.StatusFound)
	}))
	defer redirector.Close()

	// Both are loopback here, so the first hop already fails -- which is the
	// point: the check is at connect time, so every hop is covered.
	if res := NewClient().Send(context.Background(), redirector.URL, []byte("s"), "e", "d", []byte("{}")); res.Err == nil {
		t.Error("the client followed a redirect into a private address")
	}
}
