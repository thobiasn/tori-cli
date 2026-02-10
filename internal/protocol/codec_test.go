package protocol

import (
	"bytes"
	"encoding/binary"
	"io"
	"strings"
	"testing"
)

func TestWriteReadRoundtrip(t *testing.T) {
	env := &Envelope{
		Type: TypeSubscribeMetrics,
		ID:   42,
	}

	var buf bytes.Buffer
	if err := WriteMsg(&buf, env); err != nil {
		t.Fatal(err)
	}

	got, err := ReadMsg(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != TypeSubscribeMetrics {
		t.Errorf("type = %q, want %q", got.Type, TypeSubscribeMetrics)
	}
	if got.ID != 42 {
		t.Errorf("id = %d, want 42", got.ID)
	}
}

func TestReadMsgEOF(t *testing.T) {
	_, err := ReadMsg(strings.NewReader(""))
	if err != io.EOF {
		t.Errorf("expected io.EOF, got %v", err)
	}
}

func TestReadMsgPartialHeader(t *testing.T) {
	_, err := ReadMsg(strings.NewReader("ab"))
	if err != io.ErrUnexpectedEOF {
		t.Errorf("expected io.ErrUnexpectedEOF, got %v", err)
	}
}

func TestReadMsgPartialPayload(t *testing.T) {
	var buf bytes.Buffer
	// Write header claiming 100 bytes, but only provide 10.
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 100)
	buf.Write(hdr[:])
	buf.Write(make([]byte, 10))

	_, err := ReadMsg(&buf)
	if err != io.ErrUnexpectedEOF {
		t.Errorf("expected io.ErrUnexpectedEOF, got %v", err)
	}
}

func TestReadMsgOversized(t *testing.T) {
	var buf bytes.Buffer
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], MaxMessageSize+1)
	buf.Write(hdr[:])

	_, err := ReadMsg(&buf)
	if err == nil {
		t.Fatal("expected error for oversized message")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error = %q, want 'too large'", err)
	}
}

func TestWriteMsgOversized(t *testing.T) {
	// Create a body that exceeds MaxMessageSize.
	big := make([]byte, MaxMessageSize+1)
	env := &Envelope{
		Type: TypeResult,
		Body: big,
	}

	var buf bytes.Buffer
	err := WriteMsg(&buf, env)
	if err == nil {
		t.Fatal("expected error for oversized message")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error = %q, want 'too large'", err)
	}
}

func TestMultipleMessagesOnStream(t *testing.T) {
	var buf bytes.Buffer

	envs := []*Envelope{
		NewEnvelopeNoBody(TypeSubscribeMetrics, 1),
		NewEnvelopeNoBody(TypeSubscribeAlerts, 2),
		NewEnvelopeNoBody(TypeUnsubscribe, 3),
	}

	for _, e := range envs {
		if err := WriteMsg(&buf, e); err != nil {
			t.Fatal(err)
		}
	}

	for i, want := range envs {
		got, err := ReadMsg(&buf)
		if err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
		if got.Type != want.Type {
			t.Errorf("message %d: type = %q, want %q", i, got.Type, want.Type)
		}
		if got.ID != want.ID {
			t.Errorf("message %d: id = %d, want %d", i, got.ID, want.ID)
		}
	}

	// No more messages.
	_, err := ReadMsg(&buf)
	if err != io.EOF {
		t.Errorf("expected EOF after all messages, got %v", err)
	}
}

func TestEncodeDecodeBody(t *testing.T) {
	orig := Result{OK: true, Message: "success"}
	raw, err := EncodeBody(&orig)
	if err != nil {
		t.Fatal(err)
	}

	var decoded Result
	if err := DecodeBody(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != orig {
		t.Errorf("got %+v, want %+v", decoded, orig)
	}
}

func TestReadMsgZeroSize(t *testing.T) {
	var buf bytes.Buffer
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 0)
	buf.Write(hdr[:])

	_, err := ReadMsg(&buf)
	if err == nil {
		t.Fatal("expected error for zero-size message")
	}
}

func TestReadMsgInvalidMsgpack(t *testing.T) {
	var buf bytes.Buffer
	var hdr [4]byte
	payload := []byte{0xff, 0xfe, 0xfd} // invalid msgpack
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	buf.Write(hdr[:])
	buf.Write(payload)

	_, err := ReadMsg(&buf)
	if err == nil {
		t.Fatal("expected error for invalid msgpack")
	}
}
