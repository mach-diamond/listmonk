package webhooks

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/knadh/listmonk/models"
)

const (
	testTopic   = "arn:aws:sns:us-east-1:123456789012:marketing-ses-events"
	testCertURL = "https://sns.us-east-1.amazonaws.com/SimpleNotificationService-test.pem"
)

// signer stands in for SNS: a key whose certificate is pre-cached under the
// SNS certificate URL, so no request leaves the test.
type signer struct {
	key *rsa.PrivateKey
	ses *SES
}

func newSigner(t *testing.T, topics ...string) signer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "sns.amazonaws.com"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	s := NewSES(topics)
	s.certs["/SimpleNotificationService-test.pem"] = cert
	return signer{key: key, ses: s}
}

// notif builds a signed SNS Notification carrying an SES event.
func (sg signer) notif(t *testing.T, topic, version string, event map[string]any) []byte {
	t.Helper()
	msg, _ := json.Marshal(event)
	n := sesNotif{
		Type: "Notification", MessageId: "m-1", TopicArn: topic, Message: string(msg),
		Timestamp: "2026-10-11T10:00:00.000Z", SignatureVersion: version, SigningCertURL: testCertURL,
	}

	var (
		hash   crypto.Hash
		digest []byte
	)
	if version == "2" {
		h := sha256.Sum256(sg.ses.buildSignature(n))
		hash, digest = crypto.SHA256, h[:]
	} else {
		h := sha1.Sum(sg.ses.buildSignature(n))
		hash, digest = crypto.SHA1, h[:]
	}
	sig, err := rsa.SignPKCS1v15(rand.Reader, sg.key, hash, digest)
	if err != nil {
		t.Fatal(err)
	}
	n.Signature = base64.StdEncoding.EncodeToString(sig)

	b, _ := json.Marshal(n)
	return b
}

func event(eventType string) map[string]any {
	e := map[string]any{
		"eventType": eventType,
		"mail": map[string]any{
			"timestamp":   "2026-10-11T09:59:00.000Z",
			"destination": []string{"Reader@Example.com"},
			"headers": []map[string]string{
				{"name": models.EmailHeaderCampaignUUID, "value": "6f1c0e8a-0000-4000-8000-000000000001"},
			},
		},
	}
	if eventType == "Bounce" {
		e["bounce"] = map[string]any{"bounceType": "Permanent", "bouncedRecipients": []map[string]string{{"status": "5.1.1"}}}
	}
	return e
}

func TestSESEvents(t *testing.T) {
	sg := newSigner(t, testTopic)

	for _, c := range []struct {
		version, eventType string
		want               string
	}{
		{"1", "Bounce", models.BounceTypeHard},
		{"2", "Bounce", models.BounceTypeHard},
		{"2", "Complaint", models.BounceTypeComplaint},
	} {
		b, err := sg.ses.ProcessBounce(sg.notif(t, testTopic, c.version, event(c.eventType)))
		if err != nil {
			t.Fatalf("v%s %s: %v", c.version, c.eventType, err)
		}
		if b.Type != c.want || b.Email != "reader@example.com" || b.CampaignUUID == "" {
			t.Errorf("v%s %s: got type %q email %q campaign %q", c.version, c.eventType, b.Type, b.Email, b.CampaignUUID)
		}
	}

	if _, err := sg.ses.ProcessBounce(sg.notif(t, testTopic, "2", event("Delivery"))); err == nil {
		t.Error("a Delivery event must not be recorded as a bounce")
	}
}

func TestSESRefusesOtherTopics(t *testing.T) {
	sg := newSigner(t, testTopic)
	other := "arn:aws:sns:us-east-1:999999999999:someone-elses-topic"

	if _, err := sg.ses.ProcessBounce(sg.notif(t, other, "2", event("Complaint"))); err == nil {
		t.Error("a correctly signed message from another topic must be refused")
	}

	// A subscription confirmation from another topic is refused before its
	// SubscribeURL is fetched.
	if err := sg.ses.ProcessSubscription(sg.notif(t, other, "2", event("Bounce"))); err == nil {
		t.Error("a subscription from another topic must be refused")
	}

	// With no topics configured, nothing is accepted.
	none := newSigner(t)
	if _, err := none.ses.ProcessBounce(none.notif(t, testTopic, "2", event("Bounce"))); err == nil {
		t.Error("with no configured topic every message must be refused")
	}
}

func TestSESRefusesBadSignatures(t *testing.T) {
	sg := newSigner(t, testTopic)

	var n sesNotif
	_ = json.Unmarshal(sg.notif(t, testTopic, "2", event("Bounce")), &n)
	n.Message = `{"eventType":"Bounce","mail":{"destination":["victim@example.com"]}}`
	tampered, _ := json.Marshal(n)
	if _, err := sg.ses.ProcessBounce(tampered); err == nil {
		t.Error("a tampered message must be refused")
	}

	_ = json.Unmarshal(sg.notif(t, testTopic, "2", event("Bounce")), &n)
	n.SignatureVersion = "3"
	unknown, _ := json.Marshal(n)
	if _, err := sg.ses.ProcessBounce(unknown); err == nil {
		t.Error("an unknown signature version must be refused")
	}
}
